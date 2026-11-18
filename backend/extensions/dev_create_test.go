package extensions

import "testing"

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
