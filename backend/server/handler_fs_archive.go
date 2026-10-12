package server

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"mike/backend/archive"
	"mike/backend/fsutil"
	"mike/backend/types"
	"mike/pkg/logging"
)

// The archive viewer's two answers: what one archive holds, and the text of one member of it. Both read
// the named file through the same jail as every other file endpoint (`docs/confinement.md`), and both
// unpack nothing.

const (
	// archiveWalkBudget bounds one listing or member read. Listing a compressed archive means walking
	// every byte of it, so the work one request can ask for is bounded by time.
	archiveWalkBudget = 5 * time.Second

	// archiveMaxEntries bounds the rows a listing collects. The deadline bounds the work, not the
	// accumulation: gzip packs tar's 512-byte headers well enough that a small file can hold an enormous
	// number of rows.
	archiveMaxEntries = 20000

	// archiveCommitBufferBytes bounds holding a committed archive in memory: the reader needs bytes it can
	// seek, so reading one from git means keeping the whole compressed file. A working-tree archive is read
	// through the open file and has no such ceiling.
	archiveCommitBufferBytes int64 = 20 << 20
)

// archiveCommitTooLargeMessage names the ceiling it describes.
var archiveCommitTooLargeMessage = "archive too large to read from a commit (max " +
	strconv.FormatInt(archiveCommitBufferBytes>>20, 10) + "MB)"

// archiveWalkTimeoutMessage is the answer when the walk ran out of budget before it read anything.
const archiveWalkTimeoutMessage = "the archive took too long to read"

// archiveRequestOptions is the caller's limits; the reader holds no number of its own.
func archiveRequestOptions() archive.Options {
	return archive.Options{
		MaxEntries:     archiveMaxEntries,
		MaxMemberBytes: maxFSReadBytes,
	}
}

// handleFSArchive answers GET /api/fs/archive with the members one archive holds.
//
// Query: project (required), task (which working tree), path, ref (optional — the committed object).
func (s *Server) handleFSArchive(w http.ResponseWriter, r *http.Request) {
	read, ok := s.archiveRequest(w, r)
	if !ok {
		return
	}
	defer read.close()

	listing, err := read.reader.List(read.ctx)
	if err != nil {
		failArchive(w, err)
		return
	}
	JSONResponse(w, http.StatusOK, listing)
}

// handleFSArchiveEntry answers GET /api/fs/archive/entry with the text of one member, in the shape
// GET /api/fs/read answers, so a file and a member are shown in the same words.
//
// Query: as GET /api/fs/archive, plus entry (the name the listing showed).
func (s *Server) handleFSArchiveEntry(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("entry")
	if name == "" {
		JSONError(w, http.StatusBadRequest, "entry is required")
		return
	}
	read, ok := s.archiveRequest(w, r)
	if !ok {
		return
	}
	defer read.close()

	body, err := read.reader.ReadEntry(read.ctx, name)
	if err != nil {
		failArchive(w, err)
		return
	}
	JSONResponse(w, http.StatusOK, fsReadResponse{Content: string(body)})
}

// archiveRead is one archive request settled into something readable, with what to release once the
// answer is out.
type archiveRead struct {
	reader *archive.Reader
	ctx    context.Context
	cancel context.CancelFunc
	file   *os.File
}

func (a *archiveRead) close() {
	if a.file != nil {
		_ = a.file.Close()
	}
	a.cancel()
}

// archiveRequest settles an archive request: the path it names, jailed and opened, recognised as one of
// the containers this reads. ok is false after the response has been written.
func (s *Server) archiveRequest(w http.ResponseWriter, r *http.Request) (*archiveRead, bool) {
	path := r.URL.Query().Get("path")
	if path == "" {
		JSONError(w, http.StatusBadRequest, "path is required")
		return nil, false
	}
	project, ok := s.requiredProject(w, r)
	if !ok {
		return nil, false
	}
	scope, ok := s.fsRequestScope(w, r, project)
	if !ok {
		return nil, false
	}
	// The explorer sends the path its listing handed over; the diff panel sends one relative to the
	// repository root. A relative name belongs to the tree the request named, and only the joined absolute
	// path reaches the jail below.
	if !filepath.IsAbs(path) {
		path = filepath.Join(scope.worktreePath, filepath.FromSlash(path))
	}
	target, err := s.fsScopeTarget(scope, path)
	if err != nil {
		JSONError(w, http.StatusBadRequest, err.Error())
		return nil, false
	}
	if fsutil.HasGitSegment(target.path) {
		JSONError(w, http.StatusBadRequest, fsutil.ErrGitPath.Error())
		return nil, false
	}

	ctx, cancel := context.WithTimeout(r.Context(), archiveWalkBudget)
	src, file, ok := s.archiveSource(w, r, scope, target, ctx)
	if !ok {
		cancel()
		if file != nil {
			_ = file.Close()
		}
		return nil, false
	}
	reader, err := archive.Open(ctx, src, archiveRequestOptions())
	if err != nil {
		cancel()
		if file != nil {
			_ = file.Close()
		}
		failArchive(w, err)
		return nil, false
	}
	return &archiveRead{reader: reader, ctx: ctx, cancel: cancel, file: file}, true
}

