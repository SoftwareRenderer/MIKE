package server

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"mike/backend/exec"
	"mike/backend/extensions"
	"mike/backend/fsutil"
	"mike/backend/types"
	"mike/pkg/logging"
)

// The three answers one file read gives, named by the request's kind; an absent kind is text. The kind
// picks a policy and never widens one: each carries its own permission and its own ceiling, and the
// shared bottom half holds no permission of its own to be widened.
const (
	fsKindText  = "text"
	fsKindImage = "image"
	fsKindBytes = "bytes"
)

// imageTypes is the whole permission for the picture kind: a name outside this map is refused before the
// jail is asked anything. SVG is excluded because its bytes are markup.
// frontend/src/lib/utils/diff-image.ts mirrors this set.
var imageTypes = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".bmp":  "image/bmp",
	".ico":  "image/x-icon",
	".avif": "image/avif",
}

// maxImageBytes is the upload ceiling: what a person may commit into a repository is what this route may
// hand back.
const maxImageBytes int64 = exec.MaxUploadBytes

var imageTooLargeMessage = fmt.Sprintf("file too large (max %dMB)", maxImageBytes/(1<<20))

// maxExtensionReadBytes is a render ceiling, not the upload ceiling: a file a person can commit is far
// larger than anything a tab should hold while it renders one.
const maxExtensionReadBytes int64 = 8 << 20

// extensionReadContentType is what the bytes kind answers with. The bytes go to an extension's own
// parser, never to a browser subresource, so the core names no type and nothing a request carries picks
// one.
const extensionReadContentType = "application/octet-stream"

var extensionReadTooLarge = "file too large (max " + strconv.FormatInt(maxExtensionReadBytes/(1<<20), 10) + "MB)"

// byteReadCacheCommitted is forever, because a committed read's validator is its object id and an object
// id never names different bytes.
const byteReadCacheCommitted = "private, max-age=31536000, immutable"

// byteReadCacheWorking is for the working tree, which changes under the reader.
const byteReadCacheWorking = "no-store"

