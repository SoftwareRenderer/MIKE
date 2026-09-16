package server

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// docAssets returns a frontend whose index.html is the given document.
func docAssets(indexHTML string) fs.FS {
	return fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte(indexHTML)}}
}

func versionAtStart(t *testing.T, assets fs.FS) *Server {
	t.Helper()
	return testServerWithAssets(t, assets, nil)
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

// The id the client compares against is the one the build wrote into the document it serves.
func TestVersionAnswersTheBuildIdentityStampedIntoDist(t *testing.T) {
	assets := docAssets(`<head><meta name="app-build" content="0a1b2c3d4e5f"><title>Board</title></head>`)

	got, rr := getVersion(t, versionAtStart(t, assets))

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

// A missing, unstamped or oversized document answers empty assets with a 200.
func TestVersionAnswersUnknownForEveryAssetTreeThatCannotBeNamed(t *testing.T) {
	t.Run("no document in the tree", func(t *testing.T) {
		got, rr := getVersion(t, versionAtStart(t, fstest.MapFS{}))
		if rr.Code != http.StatusOK || got.Assets != "" {
			t.Fatalf("got %d %q, want 200 with an empty assets field", rr.Code, got.Assets)
		}
	})

	t.Run("dist without a stamped document", func(t *testing.T) {
		got, rr := getVersion(t, versionAtStart(t, docAssets(`<head><title>Board</title></head>`)))
		if rr.Code != http.StatusOK || got.Assets != "" {
			t.Fatalf("got %d %q, want 200 with an empty assets field", rr.Code, got.Assets)
		}
	})

	t.Run("a tag this rule does not describe", func(t *testing.T) {
		assets := docAssets(`<head><meta name="app-build" content="ZZZZ"></head>`)
		got, _ := getVersion(t, versionAtStart(t, assets))
		if got.Assets != "" {
			t.Errorf("assets: got %q, want the unparseable tag ignored", got.Assets)
		}
	})

	t.Run("document larger than our own read limit", func(t *testing.T) {
		assets := docAssets(`<head><meta name="app-build" content="0a1b2c3d4e5f">` + strings.Repeat("x", indexHTMLLimit))
		got, _ := getVersion(t, versionAtStart(t, assets))
		if got.Assets != "" {
			t.Errorf("assets: got %q, want nothing read past the limit", got.Assets)
		}
	})
}

func TestVersionIsSessionAuthed(t *testing.T) {
	s := versionAtStart(t, docAssets(`<head><meta name="app-build" content="0a1b2c3d4e5f"></head>`))

	rr := testResponse(t, s, testRequest(http.MethodGet, "/api/version", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous GET /api/version: got %d, want 401", rr.Code)
	}
}
