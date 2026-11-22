package extensions

import (
	"encoding/json/jsontext"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mike/backend/errs"
	"mike/backend/types"
)

// The create path navigates straight to the new project's board, and the ReviewBar reads the browser's
// app-wide copy of the extension list. A create that announces nothing leaves that copy holding the
// snapshot it took at boot, so the task of a freshly created extension shows no Try Extension until
// someone reloads the page.
func TestCreatingADevRowAnnouncesTheChange(t *testing.T) {
	h, _, _ := devHandler(t)
	changed := 0
	h.SetOnExtensionChanged(func() { changed++ })

	if _, err := h.CreateDevRow("Sidebar clock", "project-1"); err != nil {
		t.Fatalf("CreateDevRow: %v", err)
	}
	if changed != 1 {
		t.Errorf("onExtensionChanged called %d times, want 1", changed)
	}
}

// A row the server refused is not a change, and announcing it would have every tab refetch a list that
// did not move.
func TestARefusedCreateAnnouncesNothing(t *testing.T) {
	h, _, _ := devHandler(t)
	changed := 0
	h.SetOnExtensionChanged(func() { changed++ })

	if _, err := h.CreateDevRow("??? ---", "project-1"); err == nil {
		t.Fatal("a name with nothing a directory can be called from it should have been refused")
	}
	if changed != 0 {
		t.Errorf("onExtensionChanged called %d times for a refused create, want 0", changed)
	}
}

// A second row under a taken name is the person's collision, not a server fault: it arrives as a conflict
// in MIKE's words, never as driver text.
func TestASecondRowUnderATakenNameIsAConflict(t *testing.T) {
	h, _, _ := devHandler(t)
	if _, err := h.CreateDevRow("Sidebar Clock", "project-1"); err != nil {
		t.Fatalf("CreateDevRow: %v", err)
	}

	_, err := h.CreateDevRow("sidebar clock", "project-2")
	if !errs.IsConflict(err) {
		t.Fatalf("second row under a taken name = %v, want a conflict", err)
	}
	if msg := errs.Message(err); !strings.Contains(msg, "sidebar-clock") || strings.Contains(msg, "UNIQUE constraint") {
		t.Errorf("conflict message = %q, want MIKE's sentence naming the extension", msg)
	}
}

// A row is kept, and the create refused, whenever something still owns it: a repository on disk, a project,
// or a value of the person's.
func TestReserveDevNameRefusesANameSomethingStillOwns(t *testing.T) {
	for _, tt := range []struct {
		owns string
		// hold lays out what owns the row and returns it, setting the project table as it goes.
		hold func(t *testing.T, h *ExtensionHandler, store *ExtensionStore, dir string) *Extension
		// stored is how many keys the row starts with.
		stored int
	}{
		{
			owns: "its repository is on disk",
			hold: func(t *testing.T, h *ExtensionHandler, store *ExtensionStore, dir string) *Extension {
				h.SetDevSources(fakeDevSources{})
				if err := os.MkdirAll(filepath.Join(dir, DevNamespace, "sidebar-clock"), 0o755); err != nil {
					t.Fatal(err)
				}
				return mustCreateDevRow(t, h, "gone-project")
			},
		},
		{
			owns: "its project is still there",
			hold: func(t *testing.T, h *ExtensionHandler, store *ExtensionStore, dir string) *Extension {
				h.SetDevSources(fakeDevSources{projects: map[string]*types.Project{"p1": {ID: "p1", Name: "p1"}}})
				return mustCreateDevRow(t, h, "p1")
			},
		},
		{
			owns: "a project holds the repository path",
			hold: func(t *testing.T, h *ExtensionHandler, store *ExtensionStore, dir string) *Extension {
				h.SetDevSources(fakeDevSources{projects: map[string]*types.Project{
					"p2": {ID: "p2", Name: "p2", Path: filepath.Join(dir, DevNamespace, "sidebar-clock")},
				}})
				return mustCreateDevRow(t, h, "gone-project")
			},
		},
		{
			owns: "the person configured it",
			hold: func(t *testing.T, h *ExtensionHandler, store *ExtensionStore, dir string) *Extension {
				h.SetDevSources(fakeDevSources{})
				row := mustCreateDevRow(t, h, "gone-project")
				row.Config = map[string]any{"token": "a value typed by a person"}
				if err := store.Update(row.ID, row); err != nil {
					t.Fatalf("configure the row: %v", err)
				}
				return row
			},
		},
		{
			owns:   "it stored a value",
			stored: 1,
			hold: func(t *testing.T, h *ExtensionHandler, store *ExtensionStore, dir string) *Extension {
				h.SetDevSources(fakeDevSources{})
				row := mustCreateDevRow(t, h, "gone-project")
				if _, err := store.StorageSet(row.ID, "clock", "face", jsontext.Value(`"round"`)); err != nil {
					t.Fatalf("store a value: %v", err)
				}
				return row
			},
		},
	} {
		t.Run(tt.owns, func(t *testing.T) {
			h, store, dir := devHandler(t)
			row := tt.hold(t, h, store, dir)

			if err := h.ReserveDevName("Sidebar clock"); !errs.IsConflict(err) {
				t.Fatalf("ReserveDevName over a row whose %s = %v, want a conflict", tt.owns, err)
			}
			if _, err := store.GetByID(row.ID); err != nil {
				t.Errorf("the row a person can still point at was deleted (%v)", err)
			}
			stored, err := store.StorageCountAll(row.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored != tt.stored {
				t.Errorf("stored keys = %d, want the %d it started with: a refusal stores nothing and loses nothing", stored, tt.stored)
			}
		})
	}
}

// A row no project or repository claims holds a name nobody can open. The create clears it and announces
// the change.
func TestReserveDevNameClearsTheRowOfAGoneProject(t *testing.T) {
	h, store, _ := devHandler(t)
	h.SetDevSources(fakeDevSources{})
	row := mustCreateDevRow(t, h, "gone-project")
	announced := 0
	h.SetOnExtensionChanged(func() { announced++ })

	if err := h.ReserveDevName("Sidebar clock"); err != nil {
		t.Fatalf("ReserveDevName over an unclaimed row = %v, want the name freed", err)
	}
	if _, err := store.GetByID(row.ID); !errors.Is(err, ErrExtensionNotFound) {
		t.Errorf("the unclaimed row survived: %v", err)
	}
	if announced != 1 {
		t.Errorf("onExtensionChanged called %d times for a row the create took away, want 1", announced)
	}
	if _, err := h.CreateDevRow("Sidebar clock", "a-new-project"); err != nil {
		t.Errorf("the freed name is not free: %v", err)
	}
}

// Without the project table a row can never be called abandoned, so nothing may be deleted beside it.
func TestReserveDevNameKeepsEveryRowItCannotJudge(t *testing.T) {
	h, store, _ := devHandler(t)
	row := mustCreateDevRow(t, h, "gone-project")

	if err := h.ReserveDevName("Sidebar clock"); !errs.IsConflict(err) {
		t.Fatalf("ReserveDevName with no project table = %v, want a conflict", err)
	}
	if _, err := store.GetByID(row.ID); err != nil {
		t.Errorf("a row nobody could judge was deleted: %v", err)
	}
}

func mustCreateDevRow(t *testing.T, h *ExtensionHandler, projectID string) *Extension {
	t.Helper()
	row, err := h.CreateDevRow("Sidebar clock", projectID)
	if err != nil {
		t.Fatalf("CreateDevRow: %v", err)
	}
	return row
}
