package server

// One task, one worktree, one lock. These hold the lock from the outside and ask the two questions that decide
// it: does the rewrite wait, and does the stop that must precede it happen outside the lock.

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mike/backend/stream"
	"mike/backend/testutil/testgit"
	"mike/backend/types"

	"github.com/coder/websocket"
)

// worktreeLockFixture is a task with a worktree holding one untracked file, a base commit a rewind can go back
// to, and one user message to delete.
type worktreeLockFixture struct {
	s              *Server
	projID, taskID string
	worktreePath   string
	file           string
	userMsgID      string
}

func newWorktreeLockFixture(t *testing.T, name string) worktreeLockFixture {
	t.Helper()
	s := testServer(t)
	tmpDir := t.TempDir()
	initGitRepo(t, tmpDir)
	base := strings.TrimSpace(testgit.GitOutput(t, tmpDir, "rev-parse", "HEAD"))

	projID := mustJSON[map[string]any](t, testResponseAuth(t, s, testRequest("POST", "/api/projects",
		map[string]any{"name": name, "path": tmpDir})).Body)["id"].(string)
	taskID := mustJSON[map[string]any](t, testResponseAuth(t, s, testRequest("POST",
		"/api/projects/"+projID+"/tasks", map[string]any{"prompt": "keep or lose"})).Body)["id"].(string)

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
	task.BaseCommit = base
	if err := s.store.UpdateTask(task); err != nil {
		t.Fatalf("update task: %v", err)
	}
	file := filepath.Join(wt, "keep-or-lose.txt")
	if err := os.WriteFile(file, []byte("still here\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	msgID := mustJSON[map[string]any](t, testResponseAuth(t, s, testRequest("POST",
		"/api/projects/"+projID+"/tasks/"+taskID+"/messages",
		map[string]any{"role": "user", "content": "the turn to delete"})).Body)["id"].(string)

	return worktreeLockFixture{s: s, projID: projID, taskID: taskID, worktreePath: wt, file: file, userMsgID: msgID}
}

// TestConversationRewindWaitsBehindTheWorktreeLock: deleting the conversation rewinds the files, and that
// rewind is `checkout -f HEAD` → `clean -fd` → `read-tree`. Run alongside a tool call it deletes work the tool
// was still writing, and the Changes panel then reports whichever half of it the reader happened to catch.
func TestConversationRewindWaitsBehindTheWorktreeLock(t *testing.T) {
	fx := newWorktreeLockFixture(t, "rewind waits")

	// Someone else holds the task's worktree lock — the shape of a tool call mid-write.
	release := fx.s.lockWorktree(fx.taskID)

	cookie, csrf := loginTestCSRF(t, fx.s)
	req := testRequest("DELETE", "/api/projects/"+fx.projID+"/tasks/"+fx.taskID+"/messages",
		map[string]any{"message_id": fx.userMsgID})
	withAuthCSRF(req, cookie, csrf)
	rec := httptest.NewRecorder()
	handler := fx.s.CORSMiddleware(fx.s.SessionMiddleware(fx.s.CSRFMiddleware(CompressMiddleware(fx.s.mux))))
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(rec, req)
		close(done)
	}()

	select {
	case <-done:
		release()
		t.Fatalf("the delete answered while the task's worktree lock was held (status %d)", rec.Code)
	case <-time.After(750 * time.Millisecond):
	}
	if _, err := os.Stat(fx.file); err != nil {
		release()
		t.Fatal("the rewind rewrote the worktree while another holder had the lock")
	}

	release()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the delete never ran once the lock was released")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(fx.file); !os.IsNotExist(err) {
		t.Fatal("the rewind never ran: the worktree still holds a file the surviving conversation does not know about")
	}
}

