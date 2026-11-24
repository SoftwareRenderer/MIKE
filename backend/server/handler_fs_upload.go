package server

import (
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"mike/backend/fsutil"
	"mike/pkg/logging"
)

// Uploads are the only fs write whose body is not JSON: /api/fs/write's `content`
// is a string field capped by the editor's buffer, so it cannot carry binary data.
const (
	// uploadBoundaryOverhead covers the multipart framing MaxBytesReader counts on top of
	// the file body.
	uploadBoundaryOverhead = 8 << 10
	// uploadFormMemory is how much of the body stays in memory before the rest
	// spills to temporary files.
	uploadFormMemory = 1 << 20
)

// uploadedFile extracts the file from a multipart body, applying size limits
// before reading. The returned cleanup function closes the file and removes
// temporary form files.
func (s *Server) uploadedFile(w http.ResponseWriter, r *http.Request) (multipart.File, *multipart.FileHeader, func(), bool) {
	limit := s.settings.FileSizeLimitBytes()
	r.Body = http.MaxBytesReader(w, r.Body, limit+uploadBoundaryOverhead)
	if err := r.ParseMultipartForm(uploadFormMemory); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			JSONError(w, http.StatusRequestEntityTooLarge, s.fileTooLarge())
			return nil, nil, nil, false
		}
		JSONError(w, http.StatusBadRequest, "multipart form with a file part is required")
		return nil, nil, nil, false
	}
	discard := func() {
		if r.MultipartForm != nil {
			r.MultipartForm.RemoveAll()
		}
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		discard()
		JSONError(w, http.StatusBadRequest, "file part is required")
		return nil, nil, nil, false
	}
	return file, header, func() {
		_ = file.Close()
		discard()
	}, true
}

// handleFSUpload writes one uploaded file into a project's directory tree.
//
// multipart/form-data with `path` (target directory, in container form when the project is
// sandboxed), `relpath` (path inside it, defaulting to the part filename), `overwrite` ("1")
// and the `file` part. The relative path needs its own field because Go strips directories from
// a part's filename. An existing name is answered 409 unless overwrite=1.
func (s *Server) handleFSUpload(w http.ResponseWriter, r *http.Request) {
	project, ok := s.requiredProject(w, r)
	if !ok {
		return
	}

	file, header, release, ok := s.uploadedFile(w, r)
	if !ok {
		return
	}
	defer release()

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

	if info, err := os.Lstat(target.path); err == nil {
		if info.IsDir() {
			JSONError(w, http.StatusConflict, "a directory with that name already exists")
			return
		}
		if !overwrite {
			JSONError(w, http.StatusConflict, "file already exists: "+filepath.Base(target.path))
			return
		}
	}

	// MaxBytesReader already capped the body; this check is the one that names the file.
	limit := s.settings.FileSizeLimitBytes()
	content, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		logging.For(logging.HTTP).Errorf("fs upload read failed: %v", err)
		JSONError(w, http.StatusInternalServerError, "failed to read uploaded file")
		return
	}
	if int64(len(content)) > limit {
		JSONError(w, http.StatusRequestEntityTooLarge, s.fileTooLarge())
		return
	}

	if !s.writeContainedFile(w, target, content) {
		return
	}
	JSONResponse(w, http.StatusOK, map[string]string{"status": "written", "path": target.path})
}
