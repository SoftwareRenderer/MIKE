package sandbox

import (
	"strings"
	"testing"

	"kanban/backend/types"
)

// TestCreateRefusesUnsafeSandboxAdditions is the second line of the rule the project
// API enforces: container creation refuses the same values, because a project row can
// predate the API check (or arrive from an older database), and its capabilities and
// mounts are appended to the podman command line as written.
//
// The refusal must come BEFORE the availability probe, so this test holds on a host
// without gVisor: "runsc is missing" would hide the configuration error underneath it,
// which is exactly the kind of false green this repository has been bitten by.
func TestCreateRefusesUnsafeSandboxAdditions(t *testing.T) {
	sm := NewSandboxManager()
	project := &types.Project{ID: "p-args", Name: "args-proj", Path: t.TempDir()}
	worktree := t.TempDir()

	tests := []struct {
		name    string
		want    string
		opts    SandboxOpts
		comment string
	}{
		{
			name: "capability that reaches the host kernel", want: "capability",
			opts:    SandboxOpts{Capabilities: []string{"SYS_ADMIN"}},
			comment: "an extension asking for this is refused; a task sandbox must not accept it",
		},
		{
			name: "the same capability with its CAP_ prefix", want: "capability",
			opts:    SandboxOpts{Capabilities: []string{"cap_sys_ptrace"}},
			comment: "normalization must not be a way around the denylist",
		},
		{
			name: "container path that rewrites the mount spec", want: "mount",
			opts:    SandboxOpts{SandboxMounts: []types.SandboxMount{{HostPath: "/tmp/cache", ContainerPath: "/cache:rw"}}},
			comment: "the spec is host:container:rprivate,mode — a ':' there is a second option field",
		},
		{
			name: "the host filesystem root", want: "mount",
			opts:    SandboxOpts{SandboxMounts: []types.SandboxMount{{HostPath: "/", ContainerPath: "/host", Mode: "rw"}}},
			comment: "a task runs an LLM's commands; mounting / read-write is no boundary at all",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := sm.Create("task-args", worktree, project, tt.opts)
			if err == nil {
				t.Fatalf("Create accepted %s (%s)", tt.name, tt.comment)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want it to name the %s", err, tt.want)
			}
			if strings.Contains(err.Error(), "gVisor not available") {
				t.Errorf("the check ran after the availability probe, so it is unenforceable on a host without runsc: %v", err)
			}
		})
	}
}