// TestConversationRewindAnnouncesTheWaitBehindTheWorktreeLock: a delete that lands behind a long tool call has
// to say what it is waiting for, or it looks like a click that went nowhere. The announcement rides the task's
// own stream; this pins the wiring, while backend/stream pins the two frames themselves.
func TestConversationRewindAnnouncesTheWaitBehindTheWorktreeLock(t *testing.T) {
	fx := newWorktreeLockFixture(t, "announced wait")
	cookie, csrf := loginTestCSRF(t, fx.s)
	handler := fx.s.CORSMiddleware(fx.s.SessionMiddleware(fx.s.CSRFMiddleware(CompressMiddleware(fx.s.mux))))
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(t.Context(), 4*stream.LockWaitNoticeEvery+15*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/api/stream",
		&websocket.DialOptions{HTTPHeader: http.Header{"Cookie": []string{cookie}}})
	if err != nil {
		t.Fatalf("dial the app stream: %v", err)
	}
	defer conn.CloseNow()
	subscribe, _ := json.Marshal(map[string]any{"type": "subscribe_task",
		"data": map[string]any{"project_id": fx.projID, "task_id": fx.taskID}})
	if err := conn.Write(ctx, websocket.MessageText, subscribe); err != nil {
		t.Fatalf("subscribe to the task: %v", err)
	}

	release := fx.s.lockWorktree(fx.taskID)
	req := testRequest("DELETE", "/api/projects/"+fx.projID+"/tasks/"+fx.taskID+"/messages",
		map[string]any{"message_id": fx.userMsgID})
	withAuthCSRF(req, cookie, csrf)
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(httptest.NewRecorder(), req)
		close(done)
	}()

	// The wait is announced on an interval and again when the lock is won, so the budget is a multiple of the
	// interval rather than a race with it.
	announced := false
	deadline := time.Now().Add(4 * stream.LockWaitNoticeEvery)
	for !announced && time.Now().Before(deadline) {
		_, raw, readErr := conn.Read(ctx)
		if readErr != nil {
			break
		}
		var frame map[string]any
		if json.Unmarshal(raw, &frame) != nil {
			continue
		}
		if frame["type"] != "task_event" || frame["event"] != string(types.WSTypeLockWait) {
			continue
		}
		data, _ := frame["data"].(map[string]any)
		announced = data["domain"] == "worktree"
	}

	release()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the delete never finished once the lock was released")
	}
	if !announced {
		t.Fatal("the delete stood behind the task's worktree lock and nobody said what it was waiting behind")
	}
}

