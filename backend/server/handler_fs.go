package server

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"mike/backend/errs"
	"mike/backend/fsutil"
	"mike/backend/git"
	"mike/backend/sandbox"
	"mike/backend/types"
	"mike/pkg/logging"
)

func (s *Server) handleFSRead(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		JSONError(w, http.StatusBadRequest, "path is required")
		return
	}
	project, ok := s.requiredProject(w, r)
	if !ok {
		return
	}
	taskID := r.URL.Query().Get("task")

	if !s.requireSandboxAvailable(w, project) {
		return
	}
	// Resolve the host worktree path and translate sandbox mount paths
	// (shared with /api/preview).
	worktreePath, path, err := s.resolveWorktreeForFile(project, taskID, path)
	if err != nil {
		Fail(w, err, "resolve worktree", "task worktree not found")
		return
	}
	// Validate against the project root. The returned path is symlink-resolved and
	// is the one to open; the git-ref read below keeps the lexical path, which is
	// safe because `git show ref:relpath` reads the object database, never a link.
	hostPath, err := s.validateFSPath(project, worktreePath, path)
	if err != nil {
		JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Read from a git ref (e.g. HEAD) instead of the working tree.
	ref := r.URL.Query().Get("ref")
	// The ref is client-supplied and lands inside a `git show <ref>:<path>` argument,
	// so it must never be usable as an option — "--output=/tmp/pwn" is accepted by
	// git and writes anywhere the server user can.
	if ref != "" {
		if err := types.ValidateGitRef(ref); err != nil {
			JSONError(w, http.StatusBadRequest, "invalid ref: "+err.Error())
			return
		}
	}
	var content []byte
	if ref != "" {
		// --end-of-options (git >= 2.24) is a second line of defence: everything after
		// it is parsed as a revision, never as an option.
		const endOfOptions = "--end-of-options"
		relBase := project.Path
		if taskID != "" {
			// resolveWorktreeForFile resolved this to project.Path when no task was named.
			relBase = worktreePath
		}
		relPath, relErr := filepath.Rel(relBase, path)
		if relErr != nil {
			JSONError(w, http.StatusBadRequest, relErr.Error())
			return
		}
		spec := ref + ":" + relPath
		runGit := s.gitInContext(taskID, worktreePath)
		// Ask git how big the object is before asking it for the object; a cap applied
		// afterwards just reports the size of the allocation it already made.
		if size, ok := gitBlobSize(runGit, spec); ok && size > maxFSReadBytes {
			JSONError(w, http.StatusRequestEntityTooLarge, fsTooLargeMessage)
			return
		}
		output, readErr := runGit("show", endOfOptions, spec)
		if readErr != nil {
			JSONError(w, http.StatusNotFound, "file not found at ref "+ref)
			return
		}
		content = []byte(output)
	} else {
		// Host filesystem (worktree is bind-mounted into sandbox)
		info, statErr := os.Stat(hostPath)
		if statErr != nil {
			if os.IsNotExist(statErr) {
				JSONError(w, http.StatusNotFound, safeNotFoundError())
			} else {
				JSONError(w, http.StatusNotFound, "file not found")
			}
			return
		}
		if info.IsDir() {
			JSONError(w, http.StatusBadRequest, "path is a directory")
			return
		}
		// Same rule as the ref branch: refuse on the size stat reports, not the read one.
		if info.Size() > maxFSReadBytes {
			JSONError(w, http.StatusRequestEntityTooLarge, fsTooLargeMessage)
			return
		}
		data, readErr := os.ReadFile(hostPath)
		if readErr != nil {
			logging.For(logging.HTTP).Errorf("fs read failed: %v", readErr)
			JSONError(w, http.StatusInternalServerError, "failed to read file")
			return
		}
		content = data
	}
	if int64(len(content)) > maxFSReadBytes {
		JSONError(w, http.StatusRequestEntityTooLarge, fsTooLargeMessage)
		return
	}
	// Compute gutter markers (diff vs HEAD) for non-ref reads
	var markers map[int][]string
	if ref == "" {
		markers = computeGutterMarkers(s, worktreePath, path, taskID)
	}
	JSONResponse(w, http.StatusOK, fsReadResponse{
		Content:       string(content),
		GutterMarkers: markers,
	})
}

// fsReadResponse is the JSON response for GET /api/fs/read.
type fsReadResponse struct {
	Content       string           `json:"content"`
	GutterMarkers map[int][]string `json:"gutter_markers,omitempty"`
}

// maxFSReadBytes is what GET /api/fs/read returns at most — the size an editor can
// show, not a security boundary. Checking it before the read is the point: one request
// for a 4 GB file, in the working tree or in any commit, then costs one stat or one
// `cat-file -s` instead of 4 GB of memory.
const maxFSReadBytes int64 = 1_000_000

// maxFSWriteBytes is what POST /api/fs/write accepts as `content`. It is the same
// number as the read cap because it is the same reason — the size an editor shows —
// but it is its own constant because the two are free to move apart, and a shared
// name would make one cap's change silently move the other.
const maxFSWriteBytes = 1_000_000

// fsTooLargeMessage is the answer both size checks give. A test pins it to the caps it
// describes, because a cap raised without its sentence tells the user a lie.
const fsTooLargeMessage = "file too large (max 1MB)"

// gitInContext answers how to run git for a file request: in the task's worktree when
// the request named one, in the resolved checkout otherwise. The size probe and the
// read that follows must ask the same repository, so they share this rather than each
// picking a GitRunIn* variant.
func (s *Server) gitInContext(taskID, worktreePath string) func(args ...string) (string, error) {
	var wm *git.WorktreeManager
	if s.executor != nil {
		wm = s.executor.WorktreeMgr()
	}
	if wm == nil {
		return func(args ...string) (string, error) {
			return "", fmt.Errorf("worktree manager unavailable")
		}
	}
	if taskID != "" {
		return func(args ...string) (string, error) { return wm.GitRunInWorktreeOutput(taskID, args...) }
	}
	return func(args ...string) (string, error) { return wm.GitRunInDirOutput(worktreePath, args...) }
}

