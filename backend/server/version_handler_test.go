package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kanban/backend/startup"
)

// writeDist returns a dist directory holding the given document as its index.html.
func writeDist(t *testing.T, indexHTML string) string {
	t.Helper()
	dist := t.TempDir()
	if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte(indexHTML), 0o644); err != nil {
		t.Fatalf("write index.html: %v", err)
	}
	return dist
}

func versionAtStart(t *testing.T, dist string) *Server {
	t.Helper()
	return testServerWith(t, func(c *startup.Config) { c.FrontendDist = dist })
}

type versionResponse struct {
	Version string `json:"version"`
	Assets  string `json:"assets"`
}

func getVersion(t *testing.T, s *Server) (versionResponse, *httptest.ResponseRecorder) {
	t.Helper()
	req := testRequest(http.MethodGet, "/api/version", nil)
	req.Header.Set("Cookie", loginTest(t, s))
	rr := testResponse(t, s, req)
	var got versionResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode /api/version: %v (%s)", err, rr.Body.String())
	}
	return got, rr
}

// TestVersionAnswersTheBuildIdentityStampedIntoDist: what the client compares against is the id the
// build wrote into the document it serves.
func TestVersionAnswersTheBuildIdentityStampedIntoDist(t *testing.T) {
	dist := writeDist(t, `<head><meta name="app-build" content="0a1b2c3d4e5f"><title>Board</title></head>`)

	got, rr := getVersion(t, versionAtStart(t, dist))

	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/version: got %d, want 200", rr.Code)
	}
	if got.Assets != "0a1b2c3d4e5f" {
		t.Errorf("assets: got %q, want the stamped id", got.Assets)
	}
	if got.Version != Version {
		t.Errorf("version: got %q, want %q", got.Version, Version)
	}
	if cc := rr.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control: got %q, want no-store", cc)
	}
}

// TestVersionAnswersUnknownForEveryDistThatCannotBeNamed: a missing dist, a build predating the stamp,
// an unreadable file and a document too large to be ours all answer empty assets with a 200.
func TestVersionAnswersUnknownForEveryDistThatCannotBeNamed(t *testing.T) {
	t.Run("no dist configured", func(t *testing.T) {
		got, rr := getVersion(t, versionAtStart(t, ""))
		if rr.Code != http.StatusOK || got.Assets != "" {
			t.Fatalf("got %d %q, want 200 with an empty assets field", rr.Code, got.Assets)
		}
	})

	t.Run("dist without a stamped document", func(t *testing.T) {
		got, rr := getVersion(t, versionAtStart(t, writeDist(t, `<head><title>Board</title></head>`)))
		if rr.Code != http.StatusOK || got.Assets != "" {
			t.Fatalf("got %d %q, want 200 with an empty assets field", rr.Code, got.Assets)
		}
	})

	t.Run("dist directory is gone again", func(t *testing.T) {
		got, rr := getVersion(t, versionAtStart(t, filepath.Join(t.TempDir(), "not-built")))
		if rr.Code != http.StatusOK || got.Assets != "" {
			t.Fatalf("got %d %q, want 200 with an empty assets field", rr.Code, got.Assets)
		}
	})

	t.Run("a tag this rule does not describe", func(t *testing.T) {
		dist := writeDist(t, `<head><meta name="app-build" content="ZZZZ"></head>`)
		got, _ := getVersion(t, versionAtStart(t, dist))
		if got.Assets != "" {
			t.Errorf("assets: got %q, want the unparseable tag ignored", got.Assets)
		}
	})

	t.Run("document larger than our own read limit", func(t *testing.T) {
		dist := writeDist(t, `<head><meta name="app-build" content="0a1b2c3d4e5f">`+strings.Repeat("x", indexHTMLLimit))
		got, _ := getVersion(t, versionAtStart(t, dist))
		if got.Assets != "" {
			t.Errorf("assets: got %q, want nothing read past the limit", got.Assets)
		}
	})
}

// TestVersionIsSessionAuthed.
func TestVersionIsSessionAuthed(t *testing.T) {
	s := versionAtStart(t, writeDist(t, `<head><meta name="app-build" content="0a1b2c3d4e5f"></head>`))

	rr := testResponse(t, s, testRequest(http.MethodGet, "/api/version", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous GET /api/version: got %d, want 401", rr.Code)
	}
}
