package server

// A fork's files and its copied conversation have to come from the same point: inherit the chat without
// the checkpoint it was cut at and the history describes files the fork no longer has.

import (
	"bytes"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kanban/backend/git"
	"kanban/backend/types"
)

// forkFixture: a Main thread with one completed turn (a file change plus its checkpoint), a later user
// message, and a fork cut at that message.
type forkFixture struct {
	projID, taskID, worktreePath, mainThreadID, forkID, checkpoint, fileContent string
}

func setupForkWithCheckpoint(t *testing.T, s *Server, name string) forkFixture {
	t.Helper()
	tmpDir := t.TempDir()
	content := "count to 10\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("original\n"), 0o644); err != nil {
		t.Fatalf("write initial: %v", err)
	}
	initGitRepo(t, tmpDir)

	projID := mustJSON[map[string]any](t, testResponseAuth(t, s, testRequest("POST", "/api/projects",
		map[string]any{"name": name, "path": tmpDir})).Body)["id"].(string)
	taskID := mustJSON[map[string]any](t, testResponseAuth(t, s, testRequest("POST", "/api/projects/"+projID+"/tasks",
		map[string]any{"prompt": "count with me"})).Body)["id"].(string)

	wt, _, err := s.executor.WorktreeMgr().CreateWorktree(projID, taskID, "task/"+taskID, "HEAD")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	task, err := s.store.GetTask(projID, taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	task.Branch = "task/" + taskID
	task.BaseRef = "master"
	if err := s.store.UpdateTask(task); err != nil {
		t.Fatalf("update task: %v", err)
	}

	threads := mustJSON[[]map[string]any](t, testResponseAuth(t, s,
		testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID+"/threads", nil)).Body)
	mainThreadID := threads[0]["id"].(string)

	// The turn's work and its snapshot, taken the way the executor takes it. The file is never committed
	// — normal for a task mid-flight, and why a fork cannot branch from the branch.
	if err := os.WriteFile(filepath.Join(wt, "file.txt"), []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	mustRunGit(t, wt, "add", "-A")
	checkpoint := strings.TrimSpace(mustRunGitOutput(t, wt, "stash", "create"))
	if checkpoint == "" {
		t.Fatal("stash create returned empty")
	}
	base := time.Now().UTC().Add(-time.Minute)
	assistant := &types.TaskMessage{
		ID: "asst-turn-1", TaskID: taskID, ThreadID: mainThreadID, Role: "assistant", Content: "wrote it",
		GitCommitHash: checkpoint, CreatedAt: base,
	}
	if err := s.store.CreateTaskMessage(assistant); err != nil {
		t.Fatalf("create assistant: %v", err)
	}
	if err := s.executor.WorktreeMgr().PinTaskSnapshot(taskID, assistant.ID, checkpoint); err != nil {
		t.Fatalf("pin checkpoint: %v", err)
	}
	userMsg := mustJSON[map[string]any](t, testResponseAuth(t, s, testRequest("POST",
		"/api/projects/"+projID+"/tasks/"+taskID+"/messages",
		map[string]any{"role": "user", "content": "now count higher"})).Body)

	forkID := mustJSON[map[string]any](t, testResponseAuth(t, s, testRequest("POST",
		"/api/projects/"+projID+"/tasks/"+taskID+"/threads/fork",
		map[string]any{"from_thread_id": mainThreadID, "from_message_id": userMsg["id"].(string)})).Body)["id"].(string)

	return forkFixture{projID: projID, taskID: taskID, worktreePath: wt, mainThreadID: mainThreadID,
		forkID: forkID, checkpoint: checkpoint, fileContent: content}
}

// postMessageIn sends a user message into the task's currently active thread.
func postMessageIn(t *testing.T, s *Server, fx forkFixture, content string) string {
	t.Helper()
	return mustJSON[map[string]any](t, testResponseAuth(t, s, testRequest("POST",
		"/api/projects/"+fx.projID+"/tasks/"+fx.taskID+"/messages",
		map[string]any{"role": "user", "content": content})).Body)["id"].(string)
}

func forkFrom(t *testing.T, s *Server, fx forkFixture, fromThreadID, fromMessageID string) string {
	t.Helper()
	rr := testResponseAuth(t, s, testRequest("POST", "/api/projects/"+fx.projID+"/tasks/"+fx.taskID+"/threads/fork",
		map[string]any{"from_thread_id": fromThreadID, "from_message_id": fromMessageID}))
	if rr.Code != http.StatusCreated {
		t.Fatalf("fork: expected 201, got %d: %s", rr.Code, rr.Body.String())
	}
	return mustJSON[map[string]any](t, rr.Body)["id"].(string)
}

