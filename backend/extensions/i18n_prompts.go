package extensions

// The server-side half of the derived language: which pack's prompt templates the server
// renders, from the same rule the UI uses (ActiveI18N) and the same containment rule the pack's
// catalog is read with (packFilePath).
//
// This package resolves the path and never installs it: backend/server hands the answer to
// prompts.SetOverlay, because extensions already imports prompts and the reverse would be a
// cycle — the dependency is injected, not imported.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mike/pkg/logging"
)

// PromptOverlay is the resolved prompt-locale answer: the active pack's translated
// template directory (empty when there is no pack, or it ships no templates) and the
// pack's locale tag, carried through verbatim because core holds no language names.
type PromptOverlay struct {
	Dir    string
	Locale string
}

// none reports whether this overlay means plain English: no templates and no locale.
// Unexported on purpose — the server that consumes an overlay needs its two fields
// apart (they log different sentences), so the only caller is a test asserting the
// zero value.
func (o PromptOverlay) none() bool { return o.Dir == "" && o.Locale == "" }

// ActivePromptOverlay reads the extension list and resolves the active pack's
// prompt side. It is called at startup and on every extension change, so enabling
// a pack switches the prompt language without a restart.
//
// A list that cannot be read is an error rather than an English answer: the caller
// decides whether to keep the previous language or fall back, and "the database had
// a bad moment" must not silently become "the user's language is English".
func ActivePromptOverlay(store *ExtensionStore, extensionsDir string) (PromptOverlay, error) {
	exts, err := store.List()
	if err != nil {
		return PromptOverlay{}, fmt.Errorf("list extensions: %w", err)
	}
	return PromptOverlayFor(exts, extensionsDir), nil
}

// PromptOverlayFor is ActivePromptOverlay for a list the caller already holds — the
// half with no database, and the half the tests speak to.
//
// A pack whose `prompts_dir` resolves outside its own directory loses the templates
// and keeps the locale: the directive is derived from the pack that is enabled, while
// the directory is a path a manifest named, and a named path is only trusted as far as
// the pack's own folder.
func PromptOverlayFor(exts []Extension, extensionsDir string) PromptOverlay {
	pack := ActiveI18N(exts)
	if pack == nil {
		return PromptOverlay{}
	}
	i18n := pack.GetI18N()
	answer := PromptOverlay{Locale: i18n.Locale}
	if i18n.PromptsDir == "" {
		return answer
	}
	dir, ok := PromptDirInPack(extensionDir(extensionsDir, pack.Author, pack.Name), i18n.PromptsDir)
	if !ok {
		logging.For(logging.Prompts).Infof("%s/%s: capabilities.i18n.prompts_dir %q is not a directory inside the pack — prompts stay English", pack.Author, pack.Name, i18n.PromptsDir)
		return answer
	}
	answer.Dir = dir
	return answer
}

// PromptDirInPack resolves a pack-relative prompts directory and proves the result is
// the pack's own.
//
// PromptDirInPack resolves a pack-relative prompts directory and proves the result is the
// pack's own. Contained twice, like the delete path: packFilePath is the lexical proof (which
// is what refuses .. readably) and the symlink check is the enforcement, because filepath.Join
// collapses the segments a lexical check refused to see. The symlink-free path is returned.
// A prompts_dir naming the pack directory itself (rel == ".") is refused, as at install.
func PromptDirInPack(packDir, rel string) (string, bool) {
	full, ok := packFilePath(packDir, rel)
	if !ok {
		return "", false
	}
	if st, err := os.Stat(full); err != nil || !st.IsDir() {
		return "", false
	}
	// Resolve both ends: a pack directory that is itself a symlink must not become a
	// pass to anywhere on the host.
	resolvedPack, err := filepath.EvalSymlinks(packDir)
	if err != nil {
		return "", false
	}
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		return "", false
	}
	inside, err := filepath.Rel(resolvedPack, resolved)
	if err != nil || inside == "." || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
		return "", false
	}
	return resolved, true
}
