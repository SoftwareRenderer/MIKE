package sandboxruntime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The stub podman answers without a container runtime. HOME is how it records its calls, because the
// podman environment allowlist carries HOME and nothing else a test could set.

// stubPodman points the package's podman at that script and returns the calls file.
func stubPodman(t *testing.T, createOut string, createExit string) string {
	t.Helper()
	bin := t.TempDir()
	t.Setenv("HOME", bin)
	t.Setenv("PATH", bin)
	// GVisorAvailable has to pass before the create is attempted.
	writeStub(t, filepath.Join(bin, "runsc"), "#!/bin/sh\necho 'runsc version stub'\n")
	writeStub(t, filepath.Join(bin, "podman"), `#!/bin/sh
echo "$*" >> "$HOME/calls"
case " $* " in
  *" create "*) printf '%s\n' `+createOut+`; exit `+createExit+` ;;
esac
exit 0
`)
	old := podmanBinary
	podmanBinary = filepath.Join(bin, "podman")
	t.Cleanup(func() { podmanBinary = old })
	return filepath.Join(bin, "calls")
}

func stubCalls(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the stub's calls: %v", err)
	}
	calls := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if calls[0] == "" {
		return nil
	}
	return calls
}

func TestProbeReasonNamesPodmansOwnError(t *testing.T) {
	out := []byte("time=2026-10-12T20:00:00Z level=error msg=\"[graphdriver] prior storage driver overlay failed\"\n" +
		"Error: 'overlay' is not supported over overlayfs, a mount_program is required\n")
	got := probeReason(out)
	if want := "Error: 'overlay' is not supported over overlayfs"; !strings.HasPrefix(got, want) {
		t.Errorf("probeReason = %q, want the bare podman error starting %q", got, want)
	}
}

func TestProbeReasonFallsBackToTheStructuredLine(t *testing.T) {
	got := probeReason([]byte("time=2026-10-12T20:00:00Z level=error msg=\"cgroup mount failed\"\n"))
	if !strings.Contains(got, "cgroup mount failed") {
		t.Errorf("probeReason = %q, want the level=error line", got)
	}
}

func TestProbeReasonClipsAndNamesNothingEmpty(t *testing.T) {
	if got := probeReason(nil); got != "no output" {
		t.Errorf("probeReason(nil) = %q, want %q", got, "no output")
	}
	long := probeReason([]byte(strings.Repeat("x", probeReasonMax*3)))
	if want := probeReasonMax + len("…"); len(long) != want {
		t.Errorf("probeReason clipped to %d bytes, want %d", len(long), want)
	}
}

// runsc and podman both answer and the container still cannot be created: this is the answer that must
// skip the container tests rather than let them fail one by one.
func TestProbeSandboxRefusesAStorageThatWillNotInitialise(t *testing.T) {
	calls := stubPodman(t,
		`'time=2026-10-12T20:00:00Z level=error msg="[graphdriver] prior storage driver overlay failed"' `+
			`"Error: 'overlay' is not supported over overlayfs, a mount_program is required: backing file system is unsupported for this graph driver"`,
		"125")

	err := NewRuntimeWithBoot(Boot{}).ProbeSandbox(context.Background(), "docker.io/library/ubuntu:latest")
	if err == nil {
		t.Fatal("ProbeSandbox = nil on a host whose storage cannot initialise")
	}
	if !strings.Contains(err.Error(), "overlay is not supported") && !strings.Contains(err.Error(), "'overlay' is not supported") {
		t.Errorf("ProbeSandbox error = %q, want podman's storage refusal quoted in it", err)
	}
	for _, call := range stubCalls(t, calls) {
		if strings.Contains(" "+call+" ", " rm ") {
			t.Errorf("the probe removed a container it never created: %q", call)
		}
	}
}

// The store looked the image up and found it absent, which is a missing image, not an unusable sandbox.
func TestProbeSandboxUsableWhenOnlyTheImageIsMissing(t *testing.T) {
	stubPodman(t, `"Error: docker.io/library/ubuntu:latest: image not known"`, "125")

	if err := NewRuntimeWithBoot(Boot{}).ProbeSandbox(context.Background(), "docker.io/library/ubuntu:latest"); err != nil {
		t.Errorf("ProbeSandbox = %v with a missing image, want nil (the sandbox itself worked)", err)
	}
}

// The same create preface the product uses, nothing mounted, and removed whatever the start answered.
func TestProbeSandboxStartsAndRemovesItsOwnContainer(t *testing.T) {
	calls := stubPodman(t, `""`, "0")

	rt := NewRuntimeWithBoot(Boot{})
	if err := rt.ProbeSandbox(context.Background(), "docker.io/library/ubuntu:latest"); err != nil {
		t.Fatalf("ProbeSandbox = %v with a healthy stub, want nil", err)
	}

	var created, started, removed bool
	for _, call := range stubCalls(t, calls) {
		switch line := " " + call + " "; {
		case strings.Contains(line, " create "):
			created = true
			for _, want := range []string{"--runtime runsc", "--cap-drop ALL", "--network none", "--security-opt label=disable", probeContainerPrefix} {
				if !strings.Contains(call, want) {
					t.Errorf("probe create is missing %q: %s", want, call)
				}
			}
			if strings.Contains(call, " -v ") {
				t.Errorf("the probe mounted something: %s", call)
			}
		case strings.Contains(line, " start -a "):
			started = true
		case strings.Contains(line, " rm -f "+probeContainerPrefix):
			removed = true
		}
	}
	if !created || !started || !removed {
		t.Errorf("probe ran created=%v started=%v removed=%v, want all three\n%s",
			created, started, removed, strings.Join(stubCalls(t, calls), "\n"))
	}
}

// A cancelled caller bounds the child, not just its output pipes (AGENTS.md → Known Gaps).
func TestProbeSandboxStopsWhenItsContextIsGone(t *testing.T) {
	stubPodman(t, `""`, "0")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := NewRuntimeWithBoot(Boot{}).ProbeSandbox(ctx, "docker.io/library/ubuntu:latest")
	if err == nil {
		t.Fatal("ProbeSandbox = nil on a cancelled context, want an error")
	}
	if !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("ProbeSandbox error = %q, want it to name the cancellation", err)
	}
}
