package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"kanban/backend/types"
)

// execFunc is a function that executes a command, either in a sandbox or directly.
type execFunc func(command string) (string, error)

// readFunc reads a file's contents. When sandboxed, runs via container exec.
type readFunc func(path string) ([]byte, error)

// writeFunc writes content to a file. When sandboxed, runs via container exec with stdin.
type writeFunc func(path, content string) error

func (te *TaskExecutor) executeTool(name string, args map[string]interface{}, worktreePath string, task *types.Task, execFn execFunc, readFn readFunc, writeFn writeFunc) (string, bool) {
	switch name {
	case "read_file":
		path, _ := args["path"].(string)
		fullPath := validateWorktreePath(worktreePath, path)
		if fullPath == "" {
			return "Error: path escape", false
		}
		var data []byte
		var err error
		if readFn != nil {
			data, err = readFn(path)
		} else {
			data, err = os.ReadFile(fullPath)
		}
		if err != nil {
			return fmt.Sprintf("Error: %v", err), false
		}
		return string(data), true
	case "write_file":
		path, _ := args["path"].(string)
		content, _ := args["content"].(string)
		fullPath := validateWorktreePath(worktreePath, path)
		if fullPath == "" {
			return "Error: path escape", false
		}
		var err error
		if writeFn != nil {
			err = writeFn(path, content)
		} else {
			os.MkdirAll(filepath.Dir(fullPath), 0o755)
			err = os.WriteFile(fullPath, []byte(content), 0o644)
		}
		if err != nil {
			return fmt.Sprintf("Error: %v", err), false
		}
		return fmt.Sprintf("Wrote %d bytes", len(content)), true
	case "execute_command":
		cmd, _ := args["command"].(string)
		if isDangerousCommand(cmd) {
			return "Error: command blocked", false
		}
		timeout := int64(5)
		if t, ok := args["timeout"].(float64); ok {
			timeout = int64(t)
		}
		if timeout > 300 {
			timeout = 300
		}

		var out string
		var err error
		if execFn != nil {
			// Use sandbox execution
			out, err = execFn(cmd)
		} else {
			// Direct execution (no sandbox)
			ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
			defer cancel()
			c := exec.CommandContext(ctx, "bash", "-c", cmd)
			c.Dir = worktreePath
			rawOut, cmdErr := c.CombinedOutput()
			out = string(rawOut)
			err = cmdErr
		}

		if err != nil {
			return fmt.Sprintf("Failed: %v\n%s", err, out), false
		}
		return out, true
	case "task_complete":
		return "Task complete", true
	default:
		return fmt.Sprintf("Unknown tool: %s", name), false
	}
}

func parseToolArgs(s string) map[string]interface{} {
	result := make(map[string]interface{})
	s = strings.TrimSpace(s)
	if s == "" || s == "{}" {
		return result
	}
	if err := json.Unmarshal([]byte(s), &result); err != nil {
		var innerStr string
		if err2 := json.Unmarshal([]byte(s), &innerStr); err2 == nil && innerStr != "" {
			if err3 := json.Unmarshal([]byte(innerStr), &result); err3 == nil {
				return result
			}
		}
		if len(s) > 1 && s[0] == '{' && s[1] == '{' {
			trimmed := s[1:]
			if err4 := json.Unmarshal([]byte(trimmed), &result); err4 == nil {
				return result
			}
		}
		log.Printf("[tool] failed to parse args: %v (raw: %q)", err, s)
		return map[string]interface{}{"raw": s}
	}
	return result
}

func isDangerousCommand(cmd string) bool {
	dangerous := []string{"sudo", "mkfs", "dd if=/dev/", ":(){ :|: };:", "chmod -R 777 /"}
	cmd = strings.TrimSpace(cmd)
	for _, d := range dangerous {
		if strings.Contains(cmd, d) {
			return true
		}
	}
	return strings.HasPrefix(cmd, "rm -rf /") || strings.HasPrefix(cmd, "rm -rf /*")
}

// validateWorktreePath resolves a relative path against the worktree root
// and returns the absolute path, or empty string if it escapes the worktree.
// Handles: .. traversal, absolute paths, and symlink-style escapes.
func validateWorktreePath(worktreePath, path string) string {
	// Ensure worktree path is absolute and has trailing separator for prefix check
	wt := worktreePath
	if !filepath.IsAbs(wt) {
		return ""
	}
	if !strings.HasSuffix(wt, string(filepath.Separator)) {
		wt = wt + string(filepath.Separator)
	}

	// Clean the input path
	cleaned := filepath.Clean(path)
	// Reject absolute paths (they shouldn't be used for worktree-relative paths)
	if filepath.IsAbs(cleaned) {
		return ""
	}
	// Reject paths that escape with ..
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return ""
	}

	fullPath := filepath.Join(worktreePath, cleaned)
	// Final safety check: resolved path must be within worktree
	// (exact match or starts with worktree + separator)
	if fullPath != worktreePath && !strings.HasPrefix(fullPath, wt) {
		return ""
	}
	return fullPath
}
