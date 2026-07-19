package lsp

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// workspaceCache holds per-project workspace root results to avoid repeated
// filesystem walks.
var workspaceCache = struct {
	sync.Mutex
	m map[string]string // projectPath → detected root
}{m: make(map[string]string)}

// DetectWorkspaceRoot finds the LSP workspace root for a given file path.
// For Go files, walks up looking for go.mod. Falls back to projectPath.
func DetectWorkspaceRoot(filePath, projectPath string) string {
	// Resolve to absolute paths for consistent matching
	absFile, err := filepath.Abs(filePath)
	if err != nil {
		return projectPath
	}
	absProject, err := filepath.Abs(projectPath)
	if err != nil {
		return projectPath
	}

	// Check cache first
	workspaceCache.Lock()
	if root, ok := workspaceCache.m[absProject]; ok {
		workspaceCache.Unlock()
		return root
	}
	workspaceCache.Unlock()

	// Walk up from the file's directory looking for go.mod
	dir := filepath.Dir(absFile)
	for {
		candidate := filepath.Join(dir, "go.mod")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			// Found go.mod — this directory is the workspace root
			root := dir
			workspaceCache.Lock()
			workspaceCache.m[absProject] = root
			workspaceCache.Unlock()
			return root
		}

		// Stop if we've reached or passed the project boundary
		parent := filepath.Dir(dir)
		if dir == absProject || parent == dir || !strings.HasPrefix(dir+string(filepath.Separator), absProject+string(filepath.Separator)) && dir != absProject {
			break
		}
		dir = parent
	}

	// Not found — fall back to projectPath
	workspaceCache.Lock()
	workspaceCache.m[absProject] = absProject
	workspaceCache.Unlock()
	return absProject
}

// ResetWorkspaceCache clears the workspace root cache.
// Only used in tests.
func ResetWorkspaceCache() {
	workspaceCache.Lock()
	defer workspaceCache.Unlock()
	workspaceCache.m = make(map[string]string)
}
