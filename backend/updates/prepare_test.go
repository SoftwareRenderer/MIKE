package updates

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// reopen builds a second service over the same directory and the same stored answer: the process that
// downloaded a release is not the one that will install it.
func reopen(t *testing.T, f *stageFixture) *Service {
	t.Helper()
	svc, err := New(Options{
		Version:          "1.4.0",
		Store:            f.service.store,
		Fetcher:          f.fetcher,
		InstallDir:       f.dir,
		Executable:       f.exe,
		ContainerMarkers: []string{},
		Now:              fixedClock(),
		Bridge:           func() (string, error) { return f.bridge, nil },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return svc
}

// A 20 MB download is not thrown away because the server restarted: the answer names the release and the
// archive is still its bytes, so the next process can install it.
func TestAPreparedReleaseSurvivesTheProcessThatDownloadedIt(t *testing.T) {
	t.Parallel()
	f := stageFixtureFor(t, "1.4.2", goodArchive(t))
	f.check(t)
	if _, err := f.prepare(t); err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	svc := reopen(t, f)
	state := svc.Status(Settings{}).State
	if got := state.PreparedVersion; got != "1.4.2" {
		t.Fatalf("PreparedVersion after a restart = %q", got)
	}
	// The About row names the size of the click, and the next process is the one showing it.
	if state.PreparedFiles == 0 {
		t.Errorf("the stored row does not say how many files installing replaces: %+v", state)
	}
	if _, err := os.Stat(filepath.Join(f.dir, preparedDirName, preparedArchive)); err != nil {
		t.Fatalf("the archive did not survive: %v", err)
	}
	// And it is installable by the process that never fetched it.
	if _, err := svc.Apply(context.Background()); err != nil {
		t.Fatalf("Apply after a restart: %v", err)
	}
	if got := read(t, f.exe); got != "NEW-BINARY" {
		t.Errorf("the binary holds %q after an install across a restart", got)
	}
}

// An archive in that directory that the answer does not name is a stranger's file.
func TestNewDropsADownloadedReleaseTheAnswerDoesNotName(t *testing.T) {
	t.Parallel()
	f := stageFixtureFor(t, "1.4.2", goodArchive(t))
	f.check(t)
	if _, err := f.prepare(t); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	// The row is rewritten as a restored or edited one would be: the archive stays, the answer forgets.
	store := f.service.store.(*fakeStore)
	var stored State
	if err := json.Unmarshal([]byte(store.values[stateKey]), &stored); err != nil {
		t.Fatalf("stored row: %v", err)
	}
	stored.PreparedVersion = ""
	raw, err := json.Marshal(stored)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	store.values[stateKey] = string(raw)

	svc := reopen(t, f)
	if got := svc.Status(Settings{}).State.PreparedVersion; got != "" {
		t.Errorf("PreparedVersion = %q for a release the answer does not name", got)
	}
	if _, err := os.Stat(filepath.Join(f.dir, preparedDirName)); !os.IsNotExist(err) {
		t.Errorf("an unclaimed archive was kept in %s", preparedDirName)
	}
}

// A download interrupted by a shutdown is a partial file; nothing may mistake it for a release.
func TestNewReapsAHalfWrittenDownload(t *testing.T) {
	t.Parallel()
	dir := releaseDir(t)
	part := filepath.Join(dir, preparedDirName, preparedArchive+partSuffix)
	write(t, part, "HALF AN ARCHIVE", 0o600)

	if _, err := New(Options{Version: "1.4.0", Store: &fakeStore{}, Fetcher: &fakeFetcher{}, InstallDir: dir, ContainerMarkers: []string{}}); err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := os.Stat(part); !os.IsNotExist(err) {
		t.Errorf("the half-written archive is still on disk")
	}
}

// The other half of the boot settle: a release that was downloaded and has since become the running one
// is not still waiting to be installed.
func TestADownloadedReleaseThatIsNowRunningIsNotOffered(t *testing.T) {
	t.Parallel()
	f := stageFixtureFor(t, "1.4.2", goodArchive(t))
	f.check(t)
	if _, err := f.prepare(t); err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	// Same directory, same row, but this binary is already the downloaded release.
	svc, err := New(Options{
		Version: "1.4.2", Store: f.service.store, Fetcher: f.fetcher, InstallDir: f.dir,
		Executable: f.exe, ContainerMarkers: []string{}, Now: fixedClock(),
		Bridge: func() (string, error) { return f.bridge, nil },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := svc.Status(Settings{}).State.PreparedVersion; got != "" {
		t.Errorf("PreparedVersion = %q, want the release that is now running to be forgotten", got)
	}
	if _, err := os.Stat(filepath.Join(f.dir, preparedDirName)); !os.IsNotExist(err) {
		t.Errorf("the archive of a release already running is still on disk")
	}
}