// gitBlobSize asks git for the size of the object a "<ref>:<path>" spec names, which is
// the only way to cap a historical read before performing it. ok is false when git
// cannot answer, and the caller then reads and caps the result: the probe is the
// optimisation, the length check is the guarantee.
func gitBlobSize(runGit func(args ...string) (string, error), spec string) (int64, bool) {
	out, err := runGit("cat-file", "-s", "--end-of-options", spec)
	if err != nil {
		return 0, false
	}
	size, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if err != nil || size < 0 {
		return 0, false
	}
	return size, true
}

// computeGutterMarkers runs `git diff HEAD -- <path>` and marks the lines of the current working-tree
// file that diff touched, indexed 1-based. nil when the file is untracked, the repo has no commits, or
// git cannot answer.
func computeGutterMarkers(s *Server, worktreePath, path, taskID string) map[int][]string {
	relPath, err := filepath.Rel(worktreePath, path)
	if err != nil {
		return nil
	}
	patch, err := s.gitInContext(taskID, worktreePath)("diff", "HEAD", "--", relPath)
	if err != nil || patch == "" {
		return nil
	}
	return gutterMarkers(patch)
}

// gutterMarkers turns a patch into the editor's per-line marks: "added", "removed", "modified".
//
// It reads git.ParseUnifiedDiff rather than a grammar of its own, because that is the grammar the diff
// panel's `?parsed=1` contract is written against — a second parser here would drift from it in exactly
// the cases nobody re-tests (renames, binary notices, "\ No newline at end of file").
//
// A run of removed rows owns no working-tree line, so it is marked on the line it follows; a removal
// immediately followed by an addition is a modification of the added line. A new-side row whose number
// is not the next one is the start of a new hunk, and the line before it is the last one the previous
// hunk could have marked.
func gutterMarkers(patch string) map[int][]string {
	marks := make(map[int][]string)
	for _, file := range git.ParseUnifiedDiff(patch) {
		prev := 0    // working-tree line of the last new-side row; 0 before the first
		removed := 0 // removals awaiting the row that decides added-or-modified
		for _, row := range file.Rows {
			if row.LineNumber == nil {
				continue // a binary or large-file notice: text the editor must not number
			}
			line := *row.LineNumber
			if row.Variant == "removed" {
				removed++
				continue
			}
			if line != prev+1 {
				prev = line - 1
			}
			switch row.Variant {
			case "added":
				if removed > 0 {
					marks[line] = append(marks[line], "modified")
					removed = 0
				} else {
					marks[line] = append(marks[line], "added")
				}
			default: // context
				if removed > 0 && prev > 0 {
					marks[prev] = append(marks[prev], "removed")
					removed = 0
				}
			}
			prev = line
		}
		if removed > 0 && prev > 0 {
			marks[prev] = append(marks[prev], "removed")
		}
	}
	if len(marks) == 0 {
		return nil
	}
	return marks
}

func (s *Server) handleFSWrite(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
		JSONError(w, http.StatusBadRequest, "path is required")
		return
	}
	if len(req.Content) > maxFSWriteBytes {
		JSONError(w, http.StatusRequestEntityTooLarge, fsTooLargeMessage)
		return
	}
	project, ok := s.requiredProject(w, r)
	if !ok {
		return
	}
	target, ok := s.fsWriteTarget(w, r, project, req.Path)
	if !ok {
		return
	}
	if !s.writeContainedFile(w, target, []byte(req.Content)) {
		return
	}
	JSONResponse(w, http.StatusOK, map[string]string{"status": "written", "path": target.path})
}

