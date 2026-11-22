package extensions

// The development namespace holds a custom extension's own repository. It is a sibling of the installed
// extensions, not one of them: nothing installs from it, and deleting a row never deletes it. A task's
// uncommitted bytes need a served directory of their own because a worktree is outside the root the
// browser can reach. The file server refuses every dot-prefixed segment — see noEscapeFS in
// backend/server/routes.go.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"mike/backend/types"
	"mike/pkg/logging"
)

const (
	// DevInstallSource is the install_source of a row created beside an authoring repository.
	DevInstallSource = "dev"
	// DevNamespace is the one directory under the extensions root that holds authoring repositories.
	DevNamespace = "_dev"
	// devStagingNamespace holds a loaded copy of a task's uncommitted bytes, one directory per
	// (repository, task). Being inside DevNamespace is what excludes it from installed-extension reads.
	devStagingNamespace = "_try"
	// devSlugMaxLen caps the directory name a person's extension name becomes.
	devSlugMaxLen = 40
	// devAuthor is the author every development row carries, not a registry namespace, so the name alone
	// decides who holds it.
	devAuthor = "operator"
)

func (e *Extension) IsDev() bool { return e != nil && e.InstallSource == DevInstallSource }

// IsLoaded reports whether a dev row's bytes are the running ones. An unloaded row is inert — no
// manifest to register, nothing to serve — so a reader of enabled rows must skip it.
func (e *Extension) IsLoaded() bool {
	return e != nil && (!e.IsDev() || e.LoadedDir != "")
}

// DevSlug is the directory name an extension name gets, through the one slugifier this repo has. Its
// output is letters, digits and inner dashes, so no name can ever be slugged into a directory the server
// owns.
func DevSlug(name string) (string, error) {
	slug := types.Slugify(strings.TrimSpace(name), devSlugMaxLen)
	if reason := unsafeDirName(slug); reason != "" {
		return "", fmt.Errorf("%q does not name a directory: %s", name, reason)
	}
	if slug == "" {
		return "", fmt.Errorf("%q does not contain any characters a directory name can use", name)
	}
	return slug, nil
}

// DevRepoDir is the repository directory of a development extension: <extensions>/_dev/<slug>. The slug
// comes from a stored row, never from a request.
func DevRepoDir(extensionsDir, slug string) string {
	return filepath.Join(extensionsDir, DevNamespace, slug)
}

// DevStagingDir is where one task's loaded copy lives: <extensions>/_dev/_try/<slug>/<taskID>. The task
// id is an identifier this server generated, so it is checked as one rather than trusted as a path.
func DevStagingDir(extensionsDir, slug, taskID string) (string, error) {
	if taskID == "" || !isDirSafeID(taskID) {
		return "", fmt.Errorf("%q is not a task id", taskID)
	}
	return filepath.Join(extensionsDir, DevNamespace, devStagingNamespace, slug, taskID), nil
}

// DevRoot is the namespace every development path sits under.
func DevRoot(extensionsDir string) string {
	return filepath.Join(extensionsDir, DevNamespace)
}

// underDevRoot reports whether path is strictly beneath the development namespace. A path that only looks
// like it is — the root itself, or one that climbs out — is not.
func underDevRoot(extensionsDir, path string) bool {
	return insideDir(DevRoot(extensionsDir), path)
}

// isDevSegment reports whether a path relative to the extensions root is under the reserved namespace,
// which is what keeps a development repository out of the installable listing.
func isDevSegment(rel string) bool {
	slash := filepath.ToSlash(rel)
	if i := strings.Index(slash, "/"); i >= 0 {
		slash = slash[:i]
	}
	return slash == DevNamespace
}

// isStagingSegment reports whether path sits in the staging tree, which no project's directory ever is.
func isStagingSegment(extensionsDir, path string) bool {
	rel, err := filepath.Rel(DevRoot(extensionsDir), path)
	if err != nil {
		return false
	}
	slash := filepath.ToSlash(rel)
	return slash == devStagingNamespace || strings.HasPrefix(slash, devStagingNamespace+"/")
}

// isDirSafeID reports whether s can be a path segment this server generated. Anything a person could
// type — a separator, a dot, a leading dash — is a reason to refuse.
func isDirSafeID(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return !strings.HasPrefix(s, "-") && !strings.Contains(s, "..")
}

