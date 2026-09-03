package sandbox

import (
	"strings"
	"testing"

	"kanban/backend/types"
)

// envFlags returns the value of every -e flag, in order, and checks the flags are
// well-formed pairs: a value containing a space must stay one argv element or
// podman would read part of it as an argument.
func envFlags(t *testing.T, args []string) []string {
	t.Helper()
	var got []string
	for i := 0; i < len(args); i++ {
		if args[i] != "-e" {
			// A value that leaked out of its own argv element would land here and
			// be parsed by podman as an argument.
			if strings.HasPrefix(args[i], "-e") {
				t.Errorf("unexpected -e-shaped argument: %q", args[i])
			}
			continue
		}
		if i+1 >= len(args) {
			t.Fatalf("-e flag with no value argument")
		}
		got = append(got, args[i+1])
		i++
	}
	return got
}

// TestContainerEnvArgs_OnlyConfigured: a sandbox container gets the variables
// configured for it and nothing else. The hardcoded TERM/PATH this replaces
// overrode the image's own ENV.
func TestContainerEnvArgs_OnlyConfigured(t *testing.T) {
	opts := SandboxOpts{
		SandboxEnvs: []types.SandboxEnv{
			{Key: "API_TOKEN", Value: "secret"},
			{Key: "NODE_ENV", Value: "development"},
		},
	}

	got := envFlags(t, containerEnvArgs("task-1", opts, nil))
	want := []string{"API_TOKEN=secret", "NODE_ENV=development"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("containerEnvArgs() = %v, want %v", got, want)
	}
	joined := strings.Join(got, " ")
	for _, unwanted := range []string{"TERM", "PATH", "HOME"} {
		if strings.Contains(joined, unwanted+"=") {
			t.Errorf("containerEnvArgs injected %s=%q — the image owns anything not configured", unwanted, joined)
		}
	}
}

func TestContainerEnvArgs_SourcesAndPrecedence(t *testing.T) {
	opts := SandboxOpts{
		AgentSandbox: &types.AgentSandbox{
			Envs: []types.SandboxEnv{
				{Key: "AGENT_KEY", Value: "from-extension"},
				{Key: "SHARED", Value: "from-extension"},
			},
		},
		// The project's own configuration comes last, so podman's
		// later-flag-wins rule makes it the one that counts.
		SandboxEnvs: []types.SandboxEnv{
			{Key: "SHARED", Value: "from-project"},
		},
	}

	got := envFlags(t, containerEnvArgs("task-1", opts, nil))
	want := []string{"AGENT_KEY=from-extension", "SHARED=from-extension", "SHARED=from-project"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("containerEnvArgs() = %v, want %v (agent envs first, project config last)", got, want)
	}
}

func TestContainerEnvArgs_SkipsBlankKeys(t *testing.T) {
	opts := SandboxOpts{
		AgentSandbox: &types.AgentSandbox{Envs: []types.SandboxEnv{{Key: "", Value: "x"}}},
		SandboxEnvs:  []types.SandboxEnv{{Key: "", Value: "y"}, {Key: "KEEP", Value: ""}},
	}

	got := envFlags(t, containerEnvArgs("task-1", opts, nil))
	// A blank key is not a variable; an empty VALUE is (FOO= clears/sets it).
	if len(got) != 1 || got[0] != "KEEP=" {
		t.Errorf("containerEnvArgs() = %v, want [KEEP=]", got)
	}
}

func TestContainerEnvArgs_ValueStaysOneArgument(t *testing.T) {
	opts := SandboxOpts{SandboxEnvs: []types.SandboxEnv{
		{Key: "FLAGS", Value: "-v /:/host"},
		{Key: "PHRASE", Value: "a b c"},
	}}

	args := containerEnvArgs("task-1", opts, nil)
	if len(args) != 4 {
		t.Fatalf("want 2 -e pairs, got %v", args)
	}
	if args[1] != "FLAGS=-v /:/host" || args[3] != "PHRASE=a b c" {
		t.Errorf("value split across arguments: %v", args)
	}
}

func TestContainerEnvArgs_Empty(t *testing.T) {
	if got := containerEnvArgs("task-1", SandboxOpts{}, nil); len(got) != 0 {
		t.Errorf("no configuration must add no flags, got %v", got)
	}
}

// TestShellExecArgs: the terminal still gets TERM, on its own exec rather than
// baked into the container.
func TestShellExecArgs(t *testing.T) {
	got := shellExecArgs("kanban-sandbox-t1", "/home/sandbox/proj", "bash")
	want := []string{"exec", "-it", "-e", "TERM=xterm-256color", "-w", "/home/sandbox/proj", "kanban-sandbox-t1", "bash"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("shellExecArgs() = %v, want %v", got, want)
	}
}
