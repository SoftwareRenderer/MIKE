package updates

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRestartRefusesWhenNothingIsWaiting(t *testing.T) {
	var called int
	f := stageFixtureFor(t, "1.4.2", goodArchive(t))
	f.service.restart = func() error { called++; return nil }

	err := f.service.Restart()
	if !errors.Is(err, ErrNothingStaged) {
		t.Fatalf("Restart = %v, want ErrNothingStaged", err)
	}
	if called != 0 {
		t.Errorf("a refused restart still ended the process")
	}
}

// The staged release is applied by a restart and by nothing else, and the restart is the same shutdown
// a signal drives — so the only thing this route can do is ask for it.
func TestRestartAsksForTheProcessItWasGiven(t *testing.T) {
	var called int
	f := stageFixtureFor(t, "1.4.2", goodArchive(t))
	f.check(t)
	f.service.restart = func() error { called++; return nil }

	if _, err := f.install(t); err != nil {
		t.Fatalf("install: %v", err)
	}
	if err := f.service.Restart(); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if called != 1 {
		t.Errorf("restart path ran %d times, want once", called)
	}
}

func TestRestartReportsAPathThatDoesNotExist(t *testing.T) {
	f := stageFixtureFor(t, "1.4.2", goodArchive(t))
	f.check(t)
	if _, err := f.install(t); err != nil {
		t.Fatalf("install: %v", err)
	}
	err := f.service.Restart()
	if err == nil || !strings.Contains(err.Error(), "no restart path") {
		t.Fatalf("Restart = %v, want the answer that says how to restart by hand", err)
	}
}

// A restart while an update operation is running would end the process underneath it.
func TestRestartRefusesWhileAnUpdateOperationIsRunning(t *testing.T) {
	f := stageFixtureFor(t, "1.4.2", goodArchive(t))
	// A restart is refused while an operation runs only when there is something installed to apply:
	// with nothing waiting, "nothing is staged" is the truer answer.
	f.service.state.StagedVersion = "1.4.2"
	f.service.working = "prepare"
	f.service.restart = func() error { return nil }
	err := f.service.Restart()
	if err == nil || !strings.Contains(err.Error(), "prepare is running") {
		t.Fatalf("Restart = %v, want a refusal naming the running operation", err)
	}
}

// The row outlives the process it was written by. A staged release the server is now running is not a
// staged release: the About tab must stop offering a restart for the version already in the seat.
func TestAStagedReleaseThatIsNowRunningIsNoLongerStaged(t *testing.T) {
	row, err := json.Marshal(State{CurrentVersion: "1.4.0", Latest: "1.4.2", Available: false, StagedVersion: "1.4.2", BackupPath: "/mike/.update-backup"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	store := &fakeStore{values: map[string]string{stateKey: string(row)}}

	svc, err := New(Options{Version: "1.4.2", Store: store, Fetcher: &fakeFetcher{}, InstallDir: releaseDir(t), ContainerMarkers: []string{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	st := svc.Status(Settings{}).State
	if st.StagedVersion != "" || st.BackupPath != "" {
		t.Errorf("the service still believes 1.4.2 is waiting: %+v", st)
	}
	// The cleared answer is what is stored, so the next restart agrees with this one.
	var saved State
	if err := json.Unmarshal([]byte(store.values[stateKey]), &saved); err != nil {
		t.Fatalf("stored row is not what this package wrote: %v", err)
	}
	if saved.StagedVersion != "" {
		t.Errorf("the stored row still says %q is staged", saved.StagedVersion)
	}

	// A release *ahead* of the running one stays staged, which is the case the restart button is for.
	// A fresh store: the row above has just been rewritten by the service that cleared it.
	holding := &fakeStore{values: map[string]string{stateKey: string(row)}}
	svc2, err := New(Options{Version: "1.4.0", Store: holding, Fetcher: &fakeFetcher{}, InstallDir: releaseDir(t), ContainerMarkers: []string{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := svc2.Status(Settings{}).State.StagedVersion; got != "1.4.2" {
		t.Errorf("StagedVersion = %q, want the release still waiting", got)
	}
}

// The other half of the same staleness: the restart put the stored "newer release" in the seat, so the dot
// would stay lit on the version this server is running.
func TestAStoredAnswerAboutTheRunningReleaseStopsOfferingIt(t *testing.T) {
	row, err := json.Marshal(State{
		CurrentVersion: "1.0.0",
		Latest:         "9.9.9",
		Available:      true,
		Note:           "release 9.9.9 names no archive for linux/arm64",
		Asset:          Asset{URL: "http://127.0.0.1:8099/dl/mike-server-9.9.9-linux-amd64.tar.gz", SHA256: strings.Repeat("a", 64)},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	svc, err := New(Options{
		Version: "9.9.9", Store: &fakeStore{values: map[string]string{stateKey: string(row)}},
		Fetcher: &fakeFetcher{}, InstallDir: releaseDir(t), ContainerMarkers: []string{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	st := svc.Status(Settings{}).State
	if st.Available || st.Asset.URL != "" || st.Note != "" {
		t.Errorf("offering %s to a server already running it: %+v", st.Latest, st)
	}

	// A release behind the running one is still an offer, and so is one ahead of it.
	for _, running := range []string{"1.0.0", "9.9.8"} {
		store := &fakeStore{values: map[string]string{stateKey: string(row)}}
		other, err := New(Options{
			Version: running, Store: store, Fetcher: &fakeFetcher{},
			InstallDir: releaseDir(t), ContainerMarkers: []string{},
		})
		if err != nil {
			t.Fatalf("New(%s): %v", running, err)
		}
		if got := other.Status(Settings{}).State; !got.Available || got.Asset.URL == "" {
			t.Errorf("running %s: the offer was dropped: %+v", running, got)
		}
	}
}

// A download that died with its process leaves a directory named with this package's prefix, and
// nothing else will ever remove it.
func TestNewReapsStaleStagingDirectories(t *testing.T) {
	dir := releaseDir(t)
	stale := filepath.Join(dir, stagingPrefix+"123456")
	if err := os.MkdirAll(filepath.Join(stale, "unpacked"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	foreign := filepath.Join(dir, "someone-elses-temp")
	if err := os.MkdirAll(foreign, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if _, err := New(Options{Version: "1.4.0", Store: &fakeStore{}, Fetcher: &fakeFetcher{}, InstallDir: dir, ContainerMarkers: []string{}}); err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("the stale staging directory is still there")
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Errorf("a directory this package does not own was removed: %v", err)
	}
}