// devOrExtensionDir is the one answer to "where do this row's files live": the directory a development
// row loaded, and extensionDir's derivation for every other row. Every consumer that opens an
// extension's files goes through it, so the browser's version and the server's are never two things.
func devOrExtensionDir(extensionsDir string, ext *Extension) string {
	if ext == nil {
		return ""
	}
	if ext.IsDev() && ext.LoadedDir != "" {
		return filepath.Join(extensionsDir, filepath.FromSlash(ext.LoadedDir))
	}
	return extensionDir(extensionsDir, ext.Author, ext.Name)
}

// DevStagingUnderRoot is the loaded copy of a task's bytes, relative to the extensions root, or "" when
// the row loads the repository itself — which the server must not remove. LoadedDir is written only by
// the load path, so its two shapes are the whole contract.
func (e *Extension) DevStagingUnderRoot() string {
	if e == nil || !e.IsDev() || e.LoadedDir == "" {
		return ""
	}
	slash := path.Clean("/" + filepath.ToSlash(e.LoadedDir))
	if !strings.HasPrefix(slash, "/"+DevNamespace+"/"+devStagingNamespace+"/") {
		return ""
	}
	return strings.TrimPrefix(slash, "/")
}

// removeDevStaging deletes the copy this row loaded. An unloaded row copied nothing.
func (h *ExtensionHandler) removeDevStaging(ext *Extension) string {
	rel := ext.DevStagingUnderRoot()
	if rel == "" {
		return ""
	}
	root := h.config.ExtensionsDirOrDefault()
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Sprintf("the extensions directory %q could not be resolved: %v", root, err)
	}
	return removeInstalledDir(ext.ID, absRoot, filepath.Join(absRoot, filepath.FromSlash(rel)))
}

// DeleteDevRows removes the development rows naming one project and reports what it could not remove. It
// removes no file. A non-development row naming the project is not this project's to delete.
func (h *ExtensionHandler) DeleteDevRows(projectID string) string {
	if strings.TrimSpace(projectID) == "" {
		return ""
	}
	exts, err := h.store.GetByType("")
	if err != nil {
		return fmt.Sprintf("the development rows of project %s could not be listed: %v", projectID, err)
	}
	var problems []string
	deleted := false
	for i := range exts {
		ext := &exts[i]
		if !ext.IsDev() || ext.ProjectID != projectID {
			continue
		}
		leftover, rowErr := h.deleteExtension(ext.ID, ext)
		if rowErr != nil {
			problems = append(problems, fmt.Sprintf("row %s could not be deleted: %v", ext.ID, rowErr))
			continue
		}
		deleted = true
		if leftover != "" {
			problems = append(problems, leftover)
		}
	}
	// The browser's copy of the list carries these rows.
	if deleted && h.onExtensionChanged != nil {
		h.onExtensionChanged()
	}
	return strings.Join(problems, "; ")
}

// RemoveDevRepo deletes the repository directory a create made and reports what it could not remove. A path
// not strictly beneath the namespace removes nothing.
func (h *ExtensionHandler) RemoveDevRepo(projectPath string) string {
	if strings.TrimSpace(projectPath) == "" {
		return ""
	}
	root, err := filepath.Abs(h.config.ExtensionsDirOrDefault())
	if err != nil {
		return fmt.Sprintf("the extensions directory %q could not be resolved: %v", h.config.ExtensionsDirOrDefault(), err)
	}
	abs, err := filepath.Abs(projectPath)
	if err != nil {
		return fmt.Sprintf("%q could not be resolved: %v", projectPath, err)
	}
	if !underDevRoot(root, abs) {
		return ""
	}
	if isStagingSegment(root, abs) {
		// A loaded copy belongs to the row that loaded it, not to the project that holds the repository.
		return ""
	}
	if resolvedRoot, err := filepath.EvalSymlinks(root); err == nil {
		if resolved, err := filepath.EvalSymlinks(abs); err == nil && !underDevRoot(resolvedRoot, resolved) {
			return fmt.Sprintf("refused to remove %s: it resolves to %s, outside the development directory", abs, resolved)
		}
	}
	if err := os.RemoveAll(abs); err != nil && !errors.Is(err, fs.ErrNotExist) {
		logging.For(logging.Extensions).Warnf("LEFTOVER FILES: development repository %s could not be removed: %v", abs, err)
		return fmt.Sprintf("%s could not be removed: %v", abs, err)
	}
	devRoot := DevRoot(root)
	if entries, err := os.ReadDir(devRoot); err == nil && len(entries) == 0 {
		_ = os.Remove(devRoot)
	}
	logging.For(logging.Extensions).Infof("removed the development repository %s created by a failed create", abs)
	return ""
}
