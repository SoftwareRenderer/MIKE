package server

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"kanban/backend/git"
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
	// Host filesystem (worktree is bind-mounted into sandbox)
	content, err := os.ReadFile(path)
	if err != nil {
		JSONError(w, http.StatusNotFound, err.Error())
		return
	}
	if len(content) > 1_000_000 {
		JSONError(w, http.StatusRequestEntityTooLarge, "file too large (max 1MB)")
		return
	}
	JSONResponse(w, http.StatusOK, map[string]string{"content": string(content)})
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
		JSONError(w, http.StatusInternalServerError, fmt.Sprintf("create directory: %v", err))
		return
	}
	if err := os.WriteFile(req.Path, []byte(req.Content), 0644); err != nil {
		JSONError(w, http.StatusInternalServerError, fmt.Sprintf("write file: %v", err))
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
	// Validate path is within allowed project root
	if err := s.validateFSPath(path, r); err != nil {
		JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	includeFiles := r.URL.Query().Get("files") == "1"
	project, _ := s.optionalProject(r)

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
			relPath, _ := filepath.Rel(project.Path, cleanPath)
			sandboxPath = filepath.Join(mountPath, relPath)
		}
		// Single command: ls -pA marks dirs with trailing /, one entry per line
		output, err := s.sandboxMgr.ExecProject(project.ID, project.Path, project.Name, "ls -pA "+sandboxPath, nil)
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
		JSONError(w, http.StatusNotFound, err.Error())
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

// handleFSSearch recursively searches for files, returning up to 10 matches.
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
			JSONError(w, http.StatusNotFound, "failed to resolve worktree path: "+err.Error())
			return
		}
		// Worktree path is derived from server-side config — no need for client-side path validation.
		// Just ensure it's under the workspace directory.
		searchRoot = filepath.Clean(searchRoot)
		cfg, cfgErr := s.configStore.GetConfig()
		if cfgErr == nil && cfg.WorkspaceDir != "" {
			wtDir := cfg.WorkspaceDir
			if wtOverride := os.Getenv("WORKTREES_DIR"); wtOverride != "" {
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

	type searchResult struct {
		Path string `json:"path"` // relative to search root
	}
	var results []searchResult

	// Check that the search root exists
	if _, statErr := os.Stat(searchRoot); statErr != nil {
		JSONError(w, http.StatusNotFound, "search root does not exist: "+statErr.Error())
		return
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

		// Match: filename or relative path contains query (case-insensitive)
		if q != "" && !strings.Contains(strings.ToLower(rel), strings.ToLower(q)) {
			return nil
		}

		results = append(results, searchResult{Path: rel})
		if len(results) >= 10 {
			return errStopWalk
		}
		return nil
	})
	_ = err // ignore errStopWalk

	// Sort results for deterministic output
	sort.SliceStable(results, func(i, j int) bool {
		return strings.ToLower(results[i].Path) < strings.ToLower(results[j].Path)
	})

	JSONResponse(w, http.StatusOK, map[string]any{
		"root":    searchRoot,
		"results": results,
	})
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
// 1. KANBAN_GIT_AUTHOR_NAME/EMAIL (via git package vars, set at init)
// 2. GIT_COMMITTER_NAME/EMAIL env vars
// 3. GIT_AUTHOR_NAME/EMAIL env vars
// 4. git config --global fallback
func resolveGitIdentity() (name, email string) {
	// KANBAN-specific env vars (read at package init, used with -c flags)
	name = git.GitAuthorName
	email = git.GitAuthorEmail
	if name != "" && email != "" {
		return name, email
	}

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

		// Check if already a git repo
		_, gitErr := s.sandboxMgr.ExecProject(project.ID, project.Path, project.Name, "git -C "+sandboxPath+" rev-parse --git-dir 2>/dev/null", nil)
		if gitErr == nil {
			JSONError(w, http.StatusBadRequest, "path is already a git repository")
			return
		}

		// Create README.md content
		readmeContent := "# "+filepath.Base(req.Path)+"\n"
		encoded := base64.StdEncoding.EncodeToString([]byte(readmeContent))

		// git init + create README + add + commit in single command
		// Only add -c flags when KANBAN env vars are set (GitAuthorFlags skips empty values)
		authorFlags := strings.Join(git.GitAuthorFlags(), " ")
		_, err := s.sandboxMgr.ExecProjectWithInput(project.ID, project.Path, project.Name,
			"git -C "+sandboxPath+" init && " +
			"base64 -d > "+filepath.Join(sandboxPath, "README.md")+" && " +
			"git -C "+sandboxPath+" add README.md && " +
			"git -C "+sandboxPath+" "+authorFlags+" commit -m 'Initial commit'",
			[]byte(encoded+"\n"), nil)
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
		JSONError(w, http.StatusBadRequest, "git user.name is not set — set KANBAN_GIT_AUTHOR_NAME, GIT_AUTHOR_NAME, or run: git config --global user.name '<value>'")
		return
	}
	if email == "" {
		JSONError(w, http.StatusBadRequest, "git user.email is not set — set KANBAN_GIT_AUTHOR_EMAIL, GIT_AUTHOR_EMAIL, or run: git config --global user.email '<value>'")
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

	// Enforce sandbox if enabled — no fallback to host
	if project != nil && project.SandboxEnabled {
		if s.sandboxMgr == nil || !s.sandboxMgr.IsAvailable() {
			JSONError(w, http.StatusBadGateway, "sandbox is enabled but not available")
			return
		}
		// Create directory inside the sandbox
		mountPath := "/home/sandbox/" + project.Name
		relPath, _ := filepath.Rel(project.Path, req.Path)
		sandboxPath := filepath.Join(mountPath, relPath)
		sandboxParent := filepath.Dir(sandboxPath)

		// Combined check + mkdir in single command
		output, err := s.sandboxMgr.ExecProject(project.ID, project.Path, project.Name,
			"if [ ! -d "+sandboxParent+" ]; then echo 'PARENT_MISSING'; " +
			"elif [ -e "+sandboxPath+" ]; then echo 'ALREADY_EXISTS'; " +
			"else mkdir "+sandboxPath+" && echo 'OK'; fi", nil)
		if err != nil {
			JSONError(w, http.StatusInternalServerError, fmt.Sprintf("create directory: %v", err))
			return
		}
		switch strings.TrimSpace(output) {
		case "PARENT_MISSING":
			JSONError(w, http.StatusBadRequest, "parent directory does not exist")
			return
		case "ALREADY_EXISTS":
			JSONError(w, http.StatusBadRequest, "path already exists")
			return
		case "OK":
			// success
		default:
			JSONError(w, http.StatusInternalServerError, "unexpected response from mkdir")
			return
		}
		if err != nil {
			JSONError(w, http.StatusInternalServerError, fmt.Sprintf("create directory: %v", err))
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
