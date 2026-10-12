package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"mike/backend/exec"
	"mike/backend/fsutil"
	"mike/backend/git"
	"mike/backend/types"
	"mike/pkg/logging"
)

// rawImageTypes defines the allowed image types for GET /api/fs/raw. SVG is excluded
// to prevent markup injection via <img> tags. frontend/src/lib/utils/diff-image.ts
// mirrors this set.
var rawImageTypes = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".bmp":  "image/bmp",
	".ico":  "image/x-icon",
	".avif": "image/avif",
}

// maxRawFileBytes is the maximum size for a raw image, mirroring the upload limit
// for worktree writes.
const maxRawFileBytes int64 = exec.MaxUploadBytes

var rawTooLargeMessage = fmt.Sprintf("file too large (max %dMB)", maxRawFileBytes/(1<<20))

// handleFSRaw streams a project image directly. Unlike /api/fs/read, which returns JSON,
// this route provides the raw bytes.
//
// Query: project (required), task (which working tree), path (relative to the repository root), ref
// (optional — the committed object instead of the file on disk).
func (s *Server) handleFSRaw(w http.ResponseWriter, r *http.Request) {
	rel, err := repoRelativePath(r.URL.Query().Get("path"))
	if err != nil {
		JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	contentType, ok := rawImageTypes[strings.ToLower(filepath.Ext(rel))]
	if !ok {
		JSONError(w, http.StatusUnsupportedMediaType, "not a viewable image type")
		return
	}
	project, ok := s.requiredProject(w, r)
	if !ok {
		return
	}
	if !s.requireSandboxAvailable(w, project) {
		return
	}

	ref := r.URL.Query().Get("ref")
	if ref == "" {
		// fsWorktreeRoot binds ?task= to ?project= before either is honoured.
		root, ok := s.fsWorktreeRoot(w, r, project)
		if !ok {
			return
		}
		hostPath, err := s.validateFSPath(project, root, filepath.Join(root, rel))
		if err != nil {
			JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.serveWorktreeImage(w, hostPath, contentType)
		return
	}

	if err := types.ValidateGitRef(ref); err != nil {
		JSONError(w, http.StatusBadRequest, "invalid ref: "+err.Error())
		return
	}
	if err := s.requireTaskOwnership(project, r.URL.Query().Get("task")); err != nil {
		Fail(w, err, "get task", "task not found in project")
		return
	}
	// Worktrees of a project share its object database, so a committed object is read from the project
	// checkout — one whose worktree was deleted still answers.
	s.serveCommittedImage(w, r, project.Path, ref, filepath.ToSlash(rel), contentType)
}

// repoRelativePath converts a relative project path to a filesystem path, refusing
// absolute paths to avoid ambiguity.
func repoRelativePath(p string) (string, error) {
	if p == "" {
		return "", errors.New("path is required")
	}
	if strings.HasPrefix(p, "/") || looksLikeDrivePath(p) {
		return "", errors.New("path must be relative to the project root")
	}
	clean := path.Clean(filepath.ToSlash(p))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("path must stay inside the project")
	}
	return filepath.FromSlash(clean), nil
}

// looksLikeDrivePath detects Windows-style absolute paths on non-Windows platforms.
func looksLikeDrivePath(p string) bool {
	return len(p) > 2 && p[1] == ':' && (p[2] == '/' || p[2] == '\\')
}

// refusePathJail logs and rejects a path that changed after the containment check,
// preventing path-swap attacks.
func refusePathJail(w http.ResponseWriter, hostPath string) {
	logging.For(logging.HTTP).Warnf("file refused, path changed after containment: %s", hostPath)
	JSONError(w, http.StatusBadRequest, fsutil.ErrOutside.Error())
}

// serveWorktreeImage streams an image from disk, verifying it is not a directory and
// that it remains jailed before reading.
func (s *Server) serveWorktreeImage(w http.ResponseWriter, hostPath, contentType string) {
	before, err := os.Lstat(hostPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			JSONError(w, http.StatusNotFound, safeNotFoundError())
		} else {
			logging.For(logging.HTTP).Errorf("raw image stat failed: %v", err)
			JSONError(w, http.StatusInternalServerError, "failed to read file")
		}
		return
	}
	if before.IsDir() {
		JSONError(w, http.StatusBadRequest, "path is a directory")
		return
	}
	f, info, ok := openJailedFile(w, hostPath, before)
	if !ok {
		return
	}
	defer f.Close()
	if info.Size() > maxRawFileBytes {
		JSONError(w, http.StatusRequestEntityTooLarge, rawTooLargeMessage)
		return
	}
	writeImageHeaders(w, contentType, info.Size())
	if _, err := io.CopyN(w, f, maxRawFileBytes); err != nil && !errors.Is(err, io.EOF) {
		// Headers are gone, and a half-sent image is usually a closed tab rather than a fault.
		logging.For(logging.HTTP).Debugf("raw image write failed: %v", err)
	}
}

