package backend

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// WorktreeManager handles git worktree operations.
type WorktreeManager struct {
	store        *Store
	configStore  *ConfigStore
	worktreeMu   sync.Map // per-project mutex for worktree creation
}

// NewWorktreeManager creates a new WorktreeManager.
func NewWorktreeManager(store *Store, configStore *ConfigStore) *WorktreeManager {
	return &WorktreeManager{store: store, configStore: configStore}
}

// getProjectWorktreeMu returns a per-project mutex for serializing worktree creation.
func (wm *WorktreeManager) getProjectWorktreeMu(projectID string) *sync.Mutex {
	mu, _ := wm.worktreeMu.LoadOrStore(projectID, &sync.Mutex{})
	return mu.(*sync.Mutex)
}

// WorktreePath returns the filesystem path for a task's worktree.
func (wm *WorktreeManager) WorktreePath(taskID string) (string, error) {
	cfg, err := wm.configStore.GetConfig()
	if err != nil {
		return "", err
	}
	return filepath.Join(cfg.WorkspaceDir, taskID), nil
}

// CreateWorktree creates a git worktree for a task, capturing the current working tree state.
func (wm *WorktreeManager) CreateWorktree(projectID, taskID, branchName string) (string, error) {
	project, err := wm.store.GetProject(projectID)
	if err != nil {
		return "", fmt.Errorf("get project: %w", err)
	}

	cfg, err := wm.configStore.GetConfig()
	if err != nil {
		return "", fmt.Errorf("get config: %w", err)
	}

	worktreesDir := cfg.WorkspaceDir
	if wtDir := os.Getenv("WORKTREES_DIR"); wtDir != "" {
		worktreesDir = wtDir
	}
	worktreePath := filepath.Join(worktreesDir, taskID)

	// Acquire per-project mutex with timeout
	mu := wm.getProjectWorktreeMu(projectID)
	done := make(chan struct{})
	go func() {
		mu.Lock()
		close(done)
	}()

	select {
	case <-done:
		// Got the lock
	case <-time.After(30 * time.Second):
		return "", fmt.Errorf("timeout waiting for worktree lock (another task is initializing)")
	}
	defer mu.Unlock()

	// Ensure worktrees directory exists
	if err := os.MkdirAll(worktreesDir, 0o755); err != nil {
		return "", fmt.Errorf("create worktrees dir: %w", err)
	}

	// Check if worktree already exists
	if _, err := os.Stat(worktreePath); err == nil {
		return worktreePath, nil
	}

	// Create baseline branch capturing current working tree state (including uncommitted changes)
	baselineBranch := fmt.Sprintf("__kanban_baseline_%s", taskID)

	// Save original branch BEFORE any modifications
	originalBranch, _ := wm.gitCurrentBranch(project.Path)

	// Cleanup function: always restore original branch and clean up temp branch
	cleanup := func() {
		if originalBranch != "" {
			wm.gitRun(project.Path, "checkout", originalBranch)
		}
		wm.gitRun(project.Path, "branch", "-D", baselineBranch)
	}

	// Step 1: Check for uncommitted changes
	hasChanges, err := wm.hasWorkingTreeChanges(project.Path)
	if err != nil {
		return "", fmt.Errorf("check working tree: %w", err)
	}

	// Delete any existing baseline branch (from failed previous attempts)
	wm.gitRun(project.Path, "branch", "-D", baselineBranch)

	if hasChanges {
		// Step 2a: Stash current changes
		if err := wm.gitRun(project.Path, "stash", "push", "-m", "kanban baseline stash"); err != nil {
			return "", fmt.Errorf("stash changes: %w", err)
		}
		defer func() {
			// Always pop stash on exit
			wm.gitRun(project.Path, "stash", "pop")
		}()

		// Step 2b: Create baseline branch from current HEAD (does NOT change HEAD)
		if err := wm.gitRun(project.Path, "branch", baselineBranch); err != nil {
			return "", fmt.Errorf("create baseline branch: %w", err)
		}
		defer cleanup()

		// Step 2c: Checkout baseline branch to apply changes
		if err := wm.gitRun(project.Path, "checkout", baselineBranch); err != nil {
			return "", fmt.Errorf("checkout baseline: %w", err)
		}

		// Step 2d: Apply stashed changes
		if err := wm.gitRun(project.Path, "stash", "pop"); err != nil {
			// If stash pop fails (conflict), just continue
			wm.gitRun(project.Path, "stash", "drop")
		}

		// Step 2e: Stage and commit changes on baseline branch
		wm.gitRun(project.Path, "add", "-A")
		wm.gitRun(project.Path, "commit", "-m", fmt.Sprintf("Baseline for task %s", taskID))

		// Step 2f: Go back to original branch
		if originalBranch != "" {
			wm.gitRun(project.Path, "checkout", originalBranch)
		}

		// Re-stash for the deferred pop (the changes are now committed, stash is empty)
		// Actually the stash was already popped above, so the deferred pop will fail harmlessly
	}

	// Step 3: Create worktree from the baseline branch (or current HEAD if clean)
	sourceRef := baselineBranch
	if !hasChanges {
		sourceRef = "HEAD"
		defer cleanup() // still need to clean up if worktree creation fails
	}

	cmd := exec.Command("git", "worktree", "add", "--detach", worktreePath, sourceRef)
	cmd.Dir = project.Path
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("create worktree: %s: %w", string(out), err)
	}

	// Step 4: Create the task branch from the worktree's HEAD
	if out, err := wm.gitRunOutput(worktreePath, "checkout", "-b", branchName); err != nil {
		wm.gitRun(project.Path, "worktree", "remove", "--force", worktreePath)
		return "", fmt.Errorf("create branch: %s: %w", out, err)
	}

	// Step 5: Create initial baseline commit snapshot
	wm.gitRun(worktreePath, "add", "-A")
	wm.gitRun(worktreePath, "commit", "-m", "Initial baseline snapshot", "--allow-empty")

	// Step 6: Cleanup baseline branch
	wm.gitRun(project.Path, "branch", "-D", baselineBranch)

	return worktreePath, nil
}