// handleFSRead answers one project file three ways: the text an editor shows, the bytes of a picture, and
// the bytes an extension renders. Every kind asks the same questions in the same order — the path shape,
// who may ask for this name at all, which tree, the jail, the size, then the bytes — and differs only in
// what it emits.
//
// Query: project (required), path (required, relative to the repository root), task (which working tree),
// ref (optional — the committed object instead of the file on disk), kind (text | image | bytes).
//
// The path is repository-relative for every kind, which is what lets one containment rule answer all
// three; the tree a read means comes from ?task=, never from the path.
func (s *Server) handleFSRead(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("kind")
	if kind == "" {
		kind = fsKindText
	}
	rel, err := repoRelativePath(r.URL.Query().Get("path"))
	if err != nil {
		JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	project, ok := s.requiredProject(w, r)
	if !ok {
		return
	}
	// What a kind may answer at all, from the name and the stored rows — before the jail, so an absent or
	// oversized file cannot be probed for what the server would have said about it.
	var contentType string
	switch kind {
	case fsKindText:
	case fsKindImage:
		contentType, ok = imageTypes[strings.ToLower(filepath.Ext(rel))]
		if !ok {
			JSONError(w, http.StatusUnsupportedMediaType, "not a viewable image type")
			return
		}
	case fsKindBytes:
		if !s.requireExtensionReadSuffix(w, rel) {
			return
		}
		contentType = extensionReadContentType
	default:
		JSONError(w, http.StatusBadRequest, `kind must be "text", "image" or "bytes"`)
		return
	}
	// The committed half never touches a filesystem, so the relative name is the only place a .git row can
	// be seen at all; the working-tree half checks the resolved path in fsOpenWorktree.
	if !requireNoGitRel(w, rel) {
		return
	}
	scope, ok := s.fsRequestScope(w, r, project)
	if !ok {
		return
	}
	ref := r.URL.Query().Get("ref")
	if ref != "" {
		if err := types.ValidateGitRef(ref); err != nil {
			JSONError(w, http.StatusBadRequest, "invalid ref: "+err.Error())
			return
		}
	}

	switch kind {
	case fsKindText:
		s.serveFSText(w, r, scope, rel, ref)
	case fsKindImage:
		s.serveFSBytes(w, r, scope, rel, ref, contentType, maxImageBytes, imageTooLargeMessage)
	default:
		s.serveFSBytes(w, r, scope, rel, ref, contentType, maxExtensionReadBytes, extensionReadTooLarge)
	}
}

// serveFSText answers the editor's buffer. The whole file is held before the status is chosen, because
// two of the refusals are decided by the bytes: 413 when the read ran past the cap the stat promised, and
// 415 when the bytes say they are not text. A status already sent cannot be retracted, so this half does
// not stream — see serveFSBytes, where nothing the bytes can say changes the status.
func (s *Server) serveFSText(w http.ResponseWriter, r *http.Request, scope fsScope, rel, ref string) {
	var content []byte
	var markers map[int][]string
	if ref == "" {
		f, info, ok := s.fsOpenWorktree(w, scope, rel, maxFSReadBytes, fsTooLargeMessage)
		if !ok {
			return
		}
		defer f.Close()
		data, readErr := readSized(f, info.Size(), maxFSReadBytes)
		if readErr != nil {
			logging.For(logging.HTTP).Errorf("fs read failed: %v", readErr)
			JSONError(w, http.StatusInternalServerError, "failed to read file")
			return
		}
		if int64(len(data)) > maxFSReadBytes {
			JSONError(w, http.StatusRequestEntityTooLarge, fsTooLargeMessage)
			return
		}
		content = data
		markers = computeGutterMarkers(s, scope.worktreePath, rel, r.URL.Query().Get("task"))
	} else {
		object, ok := s.fsBeginCommitted(w, r, scope, ref, rel, maxFSReadBytes, fsTooLargeMessage)
		if !ok {
			return
		}
		blob, ok := fsHeldObject(w, r, scope, object, maxFSReadBytes, fsTooLargeMessage)
		if !ok {
			return
		}
		content = blob
	}
	// Unlike the cap above, this cannot run before the read: only the bytes say what they are.
	if !fsutil.IsText(content) {
		JSONError(w, http.StatusUnsupportedMediaType, fsutil.ErrNotText.Error())
		return
	}
	JSONResponse(w, http.StatusOK, fsReadResponse{
		Content:       string(content),
		GutterMarkers: markers,
	})
}

// serveFSBytes answers one file's own bytes, typed by the kind that asked. Nothing here waits on the
// bytes: the type came from the name, the length from the stat or the object probe, and the refusal for an
// oversized file from the same probe — so the working tree streams from the opened file and a committed
// object streams out of git without either being held in memory first.
func (s *Server) serveFSBytes(w http.ResponseWriter, r *http.Request, scope fsScope, rel, ref, contentType string, ceiling int64, tooLarge string) {
	if ref == "" {
		f, info, ok := s.fsOpenWorktree(w, scope, rel, ceiling, tooLarge)
		if !ok {
			return
		}
		defer f.Close()
		writeFileBodyHeaders(w, contentType, info.Size(), byteReadCacheWorking)
		// Exactly the declared length: the file is the one that stat measured (fsOpenWorktree proved it),
		// and writing past Content-Length is a response net/http has to discard and complain about.
		if n, err := io.CopyN(w, f, info.Size()); err != nil && !errors.Is(err, io.EOF) {
			logHalfSentBody(n, err)
		}
		return
	}

	object, ok := s.fsBeginCommitted(w, r, scope, ref, rel, ceiling, tooLarge)
	if !ok {
		return
	}
	// A pinned object id cannot change under the read, so its length is trustworthy and its bytes go
	// straight out of git. Anything else — a name the tree would not resolve, or a probe git could not
	// answer — is held, because the only way a refusal stays a refusal is a status, and a status cannot
	// follow a sent 200.
	if !object.sized || object.id == "" {
		data, ok := fsHeldObject(w, r, scope, object, ceiling, tooLarge)
		if !ok {
			return
		}
		writeFileBodyHeaders(w, contentType, int64(len(data)), objectCache(object.id))
		_, _ = w.Write(data)
		return
	}

	body, wait, err := gitBlobStream(r.Context(), scope.project.Path, object.spec)
	if err != nil {
		logCommittedReadFailed(err)
		JSONError(w, http.StatusNotFound, safeNotFoundError())
		return
	}
	writeFileBodyHeaders(w, contentType, object.size, objectCache(object.id))
	// One byte past the probe's answer is what proves git kept its word: the object id is pinned, so an
	// object longer than its own size is a git fault, and the stream is stopped rather than drained.
	if n, err := io.CopyN(w, body, object.size+1); err != nil && !errors.Is(err, io.EOF) {
		logHalfSentBody(n, err)
	} else if n > object.size {
		logging.For(logging.HTTP).Warnf("git object %s promised %d B and sent more", object.spec, object.size)
	}
	if waitErr := wait(); waitErr != nil {
		// The status is already sent; a refusal from git here leaves a short body the client cannot
		// revalidate away, because the validator it was given names no bytes.
		logging.For(logging.HTTP).Warnf("git cat-file blob %s exited early: %v", object.spec, waitErr)
	}
}

// logHalfSentBody reports a body write that failed after the status went out. The headers are gone, and a
// half-sent body is usually a closed tab rather than a fault.
func logHalfSentBody(n int64, err error) {
	logging.For(logging.HTTP).Debugf("file body write failed after %d B: %v", n, err)
}

// logCommittedReadFailed reports git's own refusal to name the bytes one read asked for.
func logCommittedReadFailed(err error) {
	logging.For(logging.HTTP).Debugf("committed file read failed: %v", err)
}

// fsOpenWorktree opens the working-tree file one read means, jailed and under the caller's ceiling, at the
// name the containment check named. ok is false after the response is written; the caller closes the file.
func (s *Server) fsOpenWorktree(w http.ResponseWriter, scope fsScope, rel string, ceiling int64, tooLarge string) (*os.File, os.FileInfo, bool) {
	target, err := s.fsScopeTarget(scope, filepath.Join(scope.worktreePath, rel))
	if err != nil {
		JSONError(w, http.StatusBadRequest, err.Error())
		return nil, nil, false
	}
	if !requireNoGitSegment(w, target) {
		return nil, nil, false
	}
	before, statErr := os.Lstat(target.path)
	if statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			JSONError(w, http.StatusNotFound, safeNotFoundError())
		} else {
			logging.For(logging.HTTP).Errorf("file read stat failed: %v", statErr)
			JSONError(w, http.StatusInternalServerError, "failed to read file")
		}
		return nil, nil, false
	}
	if before.IsDir() {
		JSONError(w, http.StatusBadRequest, "path is a directory")
		return nil, nil, false
	}
	// Before the open, so an oversized file costs a stat and not the read it refused.
	if before.Size() > ceiling {
		JSONError(w, http.StatusRequestEntityTooLarge, tooLarge)
		return nil, nil, false
	}
	f, info, opened := openJailedFile(w, target.path, before)
	if !opened {
		return nil, nil, false
	}
	if info.Size() > ceiling {
		_ = f.Close()
		JSONError(w, http.StatusRequestEntityTooLarge, tooLarge)
		return nil, nil, false
	}
	return f, info, true
}