func (s *Server) handleFS(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		path = string(os.PathSeparator)
	}
	includeFiles := r.URL.Query().Get("files") == "1"
	project, _ := s.optionalProject(r)
	taskID := r.URL.Query().Get("task")
	var err error

	worktreePath, rootOK := s.fsWorktreeRoot(w, r, project)
	if !rootOK {
		return
	}
	// Use the form validateFSPath returns for the host listing below, so a
	// symlinked directory cannot expose anything outside the root.
	if worktreePath != "" {
		resolved, err := s.validateFSPath(project, worktreePath, path)
		if err != nil {
			JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		path = resolved
	}

	type fsEntry struct {
		Name      string `json:"name"`
		IsDir     bool   `json:"isDir"`
		Path      string `json:"path"`
		IsGitRepo bool   `json:"isGitRepo"`
	}

	var result struct {
		Path      string    `json:"path"`
		IsGitRepo bool      `json:"isGitRepo"`
		Entries   []fsEntry `json:"entries"`
	}
	result.Path = path
	result.Entries = []fsEntry{}

	// Enforce sandbox if enabled — no fallback to host
	if !s.requireSandboxAvailable(w, project) {
		return
	}
	if project != nil && project.SandboxEnabled {
		// Accepts both host and container paths.
		mountPath := sandbox.MountPath(project.Name)
		sandboxPath := fsSandboxPath(worktreePath, project, path)
		// Single command: ls -pA marks dirs with trailing /, one entry per line
		opts, optsErr := s.fsSandboxOpts(project, taskID)
		if optsErr != nil {
			JSONError(w, http.StatusBadGateway, optsErr.Error())
			return
		}
		var output string
		var err error
		if taskID != "" {
			// ExecTask creates the task's container on demand.
			output, err = s.sandboxMgr.ExecTask(taskID, worktreePath, project, "ls -pA "+shellQuote(sandboxPath), opts)
		} else {
			output, err = s.sandboxMgr.ExecProject(project, "ls -pA "+shellQuote(sandboxPath), opts)
		}
		if err != nil {
			JSONError(w, http.StatusNotFound, err.Error())
			return
		}
		result.Path = sandboxPath
		if sandboxPath != mountPath {
			parent := filepath.Dir(sandboxPath)
			result.Entries = append(result.Entries, fsEntry{Name: "..", IsDir: true, Path: parent})
		}

		for _, line := range strings.Split(output, "\n") {
			entry := strings.TrimSpace(line)
			if entry == "" || entry == "." || entry == ".." {
				continue
			}
			// ls -p appends '/' to directory names
			isDir := strings.HasSuffix(entry, "/")
			name := strings.TrimSuffix(entry, "/")
			if !includeFiles && !isDir {
				continue
			}
			sandboxFullPath := filepath.Join(sandboxPath, name)
			// IsGitRepo stays false: the in-sandbox browser has no use for it.
			result.Entries = append(result.Entries, fsEntry{Name: name, IsDir: isDir, Path: sandboxFullPath})
		}
		sort.SliceStable(result.Entries, func(i, j int) bool {
			return strings.ToLower(result.Entries[i].Name) < strings.ToLower(result.Entries[j].Name)
		})
		JSONResponse(w, http.StatusOK, result)
		return
	}
	// Host filesystem (no project or sandbox disabled). A project's path was already contained
	// to its checkout plus the named task worktree, and a registered project may live outside
	// home; with no project the path came from the query string, so HostRoot contains it.
	var entries []fs.DirEntry
	if project == nil {
		entries, err = s.hostRoot.ReadDir(path)
		if err != nil && fsutil.IsHostRootRefusal(err) {
			JSONError(w, http.StatusForbidden, err.Error())
			return
		}
	} else {
		entries, err = os.ReadDir(path)
	}
	if err != nil {
		logging.For(logging.HTTP).Errorf("fs readdir failed: %v", err)
		JSONError(w, http.StatusNotFound, "directory not found")
		return
	}

	// One git process for the directory itself answers it for every entry too:
	// `git rev-parse` walks up the tree, so if this directory is inside a
	// repository so is everything in it. Otherwise an entry qualifies only by
	// carrying a .git of its own, which is a stat. (A rev-parse per entry cost
	// 200+ git processes for a 200-directory listing.)
	isGit := git.IsGitRepo(path)
	result.IsGitRepo = isGit

	// A jail boundary has no parent to offer. IsRoot is false when the jail is widened, so the
	// picker keeps its old behaviour then.
	if path != string(os.PathSeparator) && !s.hostRoot.IsRoot(path) {
		if parent := filepath.Dir(path); parent != path {
			result.Entries = append(result.Entries, fsEntry{Name: "..", IsDir: true, Path: parent, IsGitRepo: git.IsGitRepo(parent)})
		}
	}

	for _, e := range entries {
		if !includeFiles && !e.IsDir() {
			continue
		}
		fullPath := filepath.Join(path, e.Name())
		result.Entries = append(result.Entries, fsEntry{
			Name:      e.Name(),
			IsDir:     e.IsDir(),
			Path:      fullPath,
			IsGitRepo: isGit || git.HasGitDir(fullPath),
		})
	}

	sort.SliceStable(result.Entries, func(i, j int) bool {
		return strings.ToLower(result.Entries[i].Name) < strings.ToLower(result.Entries[j].Name)
	})

	JSONResponse(w, http.StatusOK, result)
}

// handleFSSearch recursively searches for files by name and, with content=1, by content.
//
// Query parameters:
//   - q: search query (required)
//   - project: project ID (required)
//   - task: optional task ID for a worktree-scoped search
//   - content: "1" to search file contents in addition to filenames
//   - case, word, regex: "1" to match case-sensitively, whole words, or a regular expression
//
// Filename search returns up to 10 results, content search up to 50, skipping binary files. A
// dot-prefixed file or directory is never searched; see searchSkipsName.
func (s *Server) handleFSSearch(w http.ResponseWriter, r *http.Request) {

	projectID := r.URL.Query().Get("project")
	if projectID == "" {
		JSONError(w, http.StatusBadRequest, "project parameter is required")
		return
	}

	q := strings.TrimSpace(r.URL.Query().Get("q"))
	taskID := r.URL.Query().Get("task")
	searchContent := r.URL.Query().Get("content") == "1"
	matchCase := r.URL.Query().Get("case") == "1"
	wholeWord := r.URL.Query().Get("word") == "1"
	useRegex := r.URL.Query().Get("regex") == "1"

	var contentRe *regexp.Regexp
	if searchContent && useRegex && q != "" {
		flags := ""
		if !matchCase {
			flags = "(?i)"
		}
		var err error
		contentRe, err = regexp.Compile(flags + q)
		if err != nil {
			JSONError(w, http.StatusBadRequest, "invalid regex: "+err.Error())
			return
		}
	}

	// The project bounds the walk whichever root is used, so one lookup covers both branches.
	project, ok := ResolveProjectID(w, s.store, projectID)
	if !ok {
		return
	}
	var searchRoot string

	if taskID != "" {
		// The task must exist and belong to this project.
		task, taskErr := s.store.GetTask(projectID, taskID)
		if taskErr != nil {
			Fail(w, taskErr, "get task", "task not found")
			return
		}
		_ = task // task exists and belongs to project
		searchRoot, err := s.executor.WorktreeMgr().WorktreePath(taskID)
		if err != nil {
			logging.For(logging.HTTP).Warnf("fs resolve worktree failed: %v", err)
			Fail(w, err, "resolve worktree", "failed to resolve worktree path")
			return
		}
		// The path is server-derived; only check it stays under the workspace dir.
		searchRoot = filepath.Clean(searchRoot)
		if cleanWT := s.configStore.WorkspaceDir(); cleanWT != "" {
			if !strings.HasPrefix(searchRoot, cleanWT+string(filepath.Separator)) && searchRoot != cleanWT {
				JSONError(w, http.StatusForbidden, "worktree path outside allowed workspace")
				return
			}
		}
	} else {
		searchRoot = project.Path
		searchRoot = filepath.Clean(searchRoot)
		resolved, err := s.validateFSPath(project, "", searchRoot)
		if err != nil {
			JSONError(w, http.StatusForbidden, err.Error())
			return
		}
		searchRoot = resolved
	}
	if !s.requireSandboxAvailable(w, project) {
		return
	}

	resultLimit := 10
	if searchContent {
		resultLimit = 50
	}

	// The predicate is compiled once and the walk owns the walk order, the ignores and the limit; the
	// handler keeps the part only it can do, which is deciding what directory the request may see.
	results := fsSearch{
		root:      searchRoot,
		query:     q,
		matchCase: matchCase,
		wholeWord: wholeWord,
		useRegex:  useRegex,
		content:   searchContent,
		contentRe: contentRe,
		limit:     resultLimit,
	}.walk(r.Context())

	if results == nil {
		results = []searchResult{}
	}
	JSONResponse(w, http.StatusOK, map[string]any{
		"root":    searchRoot,
		"results": results,
	})
}

