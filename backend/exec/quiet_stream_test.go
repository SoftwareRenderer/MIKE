package exec

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"mike/backend/llama"
	"mike/backend/stream"
	"mike/backend/types"
)

// streamsATokenThenGoesQuiet answers the way a provider composing a constrained tool call does: a
// first token, a long stretch of nothing while it generates, then the rest. Silence of that shape is
// the normal case - llama.ErrStreamSilent carries the measurement.
func streamsATokenThenGoesQuiet(quiet time.Duration) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		f, ok := w.(http.Flusher)
		if !ok {
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Reading\"},\"finish_reason\":null}]}\n\n")
		f.Flush()
		time.Sleep(quiet)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\" it now\"},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		f.Flush()
	}
}

// TestProviderThatGoesQuietMidAnswerFinishesTheTurn holds the rule the whole change exists for: a
// stream that goes quiet and then speaks again is one answer, and the run is not a failed one.
func TestProviderThatGoesQuietMidAnswerFinishesTheTurn(t *testing.T) {
	t.Parallel()
	srv, _ := crashLLM(t, streamsATokenThenGoesQuiet(1200*time.Millisecond))
	te, _, projectID, initialSHA := setupExecutorWithGit(t)
	const taskID = "quiet-then-finish"
	worktreePath := crashTask(t, te, projectID, initialSHA, taskID, srv.URL)

	startExecRun(t, te, projectID, taskID, srv.URL, worktreePath)

	msgs := waitForAssistantRow(t, te, taskID, " it now", 30*time.Second)
	// The invariant is the stitched sentence: a stream ended for silence throws away the tokens it
	// already sent and the retry answers again from scratch.
	for _, m := range msgs {
		if m.Role == "assistant" && strings.Contains(m.Content, "Reading") {
			if m.Content != "Reading it now" {
				t.Errorf("assistant row = %q, want the resumed stream stitched onto the tokens it already sent",
					m.Content)
			}
			break
		}
	}
	if row := mustGetTask(t, te, taskID); row.ExecutionState == types.ExecStateFailed {
		t.Errorf("the run failed on silence: output=%q", row.Output)
	}
	for _, m := range msgs {
		if m.Role == "error" {
			t.Errorf("an error row was stored for a turn that completed: %q", m.Content)
		}
	}
}

// TestQuietStreamCeilingIsReportedAsSilence holds the other half: an operator who does set a ceiling
// is told the stream went quiet, in seconds, and is never sent to check their network.
func TestQuietStreamCeilingIsReportedAsSilence(t *testing.T) {
	t.Parallel()
	srv, _ := crashLLM(t, func(w http.ResponseWriter) {
		f, ok := w.(http.Flusher)
		if !ok {
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Thinking\"},\"finish_reason\":null}]}\n\n")
		f.Flush()
		time.Sleep(6 * time.Second)
	})
	te, _, projectID, initialSHA := setupExecutorWithGit(t)
	const taskID = "quiet-ceiling"
	worktreePath := crashTask(t, te, projectID, initialSHA, taskID, srv.URL)

	cfg, err := te.configStore.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	cfg.Providers = append(cfg.Providers, types.Provider{
		Name: "slow", URL: srv.URL, Concurrency: 1,
		StreamIdleSeconds: 1, // the shortest ceiling the provider lookup can carry
	})
	if err := te.configStore.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}

	startExecRun(t, te, projectID, taskID, srv.URL, worktreePath)

	waitForFailureRow(t, te, taskID, 60*time.Second)
	row := mustGetTask(t, te, taskID)
	if row.ExecutionState != types.ExecStateFailed {
		t.Fatalf("execution=%q, want failed: the ceiling was set and never honoured", row.ExecutionState)
	}

	errRow := lastErrorRow(mustMessages(t, te, taskID, "thread-"+taskID))
	if !strings.Contains(errRow, "[llm_stream_silent]") {
		t.Errorf("error row = %q, want the llm_stream_silent one", errRow)
	}
	if strings.Contains(errRow, "[llm_connection_failed]") || strings.Contains(errRow, "deadline") {
		t.Errorf("a quiet stream was reported as a lost connection or a deadline: %q", errRow)
	}
}

