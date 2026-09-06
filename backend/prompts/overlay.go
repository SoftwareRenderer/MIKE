package prompts

// The prompt-template overlay: a translation pack's directory of templates laid
// over core's English ones.
//
// A pack is data, not code (docs/i18n.md), and its templates are the same
// `*.tmpl` files core ships — so the overlay resolves **per template**, pack
// first then core. A pack that ships only `base.tmpl` still gets core's
// `summarize.tmpl`, and a pack template that cannot parse or execute loses only
// *its own* template to core's English one. Per-template fallback is the
// prompt-side analog of the catalog's per-key fallback: a partial pack is the
// normal state, never an error state, and a translation must never fail a turn.
//
// The state is **injected** through SetOverlay and never imported from
// `backend/extensions`: extensions already imports this package for FuncMap(),
// so the dependency would be a cycle. Whoever knows which pack is enabled
// (`extensions.ActiveI18N`, called from backend/server) resolves it to a host
// directory and hands the answer here.

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"text/template"
)

// overlayState is one immutable answer to "whose templates are active". It is
// replaced whole and never mutated, because prompts render concurrently for
// every running task and one render must read the same pair throughout.
type overlayState struct {
	// dir is the pack's template directory on the host. Empty means core only.
	dir string
	// locale is the pack's locale tag, carried through verbatim — core holds no
	// language names, and this one is the pack's own manifest value.
	locale string
}

// overlay is never nil; it starts as "no pack", which is English.
var overlay atomic.Pointer[overlayState]

// reportMu guards reportOnce, the set of pack failures already logged. A pack
// template that fails would otherwise log on every turn of every task.
var (
	reportMu   sync.Mutex
	reportOnce = map[string]bool{}
)

func init() { overlay.Store(&overlayState{}) }

// SetOverlay installs a pack's prompt templates. It is called at startup and on
// every extension change, so enabling a pack switches the prompt language
// without a restart.
//
// Both arguments empty is English: today's behaviour, byte for byte. An empty
// dir with a locale is a pack that translates the UI but ships no templates —
// the answer-language directive still applies, the templates do not.
//
// Nothing here is validated: the caller resolved `dir` through the pack's own
// containment rules (extensions.PromptOverlayFor), because a path derived from
// a manifest has to be proven inside the pack's directory before anything reads
// it, and that proof belongs where the pack directory is known.
func SetOverlay(dir, locale string) {
	next := &overlayState{dir: strings.TrimSpace(dir), locale: strings.TrimSpace(locale)}
	if prev := overlay.Swap(next); prev != nil && prev.dir != next.dir {
		// New pack (or back to English): a fresh failure log means a re-enabled
		// pack reports its own problems instead of being silently muted by the
		// warning its previous enablement already printed.
		reportMu.Lock()
		reportOnce = map[string]bool{}
		reportMu.Unlock()
	}
}

// ActiveLocale returns the active pack's locale tag, or "" when no pack is
// enabled. It is the locale of the *rendered prompt*, which is why it is not
// the browser's language and never guessed.
func ActiveLocale() string { return overlay.Load().locale }

// ActiveOverlay reports the installed overlay: the pack's template directory
// ("" when it ships none) and its locale tag. It is the read side of SetOverlay —
// nothing in the server reads it, because the caller that installs an overlay keeps
// its own copy, so its callers are the cross-package tests that assert what the
// setter actually installed.
func ActiveOverlay() (dir, locale string) {
	s := overlay.Load()
	return s.dir, s.locale
}

// report logs a pack-template failure once per (dir, kind, name) and says
// nothing after that: an overlay that cannot render says it loudly once, and
// then gets on with answering in English.
func report(kind, dir, name string, err error) {
	key := dir + "\x00" + kind + "\x00" + name
	reportMu.Lock()
	seen := reportOnce[key]
	if !seen {
		reportOnce[key] = true
	}
	reportMu.Unlock()
	if seen {
		return
	}
	log.Printf("[prompts] translation pack %s: %s template %s: %v — core English answers for it",
		dir, kind, name, err)
}

// parseCore builds a template set from core's files. An empty file list is
// os.ErrNotExist, which is what a missing prompts directory has always meant.
func parseCore(files []string) (*template.Template, error) {
	if len(files) == 0 {
		return nil, os.ErrNotExist
	}
	// Funcs on every entry point: an unregistered function is a parse error, so a
	// pack template using `inc` has to find it here or it would parse at install
	// (FuncMap() is what the installer checks with) and fail at render.
	return template.New("prompts").Funcs(funcs).ParseFiles(files...)
}

// applyOverlay redefines the pack's templates over an existing set, one file at
// a time.
//
// The per-file call is the whole mechanism. text/template replaces a same-named
// definition and leaves the set untouched when a file fails to parse, so a
// translated `base.tmpl` wins over core's while a broken `plan-mode.tmpl`
// leaves core's in place — verified against the real templates in
// overlay_test.go. A pack file whose name core does not use is simply added,
// which lets a pack ship a sub-template its own `base.tmpl` calls.
//
// Glob returns names sorted, so which file wins a case-insensitive clash is
// deterministic rather than a filesystem answer.
func applyOverlay(set *template.Template, dir string) {
	if dir == "" {
		return
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.tmpl"))
	if err != nil {
		report("glob", dir, "*.tmpl", err)
		return
	}
	for _, f := range files {
		if _, err := set.ParseFiles(f); err != nil {
			report("parse", dir, filepath.Base(f), err)
		}
	}
}

// render resolves one template and renders it: core's files first, then the
// pack's over them, and core's answer again if the pack's fails to execute.
//
// The execute retry re-parses core alone rather than mutating the set, so a pack
// template that breaks on *this turn's data* (a field it invented, a range over
// something that is not a list) costs only its own template, and the cost is one
// extra parse on the path that is already reading every template from disk.
func render(coreFiles []string, name string, data any) (string, error) {
	state := overlay.Load()

	set, err := parseCore(coreFiles)
	if err != nil {
		return "", err
	}
	applyOverlay(set, state.dir)

	var buf bytes.Buffer
	err = set.ExecuteTemplate(&buf, name, data)
	if err == nil {
		return buf.String(), nil
	}
	if state.dir == "" {
		// Nothing was overlaid: this is core's own failure, reported as an error
		// exactly as it always was.
		return "", err
	}
	report("execute", state.dir, name, err)

	core, err := parseCore(coreFiles)
	if err != nil {
		return "", err
	}
	buf.Reset()
	if err := core.ExecuteTemplate(&buf, name, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// coreGlob returns core's whole template set. The system prompt needs all of it
// because base.tmpl calls {{template "plan-mode" .}}, and a sub-template
// resolves by the name its file *defines* ({{define "plan-mode"}}), not by its
// file name — which is also what a pack has to know when it translates one.
func coreGlob() ([]string, error) {
	files, err := filepath.Glob(filepath.Join(promptDir(), "*.tmpl"))
	if err != nil {
		return nil, err
	}
	return files, nil
}

// coreFile returns core's single template file for the renders that own exactly
// one (or, for the prompt pairs, two) files. Keeping each entry point's core file
// set the size it has always been means a template unrelated to this render
// cannot break it.
func coreFile(names ...string) []string {
	dir := promptDir()
	files := make([]string, 0, len(names))
	for _, n := range names {
		files = append(files, filepath.Join(dir, n))
	}
	return files
}
