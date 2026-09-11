package server

// The request half of a pasted or dropped file; where it lands and why is
// backend/exec/task_files.go's story.

import (
	"context"
	"io"
	"net/http"

	"kanban/backend/exec"
	"kanban/backend/httputil"
)

// handleTaskUpload accepts multipart/form-data with one `file` part and answers {"path": "..."} —
// the path the browser inserts into the message. MaxUploadSize and its sentence are the ones the fs
// upload endpoint uses: one request, one file, one limit.
func (s *Server) handleTaskUpload(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")

	r.Body = http.MaxBytesReader(w, r.Body, MaxUploadSize+uploadBoundaryOverhead)
	if err := r.ParseMultipartForm(uploadFormMemory); err != nil {
		httputil.JSONError(w, http.StatusRequestEntityTooLarge, uploadTooLargeMessage)
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		httputil.JSONError(w, http.StatusBadRequest, "file part is required")
		return
	}
	defer file.Close()

	// Answered for before any byte is written, so a request that cannot name its file is refused as
	// a bad request rather than as a failed write.
	if _, err := exec.UploadName(header.Filename); err != nil {
		httputil.JSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	data, err := io.ReadAll(io.LimitReader(file, MaxUploadSize+1))
	if err != nil {
		httputil.Fault(w, "read uploaded file", err)
		return
	}
	if len(data) > MaxUploadSize {
		httputil.JSONError(w, http.StatusRequestEntityTooLarge, uploadTooLargeMessage)
		return
	}

	// The guest is not cancellable: dropping the context here would stop waiting on a write that
	// completes anyway. WriteTaskFile bounds the wait.
	path, err := s.executor.WriteTaskFile(context.WithoutCancel(r.Context()), projectID, taskID, header.Filename, data)
	if err != nil {
		// Fail, not Fault: a task that is gone is a 404 and a sandbox that is not running is the
		// caller's situation to fix, not a server fault.
		httputil.Fail(w, err, "write task upload", "task not found")
		return
	}

	httputil.JSONResponse(w, http.StatusOK, map[string]string{"path": path})
}