// TestForkInsideForkKeepsTheCheckpointItInherited: a fork's file state is the last checkpoint before the
// cut, in its own thread. When the copy stripped the checkpoint hash that lookup found nothing and fell
// back to the task's starting commit, rewinding a fork's files behind its own conversation.
func TestForkInsideForkKeepsTheCheckpointItInherited(t *testing.T) {
	s := testServer(t)
	fx := setupForkWithCheckpoint(t, s, "fork-of-fork")

	// First generation: the fork is cut at the checkpoint, so its branch is the checkpoint.
	if got := strings.TrimSpace(mustRunGitOutput(t, fx.worktreePath, "rev-parse", "thread/"+fx.forkID)); got != fx.checkpoint {
		t.Fatalf("first fork branched at %s, want the inherited checkpoint %s", got, fx.checkpoint)
	}

	// Fork again from inside it. Nothing was committed since, so the only recorded state is the
	// inherited checkpoint — where the files have to be.
	innerMsg := postMessageIn(t, s, fx, "actually, count to 3")
	innerID := forkFrom(t, s, fx, fx.forkID, innerMsg)

	if got := strings.TrimSpace(mustRunGitOutput(t, fx.worktreePath, "rev-parse", "thread/"+innerID)); got != fx.checkpoint {
		t.Errorf("fork-of-a-fork branched at %s, want %s (the task's base commit means the files rewound past the conversation)", got, fx.checkpoint)
	}
	verifyWorktreeContent(t, fx.worktreePath, "file.txt", fx.fileContent, "after forking inside a fork")

	// The copied rows carry the hash, and the fork pins the snapshot under its own rows — pinned only by
	// the parent's refs, it would become prunable the day the parent is deleted.
	var copied *types.TaskMessage
	msgs, err := s.store.GetTaskMessages(fx.taskID, innerID)

	if err != nil {
		t.Fatalf("fork messages: %v", err)
	}
	for i := range msgs {
		if msgs[i].Role == "assistant" && msgs[i].GitCommitHash != "" {
			copied = &msgs[i]
		}
	}
	if copied == nil {
		t.Fatal("no copied row carries a checkpoint hash")
	}
	if copied.GitCommitHash != fx.checkpoint {
		t.Errorf("copied row holds %s, want %s", copied.GitCommitHash, fx.checkpoint)
	}
	ref := git.SnapshotRef(fx.taskID, copied.ID)
	if !refExists(t, fx.worktreePath, ref) {
		t.Errorf("the inherited snapshot is not pinned for the fork under %s", ref)
	}

	// Deleting the fork releases what its own rows named; what a surviving thread still names stays
	// pinned, and that thread holds its own pin for it anyway.
	del := testResponseAuth(t, s, testRequest("DELETE", "/api/projects/"+fx.projID+"/tasks/"+fx.taskID+"/threads",
		map[string]any{"thread_id": innerID}))
	if del.Code != http.StatusOK {
		t.Fatalf("delete thread: expected 200, got %d: %s", del.Code, del.Body.String())
	}
	if refExists(t, fx.worktreePath, ref) {
		t.Errorf("%s outlived the thread whose message pinned it", ref)
	}
	parentMsgs, err := s.store.GetTaskMessages(fx.taskID, fx.forkID)
	if err != nil {
		t.Fatalf("parent fork messages: %v", err)
	}
	var parentRef string
	for i := range parentMsgs {
		if parentMsgs[i].GitCommitHash != "" {
			parentRef = git.SnapshotRef(fx.taskID, parentMsgs[i].ID)
		}
	}
	if parentRef == "" || !refExists(t, fx.worktreePath, parentRef) {
		t.Errorf("deleting a fork unpinned the thread it was forked from (%s)", parentRef)
	}
	if !refExists(t, fx.worktreePath, git.SnapshotRef(fx.taskID, "asst-turn-1")) {
		t.Error("deleting a fork unpinned the Main thread's own checkpoint")
	}
}

