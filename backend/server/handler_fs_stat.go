package server

import (
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"mike/backend/fsutil"
	"mike/pkg/logging"
)

// A stat and nothing more: no file is opened, so the read routes' size caps have no part here. The only
// bound is the walk that prices a folder.

// Past this many entries or this much time a folder answers a partial figure rather than a request that
// never returns; 20k entries cost milliseconds on a warm tree, so the timeout is slack for a cold one.
const (
	fsStatWalkEntries = 20000
	fsStatWalkTimeout = 2 * time.Second
)

func fsStatWalkBudget() fsutil.WalkBudget {
	return fsutil.WalkBudget{MaxEntries: fsStatWalkEntries, Timeout: fsStatWalkTimeout, SkipGit: true}
}

const (
	fsStatKindFile    = "file"
	fsStatKindDir     = "dir"
	fsStatKindLink    = "link"
	fsStatKindOther   = "other"
	fsStatKindMissing = "missing"
	fsStatKindUnknown = "unknown"
)

// One path of the answer. `path` repeats what the caller asked for rather than the host file it resolved
// to, so a sandboxed project is never handed a host path. An absent Bytes or Items was not measured,
// which is not the same answer as a zero one.
type fsStatRow struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Kind string `json:"kind"`
	// A file's apparent size, or a folder's total over the files below it.
	Bytes *int64 `json:"bytes,omitempty"`
	// A folder's entries below it, the folder itself excluded.
	Items    *int   `json:"items,omitempty"`
	Partial  bool   `json:"partial,omitzero"`
	Mode     string `json:"mode,omitempty"`
	Modified string `json:"modified,omitempty"`
	MIME     string `json:"mime,omitempty"`
}

// Entries is always sent, even when it is empty.
type fsStatResponse struct {
	Entries []fsStatRow `json:"entries"`
}

// handleFSStat answers GET /api/fs/stat for each path the request names. Query: project (required),
// task (optional), path (repeatable). A path gone since the listing was read is a row rather than a
// refusal, so one vanished row cannot hide the rest of a selection.
func (s *Server) handleFSStat(w http.ResponseWriter, r *http.Request) {
	asked, ok := fsBatchPaths(w, r)
	if !ok {
		return
	}
	project, ok := s.requiredProject(w, r)
	if !ok {
		return
	}
	scope, ok := s.fsRequestScope(w, r, project)
	if !ok {
		return
	}
	budget := fsStatWalkBudget()
	answer := fsStatResponse{Entries: make([]fsStatRow, 0, len(asked))}
	for _, p := range asked {
		target, err := s.fsScopeTarget(scope, p)
		if err != nil {
			JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if !requireNoGitSegment(w, target) {
			return
		}
		answer.Entries = append(answer.Entries, statRow(p, target.path, budget))
	}
	JSONResponse(w, http.StatusOK, answer)
}

// statRow describes the host file at hostPath, naming the row by asked.
func statRow(asked, hostPath string, budget fsutil.WalkBudget) fsStatRow {
	row := fsStatRow{Name: filepath.Base(hostPath), Path: asked}
	info, err := os.Lstat(hostPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		row.Kind = fsStatKindMissing
		return row
	case err != nil:
		// The jail accepted this name and the kernel will not describe it.
		logging.For(logging.HTTP).Warnf("stat %s failed: %v", hostPath, err)
		row.Kind = fsStatKindUnknown
		return row
	}

	switch {
	case info.IsDir():
		row.Kind = fsStatKindDir
	case info.Mode()&fs.ModeSymlink != 0:
		// A task can aim a link anywhere on the host, so neither where it points nor its target's size
		// is the requester's to learn.
		row.Kind = fsStatKindLink
	case info.Mode().IsRegular():
		row.Kind = fsStatKindFile
	default:
		row.Kind = fsStatKindOther
	}
	row.Mode = fmt.Sprintf("%04o", info.Mode().Perm())
	row.Modified = info.ModTime().UTC().Format(time.RFC3339)

	switch row.Kind {
	case fsStatKindFile:
		size := info.Size()
		row.Bytes = &size
		row.MIME = mime.TypeByExtension(filepath.Ext(row.Name))
	case fsStatKindDir:
		usage, err := fsutil.TreeUsageFor(hostPath, budget)
		if err != nil {
			// A tree torn down mid-walk has no figure; the folder's own stat stands.
			logging.For(logging.HTTP).Warnf("size of %s failed: %v", hostPath, err)
			return row
		}
		size, items := usage.Bytes, usage.Items
		row.Bytes, row.Items = &size, &items
		row.Partial = !usage.Complete
	}
	return row
}
