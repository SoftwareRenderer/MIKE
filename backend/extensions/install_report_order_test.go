package extensions

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A client told "install failed" does what the message says: it retries, or it re-lists what is installed.
// Those are requests, and they arrive the moment the event is seen — so the rollback of a failed install's
// own checkout, and the release of its in-flight name, have to be finished before that event is emitted.
// `streamOperation` returns on the Done event, so the order inside the operation, not the caller's patience,
// decides what the next request sees. Getting it the other way round leaves a window in which
// `GET /api/extensions/local` offers the ghost folder the rollback has not deleted yet, under the manifest it
// shipped with, and the retry the message invites is refused as "install already in progress".

func TestFailedInstallSettlesTheStateBeforeItReportsFailure(t *testing.T) {
	repo, commit := makeGitRepo(t, map[string]string{
		"lsp/gopls/manifest.json": `{"display_name":"Go","description":"d","author":"Go Team","version":"0.7.0",` +
			`"capabilities":{"lsp":{"command":"gopls","args":["serve"],"extensions":["go"],"language":"go"}}}`,
		"lsp/gopls/Dockerfile": "FROM golang:1-alpine\nCOPY sandbox/sandbox-bridge /usr/local/bin/sandbox-bridge\n",
	})
	entry := &RegistryEntry{
		Name: "gopls", Version: "0.7.0", Type: ExtensionTypeLSP,
		DisplayName: "Go Language Server", Author: "Go Team",
		Repo: repo, Commit: commit, Subdir: "lsp/gopls",
	}
	fakePodmanFails(t, 1, `Error: building at STEP "COPY": no items matching glob`)

	// Several rounds, because the wrong order is observed as a race: one round would pass whenever the
	// removal happens to win.
	const rounds = 20
	for round := 1; round <= rounds; round++ {
		h := testHandler(t)
		extDir := t.TempDir()
		h.config.ExtensionsDir = extDir
		checkout := filepath.Join(extDir, "go-team", "gopls")

		observed := make(chan string, 1)
		events := make(chan sseEvent, 16)
		go func() {
			for ev := range events {
				if !ev.Done {
					continue
				}
				// The instant the browser sees the failure.
				status := "gone"
				if _, err := os.Stat(checkout); err == nil {
					status = "still there"
				}
				if h.inFlightOps.begin("gopls") {
					h.inFlightOps.end("gopls")
				} else {
					status += "; its name is still claimed"
				}
				observed <- status
			}
		}()

		h.installInBackground(entry.Name, entry, "", events)

		select {
		case got := <-observed:
			if got != "gone" {
				t.Fatalf("round %d: when the failure was reported the checkout %s (%s) — a client acting on "+
					"that message lists a ghost folder or is refused the retry it invites",
					round, got, describeTree(t, extDir))
			}
		case <-time.After(30 * time.Second):
			t.Fatalf("round %d: no terminal event arrived", round)
		}
		if _, err := os.Stat(checkout); !os.IsNotExist(err) {
			t.Fatalf("round %d: the checkout survived even after the operation returned (stat err = %v)", round, err)
		}
	}
}
