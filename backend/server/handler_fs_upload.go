package server

import (
	"errors"
	"io"
	"kanban/backend/httputil"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"kanban/backend/fsutil"
	"kanban/pkg/logging"
)

// Uploads are the only fs write whose body is not JSON: /api/fs/write's `content`
// is a string field capped at 1 MB, so it cannot carry binary data.
const (
	// MaxUploadSize bounds one uploaded file.
	MaxUploadSize = 20 << 20
	// uploadTooLargeMessage is the answer both upload size checks give. Written by
	// hand beside the constant it describes and pinned to it by a test, because a
	// cap raised without its sentence tells the user a lie about the cap.
	uploadTooLargeMessage = "file too large (max 20MB)"
	// uploadBoundaryOverhead is the multipart framing around a file at the cap.
	// MaxBytesReader counts the whole body, so it gets the cap plus this.
	uploadBoundaryOverhead = 8 << 10
	// uploadFormMemory is how much of the body stays in memory before the rest
	// spills to temporary files.
	uploadFormMemory = 1 << 20
)

// handleFSUpload writes one uploaded file into a project's directory tree.
//
// multipart/form-data, with `path` (target directory, in the container form when
// the project is sandboxed), `relpath` (path inside it, defaulting to the part
// filename), `overwrite` ("1"), and the `file` part. The relative path needs its
// own field because Go strips directories from a part's filename: a drop of
// assets/img/logo.png arrives as "logo.png".
//
// An existing name is answered 409 unless overwrite=1 — replacing a file is the
// user's decision, not a default of the transport.
func (s *Server) handleFSUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httputil.MethodNotAllowed(w)
		return
	}
	project, err := s.optionalProject(r)
	if err != nil {
		JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if project == nil {
		JSONError(w, http.StatusBadRequest, "project parameter is required")
		return
	}

	// Bound the body before reading any of it.
	r.Body = http.MaxBytesReader(w, r.Body, MaxUploadSize+uploadBoundaryOverhead)
	if err := r.ParseMultipartForm(uploadFormMemory); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			JSONError(w, http.StatusRequestEntityTooLarge, uploadTooLargeMessage)
			return
		}
		JSONError(w, http.StatusBadRequest, "multipart form with a file part is required")
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		JSONError(w, http.StatusBadRequest, "file part is required")
		return
	}
	defer file.Close()

	dir := strings.TrimSpace(r.FormValue("path"))
	if dir == "" {
		JSONError(w, http.StatusBadRequest, "path is required")
		return
	}
	relPath := r.FormValue("relpath")
	if relPath == "" {
		relPath = header.Filename
	}
	if err := fsutil.ValidateRelPath(relPath); err != nil {
		JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	overwrite := r.FormValue("overwrite") == "1"

	// Containment before anything else: nothing below writes until the joined path has
	// been translated to a host path, tied to the task the request named, and checked
	// against the project.
	target, ok := s.fsWriteTarget(w, r, project, filepath.Join(dir, filepath.ToSlash(relPath)))
	if !ok {
		return
	}

	if info, err := os.Lstat(target); err == nil {
		if info.IsDir() {
			JSONError(w, http.StatusConflict, "a directory with that name already exists")
			return
		}
		if !overwrite {
			JSONError(w, http.StatusConflict, "file already exists: "+filepath.Base(target))
			return
		}
	}

	// MaxBytesReader already capped the body; this check is the one that names the file.
	content, err := io.ReadAll(io.LimitReader(file, MaxUploadSize+1))
	if err != nil {
		logging.For(logging.HTTP).Errorf("fs upload read failed: %v", err)
		JSONError(w, http.StatusInternalServerError, "failed to read uploaded file")
		return
	}
	if len(content) > MaxUploadSize {
		JSONError(w, http.StatusRequestEntityTooLarge, uploadTooLargeMessage)
		return
	}

	if !s.writeContainedFile(w, target, content) {
		return
	}
	JSONResponse(w, http.StatusOK, map[string]string{"status": "written", "path": target})
}
