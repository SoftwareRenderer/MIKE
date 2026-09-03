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
//
// The path comes from the stored record through extensionDir (`{root}/{author}/{name}`, or the older
// flat `{root}/{name}`), never from a request field. The containment checks are what make it safe to
// hand this a recursive delete: author and name originate in a manifest, which is third-party content.
func (h *ExtensionHandler) removeExtensionFiles(ext *Extension) string {
	if ext == nil || ext.Name == "" {
		return "the extension record carried no name, so its files could be located"
	}
	if ext.Name == "." || ext.Name == ".." || strings.ContainsAny(ext.Name, `/\`) {
		return fmt.Sprintf("refused to remove files for %s: %q is not a directory name", ext.ID, ext.Name)
	}
	root := h.config.ExtensionsDirOrDefault()
	if root == "" {
		return "no extensions directory is configured, so nothing was removed"
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Sprintf("the extensions directory %q could not be resolved: %v", root, err)
	}
	dir := extensionDir(absRoot, ext.Author, ext.Name)

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
		log.Printf("[extensions] ext=%s LEFTOVER FILES: %s could not be removed: %v", ext.ID, dir, err)
		return fmt.Sprintf("%s could not be removed: %v", dir, err)
	}

	// An empty author folder is noise in the listing and nothing can be installed from it.
	if parent := filepath.Dir(dir); parent != absRoot {
		if entries, err := os.ReadDir(parent); err == nil && len(entries) == 0 {
			_ = os.Remove(parent)
		}
	}
	log.Printf("[extensions] ext=%s removed its files: %s", ext.ID, dir)
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
