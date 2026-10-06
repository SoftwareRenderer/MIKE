package exec

import (
	"context"
	"strings"
	"testing"

	"mike/backend/agent"
	extensions "mike/backend/extensions"
	"mike/backend/stream"
	"mike/backend/types"
)

// stubExtensionStore answers the one question an agent registry asks of an extension store.
type stubExtensionStore struct{ rows []extensions.Extension }

func (s *stubExtensionStore) GetByType(string) ([]extensions.Extension, error) { return s.rows, nil }

// TestAgentRunPanicStopsTheRowItRan: an agent run recovers its own panic inside executeTaskWithAgent, so
// executeTask's cleanup recovers nothing and no transition happens there. The row leaves flight only if
// that recover says so; otherwise it keeps execution_state=running with no executor behind it until someone
// presses Stop or the next boot's startup sweep.
func TestAgentRunPanicStopsTheRowItRan(t *testing.T) {
	t.Parallel()
	extName, _ := registerAgentAdapter(t, &stubAdapter{binary: otherAgentBinary, panicInConfig: true})

	te, s := newTestExecutorWithStore(t)
	te.agentRegistry = agent.NewAgentRegistry(&stubExtensionStore{rows: []extensions.Extension{
		{Name: extName, Type: extensions.ExtensionTypeAgent, Enabled: true},
	}}, nil)

	proj := createProject(t, s, "proj-agent-crash", "Agent crash")
	task := createTaskRow(t, s, "agent-crash-task", proj.ID, "in_progress", "running")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	te.executeTaskWithAgent(&taskExecSetup{
		task:         task,
		project:      proj,
		projectID:    proj.ID,
		worktreePath: t.TempDir(),
		serverURL:    types.AgentURLPrefix + extName,
		ctx:          ctx,
		cancel:       cancel,
		userMsgChan:  make(chan stream.UserMessage, 1),
		summarizeCh:  make(chan *summarizeRequest, 1),
		done:         make(chan struct{}),
	})

	row := waitTaskState(t, s, "agent-crash-task", isPaused)
	if row.Output != crashedOutput {
		t.Errorf("output = %q, want %q: a server fault must not be recorded as a person's pause", row.Output, crashedOutput)
	}

	msgs, err := s.GetTaskMessages("agent-crash-task", row.CurrentThreadID)
	if err != nil {
		t.Fatalf("read messages: %v", err)
	}
	var sawPanic bool
	for _, m := range msgs {
		if m.Role == "error" && strings.HasPrefix(m.Content, "[panic] ") {
			sawPanic = true
		}
	}
	if !sawPanic {
		t.Errorf("no [panic] error row stored; the fault never reached the transcript")
	}
}
