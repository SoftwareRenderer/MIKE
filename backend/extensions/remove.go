package extensions

// An extension's directory under the extensions root is not a cache that can be left behind — it is
// what the running system reads. UI code is served out of it, an agent adapter's WASM and binary are
// loaded from it, and GET /api/extensions/local lists it as installable. A row that is gone with its
// files still present is a deleted extension that keeps executing, and reappears as installable with
// its old manifest. Updates never had this problem: CheckoutExtension removes the destination first.

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// removeExtensionFiles deletes a deleted extension's materialized directory and returns a sentence
// naming the path when it could not. It returns "" when the files are gone, including "there were none".
func (h *ExtensionHandler) removeExtensionFiles(ext *Extension) string {
	if ext == nil {
		return "the extension record is missing, so no files could be located"
	}
	return h.removeMaterializedDir(ext.ID, ext.Author, ext.Name)
}

// removeMaterializedDir deletes one extension's directory under the extensions root, deriving the path
// through extensionDir from author and name - the only derivation every reader uses, and both names come
// from a third-party manifest. Used by the delete endpoint (a row exists) and by a failed install (none
// was written, and the files must not outlive the attempt).
func (h *ExtensionHandler) removeMaterializedDir(extID, author, name string) string {
	if reason := unsafeDirName(name); reason != "" {
		return fmt.Sprintf("refused to remove files for %s: %s", extID, reason)
	}
	root := h.config.ExtensionsDirOrDefault()
	if root == "" {
		return "no extensions directory is configured, so nothing was removed"
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Sprintf("the extensions directory %q could not be resolved: %v", root, err)
	}
	return removeInstalledDir(extID, absRoot, extensionDir(absRoot, author, name))
}

// removeInstalledDir removes one derived directory, and the author folder once it is empty.
func removeInstalledDir(extID, absRoot, dir string) string {
	// Never the root, never outside it: checked lexically and again with symlinks resolved, because a
	// symlinked extensions/<author> would otherwise delete whatever it points at.
	if !insideDir(absRoot, dir) {
		return fmt.Sprintf("refused to remove %s: it is not inside the extensions directory %s", dir, absRoot)
	}
	if resolvedRoot, err := filepath.EvalSymlinks(absRoot); err == nil {
		if resolvedDir, err := filepath.EvalSymlinks(dir); err == nil && !insideDir(resolvedRoot, resolvedDir) {
			return fmt.Sprintf("refused to remove %s: it resolves to %s, outside the extensions directory", dir, resolvedDir)
		}
	}

	if err := os.RemoveAll(dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
		// The row is already gone, so nothing in the UI can point at this any more. What remains is
		// executable content nobody owns — say so loudly.
		log.Printf("[extensions] ext=%s LEFTOVER FILES: %s could not be removed: %v", extID, dir, err)
		return fmt.Sprintf("%s could not be removed: %v", dir, err)
	}

	// An empty author folder is noise in the listing and nothing can be installed from it.
	if parent := filepath.Dir(dir); parent != absRoot {
		if entries, err := os.ReadDir(parent); err == nil && len(entries) == 0 {
			_ = os.Remove(parent)
		}
	}
	log.Printf("[extensions] ext=%s removed its files: %s", extID, dir)
	return ""
}

// unsafeDirName reports why an extension name cannot be used as a directory name, or "" when it can.
// Both the delete path and the startup repair derive a directory from a stored record and hand it to a
// recursive filesystem operation, so both must refuse exactly the same names: author and name
// originate in a manifest, which is third-party content, and a separator would walk out of the
// extensions root.
func unsafeDirName(name string) string {
	if name == "" {
		return "the record carried no name"
	}
	if name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return fmt.Sprintf("%q is not a directory name", name)
	}
	return ""
}

// insideDir reports whether path is strictly under root. Equal is not "inside": the root holds every
// extension and is never the thing being deleted.
func insideDir(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || rel == ".." {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