func (s *Server) serveCommittedImage(w http.ResponseWriter, r *http.Request, repoDir, ref, relPath, contentType string) {
	spec := ref + ":" + relPath
	// The probe is what lets a refusal cost a stat instead of the allocation it refused; when git
	// cannot answer, the bounded capture below is still the guarantee.
	if size, ok := gitBlobSize(gitInDir(r.Context(), repoDir), spec); ok {
		if size > maxRawFileBytes {
			JSONError(w, http.StatusRequestEntityTooLarge, rawTooLargeMessage)
			return
		}
	}
	data, truncated, err := gitBlobBytes(r.Context(), repoDir, spec)
	if truncated {
		JSONError(w, http.StatusRequestEntityTooLarge, rawTooLargeMessage)
		return
	}
	if err != nil {
		logging.For(logging.HTTP).Debugf("raw image blob read failed: %v", err)
		JSONError(w, http.StatusNotFound, safeNotFoundError())
		return
	}
	writeImageHeaders(w, contentType, int64(len(data)))
	_, _ = w.Write(data)
}

// gitInDir runs git commands in a specified directory, returning a function for easy execution.
func gitInDir(ctx context.Context, dir string) func(args ...string) (string, error) {
	return func(args ...string) (string, error) {
		cmd := git.CommandContext(ctx, dir, args...)
		var out, errOut cappedBuffer
		cmd.Stdout, cmd.Stderr = &out, &errOut
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("%v: %s", err, errOut.String())
		}
		return out.String(), nil
	}
}

// gitBlobBytes reads the raw bytes of a git blob using cat-file, ensuring no extra output is returned.
func gitBlobBytes(ctx context.Context, dir, spec string) ([]byte, bool, error) {
	return gitBlobSpool(ctx, dir, spec, maxRawFileBytes)
}

// gitBlobSpool is gitBlobBytes with the caller's own ceiling on the bytes it will hold: what an image
// route will not stream and an archive route will not list are different numbers, and neither belongs to
// the other. truncated is true when the blob ran past it, and the process is stopped rather than drained —
// draining is the read the answer just refused.
func gitBlobSpool(ctx context.Context, dir, spec string, limit int64) ([]byte, bool, error) {
	cmd := git.CommandContext(ctx, dir, "cat-file", "blob", "--end-of-options", spec)
	var errOut cappedBuffer
	cmd.Stderr = &errOut
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, false, err
	}
	if err := cmd.Start(); err != nil {
		return nil, false, err
	}
	data, readErr := io.ReadAll(io.LimitReader(pipe, limit+1))
	truncated := int64(len(data)) > limit
	if truncated {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if truncated {
		return nil, true, nil
	}
	if waitErr != nil {
		return nil, false, fmt.Errorf("%v: %s", waitErr, errOut.String())
	}
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return nil, false, readErr
	}
	return data, false, nil
}

// cappedBuffer captures only the first few kilobytes of output to prevent memory
// exhaustion from failing subprocesses.
type cappedBuffer struct {
	buf bytes.Buffer
}

const cappedKeepBytes = 4 << 10

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if room := cappedKeepBytes - c.buf.Len(); room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		_, _ = c.buf.Write(p)
	}
	return len(p), nil
}

func (c *cappedBuffer) String() string { return c.buf.String() }

// writeImageHeaders sets image headers, omitting Content-Disposition to avoid
// header injection from filenames.
func writeImageHeaders(w http.ResponseWriter, contentType string, size int64) {
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Content-Length", strconv.FormatInt(size, 10))
	h.Set("Cache-Control", "no-store")
}
