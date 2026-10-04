package sandbox

import (
	"strings"
	"testing"

	"mike/backend/types"
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

	got := envFlags(t, containerEnvArgsOK(t, "task-1", opts))
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

	got := envFlags(t, containerEnvArgsOK(t, "task-1", opts))
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

	got := envFlags(t, containerEnvArgsOK(t, "task-1", opts))
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

	args := containerEnvArgsOK(t, "task-1", opts)
	if len(args) != 4 {
		t.Fatalf("want 2 -e pairs, got %v", args)
	}
	if args[1] != "FLAGS=-v /:/host" || args[3] != "PHRASE=a b c" {
		t.Errorf("value split across arguments: %v", args)
	}
}

func TestContainerEnvArgs_Empty(t *testing.T) {
	if got := containerEnvArgsOK(t, "task-1", SandboxOpts{}); len(got) != 0 {
		t.Errorf("no configuration must add no flags, got %v", got)
	}
}

// containerEnvArgsOK is the test's adapter to containerEnvArgs's (args, error) return: these tests
// are about which flags are produced, so a refusal here is a fixture bug, not the behaviour under
// test. TestContainerEnvArgs_RejectsAMalformedKey is where a refusal is the behaviour.
func containerEnvArgsOK(t *testing.T, taskID string, opts SandboxOpts) []string {
	t.Helper()
	args, err := containerEnvArgs(taskID, opts)
	if err != nil {
		t.Fatalf("containerEnvArgs(%s) refused a well-formed configuration: %v", taskID, err)
	}
	return args
}

// TestEnvArgsNeverEmitABareFlag is the property the whole container-environment rule rests on.
// `-e NAME` with no `=` is not an empty assignment: podman copies NAME out of the environment of the
// podman process, which is the server's. So the argument builder must be incapable of producing one,
// whatever it is given.
func TestEnvArgsNeverEmitABareFlag(t *testing.T) {
	cases := []struct {
		name string
		envs []types.SandboxEnv
	}{
		{"configured", []types.SandboxEnv{{Key: "A", Value: "1"}}},
		{"empty value", []types.SandboxEnv{{Key: "A", Value: ""}}},
		{"value that looks like a flag", []types.SandboxEnv{{Key: "A", Value: "-e MIKE_ENCRYPTION_KEY"}}},
		{"many", []types.SandboxEnv{{Key: "A", Value: "1"}, {Key: "B", Value: "2"}}},
	}
	for _, tc := range cases {
		args, err := types.SandboxEnvArgs(tc.envs)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		for i := 0; i < len(args); i++ {
			if args[i] != "-e" {
				continue
			}
			if i+1 >= len(args) {
				t.Fatalf("%s: -e with no following argument: %v", tc.name, args)
			}
			if !strings.Contains(args[i+1], "=") {
				t.Errorf("%s: %q is a bare -e, which copies a HOST variable into the container: %v",
					tc.name, args[i+1], args)
			}
			if args[i+1] == "" || strings.HasPrefix(args[i+1], "=") {
				t.Errorf("%s: assignment with no name: %q", tc.name, args[i+1])
			}
		}
	}
}

// TestContainerEnvArgs_RejectsAMalformedKey: a configuration row that cannot become a clean
// assignment is refused, and the container does not start. Silently dropping it would produce a
// container missing a variable nobody told anyone about.
func TestContainerEnvArgs_RejectsAMalformedKey(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  types.SandboxEnv
	}{
		{"key containing =", types.SandboxEnv{Key: "A=B", Value: "x"}},
		{"key with a space", types.SandboxEnv{Key: "A B", Value: "x"}},
		{"key with a comma", types.SandboxEnv{Key: "A,B", Value: "x"}},
		{"leading dash", types.SandboxEnv{Key: "-e", Value: "x"}},
		{"newline in value", types.SandboxEnv{Key: "A", Value: "x\nB=c"}},
		{"carriage return in value", types.SandboxEnv{Key: "A", Value: "x\rB=c"}},
	} {
		_, err := containerEnvArgs("task-1", SandboxOpts{SandboxEnvs: []types.SandboxEnv{tc.env}})
		if err == nil {
			t.Errorf("%s: accepted, want refusal", tc.name)
		}
		// The agent-extension list gets the same treatment; a manifest is not trusted more than a form.
		_, err = containerEnvArgs("task-1", SandboxOpts{AgentSandbox: &types.AgentSandbox{Envs: []types.SandboxEnv{tc.env}}})
		if err == nil {
			t.Errorf("%s: accepted in the agent extension envs, want refusal", tc.name)
		}
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
