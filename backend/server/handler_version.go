package server

import (
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"regexp"

	"kanban/pkg/logging"
)

// indexHTMLLimit bounds the read of our own build artifact; index.html holds the pre-paint script
// inline, so its size is not fixed by the app's structure.
const indexHTMLLimit = 1 << 20

// The mirror of the tag scripts/build-id.ts writes into dist/index.html — a change to the tag's shape
// has to be made there too. Exact on purpose: an id this rule does not describe is answered as none.
var assetBuildIDPattern = regexp.MustCompile(`<meta name="app-build" content="([0-9a-f]{12})">`)

// handleVersion answers the identity of the binary and of the frontend it serves. Empty assets means
// "cannot tell", never "you are up to date".
func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	// The compress middleware puts an ETag on every GET; this is the only thing saying "do not keep it".
	w.Header().Set("Cache-Control", "no-store")
	JSONResponse(w, http.StatusOK, map[string]string{
		"version": Version,
		"assets":  s.assetBuildID(),
	})
}

func (s *Server) assetBuildID() string {
	id, err := readAssetBuildID(s.assets)
	if err != nil {
		logging.For(logging.HTTP).Debugf("assets identity unavailable: %v", err)
	}
	return id
}

// Reads the tag rather than recomputing the digest, so the grammar stays in the build.
func readAssetBuildID(assets fs.FS) (string, error) {
	f, err := assets.Open("index.html")
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
