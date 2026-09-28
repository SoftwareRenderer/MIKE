package git

import (
	"fmt"
	"strings"
)

// PushStash stashes the worktree changes under `message` and returns the hash of the entry it
// created, found by diffing the stash list across the push: the stack is one per repository and
// holds the user's own entries, so the entry on top is not necessarily ours.
// The caller holds the project stash mutex (LockProjectStash) — WithStash, a thread switch, whoever
// runs the sequence — never these methods: the lock is not reentrant (PopStashByHash calls
// DropStashByHash), and the unit of correctness is the list-then-write, not one git call.
func (wm *WorktreeManager) PushStash(repoPath, message string) (string, error) {
	before, err := wm.stashHashSet(repoPath)
	if err != nil {
		return "", fmt.Errorf("stash list: %w", err)
	}
	if out, err := wm.gitRunInGitDir(repoPath, "stash", "push", "-u", "-m", message); err != nil {
		return "", fmt.Errorf("stash push: %s", gitMessage(out, err))
	}
	entries, err := wm.listStashEntries(repoPath)
	if err != nil {
		return "", fmt.Errorf("stash list: %w", err)
	}
	var fresh []StashEntry
	for _, e := range entries { // newest first
		if !before[e.Hash] {
			fresh = append(fresh, e)
		}
	}
	for _, e := range fresh {
		if strings.Contains(e.Subject, message) {
			return e.Hash, nil
		}
	}
	switch len(fresh) {
	case 0:
		return "", ErrNothingToStash
	case 1:
		// git rewrapped the label; the one entry that appeared during our push is ours.
		return fresh[0].Hash, nil
	default:
		return "", fmt.Errorf("stash push: %d entries appeared and none carries %q", len(fresh), message)
	}
}

// PopStashByHash restores the stash entry with this commit hash into dir's working tree and drops
// it, returning whether the entry was found. `git stash pop` pops stash@{0} with no idea who pushed
// it, so on a shared stack one merge restores another's work and keeps its own entry, with no error
// on either side — hence identity by hash. A failed apply leaves the entry on the stack: dropping
// what could not be applied turns a resolvable conflict into lost work. When the failure left unmerged
// paths the error is a *RestoreConflictError naming them, which is how a caller tells a conflict it must
// report from a refusal that left the tree alone.
//
// The restore keeps the pushed work's staged/unstaged split: a plain apply returns a modification
// unstaged, and commit-and-done commits the index only.
// The caller holds the project stash mutex — see PushStash.
func (wm *WorktreeManager) PopStashByHash(dir, hash string) (bool, error) {
	if hash == "" {
		return false, nil
	}
	hash = strings.TrimSpace(hash)

	found := false
	entries, err := wm.listStashEntries(dir)
	if err != nil {
		return false, fmt.Errorf("stash list: %w", err)
	}
	for _, e := range entries {
		if e.Hash == hash || strings.HasPrefix(e.Hash, hash) {
			found = true
			break
		}
	}
	if !found {
		// Not an error: the entry was already dropped or swept. Callers use the result to decide
		// whether to say so, because silently continuing is how lost work stays lost.
		return false, nil
	}

	// A refused --index leaves the tree untouched, which is what makes the retry below safe.
	before, beforeOK := wm.porcelainStatus(dir)
	out, err := wm.gitRunInGitDir(dir, "stash", "apply", "--index", hash)
	if after, afterOK := wm.porcelainStatus(dir); err != nil && beforeOK && afterOK && before == after {
		out, err = wm.gitRunInGitDir(dir, "stash", "apply", hash)
	}
	if err != nil {
		// Deliberately not dropped — see the doc comment. The unmerged index, not git's wording, is what
		// says the work reached the tree as well as staying on the stack.
		if paths := wm.unmergedPaths(dir); len(paths) > 0 {
			return true, &RestoreConflictError{Hash: hash, Paths: paths, GitMessage: gitMessage(out, err)}
		}
		return true, fmt.Errorf("stash apply %s: %s", shortHash(hash), gitMessage(out, err))
	}
	if err := wm.DropStashByHash(dir, hash); err != nil {
		// The work is already restored; a leftover entry is cosmetic, not data loss.
		return true, fmt.Errorf("stash applied but the entry could not be dropped: %w", err)
	}
	return true, nil
}

