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
		t.Fatalf("the fixture repo holds a commit, so rev-parse must name its branch: got %q (err %v)", mainBranch, err)
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

// The links below TaskDiffBaseline: a recorded baseline is kept only when it is a meaningful diff
// baseline for this task's branch. An unset one, one that equals the task's own branch (a structurally
// empty diff), or one typed in before the branch existed all fall back to the divergence point, and a
// task with no branch has nothing to resolve against, so its recorded name passes through.
func TestResolveTaskBaselineKeepsOnlyAMeaningfulRecordedBase(t *testing.T) {
	repoDir, _, _, _, wm := setupTestRepo(t)

	mainBranch, err := wm.gitCurrentBranch(repoDir)
	if err != nil || mainBranch == "" || mainBranch == "HEAD" {
		t.Fatalf("the fixture repo holds a commit, so rev-parse must name its branch: got %q (err %v)", mainBranch, err)
	}
	forkPoint := revParse(t, repoDir, "HEAD")

	// Fork a task branch with a commit on it.
	runGit(t, repoDir, "branch", "task/baseline-check")
	runGit(t, repoDir, "checkout", "task/baseline-check")
	writeFile(t, repoDir, "work.txt", "work\n")
	runGit(t, repoDir, "add", "work.txt")
	runGit(t, repoDir, "commit", "-m", "task work")
	task := &types.Task{Branch: "task/baseline-check"}

	if got := wm.resolveTaskBaseline(repoDir, mainBranch, task); got != forkPoint {
		t.Errorf("empty baseline = %q, want divergence point %q", got, forkPoint)
	}

	own := *task
	own.BaseRef = "task/baseline-check"
	if got := wm.resolveTaskBaseline(repoDir, mainBranch, &own); got != forkPoint {
		t.Errorf("own-branch baseline = %q, want divergence point %q", got, forkPoint)
	}

	// A valid, different base is kept as-is.
	valid := *task
	valid.BaseRef = mainBranch
	if got := wm.resolveTaskBaseline(repoDir, mainBranch, &valid); got != mainBranch {
		t.Errorf("valid baseline = %q, want %q", got, mainBranch)
	}

	// A task without a branch passes the recorded baseline through untouched
	// (nothing to resolve a divergence point against).
	noBranch := &types.Task{BaseRef: "explore/idea"}
	if got := wm.resolveTaskBaseline(repoDir, mainBranch, noBranch); got != "explore/idea" {
		t.Errorf("no-branch baseline = %q, want pass-through %q", got, "explore/idea")
	}
}