// searchMaxFileBytes: with no matches to stop at, a content search would read the whole file.
const searchMaxFileBytes int64 = 2 << 20

// searchContentInFile searches for matches within a single file.
// Returns true if the file was binary (content search skipped).
func searchContentInFile(path, q string, matchCase, wholeWord, useRegex bool, contentRe *regexp.Regexp, onMatch func(lineNum int, lineText string)) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	// Binary sniff: a null byte in the first 8KB means the file is skipped.
	buf := make([]byte, 8192)
	n, _ := f.Read(buf)
	if n > 0 {
		for _, b := range buf[:n] {
			if b == 0 {
				return true // binary file
			}
		}
	}

	f.Seek(0, io.SeekStart)
	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		if matchLine(line, q, matchCase, wholeWord, useRegex, contentRe) {
			onMatch(lineNum, line)
		}
	}
	return false
}

// matchLine checks if a line matches the search query.
func matchLine(line, q string, matchCase, wholeWord, useRegex bool, contentRe *regexp.Regexp) bool {
	if q == "" {
		return true
	}
	if useRegex && contentRe != nil {
		if wholeWord {
			// contentRe is already compiled, so check word boundaries around each match.
			matches := contentRe.FindAllStringIndex(line, -1)
			for _, m := range matches {
				beforeOK := m[0] == 0 || !types.IsWordByte(line[m[0]-1])
				afterOK := m[1] >= len(line) || !types.IsWordByte(line[m[1]])
				if beforeOK && afterOK {
					return true
				}
			}
			return false
		}
		return contentRe.MatchString(line)
	}
	if useRegex {
		// contentRe is only compiled for content search.
		flags := ""
		if !matchCase {
			flags = "(?i)"
		}
		re, err := regexp.Compile(flags + q)
		if err != nil {
			return false
		}
		return re.MatchString(line)
	}
	// Plain text search
	checkLine := line
	searchStr := q
	if !matchCase {
		checkLine = strings.ToLower(checkLine)
		searchStr = strings.ToLower(searchStr)
	}
	if wholeWord {
		idx := 0
		for idx <= len(checkLine)-len(searchStr) {
			pos := strings.Index(checkLine[idx:], searchStr)
			if pos == -1 {
				break
			}
			absPos := idx + pos
			beforeOK := absPos == 0 || !types.IsWordByte(checkLine[absPos-1])
			afterPos := absPos + len(searchStr)
			afterOK := afterPos >= len(checkLine) || !types.IsWordByte(checkLine[afterPos])
			if beforeOK && afterOK {
				return true
			}
			idx = absPos + 1
		}
		return false
	}
	return strings.Contains(checkLine, searchStr)
}

// errStopWalk is a sentinel error to stop filepath.WalkDir early.
var errStopWalk = fmt.Errorf("stop walk: limit reached")

// optionalProject looks up the project from the "project" query param.
func (s *Server) optionalProject(r *http.Request) (*types.Project, error) {
	projectID := r.URL.Query().Get("project")
	if projectID == "" {
		return nil, nil
	}
	project, err := s.store.GetProject(projectID)
	if err != nil {
		return nil, fmt.Errorf("project not found")
	}
	return project, nil
}

// requiredProject is optionalProject for the file endpoints that cannot work without one: the
// project is what the path is jailed to, so a request naming none has nothing to jail against.
// ok is false after writing the response.
func (s *Server) requiredProject(w http.ResponseWriter, r *http.Request) (*types.Project, bool) {
	project, err := s.optionalProject(r)
	if err != nil {
		JSONError(w, http.StatusBadRequest, err.Error())
		return nil, false
	}
	if project == nil {
		JSONError(w, http.StatusBadRequest, "project parameter is required")
		return nil, false
	}
	return project, true
}

// fsWorktreeRoot answers which host directory a file request operates on: the task's
// worktree when ?task names one, otherwise the project checkout. An empty root means
// neither was supplied, which /api/fs allows for a bare browse. ok is false after
// writing the response.
func (s *Server) fsWorktreeRoot(w http.ResponseWriter, r *http.Request, project *types.Project) (root string, ok bool) {
	taskID := r.URL.Query().Get("task")
	if taskID == "" {
		if project != nil {
			return project.Path, true
		}
		return "", true
	}
	if err := s.requireTaskOwnership(project, taskID); err != nil {
		if errs.IsNotFound(err) {
			Fail(w, err, "get task", "task not found in project")
			return "", false
		}
		JSONError(w, http.StatusBadRequest, err.Error())
		return "", false
	}
	wtPath, err := s.executor.WorktreeMgr().WorktreePath(taskID)
	if err != nil {
		Fail(w, err, "resolve worktree", "task worktree not found")
		return "", false
	}
	return wtPath, true
}

