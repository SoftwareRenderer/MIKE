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

// rawImageTypes is the whole permission of GET /api/fs/raw: a name is served if and only if it is a
// key, and its value is the Content-Type. SVG is absent because it is markup, and an <img> pointing at
// the app's own origin would load it. frontend/src/lib/utils/diff-image.ts mirrors this set.
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

// maxRawFileBytes is the upload cap, not maxFSReadBytes: this route answers for an image a person put
// in the repository, so it is bounded by what the server will write into a worktree.
const maxRawFileBytes int64 = exec.MaxUploadBytes

var rawTooLargeMessage = fmt.Sprintf("file too large (max %dMB)", maxRawFileBytes/(1<<20))

// handleFSRaw streams one image out of a project. GET /api/fs/read cannot: it answers JSON, and a JSON
// string of a PNG is the PNG with every invalid byte replaced.
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

// repoRelativePath takes the path shape the diff surfaces hold and returns it for the filesystem. An
// absolute path is refused rather than translated, so the route never guesses whether it was sent a host
// path or a container path.
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

// looksLikeDrivePath catches the Windows spelling of an absolute path, which filepath.IsAbs answers
// only on Windows. A colon not followed by a separator is an ordinary filename.
func looksLikeDrivePath(p string) bool {
	return len(p) > 2 && p[1] == ':' && (p[2] == '/' || p[2] == '\\')
}

// refusePathJail answers a path that stopped being the one the jail checked. The name goes to the
// log and the browser gets the sentence validateFSPath returns, so a swap attempt cannot be used
// to read a host path back out of an error.
func refusePathJail(w http.ResponseWriter, hostPath string) {
	logging.For(logging.HTTP).Warnf("raw image refused, path changed after containment: %s", hostPath)
	JSONError(w, http.StatusBadRequest, fsutil.ErrOutside.Error())
}

// serveWorktreeImage writes the file on disk, refusing on the stat before reading anything.
//
// The validateFSPath that jailed hostPath resolved the path a moment earlier, and the task owns
// that directory, so a symlink or a different file can appear there in the window. The lstat, the
// regular-file rule and the os.SameFile comparison below are what notice.
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
	if before.Mode()&os.ModeSymlink != 0 {
		refusePathJail(w, hostPath)
		return
	}
	if !before.Mode().IsRegular() {
		// A FIFO or a device would block the request on whatever a task chooses to write into it.
		if before.IsDir() {
			JSONError(w, http.StatusBadRequest, "path is a directory")
			return
		}
		JSONError(w, http.StatusBadRequest, "path is not a regular file")
		return
	}
	f, err := os.Open(hostPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			JSONError(w, http.StatusNotFound, safeNotFoundError())
		} else {
			logging.For(logging.HTTP).Errorf("raw image open failed: %v", err)
			JSONError(w, http.StatusInternalServerError, "failed to read file")
		}
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		Fault(w, "stat file", err)
		return
	}
	if !os.SameFile(before, info) {
		refusePathJail(w, hostPath)
		return
	}
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

// gitInDir runs git in a directory that no task owns, in the shape gitBlobSize asks for.
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

// gitBlobBytes captures one object's bytes. `cat-file blob` is plumbing and writes the object and
// nothing else, which is what makes the result safe to hand to an <img>.
func gitBlobBytes(ctx context.Context, dir, spec string) ([]byte, bool, error) {
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
	data, readErr := io.ReadAll(io.LimitReader(pipe, maxRawFileBytes+1))
	truncated := int64(len(data)) > maxRawFileBytes
	if truncated {
		// Draining the pipe would be the read that was just refused, so stop the process instead.
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

// cappedBuffer keeps the first cappedKeepBytes written to it, so a failing subprocess cannot make the
// server hold what it printed. Write reports the full length: a short count stops os/exec's copier.
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

// writeImageHeaders writes no Content-Disposition: inline is the default, and putting the file's name
// into a header would make a crafted name a header writer.
func writeImageHeaders(w http.ResponseWriter, contentType string, size int64) {
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Content-Length", strconv.FormatInt(size, 10))
	h.Set("Cache-Control", "no-store")
}
