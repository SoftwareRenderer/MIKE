package server

import (
	"path/filepath"
	"strings"
	"testing"

	"mike/pkg/logging"
)

// The Done count either names the filesystem holding the worktrees root or says why it has no reading —
// and the saying happens once, because a probe polled every few minutes must not fill the log with one
// refusal.
func TestTheStorageProbeReadsTheRootOrSaysWhyItCannot(t *testing.T) {
	t.Run("a root it can read", func(t *testing.T) {
		root := t.TempDir()

		usage, ok := workspaceStorageProbe(func() string { return root })()
		if !ok {
			t.Fatalf("probe of %q reported no reading", root)
		}
		if usage.TotalBytes == 0 {
			t.Fatalf("probe of %q answered %+v, want a filesystem with a size", root, usage)
		}
	})

	t.Run("a root that is not there", func(t *testing.T) {
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
		if n := strings.Count(logged, why); n != 1 {
			t.Fatalf("the refusal was logged %d times, want once: %q", n, logged)
		}
	})
}