// requireTaskOwnership ties the two independent query parameters of an fs request
// together: ?project and ?task arrive uncorrelated, so without this a request could
// name one of its projects and another project's task, aiming every file operation —
// and, for a sandboxed project, every command — at that task's worktree and container.
func (s *Server) requireTaskOwnership(project *types.Project, taskID string) error {
	if taskID == "" {
		return nil
	}
	if project == nil {
		return fmt.Errorf("the task parameter requires a project")
	}
	if _, err := s.store.GetTask(project.ID, taskID); err != nil {
		return fmt.Errorf("task %q not found in project %q: %w", taskID, project.ID, err)
	}
	return nil
}

// requireSandboxAvailable refuses a request whose project wants a sandbox the server
// cannot run. There is no fallback to the host: a sandboxed project's files are only ever
// read and written inside its container, so serving one from the host would be the escape,
// not a degraded mode. ok is false after writing the response.
func (s *Server) requireSandboxAvailable(w http.ResponseWriter, project *types.Project) bool {
	if project == nil || !project.SandboxEnabled {
		return true
	}
	if !s.sandboxMgr.IsAvailable() {
		JSONError(w, http.StatusBadGateway, "sandbox is enabled but not available")
		return false
	}
	return true
}

// fsSandboxPath returns the in-container path for p, which may arrive either as
// a host path under the worktree or as a container path under the project's mount.
func fsSandboxPath(worktreePath string, project *types.Project, p string) string {
	clean := filepath.Clean(p)
	if _, ok := sandbox.TrimMountPrefix(project.Name, clean); ok {
		return clean // already a container path
	}
	return sandbox.HostToContainer(worktreePath, project.Name, clean)
}

// projectFSRoots is what a project-scoped file request is contained to: the checkout plus,
// when the request names a task, that task's worktree. Never the workspace dir.
func projectFSRoots(project *types.Project, worktreeRoot string) []string {
	roots := []string{filepath.Clean(project.Path)}
	if worktreeRoot == "" {
		return roots
	}
	if wt := filepath.Clean(worktreeRoot); wt != roots[0] {
		roots = append(roots, wt)
	}
	return roots
}

// validateFSPath returns the form of path that is safe to open, jailed to projectFSRoots.
// Callers MUST open the returned path.
//
// A refusal is logged with both paths and returned as fsutil.ErrOutside alone, because every
// caller answers the browser with err.Error(): the host path a symlink committed into a
// repository resolves to is the server's to read, not the requester's.
func (s *Server) validateFSPath(project *types.Project, worktreeRoot, path string) (string, error) {
	if project == nil {
		return path, nil // no project constraint
	}
	cleanPath := filepath.Clean(path)

	// A container path is accepted only inside this project's mount.
	if strings.HasPrefix(cleanPath, sandbox.MountRoot+string(filepath.Separator)) {
		if project.SandboxEnabled {
			if _, ok := sandbox.TrimMountPrefix(project.Name, cleanPath); ok {
				return cleanPath, nil
			}
		}
		return "", fmt.Errorf("path outside allowed project directory")
	}

	resolved, err := fsutil.Contained(cleanPath, projectFSRoots(project, worktreeRoot))
	if err != nil {
		logging.For(logging.HTTP).Warnf("file path refused: %v", err)
		return "", fsutil.ErrOutside
	}
	return resolved, nil
}

// fsTarget is a contained write destination and the roots its parent creation stays inside.
type fsTarget struct {
	path  string
	roots []string
}

// fsScope is what a request settles before touching any path: which tree it writes into, and which
// roots nothing may leave.
type fsScope struct {
	project      *types.Project
	worktreePath string
	roots        []string
}

// fsWriteScope answers the request-level questions every write shares: which tree it names (?task=
// must belong to ?project=), and whether the host can run that tree's sandbox. The order matters,
// because each check is a precondition of the next. project must not be nil, and ok is false after
// the response is written.
func (s *Server) fsWriteScope(w http.ResponseWriter, r *http.Request, project *types.Project) (fsScope, bool) {
	worktreePath, ok := s.fsWorktreeRoot(w, r, project)
	if !ok {
		return fsScope{}, false
	}
	if !s.requireSandboxAvailable(w, project) {
		return fsScope{}, false
	}
	return fsScope{project: project, worktreePath: worktreePath, roots: projectFSRoots(project, worktreePath)}, true
}

// fsScopeTarget resolves one path of a scope to the host file it names, or an error when that file
// is outside the scope's roots once symlinks are followed. Callers MUST open the returned path.
func (s *Server) fsScopeTarget(scope fsScope, path string) (fsTarget, error) {
	// Convert the container path first: only a host path can be checked.
	if scope.project.SandboxEnabled {
		if host, ok := sandbox.ContainerToHost(scope.worktreePath, scope.project.Name, path); ok {
			path = host
		}
	}
	// Writing through a symlink planted in the project would land wherever it points, so use the
	// path validateFSPath resolved.
	resolved, err := s.validateFSPath(scope.project, scope.worktreePath, path)
	if err != nil {
		return fsTarget{}, err
	}
	return fsTarget{path: resolved, roots: scope.roots}, nil
}

// fsWriteTarget is one path through both halves: the request's scope, then the target inside it.
// ok is false after the response is written.
func (s *Server) fsWriteTarget(w http.ResponseWriter, r *http.Request, project *types.Project, path string) (fsTarget, bool) {
	scope, ok := s.fsWriteScope(w, r, project)
	if !ok {
		return fsTarget{}, false
	}
	target, err := s.fsScopeTarget(scope, path)
	if err != nil {
		JSONError(w, http.StatusBadRequest, err.Error())
		return fsTarget{}, false
	}
	return target, true
}