// archiveSource is where the archive's bytes come from: the file on disk, or the object a commit holds. A
// directory, a FIFO and a name that moved after the containment check are refused here as /api/fs/read
// refuses them. ok is false once the response is written.
func (s *Server) archiveSource(w http.ResponseWriter, r *http.Request, scope fsScope, target fsTarget, ctx context.Context) (*archive.Source, *os.File, bool) {
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		before, err := os.Lstat(target.path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				JSONError(w, http.StatusNotFound, safeNotFoundError())
			} else {
				Fail(w, err, "read archive", safeNotFoundError())
			}
			return nil, nil, false
		}
		if before.IsDir() {
			JSONError(w, http.StatusBadRequest, "path is a directory")
			return nil, nil, false
		}
		f, _, opened := openJailedFile(w, target.path, before)
		if !opened {
			return nil, nil, false
		}
		return archive.NewSeekable(f, before.Size()), f, true
	}

	if err := types.ValidateGitRef(ref); err != nil {
		JSONError(w, http.StatusBadRequest, "invalid ref: "+err.Error())
		return nil, nil, false
	}
	rel, ok := committedArchiveRel(w, scope, target)
	if !ok {
		return nil, nil, false
	}
	// Worktrees share the project's object database, so the committed object is read from the project
	// checkout: a task whose worktree was deleted still answers.
	spec := ref + ":" + filepath.ToSlash(rel)
	repo := scope.project.Path
	if size, ok := gitBlobSize(gitInDir(ctx, repo), spec); ok && size > archiveCommitBufferBytes {
		JSONError(w, http.StatusRequestEntityTooLarge, archiveCommitTooLargeMessage)
		return nil, nil, false
	}
	data, truncated, err := gitBlobSpool(ctx, repo, spec, archiveCommitBufferBytes)
	switch {
	case truncated:
		JSONError(w, http.StatusRequestEntityTooLarge, archiveCommitTooLargeMessage)
		return nil, nil, false
	case err != nil:
		logging.For(logging.HTTP).Debugf("committed archive read failed: %v", err)
		JSONError(w, http.StatusNotFound, "file not found at ref "+ref)
		return nil, nil, false
	}
	return archive.NewSeekable(bytes.NewReader(data), int64(len(data))), nil, true
}

// committedArchiveRel is the path inside the repository for a jailed file: the tree the request named
// first, then the project checkout, since a request naming a task may still name a file in the checkout.
func committedArchiveRel(w http.ResponseWriter, scope fsScope, target fsTarget) (string, bool) {
	for _, root := range []string{scope.worktreePath, scope.project.Path} {
		if root == "" {
			continue
		}
		rel, err := filepath.Rel(root, target.path)
		if err == nil && fsutil.RelInsideRoot(rel) && rel != "." {
			return rel, true
		}
	}
	JSONError(w, http.StatusBadRequest, fsutil.ErrOutside.Error())
	return "", false
}

// failArchive maps the refusals an archive read produces onto a status and MIKE's own sentence. Each one
// is something the person can act on, and no archive byte or server path reaches the browser.
func failArchive(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, archive.ErrNotArchive):
		JSONError(w, http.StatusUnsupportedMediaType, archive.ErrNotArchive.Error())
	case errors.Is(err, archive.ErrNoEntry):
		JSONError(w, http.StatusNotFound, archive.ErrNoEntry.Error())
	case errors.Is(err, archive.ErrEntryTooLarge):
		JSONError(w, http.StatusRequestEntityTooLarge, fsTooLargeMessage)
	case errors.Is(err, archive.ErrBinaryEntry):
		// 415 rather than 404: the member is there, and it is not text.
		JSONError(w, http.StatusUnsupportedMediaType, archive.ErrBinaryEntry.Error())
	case errors.Is(err, context.DeadlineExceeded):
		logging.For(logging.HTTP).Warnf("archive read ran out of budget: %v", err)
		JSONError(w, http.StatusGatewayTimeout, archiveWalkTimeoutMessage)
	default:
		Fault(w, "read archive", err)
	}
}