// fsObject is the committed half of one read: what to ask git for, how long git says it is, and whether
// the answer can be cached. The object database is the project checkout's even for a task's file —
// worktrees of a project share it — which is what lets a merged diff panel still ask for the committed
// file of a task whose tree is gone.
type fsObject struct {
	// spec is what `git cat-file` is asked for, "<object or name>:<path>".
	spec string
	// id is the object's own id when the tree named one, and empty when the ref named only a name.
	id string
	// size is git's answer, trustworthy only when sized is true.
	size  int64
	sized bool
}

// fsBeginCommitted answers what the committed half asks before any byte is read: which object the request's
// ref means, whether the client already holds it, and whether the object is over the caller's ceiling. The
// size probe is asked of the same repository the read will be, and its answer both refuses an oversized
// object and sizes the buffer the object is read into. An unresolvable ref is kept as written, so a name git
// itself can still answer (a tag in the checkout, say) reads; it just earns no validator. ok is false after
// the response is written.
func (s *Server) fsBeginCommitted(w http.ResponseWriter, r *http.Request, scope fsScope, ref, rel string, ceiling int64, tooLarge string) (fsObject, bool) {
	// Only a request that named a task has its ref resolved. HEAD belongs to a worktree, so the task's
	// tree is the one that answers — and a task's tree is pinned, which is what lets its object id stand
	// behind a cached answer. No task means the checkout's own HEAD, which moves every time the person
	// commits: keeping that name as written is what stops a browser holding a year of bytes the commit no
	// longer holds.
	tree := ""
	if r.URL.Query().Get("task") != "" {
		tree = scope.worktreePath
	}
	object := refWhere(r.Context(), tree, ref)
	id := objectIDOf(object)
	if !revalidateObject(w, r, id) {
		return fsObject{}, false
	}
	spec := object + ":" + filepath.ToSlash(rel)
	size, sized := fsBlobSize(r.Context(), scope.project.Path, spec)
	if sized && size > ceiling {
		JSONError(w, http.StatusRequestEntityTooLarge, tooLarge)
		return fsObject{}, false
	}
	return fsObject{spec: spec, id: id, size: size, sized: sized}, true
}

