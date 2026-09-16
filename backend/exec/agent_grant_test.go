package exec

import (
	"testing"

	"mike/backend/agent"
	ext "mike/backend/extensions"
	"mike/backend/types"
)

const (
	agentURL = "agent://pi"
	llmURL   = "http://127.0.0.1:8080/v1"
)

// grantStore holds one enabled agent extension whose manifest always suggests a folder. `granted` says
// whether the user approved it, which is the only difference between the two cases the tests use.
type grantStore struct{ granted bool }

func (g grantStore) GetByType(_ string) ([]ext.Extension, error) {
	e := ext.Extension{
		Name: "pi", Type: ext.ExtensionTypeAgent, Enabled: true,
		Config: map[string]any{},
		Manifest: ext.ExtensionManifest{Version: "1.0.0", Capabilities: &ext.Capabilities{
			Agent: &ext.AgentConfig{Sandbox: &types.AgentSandbox{
				Mounts: []types.SandboxMount{{
					HostPath: "~/.pi/agent", ContainerPath: "/root/.pi/agent", Mode: "rw",
				}},
			}},
		}},
	}
	if g.granted {
		e.Config = map[string]any{"sandbox_mount": []any{
			map[string]any{"host_path": "/tmp/pi-agent-config", "container_path": "/root/.pi/agent", "mode": "rw"},
		}}
	}
	return []ext.Extension{e}, nil
}

func grantExecutor(t *testing.T, granted bool) *TaskExecutor {
	t.Helper()
	te := newBareTestExecutor(t)
	te.agentRegistry = agent.NewAgentRegistry(grantStore{granted: granted}, nil)
	return te
}

// TestAgentGrantHash_ChangeRebuildsTheContainer: hashing only the folder list gives the same value for a
// task with no agent and for an agent with no approved folder, so a folder the user removed would otherwise
// stay mounted in a running container.
func TestAgentGrantHash_ChangeRebuildsTheContainer(t *testing.T) {
	granted := grantExecutor(t, true)
	plain := grantExecutor(t, false)

	if granted.agentGrantHash(agentURL) == granted.agentGrantHash(llmURL) {
		t.Error("agent → plain provider hashes the same, so the agent's mount would stay on the container")
	}
	if granted.agentGrantHash(agentURL) == plain.agentGrantHash(agentURL) {
		t.Error("revoking the grant hashes the same, so the mount would stay on the container")
	}
	if granted.agentGrantHash(llmURL) != plain.agentGrantHash(llmURL) {
		t.Error("a task on a plain provider changed shape because an agent extension is installed")
	}
	first, again := granted.agentGrantHash(agentURL), granted.agentGrantHash(agentURL)
	if first != again {
		t.Error("the hash is not stable, so every turn would recreate the container")
	}
}

// TestSandboxCreateOpts_GrantsOnlyTheTasksAgent: the create options carry the folders approved for the agent
// this task runs, and none in any other case.
func TestSandboxCreateOpts_GrantsOnlyTheTasksAgent(t *testing.T) {
	te := grantExecutor(t, true)

	grantedRun := &taskExecSetup{serverURL: agentURL}
	if opts := te.sandboxCreateOpts(grantedRun, "ubuntu:latest", false); opts.AgentSandbox == nil ||
		len(opts.AgentSandbox.Mounts) != 1 {
		t.Errorf("task on agent://pi got %+v, want its one granted mount", opts.AgentSandbox)
	}

	llmRun := &taskExecSetup{serverURL: llmURL}
	if opts := te.sandboxCreateOpts(llmRun, "ubuntu:latest", false); opts.AgentSandbox != nil {
		t.Errorf("task on a plain provider got %+v, want no agent mount while an agent extension is enabled",
			opts.AgentSandbox)
	}

	// Switching provider during a run changes the loop's modelConfig, and the approved folders follow it.
	switched := &taskExecSetup{serverURL: agentURL, provider: &modelConfig{serverURL: llmURL}}
	if opts := te.sandboxCreateOpts(switched, "ubuntu:latest", false); opts.AgentSandbox != nil {
		t.Error("after switching to a plain provider the container would still be built with the agent's mount")
	}
}
