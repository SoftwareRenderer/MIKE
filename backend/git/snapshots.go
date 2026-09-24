package git

// Worktree snapshots are the commits `git stash create` produces: the whole working state at a moment,
// addressed in the database by hash (task_messages.git_commit_hash, task_threads.worktree_stash_hash).
//
// A hash names an object but does not keep it, so every snapshot is pinned under a ref of MIKE's own —
// reachable from a ref, git never prunes it, and it stays out of the user's `git stash list` and
// `git branch` output. Deleting a task or thread deletes its refs and makes the objects collectable again.
//
// Mechanism and history: docs/threads-checkpoints.md.

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// ErrSnapshotMissing is returned when a snapshot hash cannot be resolved in the repository. Restoring
// refuses rather than carrying on: the alternative is discarding the current work to restore nothing.
var ErrSnapshotMissing = errors.New("snapshot not found")

// ErrInvalidSnapshotRef is returned for any ref (or ref component) that is not a well-formed name inside
// the snapshot namespace. Callers classify with errors.Is; the text is for logs.
var ErrInvalidSnapshotRef = errors.New("invalid snapshot ref")

// SnapshotRefPrefix is the ref namespace every MIKE snapshot lives under. It is deliberately not under
// refs/heads or refs/tags: those are the user's.
const SnapshotRefPrefix = "refs/mike/snap"

// SnapshotRef is the ref pinning one snapshot: refs/mike/snap/<taskID>/<ownerID>. ownerID is the message a
// checkpoint belongs to, or ThreadSnapshotOwner for a thread's saved worktree. Both halves are re-validated
// on every write.
func SnapshotRef(taskID, ownerID string) string {
	return SnapshotRefPrefix + "/" + taskID + "/" + ownerID
}

// ThreadSnapshotOwner is the owner id under which a thread's saved worktree state is pinned.
func ThreadSnapshotOwner(threadID string) string { return "thread-" + threadID }

// validSnapshotPart reports whether name is safe to embed in a snapshot ref. It follows git's ref-name
// rules and adds the one that matters most here: no "/", so a part cannot escape its namespace level.
func validSnapshotPart(name string) bool {
	if name == "" || len(name) > 200 {
		return false
	}
	if name[0] == '.' || name[0] == '-' {
		return false
	}
	if strings.HasSuffix(name, ".lock") {
		return false
	}
	if strings.Contains(name, "..") || strings.Contains(name, "@{") {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		// ids MIKE generates are ASCII letters, digits, "-" and "_"; anything else is refused
		// rather than sanitised.
		if c < 0x21 || c > 0x7e {
			return false
		}
		switch c {
		case '~', '^', ':', '?', '*', '[', '\\', '/':
			return false
		}
	}
	return true
}

// snapshotRepoPath returns a directory whose refs a task's snapshots live in: its worktree while that
// exists, the project checkout afterwards. Refs are shared across worktrees, and cleanup runs after the
// worktree is already gone.
func (wm *WorktreeManager) snapshotRepoPath(taskID string) (string, error) {
	if p, err := wm.WorktreePath(taskID); err == nil && p != "" {
		if _, statErr := os.Stat(p); statErr == nil {
			return p, nil
		}
	}
	task, err := wm.store.GetTaskByID(taskID)
	if err != nil || task == nil {
		return "", fmt.Errorf("%w: no worktree or project for task %s", ErrWorktreeNotFound, taskID)
	}
	project, err := wm.store.GetProject(task.ProjectID)
	if err != nil || project == nil || project.Path == "" {
		return "", fmt.Errorf("%w: no project path for task %s", ErrWorktreeNotFound, taskID)
	}
	return project.Path, nil
}

// PinTaskSnapshot pins hash as the snapshot owned by ownerID (a message id, or ThreadSnapshotOwner for a
// thread's saved worktree) in the task's repository.
func (wm *WorktreeManager) PinTaskSnapshot(taskID, ownerID, hash string) error {
	dir, err := wm.snapshotRepoPath(taskID)
	if err != nil {
		return err
	}
	return wm.PinSnapshot(dir, SnapshotRef(taskID, ownerID), hash)
}

// DeleteTaskSnapshots unpins every snapshot of a task, letting git reclaim their objects. It takes
// projectID because task cleanup runs it after the task row may already be gone.
func (wm *WorktreeManager) DeleteTaskSnapshots(projectID, taskID string) (int, error) {
	project, err := wm.store.GetProject(projectID)
	if err != nil || project == nil || project.Path == "" {
		return 0, fmt.Errorf("%w: no project path for project %s", ErrWorktreeNotFound, projectID)
	}
	return wm.DeleteSnapshotRefsUnder(project.Path, SnapshotRefPrefix+"/"+taskID+"/")
}

