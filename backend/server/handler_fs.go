package server

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"kanban/backend/fsutil"
	"kanban/backend/git"
	"kanban/backend/prompts"
	"kanban/backend/sandbox"
	"kanban/backend/types"
)

func (s *Server) handleFSRead(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		JSONError(w, http.StatusBadRequest, "path is required")
		return
	}
	project, err := s.optionalProject(r)
	if err != nil {
		JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if project == nil {
		JSONError(w, http.StatusBadRequest, "project parameter is required")
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
	hostPath, err := s.validateFSPath(project, path)
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
	var readErr error
	if ref != "" {
		// --end-of-options (git >= 2.24) is a second line of defence: everything after
		// it is parsed as a revision, never as an option.
		const endOfOptions = "--end-of-options"
		var output string
		if taskID != "" {
			relPath, relErr := filepath.Rel(worktreePath, path)
			if relErr != nil {
				JSONError(w, http.StatusBadRequest, relErr.Error())
				return
			}
			output, readErr = s.executor.WorktreeMgr().GitRunInWorktreeOutput(taskID, "show", endOfOptions, ref+":"+relPath)
		} else {
			relPath, relErr := filepath.Rel(project.Path, path)
			if relErr != nil {
				JSONError(w, http.StatusBadRequest, relErr.Error())
				return
			}
			output, readErr = s.executor.WorktreeMgr().GitRunInDirOutput(worktreePath, "show", endOfOptions, ref+":"+relPath)
		}
		if readErr != nil {
			JSONError(w, http.StatusNotFound, "file not found at ref "+ref)
			return
		}
		content = []byte(output)
	} else {
		// Host filesystem (worktree is bind-mounted into sandbox)
		// Check if path exists and is a regular file
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
		content, readErr = os.ReadFile(hostPath)
		if readErr != nil {
			log.Printf("fs read failed: %v", readErr)
			JSONError(w, http.StatusInternalServerError, "failed to read file")
			return
		}
	}
	if len(content) > 1_000_000 {
		JSONError(w, http.StatusRequestEntityTooLarge, "file too large (max 1MB)")
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

// computeGutterMarkers runs `git diff HEAD -- <path>` and parses the unified diff
// into per-line markers indexed by the current (working tree) line number (1-based).
// Returns nil if the file is untracked, the repo has no commits, or git is unavailable.
func computeGutterMarkers(s *Server, worktreePath, path, taskID string) map[int][]string {
	relPath, err := filepath.Rel(worktreePath, path)
	if err != nil {
		return nil
	}
	var diffOutput string
	if taskID != "" {
		diffOutput, err = s.executor.WorktreeMgr().GitRunInWorktreeOutput(taskID, "diff", "HEAD", "--", relPath)
	} else {
		diffOutput, err = s.executor.WorktreeMgr().GitRunInDirOutput(worktreePath, "diff", "HEAD", "--", relPath)
	}
	if err != nil || diffOutput == "" {
		return nil
	}
	return parseUnifiedDiff(diffOutput)
}

// parseUnifiedDiff parses a unified diff and returns per-line markers for the
// current (working tree) file. Line numbers are 1-based. Marker types:
// "added", "removed", "modified".
func parseUnifiedDiff(diff string) map[int][]string {
	markers := make(map[int][]string)
	lines := strings.Split(diff, "\n")
	// Remove trailing empty element from final newline
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	newLine := 0 // current line number in working tree (0-based internally)
	hunkOldStart := 0
	hunkOldCount := 0
	hunkNewStart := 0
	hunkNewCount := 0
	inHunk := false
	hunkOldPos := 0 // lines consumed from old range
	hunkNewPos := 0 // lines consumed from new range
	var pendingRemovals int

	for _, line := range lines {
		// Detect hunk header: @@ -oldStart,oldCount +newStart,newCount @@
		if strings.HasPrefix(line, "@@ ") {
			_, _ = fmt.Sscanf(line, "@@ -%d,%d +%d,%d @@", &hunkOldStart, &hunkOldCount, &hunkNewStart, &hunkNewCount)
			newLine = hunkNewStart - 1 // 0-based
			inHunk = true
			hunkOldPos = 0
			hunkNewPos = 0
			pendingRemovals = 0
			continue
		}
		if !inHunk {
			continue
		}

		if strings.HasPrefix(line, "+") {
			if pendingRemovals > 0 {
				// Paired with a removal → modified
				markers[newLine+1] = append(markers[newLine+1], "modified")
				pendingRemovals--
			} else {
				markers[newLine+1] = append(markers[newLine+1], "added")
			}
			newLine++
			hunkNewPos++
		} else if strings.HasPrefix(line, "-") {
			pendingRemovals++
			hunkOldPos++
		} else {
			// context line
			if pendingRemovals > 0 {
				// Removals followed by context — mark removed on the line before
				if newLine > 0 {
					markers[newLine] = append(markers[newLine], "removed")
				}
				pendingRemovals = 0
			}
			newLine++
			hunkOldPos++
			hunkNewPos++
		}

		// Check if hunk is exhausted
		if hunkOldPos >= hunkOldCount && hunkNewPos >= hunkNewCount {
			inHunk = false
			// Flush remaining removals at end of hunk
			if pendingRemovals > 0 && newLine > 0 {
				markers[newLine] = append(markers[newLine], "removed")
				pendingRemovals = 0
			}
		}
	}

	// Flush remaining removals at end of diff
	if pendingRemovals > 0 && newLine > 0 {
		markers[newLine] = append(markers[newLine], "removed")
	}

	if len(markers) == 0 {
		return nil
	}
	return markers
}

func (s *Server) handleFSWrite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
		JSONError(w, http.StatusBadRequest, "path is required")
		return
	}
	if len(req.Content) > 1_000_000 {
		JSONError(w, http.StatusRequestEntityTooLarge, "file too large (max 1MB)")
		return
	}
	project, err := s.optionalProject(r)
	if err != nil {
		JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if project == nil {
		JSONError(w, http.StatusBadRequest, "project parameter is required")
		return
	}
	worktreePath, ok := s.fsWorktreeRoot(w, r, project)
	if !ok {
		return
	}
	if !s.requireSandboxAvailable(w, project) {
		return
	}
	if project.SandboxEnabled {
		// Translate sandbox path to host path
		if host, ok := sandbox.ContainerToHost(worktreePath, project.Name, req.Path); ok {
			req.Path = host
		}
	}
	// Operate on the symlink-resolved path validateFSPath returns; writing through
	// a link planted in the project would land wherever it points.
	resolvedPath, err := s.validateFSPath(project, req.Path)
	if err != nil {
		JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	req.Path = resolvedPath
	// Host filesystem (worktree is bind-mounted into sandbox)
	dir := filepath.Dir(req.Path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Printf("fs mkdir failed: %v", err)
		JSONError(w, http.StatusInternalServerError, "create directory failed")
		return
	}
	if err := os.WriteFile(req.Path, []byte(req.Content), 0644); err != nil {
		log.Printf("fs write failed: %v", err)
		JSONError(w, http.StatusInternalServerError, "write file failed")
		return
	}
	JSONResponse(w, http.StatusOK, map[string]string{"status": "written", "path": req.Path})
}

func (s *Server) handleFS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		path = string(os.PathSeparator)
	}
	includeFiles := r.URL.Query().Get("files") == "1"
	project, _ := s.optionalProject(r)
	taskID := r.URL.Query().Get("task")

	worktreePath, rootOK := s.fsWorktreeRoot(w, r, project)
	if !rootOK {
		return
	}
	// Use the form validateFSPath returns for the host listing below, so a
	// symlinked directory cannot expose anything outside the root.
	if worktreePath != "" {
		resolved, err := s.validateFSPath(project, path)
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
		opts, optsErr := s.fsSandboxOpts(project)
		if optsErr != nil {
			JSONError(w, http.StatusBadGateway, optsErr.Error())
			return
		}
		var output string
		var err error
		if taskID != "" {
			// Use task sandbox (creates container on demand)
			output, err = s.sandboxMgr.ExecTask(taskID, worktreePath, project, "ls -pA "+shellQuote(sandboxPath), opts)
		} else {
			output, err = s.sandboxMgr.ExecProject(project, "ls -pA "+shellQuote(sandboxPath), opts)
		}
		if err != nil {
			JSONError(w, http.StatusNotFound, err.Error())
			return
		}
		result.Path = sandboxPath
		// IsGitRepo is unused by the in-sandbox browser.

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
			result.Entries = append(result.Entries, fsEntry{Name: name, IsDir: isDir, Path: sandboxFullPath})
		}
		sort.SliceStable(result.Entries, func(i, j int) bool {
			return strings.ToLower(result.Entries[i].Name) < strings.ToLower(result.Entries[j].Name)
		})
		JSONResponse(w, http.StatusOK, result)
		return
	}
	// Host filesystem (no project or sandbox disabled)
	entries, err := os.ReadDir(path)
	if err != nil {
		log.Printf("fs readdir failed: %v", err)
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

	if path != string(os.PathSeparator) {
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

// handleFSSearch recursively searches for files by name and optionally by content.
//
// Query parameters:
//   - q: search query (required)
//   - project: project ID (required)
//   - task: optional task ID for worktree-scoped search
//   - content: "1" to search file contents in addition to filenames
//   - case: "1" for case-sensitive matching
//   - word: "1" for whole-word matching
//   - regex: "1" to treat query as a regular expression
//
// Filename search returns up to 10 results; content search returns up to 50.
// Binary files are skipped for content search but still matched by filename.
func (s *Server) handleFSSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

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

	// Resolve the search root.
	var searchRoot string
	var project *types.Project
	var err error

	if taskID != "" {
		// The task must exist and belong to this project.
		project, err = s.store.GetProject(projectID)
		if err != nil {
			Fail(w, err, "get project", "project not found")
			return
		}
		task, taskErr := s.store.GetTask(projectID, taskID)
		if taskErr != nil {
			Fail(w, taskErr, "get task", "task not found")
			return
		}
		_ = task // task exists and belongs to project
		searchRoot, err = s.executor.WorktreeMgr().WorktreePath(taskID)
		if err != nil {
			log.Printf("fs resolve worktree failed: %v", err)
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
		project, err = s.store.GetProject(projectID)
		if err != nil {
			Fail(w, err, "get project", "project not found")
			return
		}
		searchRoot = project.Path
		searchRoot = filepath.Clean(searchRoot)
		resolved, err := s.validateFSPath(project, searchRoot)
		if err != nil {
			JSONError(w, http.StatusForbidden, err.Error())
			return
		}
		searchRoot = resolved
	}
	if project != nil && project.SandboxEnabled {
		if s.sandboxMgr == nil || !s.sandboxMgr.IsAvailable() {
			JSONError(w, http.StatusBadGateway, "sandbox is enabled but not available")
			return
		}
	}

	resultLimit := 10
	if searchContent {
		resultLimit = 50
	}

	type searchResult struct {
		Path string `json:"path"`           // relative to search root
		Line int    `json:"line,omitempty"` // 1-based line number (content search)
		Text string `json:"text,omitempty"` // matching line content (content search)
	}
	var results []searchResult

	if _, statErr := os.Stat(searchRoot); statErr != nil {
		log.Printf("fs search root stat failed: %v", statErr)
		JSONError(w, http.StatusNotFound, "search root does not exist")
		return
	}

	// Compiled once for the whole walk, not per file. An invalid pattern matches
	// nothing rather than failing the request.
	var filenameRe *regexp.Regexp
	if useRegex && q != "" {
		flags := ""
		if !matchCase {
			flags = "(?i)"
		}
		if re, reErr := regexp.Compile(flags + q); reErr == nil {
			filenameRe = re
		}
	}

	filenameMatch := func(rel string) bool {
		if q == "" {
			return true
		}
		if useRegex {
			if filenameRe == nil {
				return false // invalid pattern
			}
			if wholeWord {
				return filenameRe.MatchString("\b" + rel + "\b")
			}
			return filenameRe.MatchString(rel)
		}
		checkStr := rel
		searchStr := q
		if !matchCase {
			checkStr = strings.ToLower(checkStr)
			searchStr = strings.ToLower(searchStr)
		}
		if wholeWord {
			// Simple whole-word: split by path separators and check each segment
			parts := strings.Split(checkStr, "/")
			for _, part := range parts {
				if part == searchStr {
					return true
				}
			}
			// A whole word must equal one path segment.
			return false
		}
		return strings.Contains(checkStr, searchStr)
	}

	// Host walk: the worktree is bind-mounted into the sandbox, so it sees the same files.
	err = filepath.WalkDir(searchRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		rel, _ := filepath.Rel(searchRoot, path)
		if rel == "." {
			return nil
		}

		// WalkDir reports a link without following it, so both linked files and
		// linked directories land here; opening one would read through the host.
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}

		if strings.HasPrefix(d.Name(), ".") {
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
		if searchContent {
			searchContentInFile(path, q, matchCase, wholeWord, useRegex, contentRe, func(lineNum int, lineText string) {
				results = append(results, searchResult{Path: rel, Line: lineNum, Text: lineText})
			})
		} else if filenameMatch(rel) {
			results = append(results, searchResult{Path: rel})
		}

		if len(results) >= resultLimit {
			return errStopWalk
		}
		return nil
	})
	_ = err // ignore errStopWalk

	// Sort results for deterministic output
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Path != results[j].Path {
			return strings.ToLower(results[i].Path) < strings.ToLower(results[j].Path)
		}
		return results[i].Line < results[j].Line
	})

	if results == nil {
		results = []searchResult{}
	}
	JSONResponse(w, http.StatusOK, map[string]any{
		"root":    searchRoot,
		"results": results,
	})
}

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
				beforeOK := m[0] == 0 || !isWordChar(line[m[0]-1])
				afterOK := m[1] >= len(line) || !isWordChar(line[m[1]])
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
			beforeOK := absPos == 0 || !isWordChar(checkLine[absPos-1])
			afterPos := absPos + len(searchStr)
			afterOK := afterPos >= len(checkLine) || !isWordChar(checkLine[afterPos])
			if beforeOK && afterOK {
				return true
			}
			idx = absPos + 1
		}
		return false
	}
	return strings.Contains(checkLine, searchStr)
}