// Deleting a turn rewinds the files to the last state the remaining conversation recorded — inside a
// fork, that is the state it inherited.
func TestDeletingMessagesInAForkRollsBackToTheSurvivingCheckpoint(t *testing.T) {
	s := testServer(t)
	fx := setupForkWithCheckpoint(t, s, "fork-rollback")

	// The fork does its own turn: count to 3, captured in a second snapshot.
	later := "count to 3\n"
	if err := os.WriteFile(filepath.Join(fx.worktreePath, "file.txt"), []byte(later), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	mustRunGit(t, fx.worktreePath, "add", "-A")
	forkSnapshot := strings.TrimSpace(mustRunGitOutput(t, fx.worktreePath, "stash", "create"))
	assistant := &types.TaskMessage{
		ID: "asst-fork-turn", TaskID: fx.taskID, ThreadID: fx.forkID, Role: "assistant", Content: "counted to 3",
		GitCommitHash: forkSnapshot, CreatedAt: time.Now().UTC(),
	}
	if err := s.store.CreateTaskMessage(assistant); err != nil {
		t.Fatalf("create fork assistant: %v", err)
	}
	if err := s.executor.WorktreeMgr().PinTaskSnapshot(fx.taskID, assistant.ID, forkSnapshot); err != nil {
		t.Fatalf("pin fork snapshot: %v", err)
	}
	verifyWorktreeContent(t, fx.worktreePath, "file.txt", later, "before the deletion")

	// What remains is the copied conversation, ending at the inherited checkpoint — so the file goes back
	// to counting to 10, not to the task's origin.
	del := testResponseAuth(t, s, testRequest("DELETE", "/api/projects/"+fx.projID+"/tasks/"+fx.taskID+"/messages",
		map[string]any{"message_id": assistant.ID}))
	if del.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", del.Code, del.Body.String())
	}
	verifyWorktreeContent(t, fx.worktreePath, "file.txt", fx.fileContent, "after deleting the fork's turn")

	// The deleted turn's snapshot must stop being pinned; anything with a surviving row keeps its pin.
	if refExists(t, fx.worktreePath, git.SnapshotRef(fx.taskID, assistant.ID)) {
		t.Errorf("%s outlived the message that named it", git.SnapshotRef(fx.taskID, assistant.ID))
	}
	if !refExists(t, fx.worktreePath, git.SnapshotRef(fx.taskID, "asst-turn-1")) {
		t.Error("deleting a later turn must not unpin a checkpoint the surviving conversation still names")
	}
}

// delete_single is the same case in miniature: the row and the snapshot it named leave together.
func TestDeleteSingleMessageReleasesItsSnapshot(t *testing.T) {
	s := testServer(t)
	fx := setupForkWithCheckpoint(t, s, "dismiss-error")

	ref := git.SnapshotRef(fx.taskID, "asst-turn-1")
	if !refExists(t, fx.worktreePath, ref) {
		t.Fatal("setup: the checkpoint is not pinned")
	}
	del := testResponseAuth(t, s, testRequest("DELETE", "/api/projects/"+fx.projID+"/tasks/"+fx.taskID+"/messages",
		map[string]any{"message_id": "asst-turn-1", "delete_single": true}))
	if del.Code != http.StatusOK {
		t.Fatalf("delete: expected 200, got %d: %s", del.Code, del.Body.String())
	}
	if refExists(t, fx.worktreePath, ref) {
		t.Errorf("%s outlived the message that named it", ref)
	}
}

// Deleting the active thread: the worktree is standing on its branch, so the handler must move it to the
// replacement's branch first — for Main, a branch that has to be resolved, since Main has no git_branch.
// Git will not delete a checked-out branch.
func TestDeletingActiveThreadDeletesItsBranch(t *testing.T) {
	s := testServer(t)
	fx := setupForkWithCheckpoint(t, s, "delete-active-thread")

	if got := gitCurrentBranch(t, fx.worktreePath); got != "thread/"+fx.forkID {
		t.Fatalf("setup: expected the worktree on thread/%s, got %q", fx.forkID, got)
	}
	// Dirty on purpose: a plain checkout refuses uncommitted changes, which is how a delete used to
	// leave its branch behind.
	if err := os.WriteFile(filepath.Join(fx.worktreePath, "file.txt"), []byte("discarded work\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fx.worktreePath, "scratch.txt"), []byte("discarded file\n"), 0o644); err != nil {
		t.Fatalf("write scratch: %v", err)
	}
	del := testResponseAuth(t, s, testRequest("DELETE", "/api/projects/"+fx.projID+"/tasks/"+fx.taskID+"/threads",
		map[string]any{"thread_id": fx.forkID}))
	if del.Code != http.StatusOK {
		t.Fatalf("delete thread: expected 200, got %d: %s", del.Code, del.Body.String())
	}

	if refExists(t, fx.worktreePath, "refs/heads/thread/"+fx.forkID) {
		t.Error("the deleted thread's branch is still in the repository")
	}
	if _, err := os.Stat(filepath.Join(fx.worktreePath, "scratch.txt")); !os.IsNotExist(err) {
		t.Error("the deleted thread's untracked file is still in the worktree")
	}
	want := "task/" + fx.taskID
	if got := gitCurrentBranch(t, fx.worktreePath); got != want {
		t.Errorf("worktree is on %q, want the replacement thread's branch %q", got, want)
	}
	task, err := s.store.GetTask(fx.projID, fx.taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if task.Branch != want {
		t.Errorf("task.Branch is %q, want %q — a stale branch points every later operation at a deleted ref", task.Branch, want)
	}
}

func gitCurrentBranch(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "branch", "--show-current")
	cmd.Dir = dir
	var buf bytes.Buffer
	cmd.Stdout = &buf
	if err := cmd.Run(); err != nil {
		t.Fatalf("git branch --show-current in %s: %v", dir, err)
	}
	return strings.TrimSpace(buf.String())
}
