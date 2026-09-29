package updates

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// releaseDir is an unpacked release: the executable's directory, with the prompt templates beside it.
func releaseDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "prompts"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prompts", "system.tmpl"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return dir
}

// Only the packaged shape may replace its own files, and it is decided by two independent facts: the
// layout beside the binary, and a version that names a release.
func TestClassifyInstall(t *testing.T) {
	dir := releaseDir(t)
	// The machine running this test may itself be a container, so the shape is decided from an empty
	// marker list rather than from the host's real one.
	notInContainer := []string{}

	t.Run("an unpacked release can stage", func(t *testing.T) {
		got := classifyInstall(dir, "1.4.2", notInContainer)
		if got.Kind != KindPackaged || !got.CanStage {
			t.Errorf("classify = %+v, want packaged and stageable", got)
		}
		if got.Reason != "" {
			t.Errorf("a shape that can stage should carry no refusal, got %q", got.Reason)
		}
	})

	t.Run("a development tree is refused by its version, not its layout", func(t *testing.T) {
		got := classifyInstall(dir, "dev", notInContainer)
		if got.Kind != KindSource || got.CanStage {
			t.Errorf("classify = %+v, want source and unstageable", got)
		}
		if !strings.Contains(got.Reason, "dev") {
			t.Errorf("refusal %q does not name the version it saw", got.Reason)
		}
	})

	t.Run("a layout this build cannot name is refused", func(t *testing.T) {
		got := classifyInstall(t.TempDir(), "1.4.2", notInContainer)
		if got.Kind != KindUnknown || got.CanStage {
			t.Errorf("classify = %+v, want unknown and unstageable", got)
		}
	})

	t.Run("an empty executable path is refused rather than guessed", func(t *testing.T) {
		got := classifyInstall("", "1.4.2", notInContainer)
		if got.Kind != KindUnknown || got.CanStage {
			t.Errorf("classify = %+v, want unknown and unstageable", got)
		}
	})
}

// A container is refused even when it is shaped like a release: the swap survives `docker restart` and is
// gone after `compose up`.
func TestClassifyInstallRefusesAContainer(t *testing.T) {
	dir := releaseDir(t)
	marker := filepath.Join(t.TempDir(), ".dockerenv")
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	got := classifyInstall(dir, "1.4.2", []string{marker})
	if got.Kind != KindContainer || got.CanStage {
		t.Fatalf("classify = %+v, want container and unstageable", got)
	}
	for _, want := range []string{"container", "image"} {
		if !strings.Contains(got.Reason, want) {
			t.Errorf("refusal %q does not say %q", got.Reason, want)
		}
	}
}

func TestIsReleaseLayout(t *testing.T) {
	dir := t.TempDir()
	if isReleaseLayout(dir) {
		t.Fatalf("an empty directory is not a release layout")
	}
	if err := os.MkdirAll(filepath.Join(dir, "prompts"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if isReleaseLayout(dir) {
		t.Fatalf("an empty prompts directory is not a release layout")
	}
	if err := os.WriteFile(filepath.Join(dir, "prompts", "a.tmpl"), nil, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !isReleaseLayout(dir) {
		t.Fatalf("prompts/a.tmpl is the release layout")
	}
}