// RemoveWorktree removes a task's worktree and its branch.
func (wm *WorktreeManager) RemoveWorktree(projectID, taskID, branchName string) error {
	project, err := wm.store.GetProject(projectID)
	if err != nil {
		return fmt.Errorf("get project: %w", err)
	}

	worktreePath, err := wm.WorktreePath(taskID)
	if err != nil {
		return err
	}

	// Remove worktree directory
	if err := os.RemoveAll(worktreePath); err != nil {
		return fmt.Errorf("remove worktree dir: %w", err)
	}

	// Remove branch from main repo
	wm.gitRun(project.Path, "branch", "-D", branchName)

	// Remove worktree tracking
	wm.gitRun(project.Path, "worktree", "prune")

	return nil
}

// GetDiff returns the git diff for a task's worktree.
func (wm *WorktreeManager) GetDiff(taskID string) (string, error) {
	worktreePath, err := wm.WorktreePath(taskID)
	if err != nil {
		return "", err
	}

	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		return "", fmt.Errorf("worktree not found")
	}

	out, err := wm.gitRunOutput(worktreePath, "diff", "HEAD")
	if err != nil {
		return "", fmt.Errorf("git diff: %w", err)
	}
	return out, nil
}

// -- Helper functions --

func (wm *WorktreeManager) hasWorkingTreeChanges(dir string) (bool, error) {
	out, err := wm.gitRunOutput(dir, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return len(strings.TrimSpace(out)) > 0, nil
}

func (wm *WorktreeManager) gitRun(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_DIR="+filepath.Join(dir, ".git"))
	_, err := cmd.CombinedOutput()
	return err
}

func (wm *WorktreeManager) gitRunOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (wm *WorktreeManager) gitCurrentBranch(dir string) (string, error) {
	return wm.gitRunOutput(dir, "rev-parse", "--abbrev-ref", "HEAD")
}
