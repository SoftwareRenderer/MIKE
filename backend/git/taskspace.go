package git

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mike/backend/fsutil"
	"mike/backend/store"
	"mike/pkg/logging"
)

// Every task's worktree is a checkout, so the tasks are usually most of the space this server holds.
// Nothing records that figure, so measuring it means walking the checkouts — which is bounded here.

const (
	// taskSpaceWalkTimeout bounds one project's measurement, and taskSpaceMaxWalkEntries bounds one
	// checkout inside it. Both keep a host of monorepos from a walk that runs for minutes. The budget
	// belongs to one project, so a project that cannot be priced does not spend the time the others
	// need.
	taskSpaceWalkTimeout    = 15 * time.Second
	taskSpaceMaxWalkEntries = 200_000
)

// TaskWorktreeUsage is the disk one project's task worktrees hold.
type TaskWorktreeUsage struct {
	// Bytes is the apparent size of every file inside every task worktree. A worktree's .git is a
	// pointer file, so a task's git objects stay in the project's .git and are not in this.
	Bytes int64
	// Worktrees is how many task directories were priced.
	Worktrees int
}

// MeasureProjectTaskSpace answers what one project's task worktrees hold. ok=false means no figure —
// an unreadable directory, or a checkout left unpriced — while a project with no worktree directory yet
// is a project with no tasks, whose figure is zero.
func (wm *WorktreeManager) MeasureProjectTaskSpace(projectID string) (TaskWorktreeUsage, bool) {
	usage, err := wm.walkProjectTaskSpace(projectID)
	if err != nil {
		// The measurement is retried on every refresh, so the reason is said once.
		wm.taskSpaceWarn.Do(func() {
			logging.For(logging.Worktree).Warnf("measuring the task worktrees: the storage tooltip reports no task figure (%v)", err)
		})
		return TaskWorktreeUsage{}, false
	}
	return usage, true
}

// walkProjectTaskSpace measures one project's directory and answers why it could not, which an ok flag
// cannot carry.
//
// Only a <root>/<projectID>/<taskID> directory under a project row that still exists is priced — the
// guard GarbageCollect sweeps by, and what keeps MIKE's build scratch tree out of a task's figure.
func (wm *WorktreeManager) walkProjectTaskSpace(projectID string) (TaskWorktreeUsage, error) {
	_, err := wm.store.GetProject(projectID)
	if errors.Is(err, store.ErrProjectNotFound) {
		return TaskWorktreeUsage{}, nil
	}
	if err != nil {
		return TaskWorktreeUsage{}, fmt.Errorf("reading project %s: %w", projectID, err)
	}

	projectDir := wm.projectWorktreesDir(projectID)
	taskEntries, err := os.ReadDir(projectDir)
	if errors.Is(err, fs.ErrNotExist) {
		return TaskWorktreeUsage{}, nil
	}
	if err != nil {
		return TaskWorktreeUsage{}, fmt.Errorf("reading %s: %w", projectDir, err)
	}

	// One budget covers the project, and each checkout is handed the time still left to it.
	deadline := time.Now().Add(taskSpaceWalkTimeout)
	var total TaskWorktreeUsage
	for _, taskEntry := range taskEntries {
		// A dot entry is not a task directory, the same reading GarbageCollect takes.
		if !taskEntry.IsDir() || strings.HasPrefix(taskEntry.Name(), ".") {
			continue
		}
		worktree := filepath.Join(projectDir, taskEntry.Name())
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return TaskWorktreeUsage{}, fmt.Errorf(
				"pricing %s: the %s budget is spent", worktree, taskSpaceWalkTimeout)
		}
		tree, err := fsutil.TreeUsageFor(worktree, fsutil.WalkBudget{
			MaxEntries: taskSpaceMaxWalkEntries,
			Timeout:    remaining,
		})
		if err != nil {
			return TaskWorktreeUsage{}, err
		}
		if !tree.Complete {
			return TaskWorktreeUsage{}, fmt.Errorf(
				"pricing %s: the walk stopped before its end", worktree)
		}
		total.Bytes += tree.Bytes
		total.Worktrees++
	}
	return total, nil
}
