package extensions

import (
	"os"
	"path/filepath"
	"testing"
)

// An interrupted install leaves its checkout and its build context in the temp directory, because the
// deferred call that removes them belongs to a process that no longer exists. The sweep is the only thing
// that reclaims them, so it owns the whole name and nothing else.
func TestSweepInterruptedTempRemovesTheInstallFamiliesAndNothingElse(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	if os.TempDir() != tmp {
		t.Skipf("this platform's temp directory is not TMPDIR (%s), so the sweep cannot be aimed here", os.TempDir())
	}

	orphan := func(name string) string {
		t.Helper()
		dir := filepath.Join(tmp, name)
		if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "sub", "file"), []byte("bytes"), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	build := orphan(extensionBuildContextDirPrefix + "12345")
	checkout := orphan(extensionCheckoutDirPrefix + "67890")
	// The pattern is the prefix of a name MIKE made, so a directory that merely mentions it stays: a
	// developer's own checkout of an extension is not MIKE's to delete.
	bystander := orphan("kanban-extension-checkout-notes")
	lookalike := orphan("not-mike-build-1")

	SweepInterruptedTemp()

	for _, dir := range []string{build, checkout} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("%s survived the sweep", dir)
		}
	}
	for _, dir := range []string{bystander, lookalike} {
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("%s was removed by a sweep that does not own it: %v", dir, err)
		}
	}
}

// The families are the whole point: a prefix that drifted from the one the install uses would sweep nothing
// and leave every orphan behind in silence.
func TestSweepInterruptedTempKnowsTheNamesInstallsUse(t *testing.T) {
	if extensionBuildContextDirPrefix == "" || extensionCheckoutDirPrefix == "" {
		t.Fatal("an empty prefix would match every directory in the temp root")
	}
	if extensionBuildContextDirPrefix == extensionCheckoutDirPrefix {
		t.Fatal("the two install families share a name")
	}
}
