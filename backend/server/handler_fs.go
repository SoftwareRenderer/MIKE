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

	// Determine worktree path (host filesystem)
	var worktreePath string
	if taskID != "" {
		wtPath, err := s.executor.WorktreeMgr().WorktreePath(taskID)
		if err != nil {
			JSONError(w, http.StatusNotFound, "task worktree not found")
			return
		}
		worktreePath = wtPath
	} else {
		worktreePath = project.Path
	}
	// Sandbox availability check (required but don't exec inside sandbox)
	if project.SandboxEnabled {
		if s.sandboxMgr == nil || !s.sandboxMgr.IsAvailable() {
			JSONError(w, http.StatusBadGateway, "sandbox is enabled but not available")
			return
		}
		// Translate sandbox path to host path
		mountPath := "/home/sandbox/" + project.Name
		cleanPath := filepath.Clean(path)
		if cleanPath == mountPath || strings.HasPrefix(cleanPath, mountPath+string(os.PathSeparator)) {
			rel := cleanPath[len(mountPath):]
			if rel == "" {
				path = worktreePath
			} else {
				path = filepath.Join(worktreePath, rel)
			}
		}
	}
	// Validate path is within allowed project root
	if err := s.validateFSPath(path, r); err != nil {
		JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Optional: read file at a specific git ref (e.g. HEAD) instead of working tree
	ref := r.URL.Query().Get("ref")
	var content []byte
	var readErr error
	if ref != "" {
		var output string
		if taskID != "" {
			relPath, relErr := filepath.Rel(worktreePath, path)
			if relErr != nil {
				JSONError(w, http.StatusBadRequest, relErr.Error())
				return
			}
			output, readErr = s.executor.WorktreeMgr().GitRunInWorktreeOutput(taskID, "show", ref+":"+relPath)
		} else {
			relPath, relErr := filepath.Rel(project.Path, path)
			if relErr != nil {
				JSONError(w, http.StatusBadRequest, relErr.Error())
				return
			}
			output, readErr = s.executor.WorktreeMgr().GitRunInDirOutput(worktreePath, "show", ref+":"+relPath)
		}
		if readErr != nil {
			JSONError(w, http.StatusNotFound, "file not found at ref "+ref)
			return
		}
		content = []byte(output)
	} else {
		// Host filesystem (worktree is bind-mounted into sandbox)
		// Check if path exists and is a regular file
		info, statErr := os.Stat(path)
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
		content, readErr = os.ReadFile(path)
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
			// Added line
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
			// Removed line
			pendingRemovals++
			hunkOldPos++
		} else {
			// Context line (starts with " ")
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
	// Limit file size to 1MB
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
	taskID := r.URL.Query().Get("task")

	// Determine worktree path (host filesystem)
	var worktreePath string
	if taskID != "" {
		wtPath, err := s.executor.WorktreeMgr().WorktreePath(taskID)
		if err != nil {
			JSONError(w, http.StatusNotFound, "task worktree not found")
			return
		}
		worktreePath = wtPath
	} else {
		worktreePath = project.Path
	}
	// Sandbox availability check (required but don't exec inside sandbox)
	if project.SandboxEnabled {
		if s.sandboxMgr == nil || !s.sandboxMgr.IsAvailable() {
			JSONError(w, http.StatusBadGateway, "sandbox is enabled but not available")
			return
		}
		// Translate sandbox path to host path
		mountPath := "/home/sandbox/" + project.Name
		cleanPath := filepath.Clean(req.Path)
		if cleanPath == mountPath || strings.HasPrefix(cleanPath, mountPath+string(os.PathSeparator)) {
			rel := cleanPath[len(mountPath):]
			if rel == "" {
				req.Path = worktreePath
			} else {
				req.Path = filepath.Join(worktreePath, rel)
			}
		}
	}
	// Validate path is within allowed project root
	if err := s.validateFSPath(req.Path, r); err != nil {
		JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
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

	// Determine worktree path (host filesystem)
	var worktreePath string
	if taskID != "" {
		wtPath, err := s.executor.WorktreeMgr().WorktreePath(taskID)
		if err != nil {
			JSONError(w, http.StatusNotFound, "task worktree not found")
			return
		}
		worktreePath = wtPath
	} else if project != nil {
		worktreePath = project.Path
	}
	// Validate path is within allowed worktree root
	if worktreePath != "" {
		if err := s.validateFSPath(path, r); err != nil {
			JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
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
	if project != nil && project.SandboxEnabled {
		if s.sandboxMgr == nil || !s.sandboxMgr.IsAvailable() {
			JSONError(w, http.StatusBadGateway, "sandbox is enabled but not available")
			return
		}
		// Convert path to sandbox path (accept both host and sandbox paths)
		mountPath := "/home/sandbox/" + project.Name
		cleanPath := filepath.Clean(path)
		var sandboxPath string
		if cleanPath == mountPath || strings.HasPrefix(cleanPath, mountPath+string(os.PathSeparator)) {
			sandboxPath = cleanPath
		} else {
			relPath, _ := filepath.Rel(worktreePath, cleanPath)
			sandboxPath = filepath.Join(mountPath, relPath)
		}
		// Single command: ls -pA marks dirs with trailing /, one entry per line
		opts := s.fsSandboxConfig(project)
		var output string
		var err error
		if taskID != "" {
			// Use task sandbox (creates container on demand)
			output, err = s.sandboxMgr.ExecTask(taskID, worktreePath, project.Name, project.Path, "ls -pA "+shellQuote(sandboxPath), opts)
		} else {
			output, err = s.sandboxMgr.ExecProject(project.ID, project.Path, project.Name, "ls -pA "+shellQuote(sandboxPath), opts)
		}
		if err != nil {
			JSONError(w, http.StatusNotFound, err.Error())
			return
		}
		// Return sandbox path in result
		result.Path = sandboxPath
		// Note: IsGitRepo not needed for in-project browser (BranchFileBrowser doesn't use it)

		// Add ".." entry when not at mount root
		if sandboxPath != mountPath {
			parent := filepath.Dir(sandboxPath)
			result.Entries = append(result.Entries, fsEntry{Name: "..", IsDir: true, Path: parent})
		}

		for _, line := range strings.Split(output, "\n") {
			entry := strings.TrimSpace(line)
			if entry == "" || entry == "." || entry == ".." {
				continue
			}
			if entry[0] == '.' {
				continue
			}
			// ls -p appends '/' to directory names
			isDir := strings.HasSuffix(entry, "/")
			name := strings.TrimSuffix(entry, "/")
			if !includeFiles && !isDir {
				continue
			}
			// Return sandbox path in entry
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

	isGit := git.IsGitRepo(path)
	result.IsGitRepo = isGit

	if path != string(os.PathSeparator) {
		if parent := filepath.Dir(path); parent != path {
			result.Entries = append(result.Entries, fsEntry{Name: "..", IsDir: true, Path: parent, IsGitRepo: git.IsGitRepo(parent)})
		}
	}

	for _, e := range entries {
		if e.Name()[0] == '.' {
			continue
		}
		if !includeFiles && !e.IsDir() {
			continue
		}
		fullPath := filepath.Join(path, e.Name())
		result.Entries = append(result.Entries, fsEntry{
			Name:      e.Name(),
			IsDir:     e.IsDir(),
			Path:      fullPath,
			IsGitRepo: git.IsGitRepo(fullPath),
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

	// For content search, compile regex upfront if needed
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

	// Resolve search root from project or task worktree
	var searchRoot string
	var project *types.Project
	var err error

	if taskID != "" {
		// Search within task worktree.
		// Verify the task belongs to this project first.
		project, err = s.store.GetProject(projectID)
		if err != nil {
			JSONError(w, http.StatusNotFound, "project not found")
			return
		}
		task, taskErr := s.store.GetTask(projectID, taskID)
		if taskErr != nil {
			JSONError(w, http.StatusNotFound, "task not found")
			return
		}
		_ = task // task exists and belongs to project
		searchRoot, err = s.executor.WorktreeMgr().WorktreePath(taskID)
		if err != nil {
			log.Printf("fs resolve worktree failed: %v", err)
			JSONError(w, http.StatusNotFound, "failed to resolve worktree path")
			return
		}
		// Worktree path is derived from server-side config — no need for client-side path validation.
		// Just ensure it's under the workspace directory.
		searchRoot = filepath.Clean(searchRoot)
		cfg, cfgErr := s.configStore.GetConfig()
		if cfgErr == nil && cfg.WorkspaceDir != "" {
			wtDir := cfg.WorkspaceDir
			if wtOverride := os.Getenv("MIKE_WORKTREES_DIR"); wtOverride != "" {
				wtDir = wtOverride
			}
			cleanWT := filepath.Clean(wtDir)
			if !strings.HasPrefix(searchRoot, cleanWT+string(os.PathSeparator)) && searchRoot != cleanWT {
				JSONError(w, http.StatusForbidden, "worktree path outside allowed workspace")
				return
			}
		}
	} else {
		// Search within project root
		project, err = s.store.GetProject(projectID)
		if err != nil {
			JSONError(w, http.StatusNotFound, "project not found")
			return
		}
		searchRoot = project.Path
		searchRoot = filepath.Clean(searchRoot)
		if err := s.validateFSPath(searchRoot, r); err != nil {
			JSONError(w, http.StatusForbidden, err.Error())
			return
		}
	}
	// Sandbox availability check (required but don't exec inside sandbox)
	if project != nil && project.SandboxEnabled {
		if s.sandboxMgr == nil || !s.sandboxMgr.IsAvailable() {
			JSONError(w, http.StatusBadGateway, "sandbox is enabled but not available")
			return
		}
	}

	// Result limit: 50 for content search, 10 for filename-only
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

	// Check that the search root exists
	if _, statErr := os.Stat(searchRoot); statErr != nil {
		log.Printf("fs search root stat failed: %v", statErr)
		JSONError(w, http.StatusNotFound, "search root does not exist")
		return
	}

	// Helper: check if filename/relative path matches query
	filenameMatch := func(rel string) bool {
		if q == "" {
			return true
		}
		if useRegex {
			flags := ""
			if !matchCase {
				flags = "(?i)"
			}
			re, err := regexp.Compile(flags + q)
			if err != nil {
				return false
			}
			if wholeWord {
				return re.MatchString("\b" + rel + "\b")
			}
			return re.MatchString(rel)
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
			// Also check if any word in the path matches
			// For simplicity, treat the whole relative path as words separated by /_.-\n
			return false
		}
		return strings.Contains(checkStr, searchStr)
	}

	// Recursively walk and collect matching files (worktree is bind-mounted into sandbox)
	err = filepath.WalkDir(searchRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		rel, _ := filepath.Rel(searchRoot, path)
		if rel == "." {
			return nil
		}

		// Skip hidden files/dirs (starting with .)
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		// Skip ignored directories
		if git.ShouldIgnorePath(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if d.IsDir() {
			return nil
		}

		// Content search: search all files' contents (filename filter doesn't apply)
		if searchContent {
			searchContentInFile(path, q, matchCase, wholeWord, useRegex, contentRe, func(lineNum int, lineText string) {
				results = append(results, searchResult{Path: rel, Line: lineNum, Text: lineText})
			})
		} else if filenameMatch(rel) {
			// Filename-only search
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

	// Check for binary content: read first 8KB and look for null bytes
	buf := make([]byte, 8192)
	n, _ := f.Read(buf)
	if n > 0 {
		for _, b := range buf[:n] {
			if b == 0 {
				return true // binary file
			}
		}
	}

	// Seek back to start and read line by line
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
			// For regex with whole word, wrap the pattern with \b
			// Since contentRe is already compiled, we need a different approach.
			// We'll do a simple word-boundary check around each match.
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
		// Fallback if contentRe is nil (shouldn't happen, but be safe)
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
		// Find all occurrences and check word boundaries
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

// validateFSPath ensures the path is within the project root or workspace.
func (s *Server) validateFSPath(path string, r *http.Request) error {
	project, err := s.optionalProject(r)
	if err != nil {
		return err
	}
	if project == nil {
		return nil // no project constraint
	}
	// Clean the path
	cleanPath := filepath.Clean(path)
	cleanRoot := filepath.Clean(project.Path)

	// Handle sandbox paths: /home/sandbox/<basename>/... → convert to host path
	// The sandbox mounts the worktree, not the project. The worktree basename
	// is the task ID. We can't resolve the exact worktree here, so we check
	// if the path is within the workspace directory instead.
	cfg, cfgErr := s.configStore.GetConfig()
	var workspaceDir string
	if cfgErr == nil && cfg.WorkspaceDir != "" {
		workspaceDir = filepath.Clean(cfg.WorkspaceDir)
	}
	if workspaceDir != "" && strings.HasPrefix(cleanPath, "/home/sandbox/") {
		// Sandbox path — allow if workspace is configured (worktree will be resolved by caller)
		return nil
	}

	// Host path: allow if within project root or workspace directory
	if strings.HasPrefix(cleanPath, cleanRoot+string(os.PathSeparator)) || cleanPath == cleanRoot {
		return nil
	}
	if workspaceDir != "" && (strings.HasPrefix(cleanPath, workspaceDir+string(os.PathSeparator)) || cleanPath == workspaceDir) {
		return nil
	}
	return fmt.Errorf("path outside allowed project directory")
}

// resolveGitIdentity checks git identity from multiple sources in priority order:
// 1. GIT_COMMITTER_NAME/EMAIL env vars
// 2. GIT_AUTHOR_NAME/EMAIL env vars
// 3. git config --global fallback
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

	// Fall back to git config --global
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

	// Enforce sandbox if enabled — no fallback to host
	if project != nil && project.SandboxEnabled {
		if s.sandboxMgr == nil || !s.sandboxMgr.IsAvailable() {
			JSONError(w, http.StatusBadGateway, "sandbox is enabled but not available")
			return
		}
		// Initialize git repo inside the sandbox
		mountPath := "/home/sandbox/" + project.Name
		relPath, _ := filepath.Rel(project.Path, req.Path)
		sandboxPath := filepath.Join(mountPath, relPath)

		// Resolve sandbox config once, reuse for both calls
		opts := s.fsSandboxConfig(project)

		// Check if already a git repo
		_, gitErr := s.sandboxMgr.ExecProject(project.ID, project.Path, project.Name, "git -C "+shellQuote(sandboxPath)+" rev-parse --git-dir 2>/dev/null", opts)
		if gitErr == nil {
			JSONError(w, http.StatusBadRequest, "path is already a git repository")
			return
		}

		// Create README.md content
		readmeContent := "# " + filepath.Base(req.Path) + "\n"
		encoded := base64.StdEncoding.EncodeToString([]byte(readmeContent))

		// git init + create README + add + commit in single command
		// Use -c flags to override the commit author identity (if configured in git package)
		flagCmd := gitAuthorFlagCmd()
		_, err := s.sandboxMgr.ExecProjectWithInput(project.ID, project.Path, project.Name,
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
	// Initialize git repo on host
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

	// Check that git identity is available (env vars or git config)
	name, email := resolveGitIdentity()
	if name == "" {
		JSONError(w, http.StatusBadRequest, "git user.name is not set — set GIT_AUTHOR_NAME, or run: git config --global user.name '<value>'")
		return
	}
	if email == "" {
		JSONError(w, http.StatusBadRequest, "git user.email is not set — set GIT_AUTHOR_EMAIL, or run: git config --global user.email '<value>'")
		return
	}

	// git init
	cmd := exec.Command("git", "-C", req.Path, "init")
	if out, err := cmd.CombinedOutput(); err != nil {
		JSONError(w, http.StatusInternalServerError, fmt.Sprintf("git init failed: %s", out))
		return
	}

	// Create README.md
	readmePath := filepath.Join(req.Path, "README.md")
	if err := os.WriteFile(readmePath, []byte("# "+filepath.Base(req.Path)+"\n"), 0644); err != nil {
		JSONError(w, http.StatusInternalServerError, fmt.Sprintf("create README.md: %v", err))
		return
	}

	// git add + commit
	cmd = exec.Command("git", "-C", req.Path, "add", "README.md")
	if out, err := cmd.CombinedOutput(); err != nil {
		JSONError(w, http.StatusInternalServerError, fmt.Sprintf("git add failed: %s", out))
		return
	}
	commitArgs := append([]string{"-C", req.Path}, git.GitAuthorFlags()...)
	commitArgs = append(commitArgs, "commit", "-m", "Initial commit")
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

	// Determine worktree path (host filesystem)
	var worktreePath string
	if taskID != "" {
		wtPath, err := s.executor.WorktreeMgr().WorktreePath(taskID)
		if err != nil {
			JSONError(w, http.StatusNotFound, "task worktree not found")
			return
		}
		worktreePath = wtPath
	} else {
		worktreePath = project.Path
	}

	// Enforce sandbox if enabled — no fallback to host
	if project != nil && project.SandboxEnabled {
		if s.sandboxMgr == nil || !s.sandboxMgr.IsAvailable() {
			JSONError(w, http.StatusBadGateway, "sandbox is enabled but not available")
			return
		}
		// Create directory inside the sandbox
		mountPath := "/home/sandbox/" + project.Name
		cleanPath := filepath.Clean(req.Path)
		var sandboxPath string
		if cleanPath == mountPath || strings.HasPrefix(cleanPath, mountPath+string(os.PathSeparator)) {
			// req.Path is already a sandbox path — use it directly
			sandboxPath = cleanPath
		} else {
			// req.Path is a host path — convert to sandbox path
			relPath, _ := filepath.Rel(worktreePath, cleanPath)
			sandboxPath = filepath.Join(mountPath, relPath)
		}
		sandboxParent := filepath.Dir(sandboxPath)

		// Combined check + mkdir in single command
		opts := s.fsSandboxConfig(project)
		cmd := "if [ ! -d " + shellQuote(sandboxParent) + "; then echo 'PARENT_MISSING'; " +
			"elif [ -e " + shellQuote(sandboxPath) + "; then echo 'ALREADY_EXISTS'; " +
			"else mkdir " + shellQuote(sandboxPath) + " && echo 'OK'; fi"
		var output string
		var execErr error
		if taskID != "" {
			// Use task sandbox (creates container on demand)
			output, execErr = s.sandboxMgr.ExecTask(taskID, worktreePath, project.Name, project.Path, cmd, opts)
		} else {
			output, execErr = s.sandboxMgr.ExecProject(project.ID, project.Path, project.Name, cmd, opts)
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
			// mkdir may have succeeded despite unexpected output (e.g. shell banners)
			// Verify the directory actually exists before reporting an error
			var checkOut string
			var checkErr error
			checkCmd := "test -d " + shellQuote(sandboxPath) + " && echo EXISTS"
			if taskID != "" {
				checkOut, checkErr = s.sandboxMgr.ExecTask(taskID, worktreePath, project.Name, project.Path, checkCmd, opts)
			} else {
				checkOut, checkErr = s.sandboxMgr.ExecProject(project.ID, project.Path, project.Name, checkCmd, opts)
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
	// Check parent exists
	parent := filepath.Dir(req.Path)
	info, err := os.Stat(parent)
	if err != nil || !info.IsDir() {
		JSONError(w, http.StatusBadRequest, "parent directory does not exist")
		return
	}

	// Check target doesn't already exist
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

// resolveSandboxConfig resolves the full sandbox configuration for a project.
// Returns the image to use and all sandbox settings needed for ExecProject/ExecProjectWithInput.
// The caller is responsible for ensuring the image is available (pull/build).
func (s *Server) resolveSandboxConfig(project *types.Project) string {
	imageToUse := ""
	if project.SandboxDockerfilePath != "" {
		imageToUse = sandbox.ProjectImageName(project.Name)
	} else {
		imageToUse = project.SandboxImage
		if imageToUse == "" {
			imageToUse = sandbox.BaseImage
		}
	}
	return imageToUse
}

// fsSandboxConfig returns the full sandbox config for FS operations.
func (s *Server) fsSandboxConfig(project *types.Project) sandbox.SandboxOpts {
	return sandbox.SandboxOpts{
		BaseImage:        s.resolveSandboxConfig(project),
		NetworkIsolation: project.SandboxNetworkIsolation,
		MemoryLimit:      project.SandboxMemoryLimit,
		ForwardPorts:     project.ForwardPorts,
		AgentSandbox:     s.executor.AgentRegistry().GetAnyAgentSandbox(),
		SkillsDir:        prompts.SkillsDir(),
		SandboxMounts:    project.SandboxMounts,
		SandboxEnvs:      project.SandboxEnvs,
		Capabilities:     project.SandboxCapabilities,
	}
}

// gitAuthorFlagCmd builds a shell-safe command fragment
func gitAuthorFlagCmd() string {
	flags := git.GitAuthorFlags()
	var parts []string
	for i := 0; i+1 < len(flags); i += 2 {
		parts = append(parts, shellQuote(flags[i]), shellQuote(flags[i+1]))
	}
	return strings.Join(parts, " ")
}
