package updates

import (
	"encoding/json/v2"
	"errors"
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

// The row outlives the process that wrote it, and an answer about the release already in the seat is no
// answer at all: neither a restart nor a download may be offered for the version this server runs. A
// release behind it or ahead of it stays offered, which is what those two buttons are for.
func TestNewDropsTheStoredAnswerAboutTheReleaseAlreadyRunning(t *testing.T) {
	stagedRow := func() string {
		row, err := json.Marshal(State{
			CurrentVersion: "1.4.0", Latest: "1.4.2",
			StagedVersion: "1.4.2", BackupPath: "/mike/.update-backup",
		})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return string(row)
	}
	offeredRow := func() string {
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
		return string(row)
	}
	boot := func(t *testing.T, running, row string) (*Service, *fakeStore) {
		t.Helper()
		store := &fakeStore{values: map[string]string{stateKey: row}}
		svc, err := New(Options{
			Version: running, Store: store, Fetcher: &fakeFetcher{},
			InstallDir: releaseDir(t), ContainerMarkers: []string{},
		})
		if err != nil {
			t.Fatalf("New running %s: %v", running, err)
		}
		return svc, store
	}

	t.Run("a staged release that is now running is no longer staged", func(t *testing.T) {
		svc, store := boot(t, "1.4.2", stagedRow())
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
	})

	t.Run("a staged release still ahead of the running one stays staged", func(t *testing.T) {
		svc, _ := boot(t, "1.4.0", stagedRow())
		if got := svc.Status(Settings{}).State.StagedVersion; got != "1.4.2" {
			t.Errorf("StagedVersion = %q, want the release still waiting", got)
		}
	})

	t.Run("an offered release that is now running is not offered", func(t *testing.T) {
		svc, _ := boot(t, "9.9.9", offeredRow())
		st := svc.Status(Settings{}).State
		if st.Available || st.Asset.URL != "" || st.Note != "" {
			t.Errorf("offering %s to a server already running it: %+v", st.Latest, st)
		}
	})

	for _, running := range []string{"1.0.0", "9.9.8"} {
		t.Run("a release ahead of the running "+running+" stays offered", func(t *testing.T) {
			svc, _ := boot(t, running, offeredRow())
			if got := svc.Status(Settings{}).State; !got.Available || got.Asset.URL == "" {
				t.Errorf("running %s: the offer was dropped: %+v", running, got)
			}
		})
	}
}