// writeContainedFile writes content to a target that fsWriteTarget already contained,
// creating its parent directories on the way. The target must come from fsWriteTarget:
// the parent creation resolves ".." on the host and the task owns that directory, so the
// walk is contained rather than a mkdir. ok is false after writing the response.
func (s *Server) writeContainedFile(w http.ResponseWriter, target fsTarget, content []byte) bool {
	jailed := fsutil.NewContainedRoots(target.roots...)
	if err := jailed.MkdirAll(filepath.Dir(target.path), 0755); err != nil {
		if fsutil.IsHostRootRefusal(err) {
			JSONError(w, http.StatusForbidden, err.Error())
			return false
		}
		logging.For(logging.HTTP).Errorf("fs mkdir failed: %v", err)
		JSONError(w, http.StatusInternalServerError, "create directory failed")
		return false
	}
	// Not a plain write: a save that dies halfway must not leave a truncated file.
	if err := fsutil.WriteFileAtomic(target.path, content); err != nil {
		logging.For(logging.HTTP).Errorf("fs write failed: %v", err)
		JSONError(w, http.StatusInternalServerError, "write file failed")
		return false
	}
	return true
}

// resolveGitIdentity reads the commit identity from git's environment
// (GIT_COMMITTER_* first, then GIT_AUTHOR_*), then from the global git config.
func resolveGitIdentity() (name, email string) {
	// Standard git env vars (GIT_COMMITTER takes precedence over GIT_AUTHOR)
	if name == "" {
		name = os.Getenv("GIT_COMMITTER_NAME")
	}
	if name == "" {
		name = os.Getenv("GIT_AUTHOR_NAME")
	}
	if email == "" {
		email = os.Getenv("GIT_COMMITTER_EMAIL")
	}
	if email == "" {
		email = os.Getenv("GIT_AUTHOR_EMAIL")
	}
	if name != "" && email != "" {
		return name, email
	}

	if name == "" {
		if out, err := git.Command("", "config", "--global", "user.name").CombinedOutput(); err == nil {
			name = strings.TrimSpace(string(out))
		}
	}
	if email == "" {
		if out, err := git.Command("", "config", "--global", "user.email").CombinedOutput(); err == nil {
			email = strings.TrimSpace(string(out))
		}
	}
	return name, email
}

