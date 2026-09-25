package server

// The request half of a pasted or dropped file; where it lands and why is
// backend/exec/task_files.go's story.

import (
	"io"
	"net/http"

	"mike/backend/exec"
	"mike/backend/httputil"
)

// handleTaskUpload accepts multipart/form-data with one `file` part and answers {"path": "..."} —
// the path the browser inserts into the message. MaxUploadSize and its sentence are the ones the fs
// upload endpoint uses: one request, one file, one limit.
func (s *Server) handleTaskUpload(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")

	file, header, release, ok := uploadedFile(w, r)
	if !ok {
		return
	}
	defer release()

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

	// The bytes land on the host, in the task's worktree, so there is no guest to wait for and nothing for a
	// disconnected browser to leave half-written.
	path, err := s.executor.WriteTaskFile(projectID, taskID, header.Filename, data)
	if err != nil {
		// Fail, not Fault: a task that is gone is a 404 and a sandbox that is not running is the
		// caller's situation to fix, not a server fault.
		httputil.Fail(w, err, "write task upload", "task not found")
		return
	}

	httputil.JSONResponse(w, http.StatusOK, map[string]string{"path": path})
}