// porcelainStatus fingerprints dir's working tree, to tell a refused apply from a partial one.
func (wm *WorktreeManager) porcelainStatus(dir string) (string, bool) {
	out, err := Run(dir, "status", "--porcelain")
	return out, err == nil
}

// unmergedPaths lists the paths dir's index holds unmerged. Empty means nothing conflicted.
func (wm *WorktreeManager) unmergedPaths(dir string) []string {
	out, err := Run(dir, "diff", "--name-only", "--diff-filter=U", "-z")
	if err != nil {
		return nil
	}
	var paths []string
	for _, p := range strings.Split(out, "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths
}

// listStashEntries parses "git stash list --format=%gd%x00%H%x00%gs" output.
func (wm *WorktreeManager) listStashEntries(repoPath string) ([]StashEntry, error) {
	out, err := Run(repoPath, "stash", "list", "--format=%gd%x00%H%x00%gs")
	if err != nil {
		return nil, err
	}
	var entries []StashEntry
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\x00", 3)
		if len(parts) < 3 {
			continue
		}
		entries = append(entries, StashEntry{
			Ref:     parts[0],
			Hash:    strings.TrimSpace(parts[1]),
			Subject: parts[2],
		})
	}
	return entries, nil
}

// dropMatchingStashes lists stashes in the project, filters by predicate, and drops by ref.
// Drops in reverse order (highest index first) so refs don't shift mid-loop.
func (wm *WorktreeManager) dropMatchingStashes(projectID string, filter func(subject string) bool) error {
	project, err := wm.store.GetProject(projectID)
	if err != nil {
		return fmt.Errorf("get project: %w", err)
	}
	entries, err := wm.listStashEntries(project.Path)
	if err != nil {
		return nil
	}
	// Reverse order: highest stash index first so refs don't shift.
	for i := len(entries) - 1; i >= 0; i-- {
		if filter(entries[i].Subject) {
			_ = wm.gitRun(project.Path, "stash", "drop", entries[i].Ref)
		}
	}
	return nil
}

// DropStashByHash drops the stash entry with this commit hash. An entry not in the list is treated
// as already gone and returns nil — never a fallback to stash@{0}, which when ours has vanished is
// somebody else's state. The caller holds the project stash mutex — see PushStash.
func (wm *WorktreeManager) DropStashByHash(repoPath, hash string) error {
	if hash == "" {
		return nil
	}
	hash = strings.TrimSpace(hash)
	entries, err := wm.listStashEntries(repoPath)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if e.Hash == hash || strings.HasPrefix(e.Hash, hash) {
			return wm.gitRun(repoPath, "stash", "drop", e.Ref)
		}
	}
	return nil
}

// NewestStashHashForMessage returns the hash of the newest entry whose reflog subject carries
// label, for restores that happen in code that never saw the push; labels embed the task ID. Weaker
// than a hash — a caller still holding one should use PopStashByHash.
func (wm *WorktreeManager) NewestStashHashForMessage(dir, label string) (string, bool, error) {
	if label == "" {
		return "", false, nil
	}
	entries, err := wm.listStashEntries(dir)
	if err != nil {
		return "", false, err
	}
	for _, e := range entries { // newest first
		if strings.Contains(e.Subject, label) {
			return e.Hash, true, nil
		}
	}
	return "", false, nil
}

func StashMessageForSyncMerge(taskID string) string {
	return stashMessageWithPrefix(stashLabelSyncMerge, taskID)
}

// StashMessageForAutoMerge returns a stash message for an auto-merge operation.
func StashMessageForAutoMerge(taskID string) string {
	return stashMessageWithPrefix(stashLabelAutoMerge, taskID)
}

// DropStashEntriesForBranch removes stash entries for the given branch.
// Drops by commit hash — no index renumbering issues.
func (wm *WorktreeManager) DropStashEntriesForBranch(projectID string, branchName string) error {
	defer wm.LockProjectStash(projectID)()
	branchMarker := " " + branchName + ":"
	return wm.dropMatchingStashes(projectID, func(subject string) bool {
		return strings.Contains(subject, branchMarker)
	})
}
