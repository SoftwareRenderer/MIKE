package sandboxruntime

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeOverlayPodman puts a stand-in podman on PATH: it reports the container running or not, answers
// one fixed MergedDir, and fails the first `rm -f` so the retry path runs.
func fakeOverlayPodman(t *testing.T, mergedDir string, running, rmAlwaysFails bool) {
	t.Helper()
	second := "exit 0"
	if rmAlwaysFails {
		second = "exit 1"
	}
	dir := t.TempDir()
	flag := filepath.Join(dir, "rm-attempted")
	shim := `#!/bin/sh
if [ "$1" = "inspect" ] && [ "$2" = "-f" ]; then
	case "$3" in
	'{{.State.Running}}') echo '` + boolWord(running) + `' ; exit 0 ;;
	'{{.GraphDriver.Data.MergedDir}}') echo '` + mergedDir + `' ; exit 0 ;;
	esac
	exit 1
fi
if [ "$1" = "rm" ]; then
	if [ -e '` + flag + `' ]; then ` + second + `; fi
	touch '` + flag + `'
	exit 1
fi
exit 0
`
	path := filepath.Join(dir, "podman")
	if err := os.WriteFile(path, []byte(shim), 0o755); err != nil {
		t.Fatalf("write fake podman: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func boolWord(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// writeMergedDir creates <root>/<imageID>/merged with one file in it, and returns both paths.
func writeMergedDir(t *testing.T, root, imageID string) (merged, file string) {
	t.Helper()
	merged = filepath.Join(root, imageID, "merged")
	if err := os.MkdirAll(merged, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", merged, err)
	}
	file = filepath.Join(merged, "payload")
	if err := os.WriteFile(file, []byte("in use"), 0o600); err != nil {
		t.Fatalf("write %s: %v", file, err)
	}
	return merged, file
}

// TestRemoveContainerCleansOnlyTheNamedContainersMergedDir: a directory taken from a listing of the
// storage root would belong to a container MIKE does not own.
func TestRemoveContainerCleansOnlyTheNamedContainersMergedDir(t *testing.T) {
	storageRoot := t.TempDir()
	_, target := writeMergedDir(t, storageRoot, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	_, sibling := writeMergedDir(t, storageRoot, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	targetMerged := filepath.Dir(target)

	fakeOverlayPodman(t, targetMerged, false, false)

	if err := NewRuntimeWithBoot(Boot{}).RemoveContainer("kanban-sandbox-deadbeef"); err != nil {
		t.Fatalf("RemoveContainer: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("the named container's merged file survived: %v", err)
	}
	if _, err := os.Stat(sibling); err != nil {
		t.Errorf("a sibling image's merged directory was touched: %v", err)
	}
}

// TestRemoveContainerLeavesARunningContainersMergedDirAlone: an unmounted merged directory is a
// leftover, a mounted one is somebody's filesystem.
func TestRemoveContainerLeavesARunningContainersMergedDirAlone(t *testing.T) {
	storageRoot := t.TempDir()
	merged, file := writeMergedDir(t, storageRoot, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	fakeOverlayPodman(t, merged, true, true)

	if err := NewRuntimeWithBoot(Boot{}).RemoveContainer("kanban-sandbox-alive"); err == nil {
		t.Error("want the force-remove to report failure while the container is running")
	}
	if _, err := os.Stat(file); err != nil {
		t.Errorf("a running container's merged file was removed: %v", err)
	}
}

func TestMergedDirFromInspectRefusesAnythingThatIsNotAMergedDirectory(t *testing.T) {
	for _, out := range []string{
		"",
		"<no value>",          // what a field podman does not have prints
		"/var/tmp/containers", // not a merged directory
		"overlay/merged",      // relative
		"    \n",
		"/merged/../etc/merged", // not clean
	} {
		if got, err := mergedDirFromInspect("kanban-sandbox-x", out); err == nil {
			t.Errorf("mergedDirFromInspect(%q) = %q, want a refusal", out, got)
		}
	}
	const ok = "/var/lib/containers/storage/overlay/abc123/merged"
	got, err := mergedDirFromInspect("kanban-sandbox-x", ok+"\n")
	if err != nil {
		t.Fatalf("mergedDirFromInspect(%q): %v", ok, err)
	}
	if got != ok {
		t.Errorf("mergedDirFromInspect = %q, want %q", got, ok)
	}
}