// TestQuietProviderWaitIsAnnouncedOnceAndWithdrawn holds the notice itself: a wait a person is
// watching says what it waits on, repeats at the same cadence a slow lock uses, and is taken back.
func TestQuietProviderWaitIsAnnouncedOnceAndWithdrawn(t *testing.T) {
	t.Parallel()
	e := newTestExecutor(t)
	now := time.Now().UTC()
	proj := &types.Project{ID: "proj-quiet", Name: "Quiet", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now}
	if err := e.store.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &types.Task{ID: "task-quiet", ProjectID: proj.ID, Title: "Quiet", Prompt: "Quiet",
		Status: types.StatusInProgress, ExecutionState: types.ExecStateRunning, CreatedAt: now, UpdatedAt: now}
	if err := e.store.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	frames := taskStreamFramesDuring(t, e.appStreamMgr, proj.ID, task.ID, func() {
		w := e.newStreamSilenceWatcher(proj.ID, task.ID)
		w.onSilence(16*time.Second, llama.StreamPhaseFirstToken)
		w.onSilence(17*time.Second, llama.StreamPhaseFirstToken) // inside the repeat interval
		w.clear()
		w.clear() // an exit that announced nothing withdraws nothing
	})

	var waiting, withdrawn []string
	for _, f := range frames {
		if f.Event != string(types.WSTypeLockWait) || f.Data["domain"] != streamSilenceDomain {
			continue
		}
		phase, _ := f.Data["phase"].(string)
		if acquired, _ := f.Data["acquired"].(bool); acquired {
			withdrawn = append(withdrawn, phase)
		} else {
			waiting = append(waiting, phase)
		}
	}
	if len(waiting) != 1 || waiting[0] != llama.StreamPhaseFirstToken {
		t.Errorf("wait announcements = %v, want one naming %q: the second fell inside %v",
			waiting, llama.StreamPhaseFirstToken, stream.LockWaitNoticeEvery)
	}
	if len(withdrawn) != 1 {
		t.Errorf("%d withdrawals, want 1: a stale notice hides the queue position behind it", len(withdrawn))
	}
}

// waitForAssistantRow polls until an assistant row holds want, or the deadline passes.
func waitForAssistantRow(t *testing.T, te *TaskExecutor, taskID, want string, wait time.Duration) []types.TaskMessage {
	t.Helper()
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		msgs := mustMessages(t, te, taskID, "thread-"+taskID)
		for _, m := range msgs {
			if m.Role == "assistant" && strings.Contains(m.Content, want) {
				return msgs
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no assistant row holding %q within %v; rows: %v", want, wait,
		rolesInOrder(t, te, taskID, "thread-"+taskID))
	return nil
}

// TestQuietStreamBackoffWaitsAtLeastTheSilenceItSaw: a provider that said nothing for 40 seconds
// does not want the request again in 2. A transport fault keeps the short ladder, because there the
// connection is gone and the next attempt is a different connection.
func TestQuietStreamBackoffWaitsAtLeastTheSilenceItSaw(t *testing.T) {
	t.Parallel()
	quiet := &llama.ErrStreamSilent{Silent: 40 * time.Second, Phase: llama.StreamPhaseMidResponse}
	if d := backoffFor(0, quiet); d < 40*time.Second {
		t.Errorf("backoff after 40s of silence = %v, want at least the silence itself", d)
	}
	if d := backoffFor(2, fmt.Errorf("wrapped: %w", quiet)); d < 40*time.Second {
		t.Errorf("backoff must survive wrapping, got %v", d)
	}
	if d := backoffFor(0, errors.New("read: connection reset by peer")); d > 3*time.Second {
		t.Errorf("backoff for a broken connection = %v, want the short ladder", d)
	}
}