// isWordChar checks if a byte is a word character (alphanumeric or underscore).
func isWordChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
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

// fsWorktreeRoot answers which host directory a file request operates on: the task's
// worktree when ?task names one, otherwise the project checkout. An empty root means
// neither was supplied, which /api/fs allows for a bare browse.
// ok is false after writing the response, when the task has no worktree.
func (s *Server) fsWorktreeRoot(w http.ResponseWriter, r *http.Request, project *types.Project) (root string, ok bool) {
	if taskID := r.URL.Query().Get("task"); taskID != "" {
		wtPath, err := s.executor.WorktreeMgr().WorktreePath(taskID)
		if err != nil {
			Fail(w, err, "resolve worktree", "task worktree not found")
			return "", false
		}
		return wtPath, true
	}
	if project != nil {
		return project.Path, true
	}
	return "", true
}

// requireSandboxAvailable refuses a request whose project wants a sandbox the server
// cannot run. There is no fallback to the host: a sandboxed project's files are only ever
// read and written inside its container, so serving one from the host would be the escape,
// not a degraded mode. ok is false after writing the response.
func (s *Server) requireSandboxAvailable(w http.ResponseWriter, project *types.Project) bool {
	if project == nil || !project.SandboxEnabled {
		return true
	}
	if s.sandboxMgr == nil || !s.sandboxMgr.IsAvailable() {
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

// validateFSPath checks a path against the project root (or the workspace dir)
// and returns the form that is safe to open. The caller passes the project the
// handler already loaded. Callers MUST open the returned path, not the one they
// passed in.
//
// The lexical prefix check alone is not containment: a symlink inside the project
// resolves against the host, and a sandboxed task writes through an rw bind mount,
// so it can plant `link -> /etc` in the worktree. The path is therefore resolved
// and containment re-checked against the resolved roots.
func (s *Server) validateFSPath(project *types.Project, path string) (string, error) {
	if project == nil {
		return path, nil // no project constraint
	}
	cleanPath := filepath.Clean(path)
	cleanRoot := filepath.Clean(project.Path)

	// A container path is accepted only inside this project's mount.
	if strings.HasPrefix(cleanPath, sandbox.MountRoot+string(filepath.Separator)) {
		if project.SandboxEnabled {
			if _, ok := sandbox.TrimMountPrefix(project.Name, cleanPath); ok {
				return cleanPath, nil
			}
		}
		return "", fmt.Errorf("path outside allowed project directory")
	}

	// WorkspaceDir() avoids the config-row reads GetConfig would do for one path.
	workspaceDir := s.configStore.WorkspaceDir()

	// Host path: allow if within project root or workspace directory…
	roots := []string{cleanRoot}
	if workspaceDir != "" {
		roots = append(roots, workspaceDir)
	}
	if !fsutil.Inside(cleanPath, roots) {
		return "", fmt.Errorf("path outside allowed project directory")
	}
	// …then again for the resolved form, against the resolved roots. The lexical
	// roots stay in the list so a root that itself sits behind a link still works.
	resolved, err := fsutil.ResolveSymlinks(cleanPath)
	if err != nil {
		return "", fmt.Errorf("cannot resolve path: %w", err)
	}
	check := append([]string{}, roots...)
	for _, root := range roots {
		if rr, err := filepath.EvalSymlinks(root); err == nil {
			check = append(check, filepath.Clean(rr))
		}
	}
	if !fsutil.Inside(resolved, check) {
		return "", fmt.Errorf("path resolves outside allowed project directory")
	}
	return resolved, nil
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
		if out, err := exec.Command("git", "config", "--global", "user.name").CombinedOutput(); err == nil {
			name = strings.TrimSpace(string(out))
		}
	}
	if email == "" {
		if out, err := exec.Command("git", "config", "--global", "user.email").CombinedOutput(); err == nil {
			email = strings.TrimSpace(string(out))
		}
	}
	return name, email
}

func (s *Server) handleFSInitGit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
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
	// With a project context the path is jailed to it (or the workspace dir) like every
	// other fs endpoint. The no-project directory-picker flow stays unconstrained, as in
	// handleFSMkdir.
	if project != nil {
		resolved, err := s.validateFSPath(project, req.Path)
		if err != nil {
			JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		req.Path = resolved
	}

	// Enforce sandbox if enabled — no fallback to host
	if project != nil && project.SandboxEnabled {
		if s.sandboxMgr == nil || !s.sandboxMgr.IsAvailable() {
			JSONError(w, http.StatusBadGateway, "sandbox is enabled but not available")
			return
		}
		sandboxPath := fsSandboxPath(project.Path, project, req.Path)

		// Resolve sandbox config once, reuse for both calls
		opts, optsErr := s.fsSandboxOpts(project)
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
		git.GitAuthorName = name
		git.GitAuthorEmail = email

		// git init + create README + add + commit in single command
		flagCmd := gitAuthorFlagCmd()
		_, err := s.sandboxMgr.ExecProjectWithInput(project,
			"git -C "+shellQuote(sandboxPath)+" init && "+
				"base64 -d > "+shellQuote(filepath.Join(sandboxPath, "README.md"))+" && "+
				"git -C "+shellQuote(sandboxPath)+" add README.md && "+
				"git -C "+shellQuote(sandboxPath)+" "+flagCmd+" commit -m 'Initial commit'",
			[]byte(encoded+"\n"), opts)
		if err != nil {
			JSONError(w, http.StatusInternalServerError, "git commit failed: "+err.Error())
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

	cmd := exec.Command("git", "-C", req.Path, "init")
	if out, err := cmd.CombinedOutput(); err != nil {
		JSONError(w, http.StatusInternalServerError, fmt.Sprintf("git init failed: %s", out))
		return
	}

	readmePath := filepath.Join(req.Path, "README.md")
	if err := os.WriteFile(readmePath, []byte("# "+filepath.Base(req.Path)+"\n"), 0644); err != nil {
		JSONError(w, http.StatusInternalServerError, fmt.Sprintf("create README.md: %v", err))
		return
	}

	cmd = exec.Command("git", "-C", req.Path, "add", "README.md")
	if out, err := cmd.CombinedOutput(); err != nil {
		JSONError(w, http.StatusInternalServerError, fmt.Sprintf("git add failed: %s", out))
		return
	}
	commitArgs := []string{"-C", req.Path, "-c", "user.name=" + name, "-c", "user.email=" + email, "commit", "-m", "Initial commit"}
	cmd = exec.Command("git", commitArgs...)
	if out, err := cmd.CombinedOutput(); err != nil {
		JSONError(w, http.StatusInternalServerError, fmt.Sprintf("git commit failed: %s", out))
		return
	}

	JSONResponse(w, http.StatusOK, map[string]string{"status": "ok", "path": req.Path})
}

func (s *Server) handleFSMkdir(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
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
		sandboxPath := fsSandboxPath(worktreePath, project, req.Path)
		sandboxParent := filepath.Dir(sandboxPath)

		// Combined check + mkdir in single command
		opts, optsErr := s.fsSandboxOpts(project)
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
			JSONError(w, http.StatusInternalServerError, fmt.Sprintf("create directory: %v", execErr))
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
			log.Printf("mkdir unexpected output for %s: %q (check: %q)", sandboxPath, output, checkOut)
			JSONError(w, http.StatusInternalServerError, "unexpected response from mkdir")
			return
		}
		JSONResponse(w, http.StatusOK, map[string]string{"status": "ok", "path": req.Path})
		return
	}
	// Host filesystem (no project or sandbox disabled)
	// Project-scoped mkdir must stay within the project (or workspace); the
	// no-project directory-picker flow intentionally has no constraint.
	if project != nil {
		resolved, err := s.validateFSPath(project, req.Path)
		if err != nil {
			JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		req.Path = resolved
	}
	parent := filepath.Dir(req.Path)
	info, err := os.Stat(parent)
	if err != nil || !info.IsDir() {
		JSONError(w, http.StatusBadRequest, "parent directory does not exist")
		return
	}

	if _, err := os.Stat(req.Path); err == nil {
		JSONError(w, http.StatusBadRequest, "path already exists")
		return
	}

	if err := os.MkdirAll(req.Path, 0755); err != nil {
		JSONError(w, http.StatusInternalServerError, fmt.Sprintf("create directory: %v", err))
		return
	}

	JSONResponse(w, http.StatusOK, map[string]string{"status": "ok", "path": req.Path})
}

// fsSandboxOpts returns the sandbox config for FS operations, having made sure the project's image
// exists. Opening an explorer is often the first thing that touches a project's image, and container
// creation runs with --pull=never, so this is where the image arrives — and its output goes out over
// the project's WebSocket channel, which is how the explorer can show progress while this request is
// blocked on the build.
func (s *Server) fsSandboxOpts(project *types.Project) (sandbox.SandboxOpts, error) {
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
	opts := sandbox.SandboxOpts{
		BaseImage:        image,
		NetworkIsolation: project.SandboxNetworkIsolation,
		MemoryLimit:      project.SandboxMemoryLimit,
		ForwardPorts:     project.ForwardPorts,
		AgentSandbox:     s.executor.AgentRegistry().GetAnyAgentSandbox(),
		SkillsDir:        prompts.SkillsDir(),
		SandboxMounts:    project.SandboxMounts,
		SandboxEnvs:      project.SandboxEnvs,
		Capabilities:     project.SandboxCapabilities,
	}
	return opts, nil
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
