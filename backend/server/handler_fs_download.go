package server

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"mike/backend/fsutil"
	"mike/pkg/logging"
)

// archiveSuffix is what a folder or a selection is saved as.
const archiveSuffix = ".tar.gz"

// handleFSDownload streams one or more project paths as downloadable files. A single
// regular file is served directly; others are bundled into a .tar.gz archive named after
// the common parent folder. Query: project (required), task (optional), path (repeatable).
func (s *Server) handleFSDownload(w http.ResponseWriter, r *http.Request) {
	asked := r.URL.Query()["path"]
	if len(asked) == 0 {
		JSONError(w, http.StatusBadRequest, "path is required")
		return
	}
	if len(asked) > maxFSOpsBatch {
		JSONError(w, http.StatusBadRequest, "too many paths (max "+strconv.Itoa(maxFSOpsBatch)+")")
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
	paths, root, ok := s.downloadTargets(w, scope, asked)
	if !ok {
		return
	}
	infos, ok := s.downloadEntries(w, paths)
	if !ok {
		return
	}
	if !anyPresent(infos) {
		JSONError(w, http.StatusNotFound, safeNotFoundError())
		return
	}
	if len(paths) == 1 {
		switch {
		case infos[0].Mode().IsRegular():
			s.serveDownloadedFile(w, paths[0], infos[0])
			return
		case !infos[0].IsDir():
			JSONError(w, http.StatusBadRequest, "path is not a file or a folder")
			return
		}
	}
	s.serveDownloadedArchive(w, r, root, paths, downloadName(paths, root))
}

// anyPresent returns true if at least one file in the selection exists, allowing for stale listings.
func anyPresent(infos []os.FileInfo) bool {
	return slices.ContainsFunc(infos, func(info os.FileInfo) bool { return info != nil })
}

// downloadTargets jails all paths and ensures they share the common parent of the first
// path to ensure a valid archive structure.
func (s *Server) downloadTargets(w http.ResponseWriter, scope fsScope, asked []string) (paths []string, root string, ok bool) {
	paths = make([]string, 0, len(asked))
	for _, p := range asked {
		target, err := s.fsScopeTarget(scope, p)
		if err != nil {
			JSONError(w, http.StatusBadRequest, err.Error())
			return nil, "", false
		}
		if fsutil.HasGitSegment(target.path) {
			JSONError(w, http.StatusBadRequest, fsutil.ErrGitPath.Error())
			return nil, "", false
		}
		paths = append(paths, target.path)
	}
	root = filepath.Dir(paths[0])
	for _, p := range paths[1:] {
		rel, err := filepath.Rel(root, p)
		if err != nil || !fsutil.RelInsideRoot(rel) {
			JSONError(w, http.StatusBadRequest, "every path must sit in the first path's folder or below")
			return nil, "", false
		}
	}
	return paths, root, true
}

// downloadEntries stats each path, returning nil for non-existent files to handle stale selections.
func (s *Server) downloadEntries(w http.ResponseWriter, paths []string) ([]os.FileInfo, bool) {
	infos := make([]os.FileInfo, len(paths))
	for i, p := range paths {
		info, err := os.Lstat(p)
		switch {
		case errors.Is(err, os.ErrNotExist):
			continue
		case err != nil:
			Fault(w, "read file", err)
			return nil, false
		}
		infos[i] = info
	}
	return infos, true
}

// downloadName is the archive's name: a folder's own, or the folder a selection's rows sit in.
func downloadName(paths []string, root string) string {
	if len(paths) == 1 {
		return filepath.Base(paths[0]) + archiveSuffix
	}
	if parent := filepath.Base(root); parent != "." && parent != ".." {
		return parent + archiveSuffix
	}
	return "selection" + archiveSuffix
}

// serveDownloadedFile writes one file's bytes as a saved download, named as it is on disk.
func (s *Server) serveDownloadedFile(w http.ResponseWriter, hostPath string, before os.FileInfo) {
	f, info, ok := openJailedFile(w, hostPath, before)
	if !ok {
		return
	}
	defer f.Close()
	// octet-stream for everything: the browser saves this rather than showing it, so nothing is sniffed
	// and no repository file becomes markup on this origin.
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	w.Header().Set("Content-Disposition", attachmentDisposition(filepath.Base(hostPath)))
	w.Header().Set("Cache-Control", "no-store")
	if _, err := io.Copy(w, f); err != nil {
		// Headers are gone, and a half-sent file is usually a closed tab rather than a fault.
		logging.For(logging.HTTP).Debugf("download write failed: %v", err)
	}
}

// serveDownloadedArchive streams the paths as one archive. No Content-Length: gzip's size is not
// known before the walk.
func (s *Server) serveDownloadedArchive(w http.ResponseWriter, r *http.Request, root string, paths []string, name string) {
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", attachmentDisposition(name))
	w.Header().Set("Cache-Control", "no-store")
	if err := fsutil.WriteTarGz(r.Context(), w, root, paths); err != nil {
		// The response is already streaming, so there is no status left to answer with.
		logging.For(logging.HTTP).Warnf("download archive failed: %v", err)
	}
}

// attachmentDisposition creates the Content-Disposition header, encoding the filename to prevent header injection.
func attachmentDisposition(name string) string {
	full := sanitizeDownloadName(name)
	// sanitizeDownloadName has already removed what a header cannot carry; only non-ASCII is left here.
	ascii := strings.Map(func(r rune) rune {
		if r > 0x7e {
			return '_'
		}
		return r
	}, full)
	if ascii == full {
		return mime.FormatMediaType("attachment", map[string]string{"filename": full})
	}
	return mime.FormatMediaType("attachment", map[string]string{"filename": ascii}) + "; " +
		mime.FormatMediaType("attachment", map[string]string{"filename": full})
}

// sanitizeDownloadName sanitizes a filename by replacing separators and control characters with underscores and trimming trailing dots or spaces.
func sanitizeDownloadName(name string) string {
	mapped := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '\\' || r == '/' {
			return '_'
		}
		return r
	}, name)
	mapped = strings.TrimRight(mapped, ". ")
	if strings.Trim(mapped, ".") == "" {
		return "download"
	}
	if len(mapped) > fsutil.MaxRelSegmentLen {
		mapped = strings.ToValidUTF8(mapped[:fsutil.MaxRelSegmentLen], "")
	}
	return mapped
}
