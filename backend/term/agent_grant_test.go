package term

// A task's terminal sees the host folders the user approved for the agent that task runs. A project-level
// terminal gets none: installing an agent extension is not a decision to mount something into every project.

import (
	"testing"
	"time"

	"mike/backend/agent"
	ext "mike/backend/extensions"
	"mike/backend/types"
)

type agentGrantStore struct{}

func (agentGrantStore) GetByType(_ string) ([]ext.Extension, error) {
	return []ext.Extension{{
		Name: "pi", Type: ext.ExtensionTypeAgent, Enabled: true,
		Config: map[string]any{"sandbox_mount": []any{
			map[string]any{"host_path": "/home/dev/.pi/agent", "container_path": "/root/.pi/agent", "mode": "rw"},
		}},
		Manifest: ext.ExtensionManifest{Version: "1.0.0", Capabilities: &ext.Capabilities{
			Agent: &ext.AgentConfig{Sandbox: &types.AgentSandbox{
				Mounts: []types.SandboxMount{{HostPath: "~/.pi/agent", ContainerPath: "/root/.pi/agent", Mode: "rw"}},
			}},
		}},
	}}, nil
}

func TestAgentGrantForShell(t *testing.T) {
	s, cs := newTestConfigStore(t)
	const projID = "project-agent-shell"
	if err := s.CreateProject(&types.Project{ID: projID, Name: "Agent Shell", Path: t.TempDir(), SandboxEnabled: true}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := s.CreateTask(&types.Task{
		ID: "agent-task", ProjectID: projID, Status: types.StatusBacklog,
		ExecutionState: types.ExecStateIdle, LlamaServerURL: "agent://pi",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateTask(&types.Task{
		ID: "llm-task", ProjectID: projID, Status: types.StatusBacklog,
		ExecutionState: types.ExecStateIdle, LlamaServerURL: "http://127.0.0.1:8080/v1",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	tm := NewTerminalManager(s, cs, nil, agent.NewAgentRegistry(agentGrantStore{}, nil), dangerZoneForTest)

	// The project's own terminal is keyed "proj:<id>", so there is no task to look up.
	if got := tm.agentGrantForShell(projID, "proj:"+projID); got != nil {
		t.Errorf("project terminal grant = %+v, want nil", got)
	}
	// A task running on a plain (non-agent) provider gets none either.
	if got := tm.agentGrantForShell(projID, "llm-task"); got != nil {
		t.Errorf("plain-provider task grant = %+v, want nil", got)
	}
	// The task that does run the agent gets exactly the folder approved for it.
	got := tm.agentGrantForShell(projID, "agent-task")
	if got == nil || len(got.Mounts) != 1 || got.Mounts[0].HostPath != "/home/dev/.pi/agent" {
		t.Errorf("agent task grant = %+v, want the one granted directory", got)
	}
	// A task that does not exist gets none.
	if got := tm.agentGrantForShell(projID, "no-such-task"); got != nil {
		t.Errorf("unknown task grant = %+v, want nil", got)
	}
}
