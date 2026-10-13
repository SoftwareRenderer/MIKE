package sandbox

import (
	"fmt"
	"strings"
	"testing"

	"mike/backend/testutil/sandboxcheck"
	"mike/backend/types"
)

// TestSandboxContainerSeesNoHostEnv is the end-to-end form of the rule the rest of this package
// enforces by construction: a container's environment is its image plus what the project declared, never
// the server's.
//
// A real container, not an argument assertion, because the failure is a podman feature rather than a Go
// bug: `--env-host` and a bare `-e NAME` both read fine in a diff and both put MIKE_ENCRYPTION_KEY
// inside the guest running untrusted repo content.
//
// Gated like every other integration test here. A skip prints why; it is never a pass.
func TestSandboxContainerSeesNoHostEnv(t *testing.T) {

	sm := NewSandboxManager(bootWithBridge(t))
	sandboxcheck.Require(t, BaseImage)

	// Secrets the running server genuinely holds. t.Setenv restores them on cleanup, so the
	// surrounding suite is unaffected.
	t.Setenv("MIKE_ENCRYPTION_KEY", "host-key-that-decrypts-provider-keys")
	t.Setenv("DANGER_ZONE", "1")
	// An innocuous name, to prove the rule is not "MIKE_* is filtered at the container" but "nothing
	// is inherited at all" — a filter by prefix would pass this and fail the real case.
	t.Setenv("MIKE_TEST_UNDECLARED", "should-not-cross")
	// ...and one that IS allowed into the podman client's environment, by the CONTAINERS_ prefix in
	// sandboxruntime.PodmanEnv. This is what keeps the test sharp.
	//
	// A leak needs two failures at once: the client holds the variable AND something emits a bare `-e NAME`
	// to copy it. PodmanEnv removes the first, so a test that only asserts about MIKE_* variables passes even
	// with the second defense gone - nothing is left to copy, and "guarded" is indistinguishable from "moot".
	// A variable PodmanEnv deliberately passes through keeps the bare-`-e` mechanism live inside the test.
	//
	// Verified to bite: `-e CONTAINERS_UNRELATED_PROBE` in containerEnvArgs fails this test;
	// `-e MIKE_TEST_UNDECLARED` does not.
	t.Setenv("CONTAINERS_UNRELATED_PROBE", "in-the-client-on-purpose")

	dir := taskWorktreeDir(t)
	taskID := "test-hostenv-task"

	const declaredName = "MIKE_ENCRYPTION_KEY"
	const declaredValue = "declared-by-the-project"

	// A declared variable deliberately named like the host's secret: the guest must get the declared
	// value, which is only possible if nothing was passed through and merged over it.
	opts := SandboxOpts{SandboxEnvs: []types.SandboxEnv{
		{Key: declaredName, Value: declaredValue},
	}}

	if _, err := sm.Create(taskID, dir, &types.Project{Name: "hostenv-proj"}, opts); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	t.Cleanup(func() { _ = sm.Stop(taskID) })

	// One exec, one line per variable, so the guest is asked once and the parsing is shared.
	// `${VAR-unset}` distinguishes "empty" from "not present"; both are acceptable for a host
	// variable, and only the declared one may have a value at all.
	query := strings.Join([]string{
		guestEcho(declaredName),
		guestEcho("MIKE_TEST_UNDECLARED"),
		guestEcho("CONTAINERS_UNRELATED_PROBE"),
	}, "; ")

	out, err := sm.Exec(taskID, query)
	if err != nil {
		t.Fatalf("exec in sandbox: %v\noutput: %s", err, out)
	}

	lines := strings.SplitN(strings.TrimSpace(out), "\n", 3)
	if len(lines) != 3 {
		t.Fatalf("expected 4 lines from the guest, got %d from %q", len(lines), out)
	}

	if lines[0] != declaredValue {
		t.Errorf("declared %s = %q in the guest, want %q — the declaration must be what arrives",
			declaredName, lines[0], declaredValue)
	}
	for i, name := range []string{"MIKE_TEST_UNDECLARED", "CONTAINERS_UNRELATED_PROBE"} {
		if lines[i+1] != "unset" {
			t.Errorf("%s reached the sandbox container as %q — a container must receive only what "+
				"was configured for it. Check for --env-host, or a bare `-e NAME` (which tells podman "+
				"to copy NAME out of the server's own environment)", name, lines[i+1])
		}
	}
}

// guestEcho renders one guest-side probe as shell source: `printf '%s\n' "${NAME-unset}"`.
//
// `${VAR-unset}` rather than `${VAR:-}` distinguishes "empty" from "not present" (both are
// acceptable for a host variable, and only the declared one may carry a value), and printf rather
// than echo keeps the output exactly one line per probe whatever the value contains, so a stray
// newline cannot shift the assertions onto the wrong variable.
func guestEcho(name string) string {
	return fmt.Sprintf(`printf '%%s\n' "${%s-unset}"`, name)
}

// Podman reads no flag after the container name, so an exec-time `-e` would name the program to run.
// Declared variables reach a container at creation (containerEnvArgs).
func TestExecCommandArgsPutsEveryFlagLeftOfTheContainerName(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stdin bool
		want  []string
	}{
		{"no stdin", false, []string{"exec", "-w", "/home/sandbox/proj", "kanban-sandbox-t1", "sh", "-c", "echo hi"}},
		{"stdin", true, []string{"exec", "-i", "-w", "/home/sandbox/proj", "kanban-sandbox-t1", "sh", "-c", "echo hi"}},
	} {
		got := execCommandArgs(tc.stdin, "/home/sandbox/proj", "kanban-sandbox-t1", "echo hi")
		if strings.Join(got, "\x00") != strings.Join(tc.want, "\x00") {
			t.Errorf("%s: execCommandArgs() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestContainerEnvArgsHasNoEnvHostFlag keeps the other podman pass-through out by name: --env-host
// copies the entire client environment into the container, and it has exactly one legitimate use
// here, which is none.
func TestContainerEnvArgsHasNoEnvHostFlag(t *testing.T) {
	for _, flag := range []string{"--env-host", "--env-host=true"} {
		got := containerEnvArgsOK(t, "task-1", SandboxOpts{
			SandboxEnvs: []types.SandboxEnv{{Key: "A", Value: "1"}},
		})
		for _, a := range got {
			if a == flag {
				t.Errorf("containerEnvArgs() emits %s, which copies the whole host environment into the container", flag)
			}
		}
	}
}

// findAgentBinary lives in sandbox_test.go.
