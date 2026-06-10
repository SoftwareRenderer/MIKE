package git

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"kanban/backend/config"
	"kanban/backend/store"
)

// WorktreeManager handles git worktree operations.
type WorktreeManager struct {
	store        *store.Store
	configStore  *config.ConfigStore
	worktreeMu   sync.Map // per-project mutex for worktree creation
}

// NewWorktreeManager creates a new WorktreeManager.
func NewWorktreeManager(store *store.Store, configStore *config.ConfigStore) *WorktreeManager {
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
	if !filepath.IsAbs(worktreesDir) {
		abs, err := filepath.Abs(worktreesDir)
		if err == nil {
			worktreesDir = abs
		}
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
	case <-time.After(30 * time.Second):
		return "", fmt.Errorf("timeout waiting for worktree lock (another task is initializing)")
	}
	defer mu.Unlock()

	if err := os.MkdirAll(worktreesDir, 0o755); err != nil {
		return "", fmt.Errorf("create worktrees dir: %w", err)
	}

	if _, err := os.Stat(worktreePath); err == nil {
		return worktreePath, nil
	}

	baselineBranch := fmt.Sprintf("__kanban_baseline_%s", taskID)
	originalBranch, _ := wm.gitCurrentBranch(project.Path)

	cleanup := func() {
		if originalBranch != "" {
			wm.gitRun(project.Path, "checkout", originalBranch)
		}
		wm.gitRun(project.Path, "branch", "-D", baselineBranch)
	}

	hasChanges, err := wm.hasWorkingTreeChanges(project.Path)
	if err != nil {
		return "", fmt.Errorf("check working tree: %w", err)
	}

	wm.gitRun(project.Path, "branch", "-D", baselineBranch)

	if hasChanges {
		if err := wm.gitRun(project.Path, "stash", "push", "-m", "kanban baseline stash"); err != nil {
			return "", fmt.Errorf("stash changes: %w", err)
		}
		defer func() {
			wm.gitRun(project.Path, "stash", "pop")
		}()

		if err := wm.gitRun(project.Path, "branch", baselineBranch); err != nil {
			return "", fmt.Errorf("create baseline branch: %w", err)
		}
		defer cleanup()

		if err := wm.gitRun(project.Path, "checkout", baselineBranch); err != nil {
			return "", fmt.Errorf("checkout baseline: %w", err)
		}

		if err := wm.gitRun(project.Path, "stash", "pop"); err != nil {
			wm.gitRun(project.Path, "stash", "drop")
		}

		wm.gitRun(project.Path, "add", "-A")
		wm.gitRun(project.Path, "commit", "-m", fmt.Sprintf("Baseline for task %s", taskID))

		if originalBranch != "" {
			wm.gitRun(project.Path, "checkout", originalBranch)
		}
	}

	sourceRef := baselineBranch
	if !hasChanges {
		sourceRef = "HEAD"
		defer cleanup()
	}

	cmd := exec.Command("git", "worktree", "add", "--detach", worktreePath, sourceRef)
	cmd.Dir = project.Path
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("create worktree: %s: %w", string(out), err)
	}

	if out, err := wm.gitRunOutput(worktreePath, "checkout", "-b", branchName); err != nil {
		wm.gitRun(project.Path, "worktree", "remove", "--force", worktreePath)
		return "", fmt.Errorf("create branch: %s: %w", out, err)
	}

	wm.gitRun(worktreePath, "add", "-A")
	wm.gitRun(worktreePath, "commit", "-m", "Initial baseline snapshot", "--allow-empty")

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

	if err := os.RemoveAll(worktreePath); err != nil {
		return fmt.Errorf("remove worktree dir: %w", err)
	}

	wm.gitRun(project.Path, "branch", "-D", branchName)
	wm.gitRun(project.Path, "worktree", "prune")

	return nil
}

// RemoveWorktreeDir removes a task's worktree directory and prunes git worktree tracking.
func (wm *WorktreeManager) RemoveWorktreeDir(projectID, taskID string) error {
	project, err := wm.store.GetProject(projectID)
	if err != nil {
		return fmt.Errorf("get project: %w", err)
	}

	worktreePath, err := wm.WorktreePath(taskID)
	if err != nil {
		return err
	}

	if err := os.RemoveAll(worktreePath); err != nil {
		return fmt.Errorf("remove worktree dir: %w", err)
	}

	wm.gitRun(project.Path, "worktree", "prune")

	return nil
}

// GarbageCollect removes worktree directories that have no matching task in the database.
func (wm *WorktreeManager) GarbageCollect(dryRun bool) (int, error) {
	cfg, err := wm.configStore.GetConfig()
	if err != nil {
		return 0, fmt.Errorf("get config: %w", err)
	}

	worktreesDir := cfg.WorkspaceDir
	if wtDir := os.Getenv("WORKTREES_DIR"); wtDir != "" {
		worktreesDir = wtDir
	}
	if !filepath.IsAbs(worktreesDir) {
		if abs, err := filepath.Abs(worktreesDir); err == nil {
			worktreesDir = abs
		}
	}

	if _, err := os.Stat(worktreesDir); os.IsNotExist(err) {
		return 0, nil
	}

	activeTaskIDs, err := wm.store.GetAllTaskIDs()
	if err != nil {
		return 0, fmt.Errorf("get task IDs: %w", err)
	}
	activeSet := make(map[string]bool, len(activeTaskIDs))
	for _, id := range activeTaskIDs {
		activeSet[id] = true
	}

	entries, err := os.ReadDir(worktreesDir)
	if err != nil {
		return 0, fmt.Errorf("read worktrees dir: %w", err)
	}

	removed := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if name[0] == '.' {
			continue
		}
		if !activeSet[name] {
			orphanPath := filepath.Join(worktreesDir, name)
			if dryRun {
				log.Printf("[gc] would remove orphaned worktree: %s", orphanPath)
				removed++
			} else {
				log.Printf("[gc] removing orphaned worktree: %s", orphanPath)
				if err := os.RemoveAll(orphanPath); err != nil {
					log.Printf("[gc] error removing %s: %v", orphanPath, err)
				} else {
					removed++
				}
			}
		}
	}

	projects, err := wm.store.ListProjects()
	if err != nil {
		log.Printf("[gc] error listing projects for prune: %v", err)
		return removed, nil
	}
	for _, p := range projects {
		wm.gitRun(p.Path, "worktree", "prune")
	}

	if dryRun {
		log.Printf("[gc] dry-run complete: %d orphaned worktrees would be removed", removed)
	} else {
		log.Printf("[gc] complete: %d orphaned worktrees removed", removed)
	}

	return removed, nil
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