func (s *Server) handleFSInitGit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
		JSONError(w, http.StatusBadRequest, "path is required")
		return
	}
	req.Path = strings.TrimSpace(req.Path)

	project, err := s.optionalProject(r)
	if err != nil {
		JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	// With a project the path is jailed to it like every other fs endpoint; with none the host
	// jail decides.
	if project != nil {
		resolved, err := s.validateFSPath(project, "", req.Path)
		if err != nil {
			JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		req.Path = resolved
	} else if _, err := s.hostRoot.Stat(req.Path); err != nil && fsutil.IsHostRootRefusal(err) {
		// Refuse before git is spawned.
		JSONError(w, http.StatusForbidden, err.Error())
		return
	}

	if !s.requireSandboxAvailable(w, project) {
		return
	}
	// A sandboxed project has no host checkout to write into — no fallback to host.
	if project != nil && project.SandboxEnabled {
		sandboxPath := fsSandboxPath(project.Path, project, req.Path)

		// This is the project's own container rather than a task's, so no agent's approved folders apply.
		opts, optsErr := s.fsSandboxOpts(project, "")
		if optsErr != nil {
			JSONError(w, http.StatusBadGateway, optsErr.Error())
			return
		}

		_, gitErr := s.sandboxMgr.ExecProject(project, "git -C "+shellQuote(sandboxPath)+" rev-parse --git-dir 2>/dev/null", opts)
		if gitErr == nil {
			JSONError(w, http.StatusBadRequest, "path is already a git repository")
			return
		}

		readmeContent := "# " + filepath.Base(req.Path) + "\n"
		encoded := base64.StdEncoding.EncodeToString([]byte(readmeContent))

		name, email := resolveGitIdentity()
		if name == "" || email == "" {
			JSONError(w, http.StatusBadRequest, "git user.name and user.email are not set — set GIT_AUTHOR_NAME/GIT_AUTHOR_EMAIL, or run: git config --global user.name '<value>'")
			return
		}
		// Propagate resolved identity to git package so gitAuthorFlagCmd() includes -c flags
		git.SetAuthorIdentity(name, email)

		// git init + create README + add + commit in single command
		flagCmd := gitAuthorFlagCmd()
		_, err := s.sandboxMgr.ExecProjectWithInput(project,
			"git -C "+shellQuote(sandboxPath)+" init && "+
				"base64 -d > "+shellQuote(filepath.Join(sandboxPath, "README.md"))+" && "+
				"git -C "+shellQuote(sandboxPath)+" add README.md && "+
				"git -C "+shellQuote(sandboxPath)+" "+flagCmd+" commit -m 'Initial commit'",
			[]byte(encoded+"\n"), opts)
		if err != nil {
			Fault(w, "git commit", err)
			return
		}
		JSONResponse(w, http.StatusOK, map[string]string{"status": "ok", "path": req.Path})
		return
	}
	// Host filesystem (no project or sandbox disabled)
	info, err := os.Stat(req.Path)
	if err != nil {
		JSONError(w, http.StatusBadRequest, "path does not exist: "+err.Error())
		return
	}
	if !info.IsDir() {
		JSONError(w, http.StatusBadRequest, "path is not a directory")
		return
	}
	if git.IsGitRepo(req.Path) {
		JSONError(w, http.StatusBadRequest, "path is already a git repository")
		return
	}

	name, email := resolveGitIdentity()
	if name == "" {
		JSONError(w, http.StatusBadRequest, "git user.name is not set — set GIT_AUTHOR_NAME, or run: git config --global user.name '<value>'")
		return
	}
	if email == "" {
		JSONError(w, http.StatusBadRequest, "git user.email is not set — set GIT_AUTHOR_EMAIL, or run: git config --global user.email '<value>'")
		return
	}

	cmd := git.Command("", "-C", req.Path, "init")
	if out, err := cmd.CombinedOutput(); err != nil {
		JSONError(w, http.StatusInternalServerError, fmt.Sprintf("git init failed: %s", out))
		return
	}

	readmePath := filepath.Join(req.Path, "README.md")
	if err := os.WriteFile(readmePath, []byte("# "+filepath.Base(req.Path)+"\n"), 0644); err != nil {
		Fault(w, "create README.md", err)
		return
	}

	cmd = git.Command("", "-C", req.Path, "add", "README.md")
	if out, err := cmd.CombinedOutput(); err != nil {
		JSONError(w, http.StatusInternalServerError, fmt.Sprintf("git add failed: %s", out))
		return
	}
	commitArgs := []string{"-C", req.Path, "-c", "user.name=" + name, "-c", "user.email=" + email, "commit", "-m", "Initial commit"}
	cmd = git.Command("", commitArgs...)
	if out, err := cmd.CombinedOutput(); err != nil {
		JSONError(w, http.StatusInternalServerError, fmt.Sprintf("git commit failed: %s", out))
		return
	}

	JSONResponse(w, http.StatusOK, map[string]string{"status": "ok", "path": req.Path})
}

func (s *Server) handleFSMkdir(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
		JSONError(w, http.StatusBadRequest, "path is required")
		return
	}
	req.Path = strings.TrimSpace(req.Path)

	project, err := s.optionalProject(r)
	if err != nil {
		JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	taskID := r.URL.Query().Get("task")

	worktreePath, rootOK := s.fsWorktreeRoot(w, r, project)
	if !rootOK {
		return
	}

	// Enforce sandbox if enabled — no fallback to host
	if !s.requireSandboxAvailable(w, project) {
		return
	}
	if project != nil && project.SandboxEnabled {
		// Contain the path first: fsSandboxPath joins it relative to the mount, and a path
		// outside the worktree becomes "../../.." that filepath.Join cleans away.
		resolved, err := s.validateFSPath(project, worktreePath, req.Path)
		if err != nil {
			JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		req.Path = resolved
		sandboxPath := fsSandboxPath(worktreePath, project, req.Path)
		sandboxParent := filepath.Dir(sandboxPath)

		// Combined check + mkdir in single command
		opts, optsErr := s.fsSandboxOpts(project, taskID)
		if optsErr != nil {
			JSONError(w, http.StatusBadGateway, optsErr.Error())
			return
		}
		cmd := "if [ ! -d " + shellQuote(sandboxParent) + "; then echo 'PARENT_MISSING'; " +
			"elif [ -e " + shellQuote(sandboxPath) + "; then echo 'ALREADY_EXISTS'; " +
			"else mkdir " + shellQuote(sandboxPath) + " && echo 'OK'; fi"
		var output string
		var execErr error
		if taskID != "" {
			// Use task sandbox (creates container on demand)
			output, execErr = s.sandboxMgr.ExecTask(taskID, worktreePath, project, cmd, opts)
		} else {
			output, execErr = s.sandboxMgr.ExecProject(project, cmd, opts)
		}
		if execErr != nil {
			Fault(w, "create directory", execErr)
			return
		}
		trimmed := strings.TrimSpace(output)
		switch trimmed {
		case "PARENT_MISSING":
			JSONError(w, http.StatusBadRequest, "parent directory does not exist")
			return
		case "ALREADY_EXISTS":
			JSONError(w, http.StatusBadRequest, "path already exists")
			return
		case "OK":
			// success
		default:
			// Odd output (e.g. a shell banner) can still mean success — verify first.
			var checkOut string
			var checkErr error
			checkCmd := "test -d " + shellQuote(sandboxPath) + " && echo EXISTS"
			if taskID != "" {
				checkOut, checkErr = s.sandboxMgr.ExecTask(taskID, worktreePath, project, checkCmd, opts)
			} else {
				checkOut, checkErr = s.sandboxMgr.ExecProject(project, checkCmd, opts)
			}
			if checkErr == nil && strings.TrimSpace(checkOut) == "EXISTS" {
				// Directory exists despite weird output — treat as success
				break
			}
			logging.For(logging.HTTP).Infof("mkdir unexpected output for %s: %q (check: %q)", sandboxPath, output, checkOut)
			JSONError(w, http.StatusInternalServerError, "unexpected response from mkdir")
			return
		}
		JSONResponse(w, http.StatusOK, map[string]string{"status": "ok", "path": req.Path})
		return
	}
	// Host filesystem (no project or sandbox disabled): the picker's path, so everything below
	// goes through fsutil.HostRoot instead of os.
	stat := os.Stat
	mkdir := func(p string) error { return os.MkdirAll(p, 0755) }
	// hostRefused answers a jail refusal wherever it surfaced — on the path, its parent, or the
	// mkdir walk. Unreported it arrives as a plausible 400 or a 500 instead of a 403.
	hostRefused := func(err error) bool {
		if err != nil && project == nil && fsutil.IsHostRootRefusal(err) {
			JSONError(w, http.StatusForbidden, err.Error())
			return true
		}
		return false
	}
	if project != nil {
		resolved, err := s.validateFSPath(project, worktreePath, req.Path)
		if err != nil {
			JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		req.Path = resolved
	} else {
		stat = s.hostRoot.Stat
		mkdir = func(p string) error { return s.hostRoot.MkdirAll(p, 0755) }
		// Stat the requested path before its parent so a jail refusal is not blamed on the parent.
		if _, err := s.hostRoot.Stat(req.Path); err != nil && !errors.Is(err, fs.ErrNotExist) && hostRefused(err) {
			return
		}
	}
	parent := filepath.Dir(req.Path)
	info, err := stat(parent)
	if hostRefused(err) {
		return
	}
	if err != nil || !info.IsDir() {
		JSONError(w, http.StatusBadRequest, "parent directory does not exist")
		return
	}

	if _, err := stat(req.Path); err == nil {
		JSONError(w, http.StatusBadRequest, "path already exists")
		return
	}

	if err := mkdir(req.Path); err != nil {
		if hostRefused(err) {
			return
		}
		Fault(w, "create directory", err)
		return
	}

	JSONResponse(w, http.StatusOK, map[string]string{"status": "ok", "path": req.Path})
}

// fsSandboxOpts returns the sandbox config for FS operations, having made sure the project's image
// exists — opening an explorer is often the first thing to touch it, and the build output goes out
// over the project's WebSocket so the explorer can show progress while this request blocks.
func (s *Server) fsSandboxOpts(project *types.Project, taskID string) (sandbox.SandboxOpts, error) {
	image, err := s.sandboxMgr.EnsureImageForProject(project, func(status types.SandboxImageStatus) {
		if s.appStreamMgr != nil {
			s.appStreamMgr.SendProjectEvent(project.ID, types.StreamEvent{
				Type: types.WSTypeSandboxImageStatus,
				Data: status,
			})
		}
	})
	if err != nil {
		return sandbox.SandboxOpts{}, err
	}
	opts := sandbox.ProjectOpts(project)
	opts.BaseImage = image
	opts.AgentSandbox = s.agentGrantForTask(project.ID, taskID)
	return opts, nil
}

// agentGrantForTask returns nothing for an empty taskID: a project container or a plain file browse has no
// agent of its own.
func (s *Server) agentGrantForTask(projectID, taskID string) *types.AgentSandbox {
	if taskID == "" {
		return nil
	}
	task, err := s.store.GetTask(projectID, taskID)
	if err != nil {
		return nil
	}
	return s.executor.AgentRegistry().AgentSandboxForURL(task.LlamaServerURL)
}

// gitAuthorFlagCmd quotes git.GitAuthorFlags() into a fragment for a sandbox shell command.
func gitAuthorFlagCmd() string {
	flags := git.GitAuthorFlags()
	var parts []string
	for i := 0; i+1 < len(flags); i += 2 {
		parts = append(parts, shellQuote(flags[i]), shellQuote(flags[i+1]))
	}
	return strings.Join(parts, " ")
}

// searchResult is one matching file, or one matching line of one file when the search reads contents.
type searchResult struct {
	Path string `json:"path"`           // relative to search root
	Line int    `json:"line,omitempty"` // 1-based line number (content search)
	Text string `json:"text,omitempty"` // matching line content (content search)
}

// fsSearch is one search request: a root the handler already decided the caller may see, plus the
// predicate compiled once for the whole walk. The patterns are built up front because the walk asks them
// per file, and an invalid filename pattern has no safe answer mid-walk — it matches nothing, which is the
// same answer an empty result gives.
type fsSearch struct {
	root      string
	query     string
	matchCase bool
	wholeWord bool
	useRegex  bool
	content   bool
	contentRe *regexp.Regexp // content+regex: compiled by the caller, which answers 400 when it fails
	nameRe    *regexp.Regexp // nil when the pattern does not compile
	limit     int
}

// matchesFile is the filename half of the predicate.
func (f fsSearch) matchesFile(rel string) bool {
	if f.query == "" {
		return true
	}
	if f.useRegex {
		if f.nameRe == nil {
			return false // invalid pattern
		}
		if f.wholeWord {
			return f.nameRe.MatchString("\b" + rel + "\b")
		}
		return f.nameRe.MatchString(rel)
	}
	checkStr := rel
	searchStr := f.query
	if !f.matchCase {
		checkStr = strings.ToLower(checkStr)
		searchStr = strings.ToLower(searchStr)
	}
	if f.wholeWord {
		// A whole word must equal one path segment.
		for _, part := range strings.Split(checkStr, "/") {
			if part == searchStr {
				return true
			}
		}
		return false
	}
	return strings.Contains(checkStr, searchStr)
}

// searchSkipsName reports whether a search stops at an entry: a dot-prefixed name is tool or editor
// state, and the file picker hides those behind its own option (FileBrowser.svelte). The rule covers
// .git, .vscode and .idea, which stay out of git.ignoredDirs because the watchers and the diff
// broadcasts also read that set, where a person's editor settings must remain reviewable.
func searchSkipsName(name string) bool {
	return strings.HasPrefix(name, ".")
}

// walk collects the matches under the root, in a stable order, stopping at the limit or when ctx ends.
// It reads the host filesystem even for a sandboxed task, because the worktree is bind-mounted into the
// sandbox and both sides see the same files.
func (f fsSearch) walk(ctx context.Context) []searchResult {
	if f.useRegex && f.query != "" {
		flags := ""
		if !f.matchCase {
			flags = "(?i)"
		}
		// An invalid pattern matches nothing rather than failing the request; the content search is the
		// one that reports a bad pattern, because it is compiled before the walk starts.
		f.nameRe, _ = regexp.Compile(flags + f.query)
	}

	var results []searchResult
	_ = filepath.WalkDir(f.root, func(path string, d os.DirEntry, err error) error {
		if ctx.Err() != nil {
			return errStopWalk
		}
		if err != nil {
			return nil // skip unreadable entries
		}
		rel, _ := filepath.Rel(f.root, path)
		if rel == "." {
			return nil
		}

		// WalkDir reports a link without following it, so both linked files and
		// linked directories land here; opening one would read through the host.
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}

		if searchSkipsName(d.Name()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if git.ShouldIgnorePath(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if d.IsDir() {
			return nil
		}

		// Content search ignores the filename filter.
		if f.content {
			if info, infoErr := d.Info(); infoErr == nil && info.Size() > searchMaxFileBytes {
				return nil
			}
			searchContentInFile(path, f.query, f.matchCase, f.wholeWord, f.useRegex, f.contentRe, func(lineNum int, lineText string) {
				results = append(results, searchResult{Path: rel, Line: lineNum, Text: lineText})
			})
		} else if f.matchesFile(rel) {
			results = append(results, searchResult{Path: rel})
		}

		if len(results) >= f.limit {
			return errStopWalk
		}
		return nil
	})

	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Path != results[j].Path {
			return strings.ToLower(results[i].Path) < strings.ToLower(results[j].Path)
		}
		return results[i].Line < results[j].Line
	})
	return results
}
