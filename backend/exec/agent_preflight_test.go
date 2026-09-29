package exec

import (
	"context"
	"errors"
	"strings"
	"testing"

	"mike/backend/agent"
	"mike/backend/extensions"
	pkgagent "mike/pkg/agent"
)

// Neither the extension nor its binary is named "pi": an agent whose binary has some other name is
// the only case that tells the two names apart.
const (
	otherAgentName   = "codelint"
	otherAgentBinary = "codelint-cli"
)

// stubAdapter is the adapter an agent extension ships. The field that matters is the binary its
// SubprocessConfig names — the only place in the product that name exists.
type stubAdapter struct {
	binary string
	closed bool
}

var _ pkgagent.AgentAdapter = (*stubAdapter)(nil)

func (s *stubAdapter) Init(context.Context, []byte) error { return nil }
func (s *stubAdapter) SubprocessConfig(context.Context) pkgagent.SubprocessConfig {
	return pkgagent.SubprocessConfig{Binary: s.binary}
}
func (s *stubAdapter) EncodeCommand(context.Context, string, []byte) []byte   { return nil }
func (s *stubAdapter) ParseOutput(context.Context, []byte) []pkgagent.Event   { return nil }
func (s *stubAdapter) HandleUIRequest(context.Context, string, string) []byte { return nil }
func (s *stubAdapter) ListModels(context.Context) []pkgagent.AgentModel       { return nil }
func (s *stubAdapter) Close()                                                 { s.closed = true }

// recordingProber answers the container question and keeps every name it was asked about.
type recordingProber struct {
	available bool
	probed    []string
}

func (p *recordingProber) BinaryAvailable(taskID, binary string) bool {
	p.probed = append(p.probed, binary)
	return p.available
}

// registerStubAgent wires a name this test owns to an adapter naming its own binary, and hands the
// adapter back so a test can see whether it was released. The adapter-factory registry is a package
// global keyed on the extension name, so tests that share a name share an adapter: one test's Close
// is then another test's "released although the task may still start".
func registerStubAgent(t *testing.T) (*stubAdapter, string) {
	t.Helper()
	name := otherAgentName + "-" + strings.TrimPrefix(t.Name(), "Test")
	adapter := &stubAdapter{binary: otherAgentBinary}
	agent.RegisterAdapterFactory(name, func() pkgagent.AgentAdapter { return adapter })
	return adapter, name
}

func TestPrepareAgentProbesTheAdaptersOwnBinary(t *testing.T) {
	t.Parallel()
	_, name := registerStubAgent(t)
	prober := &recordingProber{available: false}
	te := newBareTestExecutor(t)
	te.agentBinaries = prober

	prepared, err := te.prepareAgent(context.Background(), &extensions.Extension{Name: name},
		"task-1", t.TempDir(), "kanban-task-1", true)

	if prepared != nil {
		prepared.Close()
		t.Fatal("prepareAgent returned a prepared agent for a container that does not have its binary")
	}
	if !errors.Is(err, errAgentBinaryNotAvailable) {
		t.Fatalf("err = %v, want errAgentBinaryNotAvailable", err)
	}
	if len(prober.probed) != 1 || prober.probed[0] != otherAgentBinary {
		t.Errorf("container was asked about %v, want exactly [%s] - the binary the adapter named",
			prober.probed, otherAgentBinary)
	}
}

// TestPrepareAgentRefusalNamesTheBinaryItMissed: naming the extension but not the binary leaves the
// operator guessing what the container lacked.
func TestPrepareAgentRefusalNamesTheBinaryItMissed(t *testing.T) {
	t.Parallel()
	_, name := registerStubAgent(t)
	te := newBareTestExecutor(t)
	te.agentBinaries = &recordingProber{available: false}

	_, err := te.prepareAgent(context.Background(), &extensions.Extension{Name: name},
		"task-1", t.TempDir(), "kanban-task-1", true)

	if err == nil {
		t.Fatal("expected a refusal")
	}
	for _, want := range []string{name, otherAgentBinary} {
		if !containsString(err.Error(), want) {
			t.Errorf("err = %q, want it to name %q", err, want)
		}
	}
}

func TestPrepareAgentWithBinaryPresentStartsNothingEarly(t *testing.T) {
	t.Parallel()
	adapter, name := registerStubAgent(t)
	prober := &recordingProber{available: true}
	te := newBareTestExecutor(t)
	te.agentBinaries = prober

	prepared, err := te.prepareAgent(context.Background(), &extensions.Extension{Name: name},
		"task-1", t.TempDir(), "kanban-task-1", true)
	if err != nil {
		t.Fatalf("prepareAgent: %v", err)
	}
	defer prepared.Close()

	if prepared.Binary() != otherAgentBinary {
		t.Errorf("Binary() = %q, want %q", prepared.Binary(), otherAgentBinary)
	}
	if len(prober.probed) != 1 || prober.probed[0] != otherAgentBinary {
		t.Errorf("container was asked about %v, want exactly [%s]", prober.probed, otherAgentBinary)
	}
	if adapter.closed {
		t.Error("adapter was closed although the task may still start")
	}
}

// TestPrepareAgentHostModeAsksTheContainerNothing: the pre-flight is a container question, and an
// unsandboxed project has no container to ask.
func TestPrepareAgentHostModeAsksTheContainerNothing(t *testing.T) {
	t.Parallel()
	_, name := registerStubAgent(t)
	prober := &recordingProber{available: false}
	te := newBareTestExecutor(t)
	te.agentBinaries = prober

	prepared, err := te.prepareAgent(context.Background(), &extensions.Extension{Name: name},
		"task-1", t.TempDir(), "kanban-task-1", false)
	if err != nil {
		t.Fatalf("prepareAgent: %v", err)
	}
	prepared.Close()

	if len(prober.probed) != 0 {
		t.Errorf("host-mode start probed the container for %v, want no probe at all", prober.probed)
	}
}

// TestPrepareAgentWithoutContainerRefuses: a sandboxed task with no sandbox manager is an outage,
// never a silent pass and never a nil dereference.
func TestPrepareAgentWithoutContainerRefuses(t *testing.T) {
	t.Parallel()
	_, name := registerStubAgent(t)
	te := newBareTestExecutor(t)

	prepared, err := te.prepareAgent(context.Background(), &extensions.Extension{Name: name},
		"task-1", t.TempDir(), "kanban-task-1", true)
	if prepared != nil {
		prepared.Close()
		t.Fatal("prepareAgent returned a prepared agent with no sandbox manager to start it in")
	}
	if err == nil {
		t.Fatal("expected a refusal")
	}
}

func TestAgentStartErrorCode(t *testing.T) {
	t.Parallel()
	if got := agentStartErrorCode(errAgentBinaryNotAvailable); got != "agent_not_available" {
		t.Errorf("agentStartErrorCode(missing binary) = %q, want agent_not_available", got)
	}
	wrapped := errors.New("start agent in container: no such file or directory")
	if got := agentStartErrorCode(wrapped); got != "agent_start_failed" {
		t.Errorf("agentStartErrorCode(other failure) = %q, want agent_start_failed", got)
	}
}