// DeleteThreadSnapshot unpins one thread's saved worktree state.
func (wm *WorktreeManager) DeleteThreadSnapshot(taskID, threadID string) error {
	dir, err := wm.snapshotRepoPath(taskID)
	if err != nil {
		return err
	}
	return wm.UnpinSnapshot(dir, SnapshotRef(taskID, ThreadSnapshotOwner(threadID)))
}

// UnpinMessageSnapshots releases the snapshots the given messages captured. Safe to call on a thread being
// deleted: a thread forked from it re-pinned whatever it inherited under its own message ids (see
// CopyMessagesToThread), so nothing surviving depends on these names. Missing refs are not an error.
func (wm *WorktreeManager) UnpinMessageSnapshots(taskID string, messageIDs []string) error {
	dir, err := wm.snapshotRepoPath(taskID)
	if err != nil {
		return err
	}
	for _, id := range messageIDs {
		if id == "" {
			continue
		}
		if err := wm.UnpinSnapshot(dir, SnapshotRef(taskID, id)); err != nil {
			return err
		}
	}
	return nil
}

// isSnapshotRef reports whether ref is a well-formed ref inside the snapshot namespace. Nothing reads
// the task ID and owner ID out of a ref — the namespace is what a delete is allowed to touch — so only
// the shape is decided here.
func isSnapshotRef(ref string) bool {
	prefix := SnapshotRefPrefix + "/"
	if !strings.HasPrefix(ref, prefix) {
		return false
	}
	taskID, ownerID, found := strings.Cut(ref[len(prefix):], "/")
	if !found || taskID == "" || ownerID == "" {
		return false
	}
	return validSnapshotPart(taskID) && validSnapshotPart(ownerID)
}

// PinSnapshot makes hash reachable under ref so git cannot garbage-collect it. Idempotent. The object must
// exist: a ref to something unresolvable would fail on every later use, so a failed pin is reported rather
// than stored.
func (wm *WorktreeManager) PinSnapshot(repoPath, ref, hash string) error {
	if !isSnapshotRef(ref) {
		return fmt.Errorf("%w: %q", ErrInvalidSnapshotRef, ref)
	}
	if hash == "" {
		return fmt.Errorf("%w: %s has no object to pin", ErrInvalidSnapshotRef, ref)
	}
	if _, err := Run(repoPath, "rev-parse", "--verify", hash+"^{commit}"); err != nil {
		return fmt.Errorf("pin snapshot %s: object %s is not reachable in this repository: %w", ref, hash, err)
	}
	if err := wm.gitRun(repoPath, "update-ref", ref, hash); err != nil {
		return fmt.Errorf("pin snapshot %s: %w", ref, err)
	}
	return nil
}

// UnpinSnapshot deletes ref. A ref that is already gone is not an error.
func (wm *WorktreeManager) UnpinSnapshot(repoPath, ref string) error {
	if !isSnapshotRef(ref) {
		return fmt.Errorf("%w: %q", ErrInvalidSnapshotRef, ref)
	}
	if err := wm.gitRun(repoPath, "update-ref", "-d", ref); err != nil {
		return fmt.Errorf("unpin snapshot %s: %w", ref, err)
	}
	return nil
}

func (wm *WorktreeManager) DeleteSnapshotRefsUnder(repoPath, prefix string) (int, error) {
	if prefix != SnapshotRefPrefix && !strings.HasPrefix(prefix, SnapshotRefPrefix+"/") {
		return 0, fmt.Errorf("%w: refusing to delete refs under %q", ErrInvalidSnapshotRef, prefix)
	}
	out, err := Run(repoPath, "for-each-ref", "--format=%(refname)", prefix)
	if err != nil {
		return 0, fmt.Errorf("list snapshot refs under %s: %w", prefix, err)
	}
	removed := 0
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		ref := strings.TrimSpace(line)
		if ref == "" {
			continue
		}
		if !isSnapshotRef(ref) {
			continue // never delete outside the namespace
		}
		if err := wm.gitRun(repoPath, "update-ref", "-d", ref); err != nil {
			return removed, fmt.Errorf("delete snapshot ref %s: %w", ref, err)
		}
		removed++
	}
	return removed, nil
}
