package server

import (
	"errors"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"mike/backend/fsutil"
	"mike/backend/httputil"
	"mike/pkg/logging"
)

// Every path in a request is checked against the project before any of them is touched, so a
// refused request moves nothing. A failure while running is reported for that one item and the
// rest go ahead.
//
// These work on the host filesystem even for a sandboxed project, because that is where the bind
// mount's bytes are. fsScopeTarget converts the container path the browser sent.

// maxFSOpsBatch is the most paths one request may carry.
const maxFSOpsBatch = 100

// fsOpsRequest is the body of the three batch operations; `dest` is absent for the delete.
type fsOpsRequest struct {
	Paths []string `json:"paths"`
	Dest  string   `json:"dest"`
}

// fsOpsItem is one path of the answer, named the way the caller named it, so a caller browsing a
// sandboxed project never gets a host path back.
type fsOpsItem struct {
	From    string `json:"from"`
	To      string `json:"to,omitempty"`
	Renamed bool   `json:"renamed,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// fsOpsResponse is a finished batch. Both lists are always sent, even when empty.
type fsOpsResponse struct {
	Done   []fsOpsItem `json:"done"`
	Failed []fsOpsItem `json:"failed"`
}

func (r *fsOpsResponse) addDone(from, to string, renamed bool) {
	r.Done = append(r.Done, fsOpsItem{From: from, To: to, Renamed: renamed})
}

func (r *fsOpsResponse) addFailure(from, op string, err error) {
	r.Failed = append(r.Failed, fsOpsItem{From: from, Reason: fsOpsReason(op, err)})
}

// fsOpsReason is the message beside one failed item. The fsutil errors are already words for a
// person and name no path; anything else is logged and answered with a general sentence.
func fsOpsReason(op string, err error) string {
	switch {
	case errors.Is(err, fsutil.ErrGitPath),
		errors.Is(err, fsutil.ErrProtectedPath),
		errors.Is(err, fsutil.ErrCopyIntoItself),
		errors.Is(err, fsutil.ErrCopyTooLarge):
		return err.Error()
	case errors.Is(err, os.ErrNotExist):
		return "it no longer exists"
	default:
		logging.For(logging.HTTP).Errorf("fs %s failed: %v", op, err)
		return "the server could not do it"
	}
}

type fsOpsPath struct {
	asked  string
	target fsTarget
}

// refusal is the one thing a path inside the project can still be refused for: anything under
// .git.
func (p fsOpsPath) refusal() error {
	if fsutil.HasGitSegment(p.target.path) {
		return fsutil.ErrGitPath
	}
	return nil
}

type fsOpsBatch struct {
	paths []fsOpsPath
	dest  fsOpsPath
}

// decodeFSOpsBatch decodes a batch and verifies all paths against the project jail.
func (s *Server) decodeFSOpsBatch(w http.ResponseWriter, r *http.Request, needDest bool) (fsOpsBatch, bool) {
	var req fsOpsRequest
	if !httputil.DecodeJSON(w, r, &req) {
		return fsOpsBatch{}, false
	}
	if len(req.Paths) == 0 {
		JSONError(w, http.StatusBadRequest, "paths is required")
		return fsOpsBatch{}, false
	}
	if len(req.Paths) > maxFSOpsBatch {
		JSONError(w, http.StatusBadRequest, "too many paths (max "+strconv.Itoa(maxFSOpsBatch)+")")
		return fsOpsBatch{}, false
	}
	if needDest && req.Dest == "" {
		JSONError(w, http.StatusBadRequest, "dest is required")
		return fsOpsBatch{}, false
	}
	project, ok := s.requiredProject(w, r)
	if !ok {
		return fsOpsBatch{}, false
	}
	scope, ok := s.fsRequestScope(w, r, project)
	if !ok {
		return fsOpsBatch{}, false
	}

	batch := fsOpsBatch{}
	for _, p := range req.Paths {
		target, err := s.fsScopeTarget(scope, p)
		if err != nil {
			JSONError(w, http.StatusBadRequest, err.Error())
			return fsOpsBatch{}, false
		}
		batch.paths = append(batch.paths, fsOpsPath{asked: p, target: target})
	}
	if !needDest {
		return batch, true
	}
	dest, err := s.fsScopeTarget(scope, req.Dest)
	if err != nil {
		JSONError(w, http.StatusBadRequest, err.Error())
		return fsOpsBatch{}, false
	}
	if fsutil.HasGitSegment(dest.path) {
		JSONError(w, http.StatusBadRequest, fsutil.ErrGitPath.Error())
		return fsOpsBatch{}, false
	}
	info, err := os.Stat(dest.path)
	if err != nil {
		if os.IsNotExist(err) {
			JSONError(w, http.StatusNotFound, "destination folder no longer exists")
			return fsOpsBatch{}, false
		}
		Fault(w, "stat destination folder", err)
		return fsOpsBatch{}, false
	}
	if !info.IsDir() {
		JSONError(w, http.StatusBadRequest, "destination is not a folder")
		return fsOpsBatch{}, false
	}
	batch.dest = fsOpsPath{asked: req.Dest, target: dest}
	return batch, true
}

// handleFSDelete removes every path it is given, a folder together with everything under it.
func (s *Server) handleFSDelete(w http.ResponseWriter, r *http.Request) {
	batch, ok := s.decodeFSOpsBatch(w, r, false)
	if !ok {
		return
	}
	res := fsOpsResponse{Done: []fsOpsItem{}, Failed: []fsOpsItem{}}
	for _, p := range batch.paths {
		if err := p.refusal(); err != nil {
			res.addFailure(p.asked, "delete", err)
			continue
		}
		// A project's checkout and a task's worktree are directories this server made and removes
		// elsewhere.
		if err := fsutil.RemoveTree(p.target.path, p.target.roots); err != nil {
			res.addFailure(p.asked, "delete", err)
			continue
		}
		res.addDone(p.asked, "", false)
	}
	JSONResponse(w, http.StatusOK, res)
}

// handleFSCopyPaths copies every path it is given into the destination folder.
func (s *Server) handleFSCopyPaths(w http.ResponseWriter, r *http.Request) {
	s.fsOpsPaste(w, r, false)
}

// handleFSMovePaths moves every path it is given into the destination folder.
func (s *Server) handleFSMovePaths(w http.ResponseWriter, r *http.Request) {
	s.fsOpsPaste(w, r, true)
}

// fsOpsPaste is the paste. A name the destination already has is neither an error nor an overwrite:
// the item lands as `name copy`, `name copy2`, … and the answer says which.
func (s *Server) fsOpsPaste(w http.ResponseWriter, r *http.Request, move bool) {
	batch, ok := s.decodeFSOpsBatch(w, r, true)
	if !ok {
		return
	}
	op, run := "copy", fsutil.CopyTree
	if move {
		op, run = "move", fsutil.MovePath
	}
	res := fsOpsResponse{Done: []fsOpsItem{}, Failed: []fsOpsItem{}}
	for _, p := range batch.paths {
		if err := p.refusal(); err != nil {
			res.addFailure(p.asked, op, err)
			continue
		}
		// Moving a file into the folder it is already in does nothing. Copying it there makes a
		// duplicate, so only the move stops here.
		if move && filepath.Dir(p.target.path) == batch.dest.target.path {
			res.addDone(p.asked, "", false)
			continue
		}
		finalName, renamed, err := fsutil.UniqueName(batch.dest.target.path, filepath.Base(p.target.path))
		if err != nil {
			res.addFailure(p.asked, op, err)
			continue
		}
		if err := run(p.target.path, filepath.Join(batch.dest.target.path, finalName)); err != nil {
			res.addFailure(p.asked, op, err)
			continue
		}
		res.addDone(p.asked, path.Join(batch.dest.asked, finalName), renamed)
	}
	JSONResponse(w, http.StatusOK, res)
}

// handleFSRenamePath gives one path a new name in its own folder. A name that is taken there is
// refused rather than changed, because the person typing it chose it.
func (s *Server) handleFSRenamePath(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if !httputil.DecodeJSON(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if req.Path == "" || name == "" {
		JSONError(w, http.StatusBadRequest, "path and name are required")
		return
	}
	if name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		JSONError(w, http.StatusBadRequest, "invalid name")
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
	target, err := s.fsScopeTarget(scope, req.Path)
	if err != nil {
		JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if fsutil.HasGitSegment(target.path) {
		JSONError(w, http.StatusBadRequest, fsutil.ErrGitPath.Error())
		return
	}
	dir := filepath.Dir(target.path)
	if _, err := os.Lstat(filepath.Join(dir, name)); err == nil {
		JSONError(w, http.StatusConflict, "that name is already taken in this folder")
		return
	}
	if err := fsutil.MovePath(target.path, filepath.Join(dir, name)); err != nil {
		switch {
		case errors.Is(err, os.ErrNotExist):
			JSONError(w, http.StatusNotFound, "file not found")
		case errors.Is(err, os.ErrExist):
			JSONError(w, http.StatusConflict, "that name is already taken in this folder")
		default:
			Fault(w, "rename", err)
		}
		return
	}
	JSONResponse(w, http.StatusOK, map[string]string{"path": path.Join(path.Dir(req.Path), name)})
}
