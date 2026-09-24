package git

// TaskDiffBaseline names the ref a task's diff is computed against. Each case here is one link in that chain.

import (
	"testing"

	"mike/backend/store"
	"mike/backend/types"
)

type diffBaselineEnv struct {
	repoDir    string
	mainBranch string
	forkPoint  string // the commit the task branch was cut at
	taskBranch string
	task       *types.Task
	store      *store.Store
	wm         *WorktreeManager
}

// forkThread points the task's current thread at gitBranch, as a fork does.
func (f *diffBaselineEnv) forkThread(t *testing.T, threadID, gitBranch string) {
	t.Helper()
	if err := f.store.CreateThread(&types.TaskThread{
		ID: threadID, TaskID: f.task.ID, Name: threadID, IsActive: true, GitBranch: gitBranch,
	}); err != nil {
		t.Fatal(err)
	}
	f.task.CurrentThreadID = threadID
	if err := f.store.UpdateTask(f.task); err != nil {
		t.Fatal(err)
	}
}

func (f *diffBaselineEnv) commitOnTaskBranch(t *testing.T, name, content, message string) {
	t.Helper()
	wtPath, err := f.wm.WorktreePath(f.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, wtPath, name, content)
	runGit(t, wtPath, "add", name)
	runGit(t, wtPath, "commit", "-m", message)
}

// newDiffBaselineEnv returns a task branch with one commit of its own, cut at forkPoint, with a real
// worktree so merge-base can run in it.
func newDiffBaselineEnv(t *testing.T) *diffBaselineEnv {
	t.Helper()

	repoDir, taskID, s, _, wm := setupTestRepo(t)
	mainBranch, err := wm.gitCurrentBranch(repoDir)
	if err != nil || mainBranch == "" || mainBranch == "HEAD" {
		t.Skipf("cannot determine main branch: %v", err)
	}
	taskBranch := "task/" + taskID
	if _, _, err := wm.CreateWorktree("test-project", taskID, taskBranch, ""); err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	f := &diffBaselineEnv{
		repoDir:    repoDir,
		mainBranch: mainBranch,
		forkPoint:  revParse(t, repoDir, "HEAD"),
		taskBranch: taskBranch,
		store:      s,
		wm:         wm,
	}
	task, err := s.GetTaskByID(taskID)
	if err != nil {
		t.Fatal(err)
	}
	f.task = task
	f.commitOnTaskBranch(t, "task-work.txt", "authored by the task\n", "task commit")
	return f
}

func TestTaskDiffBaselinePinnedCommitOutlivesTheBranchItCameFrom(t *testing.T) {
	f := newDiffBaselineEnv(t)

	// The target branch moves forward after the task started.
	writeFile(t, f.repoDir, "someone-else.txt", "not this task's change\n")
	runGit(t, f.repoDir, "add", "someone-else.txt")
	runGit(t, f.repoDir, "commit", "-m", "another branch moved")

	f.task.BaseRef = f.mainBranch
	f.task.BaseCommit = f.forkPoint
	if got := f.wm.TaskDiffBaseline(f.task); got != f.forkPoint {
		t.Errorf("baseline = %q, want the pinned %q: diffing the moved branch shows commits this task "+
			"never made", got, f.forkPoint)
	}
}

func TestTaskDiffBaselineDeletedBaseRefFallsBackToDivergence(t *testing.T) {
	f := newDiffBaselineEnv(t)

	// The recorded base ref no longer resolves.
	runGit(t, f.repoDir, "branch", "explore/idea", f.forkPoint)
	runGit(t, f.repoDir, "branch", "-D", "explore/idea")

	f.task.BaseRef = "explore/idea"
	if got := f.wm.TaskDiffBaseline(f.task); got != f.forkPoint {
		t.Errorf("baseline = %q, want the divergence point %q of %s", got, f.forkPoint, f.taskBranch)
	}
}

func TestTaskDiffBaselineForkedThreadUsesMergeBase(t *testing.T) {
	f := newDiffBaselineEnv(t)

	// A thread is forked off the task branch, then the task keeps committing.
	forkBranch := "thread/forked-idea"
	runGit(t, f.repoDir, "branch", forkBranch, f.taskBranch)
	forkPoint := revParse(t, f.repoDir, forkBranch)
	f.commitOnTaskBranch(t, "after-fork.txt", "work the fork never saw\n", "commit after the fork")

	f.forkThread(t, "thread-row-1", forkBranch)
	f.task.BaseRef = f.mainBranch

	if got := f.wm.TaskDiffBaseline(f.task); got != forkPoint {
		t.Errorf("baseline = %q, want the fork's merge-base %q: the recorded base predates the fork, so "+
			"the panel shows the other thread's work as this task's", got, forkPoint)
	}
}

func TestTaskDiffBaselineThreadOnTheTaskBranchKeepsTheRecordedBase(t *testing.T) {
	f := newDiffBaselineEnv(t)

	// A thread whose branch is the task branch holds all of the task's changes itself.
	f.forkThread(t, "thread-row-2", f.taskBranch)
	f.task.BaseRef = f.mainBranch
	f.task.BaseCommit = f.forkPoint

	if got := f.wm.TaskDiffBaseline(f.task); got != f.forkPoint {
		t.Errorf("baseline = %q, want the recorded %q", got, f.forkPoint)
	}
}

func TestTaskDiffBaselineUnknownTaskNamesNoBaseline(t *testing.T) {
	_, _, _, _, wm := setupTestRepo(t)
	if got := wm.TaskDiffBaseline(nil); got != "" {
		t.Errorf("baseline for no task = %q, want empty", got)
	}
}
