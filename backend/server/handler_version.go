package server

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"

	"kanban/pkg/logging"
)

// The binary's version cannot say whether the frontend a client runs is current: `make frontend`
// replaces dist without rebuilding the binary. GET /api/config answers version too, but it is a large
// authenticated payload that moves whenever the operator edits anything in it.

// indexHTMLLimit bounds the read of our own build artifact; index.html holds the pre-paint script
// inline, so its size is not fixed by the app's structure.
const indexHTMLLimit = 1 << 20

// The mirror of the tag scripts/build-id.ts writes into dist/index.html — a change to the tag's shape
// has to be made there too. Exact on purpose: an id this rule does not describe is answered as none.
var assetBuildIDPattern = regexp.MustCompile(`<meta name="app-build" content="([0-9a-f]{12})">`)

// handleVersion answers the identity of the binary and of the frontend on disk. Empty assets means no
// identity is available, and a client must read that as "cannot tell", never as "you are up to date".
func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	// The compress middleware puts an ETag on every GET; this is the only thing saying "do not keep it".
	w.Header().Set("Cache-Control", "no-store")
	JSONResponse(w, http.StatusOK, map[string]string{
		"version": Version,
		"assets":  s.assetBuildID(),
	})
}

func (s *Server) assetBuildID() string {
	if s.frontendDist == "" {
		return ""
	}
	id, err := readAssetBuildID(s.frontendDist)
	if err != nil {
		logging.For(logging.HTTP).Debugf("assets identity unavailable: %v", err)
	}
	return id
}

// Reads the tag rather than recomputing the digest, so the grammar stays in the build.
func readAssetBuildID(dist string) (string, error) {
	f, err := os.Open(filepath.Join(dist, "index.html"))
	if err != nil {
		return "", err
	}
	defer f.Close()

	body, err := io.ReadAll(io.LimitReader(f, indexHTMLLimit+1))
	if err != nil {
		return "", err
	}
	if len(body) > indexHTMLLimit {
		return "", fmt.Errorf("index.html exceeds %d bytes", indexHTMLLimit)
	}
	match := assetBuildIDPattern.FindSubmatch(body)
	if match == nil {
		return "", nil
	}
	return string(match[1]), nil
}
