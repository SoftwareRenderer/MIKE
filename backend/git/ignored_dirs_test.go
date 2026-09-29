package git

// IgnoredDirs hands out what the watcher treats as the ignorable set. The invariant is not the
// contents, it is that a caller cannot change them for everybody: the map used to be the package's own,
// so a single `delete(git.IgnoredDirs(), "node_modules")` in one feature would silently make every
// other caller — including the worktree watcher, on a repository with a big dependency tree — start
// walking and diffing what the whole product agrees to skip.

import "testing"

func TestIgnoredDirsReturnsACopy(t *testing.T) {
	t.Parallel()
	first := IgnoredDirs()
	if len(first) == 0 {
		t.Fatal("the ignore set is empty; this test needs it populated")
	}

	// Mutate the returned map in both directions.
	delete(first, "node_modules")
	first["a-test-only-addition"] = struct{}{}

	second := IgnoredDirs()
	if _, ok := second["node_modules"]; !ok {
		t.Error("deleting from the returned map removed node_modules from the package's set")
	}
	if _, ok := second["a-test-only-addition"]; ok {
		t.Error("adding to the returned map added an entry to the package's set for every caller")
	}

	// And the two calls must not hand back the same map.
	if len(IgnoredDirs()) == 0 {
		t.Fatal("second call returned an empty map")
	}
}

func TestShouldIgnorePathStillSeesTheCanonicalSet(t *testing.T) {
	t.Parallel()
	// The copy must not have drifted from what the predicate uses.
	for name := range IgnoredDirs() {
		if !ShouldIgnorePath("a/" + name + "/b.js") {
			t.Errorf("IgnoredDirs lists %q but ShouldIgnorePath does not skip it", name)
		}
	}
	// A near-miss must NOT be skipped: the match is a whole path component.
	if ShouldIgnorePath("a/my_node_modules/b.js") {
		t.Error("a partial component match was skipped")
	}
}