// fsHeldObject reads a committed object into memory under the caller's ceiling, and answers the request
// when the bytes ran past it or git refused to name them at all. ok is false after the response is written.
func fsHeldObject(w http.ResponseWriter, r *http.Request, scope fsScope, object fsObject, ceiling int64, tooLarge string) ([]byte, bool) {
	data, truncated, err := gitBlobSpool(r.Context(), scope.project.Path, object.spec, ceiling, blobHint(object.size, object.sized))
	if truncated {
		JSONError(w, http.StatusRequestEntityTooLarge, tooLarge)
		return nil, false
	}
	if err != nil {
		logCommittedReadFailed(err)
		JSONError(w, http.StatusNotFound, safeNotFoundError())
		return nil, false
	}
	return data, true
}

// objectIDOf is the object id when the string is one, and "" when it is a name somebody hoped would
// resolve. Only an id can stand behind a cached answer, and only an id cannot change under a read.
func objectIDOf(object string) string {
	if types.ValidateCommitHash(object) != nil {
		return ""
	}
	return object
}

// objectCache is the lifetime an answer about this object can claim: forever for a pinned id, nothing for
// a name, because a name like HEAD answers differently tomorrow.
func objectCache(id string) string {
	if id == "" {
		return byteReadCacheWorking
	}
	return byteReadCacheCommitted
}

// revalidateObject answers the request with a 304 when its validator is the object id and the client
// already holds that object. ok is false when the answer is already written.
func revalidateObject(w http.ResponseWriter, r *http.Request, id string) bool {
	if id == "" {
		w.Header().Set("Cache-Control", objectCache(id))
		return true
	}
	validator := `"` + id + `"`
	header := w.Header()
	if etagMatches(validator, r.Header.Get("If-None-Match")) {
		header.Set("ETag", validator)
		header.Set("Cache-Control", objectCache(id))
		w.WriteHeader(http.StatusNotModified)
		return false
	}
	header.Set("ETag", validator)
	header.Set("Cache-Control", objectCache(id))
	return true
}

// writeFileBodyHeaders sets what a byte answer carries before its first byte: the type the kind chose, the
// exact length, and the lifetime that length can be trusted for.
func writeFileBodyHeaders(w http.ResponseWriter, contentType string, size int64, cache string) {
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Content-Length", strconv.FormatInt(size, 10))
	h.Set("Cache-Control", cache)
	// No Content-Disposition: it is how a handler names a file to save, and a filename from a path the
	// request supplied is header injection waiting for a newline.
}

// requireNoGitRel refuses a repository-relative name that runs through .git. ok is false after the
// response is written.
func requireNoGitRel(w http.ResponseWriter, rel string) bool {
	if fsutil.HasGitSegment(rel) {
		JSONError(w, http.StatusBadRequest, fsutil.ErrGitPath.Error())
		return false
	}
	return true
}

// requireExtensionReadSuffix answers the request unless an enabled extension declared this path's
// suffix. "Readable by the file endpoints" and "some extension renders it" are different claims, and only
// the second is a grant; the declaration reads back from the stored row, never from the request.
func (s *Server) requireExtensionReadSuffix(w http.ResponseWriter, rel string) bool {
	suffix := extensions.FileSuffix(rel)
	if suffix == "" {
		JSONError(w, http.StatusNotFound, "file has no suffix to declare")
		return false
	}
	installed, err := s.extStore.GetByType(extensions.ExtensionTypeUI)
	if err != nil {
		// Refuse rather than guess, and log the cause: a bare 503 explains nothing to an operator who just
		// installed a viewer.
		logging.For(logging.HTTP).Errorf("extension byte read refused, installed extensions unreadable: %v", err)
		JSONError(w, http.StatusServiceUnavailable, "installed extensions could not be read")
		return false
	}
	for _, ext := range installed {
		ui := ext.Manifest.Capabilities.GetUI()
		if !ext.Enabled || ui == nil {
			continue
		}
		for _, declared := range ui.FileSuffixes {
			if declared == suffix {
				return true
			}
		}
	}
	JSONError(w, http.StatusForbidden, "no enabled extension declares ."+suffix)
	return false
}