// TestThreadSwitchStopsTheRunOutsideTheWorktreeLock: the stop waits for the run to exit, and the run's own tool
// call is what holds that lock. A stop inside the acquisition is a deadlock, so the stop has to have landed
// while someone else still holds it.
func TestThreadSwitchStopsTheRunOutsideTheWorktreeLock(t *testing.T) {
	fx := newWorktreeLockFixture(t, "stop outside the lock")
	// UpdateTask will not write an in-flight state from a plain copy, so the state is written by the one call
	// that owns the column — and then read back, because a fixture that quietly failed to start a run would
	// watch a switch that had nothing to wait for.
	if err := fx.s.store.UpdateTaskExecutionState(fx.taskID, fx.projID, types.ExecStateRunning); err != nil {
		t.Fatalf("mark the task running: %v", err)
	}
	if cur, err := fx.s.store.GetTaskByID(fx.taskID); err != nil || cur == nil || !cur.ExecutionState.InFlight() {
		t.Fatalf("the fixture never put the task in flight: %v", err)
	}

	release := fx.s.lockWorktree(fx.taskID)
	got := make(chan func(), 1)
	go func() {
		_, _, unlock := fx.s.quietWorktree(t.Context(), httptest.NewRecorder(), fx.projID, fx.taskID, "switched")
		got <- unlock
	}()

	deadline := time.Now().Add(10 * time.Second)
	for {
		cur, err := fx.s.store.GetTaskByID(fx.taskID)
		if err == nil && cur != nil && !cur.ExecutionState.InFlight() {
			break
		}
		if time.Now().After(deadline) {
			release()
			t.Fatal("the run was still running while the lock was held: the stop sits inside the acquisition, " +
				"and StopTask waits for the goroutine whose tool call holds that lock")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// The stop landed with the lock still held by someone else. Now the switch may take it.
	release()
	select {
	case unlock := <-got:
		if unlock == nil {
			t.Fatal("the switch refused the task it had just stopped")
		}
		unlock()
	case <-time.After(10 * time.Second):
		t.Fatal("the switch never took the lock it was waiting for")
	}
}

// TestThreadSwitchRetriesOnceBeforeRefusing covers the gap the stop-then-lock order leaves: a turn started
// between the two is stopped and tried once more, and only a run still going on the second look is refused.
//
// The states are scripted onto the row the store holds, because a refusal writes that row back to record the
// stop it caused.
func TestThreadSwitchRetriesOnceBeforeRefusing(t *testing.T) {
	statesReader := func(fx worktreeLockFixture, states []types.ExecutionState) func(http.ResponseWriter, string, string) (*types.Task, bool) {
		return func(http.ResponseWriter, string, string) (*types.Task, bool) {
			cur, err := fx.s.store.GetTaskByID(fx.taskID)
			if err != nil {
				return nil, false
			}
			if len(states) == 0 {
				t.Fatal("the switch read the task more times than its two attempts allow")
			}
			cur.ExecutionState, states = states[0], states[1:]
			return cur, true
		}
	}

	t.Run("a turn that started in the gap is stopped and the switch goes ahead", func(t *testing.T) {
		fx := newWorktreeLockFixture(t, "switch retry")
		read := statesReader(fx, []types.ExecutionState{
			types.ExecStateRunning, // before the first lock: stop it
			types.ExecStateRunning, // under the first lock: still there, try once more
			types.ExecStateIdle,    // before the second lock: it is gone
			types.ExecStateIdle,    // under the second lock: the worktree is quiet
		})

		rec := httptest.NewRecorder()
		quieted, stopped, unlock := fx.s.quietWorktreeFrom(t.Context(), rec, fx.projID, fx.taskID, "switched", read)
		if quieted == nil || unlock == nil {
			t.Fatalf("the switch refused after one retry; it answered %d: %s", rec.Code, rec.Body.String())
		}
		if quieted.ExecutionState == types.ExecStateRunning {
			t.Error("the switch was handed a task it had said was still running")
		}
		unlock()
		if !stopped {
			t.Error("the stop that happened is not reported, so a later refusal could not record it")
		}
		if rec.Body.Len() != 0 {
			t.Errorf("the switch answered a success with a body: %s", rec.Body.String())
		}
	})

	t.Run("a run still going on the second look is refused with the worktree untouched", func(t *testing.T) {
		fx := newWorktreeLockFixture(t, "switch refuse")
		headBefore := strings.TrimSpace(testgit.GitOutput(t, fx.worktreePath, "rev-parse", "HEAD"))
		always := func(http.ResponseWriter, string, string) (*types.Task, bool) {
			cur, err := fx.s.store.GetTaskByID(fx.taskID)
			if err != nil {
				return nil, false
			}
			cur.ExecutionState = types.ExecStateRunning
			return cur, true
		}

		rec := httptest.NewRecorder()
		quieted, stopped, unlock := fx.s.quietWorktreeFrom(t.Context(), rec, fx.projID, fx.taskID, "switched", always)
		if quieted != nil || unlock != nil {
			if unlock != nil {
				unlock()
			}
			t.Fatal("the switch captured a worktree with a run still going")
		}
		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
		}
		if !stopped {
			t.Error("a refusal that stopped the run must say so, or the card goes on claiming it is running")
		}
		if _, err := os.Stat(fx.file); err != nil {
			t.Fatal("the refusal rewrote the worktree: a refusal promises nothing was switched")
		}
		if head := strings.TrimSpace(testgit.GitOutput(t, fx.worktreePath, "rev-parse", "HEAD")); head != headBefore {
			t.Fatal("the refusal moved the branch: a refusal promises nothing was switched")
		}
		if !strings.Contains(rec.Body.String(), "nothing was switched") {
			t.Errorf("the refusal does not say nothing happened: %s", rec.Body.String())
		}
	})
}

// A switch whose person navigated away stops queueing behind the tool call that holds the worktree, and
// nothing was captured, so nothing has to be given back.
func TestThreadSwitchCalledOffCapturesNothing(t *testing.T) {
	fx := newWorktreeLockFixture(t, "switch called off")
	holder := fx.s.lockWorktree(fx.taskID)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	read := func(http.ResponseWriter, string, string) (*types.Task, bool) {
		cur, err := fx.s.store.GetTaskByID(fx.taskID)
		if err != nil {
			return nil, false
		}
		return cur, true
	}

	rec := httptest.NewRecorder()
	quieted, stopped, unlock := fx.s.quietWorktreeFrom(ctx, rec, fx.projID, fx.taskID, "switched", read)
	holder()
	if quieted != nil || unlock != nil {
		if unlock != nil {
			unlock()
		}
		t.Fatal("a caller called off was handed the worktree it never waited into")
	}
	if stopped {
		t.Error("nothing was running, so a switch that stopped nothing must not report a stop")
	}
	if rec.Body.Len() == 0 {
		t.Error("the abandoned wait left the request unanswered")
	}
	if _, err := os.Stat(fx.file); err != nil {
		t.Fatal("the abandoned switch touched the worktree")
	}

	// The waiter that gave up left no claim on the key: whoever asks next walks straight in.
	got := make(chan struct{})
	go func() { fx.s.lockWorktree(fx.taskID)(); close(got) }()
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("the called-off waiter kept the task's worktree lock")
	}
}
