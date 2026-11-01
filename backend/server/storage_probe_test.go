package server

import (
	"path/filepath"
	"strings"
	"testing"

	"mike/pkg/logging"
)

// A probe that can read the root reports it.
func TestTheStorageProbeReadsTheFilesystemHoldingTheWorktreesRoot(t *testing.T) {
	root := t.TempDir()

	usage, ok := workspaceStorageProbe(func() string { return root })()
	if !ok {
		t.Fatalf("probe of %q reported no reading", root)
	}
	if usage.TotalBytes == 0 {
		t.Fatalf("probe of %q answered %+v, want a filesystem with a size", root, usage)
	}
}

// An unreadable root answers no reading, and says why once.
func TestTheStorageProbeSaysWhyItHasNoReading(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-created")
	buf, restore := logging.TestSink()
	defer restore()

	const why = "the Done count will report no storage usage"
	probe := workspaceStorageProbe(func() string { return missing })
	for range 3 {
		if _, ok := probe(); ok {
			t.Fatal("a root that is not there answered a reading")
		}
	}
	logged := buf.String()
	if !strings.Contains(logged, why) {
		t.Fatalf("the refusal never reached the log: %q", logged)
	}
	if n := strings.Count(logged, why); n != 1 {
		t.Fatalf("the refusal was logged %d times, want once: %q", n, logged)
	}
}
