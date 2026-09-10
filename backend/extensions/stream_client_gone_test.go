package extensions

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Phase 9's acceptance specs, written in Phase 3h: the checks that say what the user experiences when they
// stop watching an operation, asserted on server state rather than on what the page that left still shows.

// An SSE operation streams out of a goroutine that owns the work, through a buffered channel. When the
// browser goes away the reader has to keep taking events out, because an operation blocked on a send to a
// reader that has gone is an install that never finishes, a name that stays in `inFlightOps` forever (every
// later attempt answers "install already in progress"), and a row that is never written — with the progress
// stream already closed, so nothing in the UI can ever report it.
func TestStreamOperationKeepsDrainingAfterTheClientGoesAway(t *testing.T) {
	h := testHandler(t)

	const events = 400 // more than the 16-slot channel every operation is created with
	produced := make(chan int, 1)
	in := make(chan sseEvent, 16)

	// The producer takes the same two defers the real operation takes (`installInBackground`), so that
	// "the name is still claimed" below is a claim about this run rather than about nothing.
	if !h.inFlightOps.begin("gopls") {
		t.Fatal("the fixture could not claim the operation name")
	}
	go func() {
		defer h.inFlightOps.end("gopls")
		for i := range events {
			in <- sseEvent{Extension: "gopls", Progress: float64(i), Message: "step"}
		}
		close(in)
		produced <- events
	}()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.streamOperation(w, r, in)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	scanner := bufio.NewScanner(resp.Body)
	if !scanner.Scan() {
		t.Fatalf("no SSE line arrived before the client left: %v", scanner.Err())
	}
	// The tab closes: the request context is cancelled and the connection is gone.
	cancel()
	_ = resp.Body.Close()

	// One case only, plus the deadline. Reading two channels the producer finishes together would let the
	// runtime choose between two ready cases, which reports a finished operation as a stranded one.
	// The deadline is generous because the defect is a wedge, not a slowness: a blocked send never
	// completes, so nothing is gained by failing early on a busy machine.
	select {
	case n := <-produced:
		if n != events {
			t.Errorf("the operation produced %d of %d events", n, events)
		}
		// The consequence the user would meet: an operation that never finished holds its name, so the
		// next install of that extension is refused by the guard against racing installs.
		if !h.inFlightOps.begin("gopls") {
			t.Error(`the name is still claimed: every later install answers "install already in progress"`)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the operation is still blocked on a send to a client that left; its name stays in " +
			"inFlightOps, so that extension can never be installed again")
	}
}

// The same property at the seam the user touches: they press install, watch a few lines of progress, close
// the tab. The extension has to be installed when they come back — the build is not the browser's to cancel,
// and a half-installed extension is a worse answer than a slow one. Asserted on the stored row, never on the
// page that left.
func TestInstallLocalCompletesAfterTheClientGoesAway(t *testing.T) {
	h := testHandler(t)
	writeLocalLSP(t, h, "fixturedev", "fixture-lsp", lspManifestNoImage)
	if err := os.WriteFile(filepath.Join(h.config.ExtensionsDirOrDefault(), "fixturedev", "fixture-lsp", "Dockerfile"),
		[]byte("FROM scratch\nCOPY sandbox/sandbox-bridge /sandbox-bridge\n"), 0o644); err != nil {
		t.Fatalf("write Dockerfile: %v", err)
	}
	// Slow enough that the client is provably gone before the build answers.
	fakePodmanExit(t, 0, "sleep 2; printf 'Successfully tagged fake\\n'")

	srv := httptest.NewServer(http.HandlerFunc(h.HandleInstallLocal))
	defer srv.Close()

	body, _ := json.Marshal(map[string]any{"author": "fixturedev", "name": "fixture-lsp"})
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/api/extensions/install-local",
		strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	scanner := bufio.NewScanner(resp.Body)
	sawProgress := false
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), "Building image") {
			sawProgress = true
			break
		}
	}
	if !sawProgress {
		_ = resp.Body.Close()
		cancel()
		t.Fatal("the install never reported the build in progress, so the cancel proves nothing")
	}
	cancel()
	_ = resp.Body.Close()

	deadline := time.Now().Add(10 * time.Second)
	for {
		exts, err := h.store.List()
		if err != nil {
			t.Fatalf("store.List: %v", err)
		}
		for _, e := range exts {
			if e.Name == "fixture-lsp" {
				// The image ref is the build's own answer: without the build finishing there is no row
				// with an image, and a row with no image is a language server that never starts.
				if e.ImageRef == "" {
					t.Fatalf("the row landed with no image ref: %+v", e)
				}
				if _, statErr := os.Stat(filepath.Join(h.config.ExtensionsDirOrDefault(), "fixturedev", "fixture-lsp", "manifest.json")); statErr != nil {
					t.Errorf("the installed files are gone although the row is present: %v", statErr)
				}
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the install did not complete after the client went away; installed set: %+v", exts)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// lspManifestNoImage is the local-folder shape that forces a build at install: a language server with no
// declared image, whose own Dockerfile is therefore the only source of one.
const lspManifestNoImage = `{
	"display_name": "Fixture Language Server",
	"description": "Local folder that builds its image.",
	"author": "Fixture Author",
	"version": "1.0.0",
	"capabilities": {"lsp": {"command": "fixture-lsp", "args": ["--stdio"], "file_types": [{"extension": "fx", "language_id": "fx", "label": "FX"}]}}
}`
