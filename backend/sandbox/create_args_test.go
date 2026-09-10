package sandbox

import (
	"fmt"
	"strings"
	"testing"

	"kanban/backend/types"
	"kanban/pkg/sandboxruntime"
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
	sm := NewSandboxManager(sandboxruntime.Boot{})
	project := &types.Project{ID: "p-args", Name: "args-proj", Path: t.TempDir()}
	worktree := t.TempDir()

	tests := []struct {
		name    string
		want    string
		opts    SandboxOpts
		comment string
	}{
		{
			name: "capability shaped like a flag", want: "capability",
			opts:    SandboxOpts{Capabilities: []string{"--privileged"}},
			comment: "a capability is judged by its shape, not its name: an option-shaped value reaches podman as an argument",
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

// sharedPrefaceDiff names the first argument at which args stops matching the shared gVisor
// preface, and "" when it matches. backend/extensions holds the same check for its builder: the two
// command lines share the posture byte for byte and this is what fails the day one of them
// re-inlines it.
func sharedPrefaceDiff(args []string, containerName string) string {
	preface := sandboxruntime.GVisorCreateArgs(containerName)
	for i, want := range preface {
		if i >= len(args) {
			return fmt.Sprintf("args end before the shared preface does (arg %d, %q)", i, want)
		}
		if args[i] != want {
			return fmt.Sprintf("arg %d = %q, the shared preface says %q", i, args[i], want)
		}
	}
	return ""
}

// TestTaskCreateArgsBeginWithTheSharedPreface: the task sandbox's isolation posture is the one in
// pkg/sandboxruntime, not a copy of it — AppendCapabilities refuses a capability grant onto
// anything else, and a drifted copy would turn every task with capabilities into a refusal.
func TestTaskCreateArgsBeginWithTheSharedPreface(t *testing.T) {
	const name = "kanban-sandbox-task-preface"
	args := createContainerArgs(name, "/home/sandbox/proj")
	if diff := sharedPrefaceDiff(args, name); diff != "" {
		t.Errorf("the task builder no longer begins with the shared preface: %s\nargs = %v", diff, args)
	}
	if len(args) <= len(sandboxruntime.GVisorCreateArgs(name)) {
		t.Errorf("a task sandbox adds no argument of its own (workdir): %v", args)
	}
}

// TestTaskSandboxIsolatesWhatACapabilityGrantAssumes pins what the removal of the
// capability denylist rests on: the container MIKE creates runs under gVisor and starts
// from a full --cap-drop ALL, so a re-added capability is a privilege in the guest rather
// than on the host kernel. Create appends through sandboxruntime.AppendCapabilities, the only path
// to a --cap-add, so a change to createContainerArgs that drops either half turns into a refused
// task instead of a wider sandbox.
func TestTaskSandboxIsolatesWhatACapabilityGrantAssumes(t *testing.T) {
	args := createContainerArgs("kanban-sandbox-task-args", "/home/sandbox/args-proj")
	if err := sandboxruntime.GVisorIsolationForCaps(args); err != nil {
		t.Fatalf("a task sandbox no longer isolates what a capability grant assumes: %v\nargs = %v", err, args)
	}

	// The shape rule still refuses, and still refuses before the availability probe, so a
	// host without runsc reports the configuration error instead of hiding it under
	// "gVisor not available".
	sm := NewSandboxManager(sandboxruntime.Boot{})
	project := &types.Project{ID: "p-caps", Name: "caps-proj", Path: t.TempDir()}
	_, err := sm.Create("task-caps", t.TempDir(), project, SandboxOpts{Capabilities: []string{"--privileged"}})
	if err == nil {
		t.Fatal("Create accepted an option-shaped capability")
	}
	if !strings.Contains(err.Error(), "capability") {
		t.Errorf("error = %v, want it to name the capability", err)
	}
	if strings.Contains(err.Error(), "gVisor not available") {
		t.Errorf("the shape check ran after the availability probe: %v", err)
	}
}
