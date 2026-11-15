package sandboxcheck

import (
	"os"
	"path/filepath"
	"testing"
)

// The verdict belongs to the host, not to the test that asked first.
func TestProbeAnswersOncePerProcess(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no runsc, no podman
	const image = "docker.io/library/sandboxcheck-probe:1"
	if probe(t, image) == "" {
		t.Fatal("probe answered usable with neither runsc nor podman in PATH")
	}

	// A PATH that gained a healthy runtime mid-run still answers what the first probe saw.
	bin := t.TempDir()
	for _, name := range []string{"runsc", "podman"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatalf("write stub %s: %v", name, err)
		}
	}
	t.Setenv("PATH", bin)
	if probe(t, image) == "" {
		t.Error("the second call probed again; the verdict belongs to the process, not the test")
	}
}

// stubSetting stands in for the server's sandbox manager.
type stubSetting struct{ available bool }

func (s stubSetting) IsAvailable() bool { return s.available }

// The setting gate skips on the answer the product itself gets, and on nothing else: a host that
// honours a sandboxed project must still run the test, and one that does not must not reach it.
func TestRequireAvailableSkipsOnlyOnTheSetting(t *testing.T) {
	ran := false
	t.Run("the host honours the sandbox", func(t *testing.T) {
		RequireAvailable(t, stubSetting{available: true})
		ran = true
	})
	if !ran {
		t.Error("a host with the runtime installed skipped a test that needs only the setting")
	}

	t.Run("the host has no runtime", func(t *testing.T) {
		RequireAvailable(t, stubSetting{available: false})
		t.Error("a host without the runtime ran a test whose premise the server refuses")
	})
}
