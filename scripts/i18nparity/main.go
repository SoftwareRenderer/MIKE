package main

// i18nparity verifies that a locale message+prompt pack is a complete, faithful mirror of the
// English contract before it is ever installed.
//
// It exists because both failure modes of a translation pack are silent. A missing key falls back
// to English per key, so a pack that skipped 300 strings still renders 1,300 keys in Chinese and
// looks finished. A renamed placeholder or a dropped `{{if}}` in a prompt overlay does not fail
// until the executor renders that prompt inside a task. Neither shows up in the UI, so both are
// measured here.
//
// Exit codes: 0 pass, 1 findings, 2 inputs missing (the English contract has not landed yet).
//
// Usage:
//
//	go run ./scripts/i18nparity                       # en.ts <-> ../MIKE-extensions/i18n/zh
//	go run ./scripts/i18nparity --pack ../MIKE-extensions/i18n/fr
//	go run ./scripts/i18nparity --strict              # warnings fail too

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Options names the four inputs and the two switches. Everything has a default derived from the
// repo root so the common case is a bare invocation.
type Options struct {
	EN           string // English message catalogue: frontend/src/lib/i18n/en.ts
	ENPrompts    string // English prompt templates: prompts/
	Pack         string // locale pack directory: ../MIKE-extensions/i18n/zh
	Registry     string // generated registry to confirm publication through; "" skips
	MaxList      int    // cap per-category finding lists
	Strict       bool   // warnings fail
	SkipPrompts  bool
	SkipMessages bool
}

// Findings collects the two severities. Everything a pack cannot ship with is an error. A warning
// is something a human must decide: a value byte-identical to English is legitimate for "OK" and
// suspicious for a paragraph, and no checker should pretend to know which.
type Findings struct {
	Errors   []string
	Warnings []string
}

func (f *Findings) Errf(format string, a ...any) {
	f.Errors = append(f.Errors, fmt.Sprintf(format, a...))
}
func (f *Findings) Warnf(format string, a ...any) {
	f.Warnings = append(f.Warnings, fmt.Sprintf(format, a...))
}

// CatalogStats is the coverage numbers, which is what gets reported at the end: the two key counts
// must be equal, and that claim should come from a measurement. Key counts are base keys — one
// entry per message, a counted message counted once however many forms it ships.
type CatalogStats struct {
	Locale string
	ENKeys int
	// PackKeys counts every key the pack declares, including any it invented: `ENKeys` is the
	// contract's total, so the two differ by what is missing *and* by what is extra, and the
	// findings name which.
	PackKeys  int
	Compared  int
	Counted   int // messages the pack ships as plural tables
	Collapsed int // English forms the pack folded into another form (correct for fewer-form languages)
}

// PromptStats counts the overlay's files and define names.
type PromptStats struct {
	Locale      string
	ENFiles     int
	PackFiles   int
	ENDefines   int
	PackDefines int
}

// Run performs every check and reports findings into f. inputsMissing is true when an input does
// not exist, which is a different answer from "the pack is wrong": there is nothing to compare.
func Run(o Options, f *Findings) (inputsMissing bool, cs CatalogStats, ps PromptStats) {
	// Only the inputs this invocation actually reads are required: --skip-messages still wants to
	// check a manifest and an overlay when no English catalogue exists yet.
	var required []string
	if !o.SkipMessages {
		required = append(required, o.EN)
	}
	if !o.SkipPrompts {
		required = append(required, o.ENPrompts)
	}
	required = append(required, o.Pack)

	var missing []string
	for _, p := range required {
		if !exists(p) {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		for _, m := range missing {
			f.Errf("input does not exist: %s", m)
		}
		return true, cs, ps
	}

	var manifest *Manifest
	var i18nCap *I18nCapability
	extType, packName := "i18n", filepath.Base(filepath.Clean(o.Pack))
	manifestPath := filepath.Join(o.Pack, "manifest.json")
	if exists(manifestPath) {
		manifest = LoadManifest(manifestPath, f)
		if manifest != nil {
			extType, packName = manifest.DeriveIdentity(o.Pack, f)
			i18nCap = manifest.I18n(f)
		}
	} else {
		f.Errf("pack has no manifest.json at %s", manifestPath)
	}
	CheckPackFiles(o.Pack, f)

	messagesPath := filepath.Join(o.Pack, "messages.json")
	if i18nCap != nil && i18nCap.MessagesURL != "" {
		messagesPath = filepath.Join(o.Pack, filepath.FromSlash(i18nCap.MessagesURL))
	}
	packPrompts := filepath.Join(o.Pack, "prompts")
	if i18nCap != nil && i18nCap.PromptsDir != "" {
		packPrompts = filepath.Join(o.Pack, filepath.FromSlash(i18nCap.PromptsDir))
	}

	// The locale is what the plural rules are read against, and what the report calls this pack.
	// The manifest's declaration wins; the directory name is the answer when there is no readable
	// manifest, and the mismatch between the two is reported above.
	locale := filepath.Base(filepath.Clean(o.Pack))
	if i18nCap != nil {
		// The directory name and the declared locale are the same decision written twice; the
		// registry's subdir and the runtime lookup both have to agree with it.
		if base := filepath.Base(filepath.Clean(o.Pack)); base != i18nCap.Locale {
			f.Warnf(`capabilities.i18n.locale %q differs from the pack directory name %q`, i18nCap.Locale, base)
		}
		locale = i18nCap.Locale
	}

	if !o.SkipMessages {
		if !exists(messagesPath) {
			f.Errf("messages catalogue %s does not exist", messagesPath)
		} else {
			enCat, enErr := LoadCatalog(o.EN)
			packCat, packErr := LoadCatalog(messagesPath)
			if enErr != nil {
				f.Errf("English catalogue: %v", enErr)
			}
			if packErr != nil {
				f.Errf("locale catalogue: %v", packErr)
			}
			if enCat != nil && packCat != nil {
				if len(enCat.Values) == 0 {
					f.Errf("English catalogue %s declares no keys — there is no contract to mirror", o.EN)
				} else {
					cs = CompareCatalogs(enCat, packCat, locale, f, o.MaxList)
				}
			}
		}
	}

	if !o.SkipPrompts {
		if !exists(packPrompts) {
			f.Errf("prompt overlay %s does not exist", packPrompts)
		} else {
			enSet, enErr := LoadTmplSet(o.ENPrompts)
			if enErr != nil {
				f.Errf("English prompts: %v", enErr)
			}
			packSet, packErr := LoadTmplSet(packPrompts)
			if packErr != nil {
				f.Errf("locale prompts: %v", packErr)
			}
			if enSet != nil && packSet != nil {
				ps = ComparePromptSets(enSet, packSet, f, o.MaxList)
			}
		}
	}

	if o.Registry != "" && manifest != nil {
		CheckRegistry(o.Registry, extType, packName, manifest.Version, o.Pack, f)
	}
	return false, cs, ps
}

func main() {
	root := mustRepoRoot()
	var (
		o      Options
		strict bool
		skipP  bool
		skipM  bool
	)
	flag.StringVar(&o.EN, "en", filepath.Join(root, "frontend/src/lib/i18n/en.ts"), "English message catalogue (the contract)")
	flag.StringVar(&o.ENPrompts, "en-prompts", filepath.Join(root, "prompts"), "English prompt template directory")
	flag.StringVar(&o.Pack, "pack", filepath.Join(filepath.Dir(root), "MIKE-extensions", "i18n", "zh"), "locale pack directory to check")
	flag.StringVar(&o.Registry, "registry", filepath.Join(filepath.Dir(root), "MIKE-extensions", "registry.json"), "generated registry to confirm publication through (empty to skip)")
	flag.IntVar(&o.MaxList, "max-list", 40, "cap per-category finding lists (0 for no cap)")
	flag.BoolVar(&strict, "strict", false, "treat warnings as failures")
	flag.BoolVar(&skipP, "skip-prompts", false, "skip the prompt-overlay comparison")
	flag.BoolVar(&skipM, "skip-messages", false, "skip the message-catalogue comparison")
	flag.Parse()
	o.Strict, o.SkipPrompts, o.SkipMessages = strict, skipP, skipM

	f := &Findings{}
	inputsMissing, cs, ps := Run(o, f)

	if inputsMissing {
		fmt.Fprintln(os.Stderr, "i18nparity: inputs are missing — there is nothing to compare.")
		for _, m := range f.Errors {
			fmt.Fprintf(os.Stderr, "  %s\n", m)
		}
		fmt.Fprintln(os.Stderr, "\nThe English catalogue is authored by the \"English surface final\" task. Until")
		fmt.Fprintln(os.Stderr, "frontend/src/lib/i18n/en.ts exists there is no key set to mirror, and a pack written")
		fmt.Fprintln(os.Stderr, "against nothing would be an invented one.")
		os.Exit(2)
	}

	fmt.Printf("messages: en=%d %s=%d compared=%d counted=%d collapsed=%d\n",
		cs.ENKeys, cs.Locale, cs.PackKeys, cs.Compared, cs.Counted, cs.Collapsed)
	fmt.Printf("prompts:  en=%d %s=%d defines(en/pack)=%d/%d\n",
		ps.ENFiles, ps.Locale, ps.PackFiles, ps.ENDefines, ps.PackDefines)
	report(f, o.Strict)
}

// CompareCatalogs enforces that the pack mirrors the contract: the same messages, the same shape
// per message, every placeholder, and only the plural forms its own language can answer with.
//
// `locale` is what makes the plural rules expressible — without it there is no way to tell a
// Russian pack that `few` is required reading from a Turkish one that `one` is dead text.
func CompareCatalogs(en, pack *Catalog, locale string, f *Findings, maxList int) CatalogStats {
	for _, k := range en.Duplicates {
		f.Errf("English catalogue declares %s twice — the later value silently wins", k)
	}
	for _, k := range pack.Duplicates {
		f.Errf("locale catalogue declares %s twice — the later value silently wins", k)
	}
	for _, n := range pack.Notes {
		f.Warnf("locale catalogue: %s", n)
	}

	// The keys a pack owes. Compared on base keys, not flattened ones: `board.card.fileCount.one`
	// is not a message, it is one form of `board.card.fileCount`, and a language with two forms
	// legitimately has no `one`.
	var owed []string
	heldEnglish := 0
	for _, k := range en.baseKeys() {
		if isEnglishOnly(k) {
			heldEnglish++
			continue
		}
		owed = append(owed, k)
	}
	if heldEnglish > 0 && heldEnglish != englishOnlyExpected {
		// Silent when the contract has no such key at all — a test fixture's six-key catalogue is
		// not a missing rule. Loud when one is *added*: someone has to decide it stays English too.
		f.Warnf("the contract holds %d key(s) English (%s); this checker's rule expects %d — decide "+
			"whether the new ones stay English and update the rule, rather than let every pack read as short",
			heldEnglish, strings.Join(englishOnlyNamespaces, ", "), englishOnlyExpected)
	}

	packBase := keySet(pack.baseKeys())
	var missing, carried []string
	for _, k := range owed {
		if !packBase[k] {
			missing = append(missing, k)
		}
	}
	for _, k := range pack.baseKeys() {
		if isEnglishOnly(k) {
			carried = append(carried, k)
		}
	}
	_, extra := keyDiff(keySet(owed), packBase)

	if len(missing) > 0 {
		// The finding that matters most: fallback is per key, so this pack still looks translated.
		f.Errf("%d key(s) exist in English but not in the locale pack (each one silently falls back to English):\n  %s",
			len(missing), strings.Join(capList(missing, maxList), "\n  "))
	}
	if len(extra) > 0 {
		f.Errf("%d key(s) exist only in the locale pack — an invented key is never looked up:\n  %s",
			len(extra), strings.Join(capList(extra, maxList), "\n  "))
	}
	if len(carried) > 0 {
		f.Errf("%d key(s) the pack cannot use: %s stay English by design (the offer renders only with "+
			"no pack installed, and i18n.test.* renders nowhere but the runtime's tests) — shipping "+
			"them is dead weight, not coverage:\n  %s",
			len(carried), strings.Join(englishOnlyNamespaces, " and "), strings.Join(capList(carried, maxList), "\n  "))
	}

	stats := CatalogStats{Locale: locale, ENKeys: len(owed) + heldEnglish, PackKeys: len(pack.baseKeys())}
	shapeBad, noOther, deadForm := []string{}, []string{}, []string{}
	// Placeholder findings are collected by two comparisons — form-by-form below, and the folded
	// forms above — and reported as one list, because a pack with three lost tokens should say so
	// once.
	var placeholderBad, untranslatedBad []string
	reachable, known := reachableForms(locale)
	for _, key := range owed {
		if !packBase[key] {
			continue
		}
		enForms, enCounted := en.Tables[key]
		packForms, packCounted := pack.Tables[key]
		switch {
		case enCounted && !packCounted:
			shapeBad = append(shapeBad, fmt.Sprintf("%s — English counts it, the pack ships one plain string", key))
		case !enCounted && packCounted:
			shapeBad = append(shapeBad, fmt.Sprintf("%s — English is one string, the pack ships a plural table", key))
		case enCounted && packCounted:
			stats.Counted++
			if _, ok := packForms["other"]; !ok {
				// `other` is the form the runtime falls back to when it has no name for the count,
				// which every language has: without it a counted message has no answer at all.
				noOther = append(noOther, key)
			}
			for form := range packForms {
				if known && !reachable[form] {
					deadForm = append(deadForm, fmt.Sprintf("%s.%s", key, form))
				}
			}
			for form, text := range enForms {
				if _, shipped := packForms[form]; shipped {
					continue
				}
				// The form was folded into `other`. That is legal, but it is invisible to the
				// form-by-form placeholder comparison below, so its placeholders are checked here
				// against everything the pack does ship for this message.
				stats.Collapsed++
				for _, tok := range ExtractPlaceholders(text) {
					found := false
					for _, shipped := range packForms {
						for _, have := range ExtractPlaceholders(shipped) {
							if have.Text == tok.Text {
								found = true
							}
						}
					}
					if !found {
						placeholderBad = append(placeholderBad, fmt.Sprintf("%s: folded form %q lost %s", key, form, tok.Text))
					}
				}
			}
		}
	}

	for _, key := range sortedStringKeys(en.Values) {
		packText, ok := pack.Values[key]
		if !ok {
			continue
		}
		stats.Compared++
		enText := en.Values[key]

		enTok := ExtractPlaceholders(enText)
		packTok := ExtractPlaceholders(packText)

		for _, t := range packTok {
			if t.Kind != "named" {
				f.Errf("%s: locale value has a %s token %q — not a single-brace {name} placeholder", key, t.Kind, t.Text)
			}
		}
		for _, t := range enTok {
			if t.Kind != "named" {
				f.Warnf("%s: English value has a %s token %q — the contract itself is malformed here", key, t.Kind, t.Text)
			}
		}

		missing, extra := diffCounts(countPlaceholders(enTok), countPlaceholders(packTok), locale)
		for _, m := range missing {
			placeholderBad = append(placeholderBad, fmt.Sprintf("%s: lost %s", key, m))
		}
		for _, e := range extra {
			placeholderBad = append(placeholderBad, fmt.Sprintf("%s: added %s", key, e))
		}

		if packText == enText && looksLikeUntranslated(enText) {
			untranslatedBad = append(untranslatedBad, key)
		}
		if packText == key {
			untranslatedBad = append(untranslatedBad, key+" (value is the key id itself, which would render as a raw key)")
		}
	}
	if len(placeholderBad) > 0 {
		f.Errf("%d key(s) changed their placeholders (a placeholder is part of the interface: the formatter looks it up by name):\n  %s",
			len(placeholderBad), strings.Join(capList(placeholderBad, maxList), "\n  "))
	}
	if len(shapeBad) > 0 {
		f.Errf("%d key(s) changed shape from the contract (a counted message and a plain string are "+
			"different things to the runtime):\n  %s",
			len(shapeBad), strings.Join(capList(shapeBad, maxList), "\n  "))
	}
	if len(noOther) > 0 {
		f.Errf("%d counted key(s) ship no \"other\" form, which is the one form guaranteed to be "+
			"selectable in every language:\n  %s",
			len(noOther), strings.Join(capList(noOther, maxList), "\n  "))
	}
	if len(deadForm) > 0 {
		f.Errf("%d plural form(s) \"%s\" can never be selected — the language has no name for that "+
			"count, so the text is unreachable:\n  %s",
			len(deadForm), locale, strings.Join(capList(deadForm, maxList), "\n  "))
	}
	if !known && stats.Counted > 0 {
		f.Warnf("no plural form set is known for %q: \"other\" was required and no form was refused as "+
			"unreachable — add the language to pluralFormsByLocale to check it properly", locale)
	}
	if len(untranslatedBad) > 0 {
		f.Warnf("%d value(s) are byte-identical to English and look like untranslated prose rather than a shared token:\n  %s",
			len(untranslatedBad), strings.Join(capList(untranslatedBad, maxList), "\n  "))
	}
	return stats
}

// looksLikeUntranslated flags a value that was probably copied rather than translated. Warning
// only: short tokens, brand names, tool names and paths are legitimately identical everywhere.
func looksLikeUntranslated(s string) bool {
	if hasCJK(s) {
		return false
	}
	return len(strings.Fields(s)) >= 2 && len(s) >= 8
}

// keySet turns a sorted key list into a membership set.
func keySet(keys []string) map[string]bool {
	set := make(map[string]bool, len(keys))
	for _, k := range keys {
		set[k] = true
	}
	return set
}

// keyDiff returns keys only in a and keys only in b.
func keyDiff(a, b map[string]bool) (onlyA, onlyB []string) {
	for k := range a {
		if !b[k] {
			onlyA = append(onlyA, k)
		}
	}
	for k := range b {
		if !a[k] {
			onlyB = append(onlyB, k)
		}
	}
	sort.Strings(onlyA)
	sort.Strings(onlyB)
	return onlyA, onlyB
}

// ComparePromptSets enforces that the overlay changed prose and nothing else.
func ComparePromptSets(en, pack *TmplSet, f *Findings, maxList int) PromptStats {
	enFiles := toSet(en.Files)
	packFiles := toSet(pack.Files)

	// The English set is the contract. If it no longer matches the list this checker was written
	// against, the checker is stale and must say so rather than quietly check six files.
	for _, want := range promptTemplates {
		if !enFiles[want] {
			f.Errf("English prompt directory has no %s — this checker's expected template list is out of date", want)
		}
	}

	var missing, extra []string
	for _, n := range en.Files {
		if !packFiles[n] {
			missing = append(missing, n)
		}
	}
	for _, n := range pack.Files {
		if !enFiles[n] {
			extra = append(extra, n)
		}
	}
	if len(missing) > 0 {
		// A missing file is a hard render failure, not a fallback: prompts are rendered from
		// whichever directory is configured, not from two directories merged.
		f.Errf("prompt overlay is missing %d template(s):\n  %s", len(missing), strings.Join(capList(missing, maxList), "\n  "))
	}
	if len(extra) > 0 {
		// The loader globs *.tmpl into one set, so an extra file is not inert: it can collide on a
		// define name or hijack a render.
		f.Errf("prompt overlay has %d template(s) English does not have (a loader globbing *.tmpl pulls them into the set, where they can collide on a define name):\n  %s",
			len(extra), strings.Join(capList(extra, maxList), "\n  "))
	}

	for _, name := range en.Files {
		if !packFiles[name] {
			continue
		}
		enSkel, packSkel := en.Skeletons[name], pack.Skeletons[name]
		if i, ok := firstDiff(enSkel, packSkel); ok {
			f.Errf("%s: template logic differs at directive %d (count %d vs %d)\n  en: %s\n  zh: %s",
				name, i, len(enSkel), len(packSkel), skelAt(enSkel, i), skelAt(packSkel, i))
		}

		// A file identical to English means the overlay was copied, not translated: the executor
		// would then answer in English while the UI is Chinese.
		enBytes, err1 := en.ReadFile(name)
		packBytes, err2 := pack.ReadFile(name)
		if err1 == nil && err2 == nil && string(enBytes) == string(packBytes) {
			f.Warnf("%s is byte-identical to the English template — it was copied, not translated", name)
		}
	}

	// Defines are global to the parsed set, so a rename in a define-only file is caught by the
	// name set, not by per-file position.
	enDefs, packDefs := en.DefineNames(), pack.DefineNames()
	defMissing, defExtra := stringSetDiff(enDefs, packDefs)
	for _, d := range defMissing {
		f.Errf("prompt overlay lost define %q — every `{{template %q}}` naming it fails at render time, not at parse time", d, d)
	}
	for _, d := range defExtra {
		f.Errf("prompt overlay adds define %q, which English does not declare", d)
	}

	// Name resolution is deferred to execution, so every invoked name must resolve inside the
	// overlay's own set: a renamed define leaves a template that parses and then fails to render.
	for _, name := range sortedMapKeys(pack.Skeletons) {
		for _, invoked := range pack.Invoked[name] {
			if !pack.Declared[invoked] {
				f.Errf("%s calls {{template %q}}, which the overlay never declares — this parses and fails when the prompt renders", name, invoked)
			}
		}
	}

	// base.tmpl's plan-mode call is the one cross-file reference in the set. If it goes missing,
	// plan mode renders without its instructions and nothing else complains.
	if contains(en.Invoked["base.tmpl"], "plan-mode") && !contains(pack.Invoked["base.tmpl"], "plan-mode") {
		f.Errf(`base.tmpl no longer invokes {{template "plan-mode" .}}: plan mode's instructions are prompt text the executor's behaviour depends on`)
	}

	for _, name := range sortedMapKeys(pack.Skeletons) {
		for _, entry := range pack.Skeletons[name] {
			if strings.HasPrefix(entry, "unsupported:") {
				f.Warnf("%s contains %s — this checker does not model that node kind", name, entry)
			}
		}
	}

	return PromptStats{ENFiles: len(en.Files), PackFiles: len(pack.Files), ENDefines: len(enDefs), PackDefines: len(packDefs)}
}

// CheckRegistry confirms the pack is published through the generated registry instead of being
// hand-inserted into it, which is the rule scripts/gen-registry.py exists to enforce.
func CheckRegistry(path, extType, name, version, pack string, f *Findings) {
	raw, err := os.ReadFile(path)
	if err != nil {
		f.Warnf("registry: %v", err)
		return
	}
	var entries []struct {
		Name    string `json:"name"`
		Type    string `json:"type"`
		Version string `json:"version"`
		Subdir  string `json:"subdir"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		f.Warnf("registry %s: %v", path, err)
		return
	}
	wantSubdir := filepath.ToSlash(filepath.Join(extType, name))
	for _, e := range entries {
		if e.Type != extType || e.Name != name {
			continue
		}
		if e.Version != version {
			f.Warnf("registry publishes %s/%s at version %s but the manifest says %s — rerun scripts/gen-registry.py", e.Type, e.Name, e.Version, version)
		}
		if e.Subdir != "" && e.Subdir != wantSubdir {
			f.Errf("registry pins %s/%s at subdir %q but the pack checked is %s — the wrong directory would be installed", e.Type, e.Name, e.Subdir, wantSubdir)
		}
		return
	}
	f.Errf("registry %s has no %s/%s entry — run python3 scripts/gen-registry.py (never hand-edit it)", filepath.Base(path), extType, name)
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func toSet(names []string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// firstDiff returns the index of the first differing skeleton entry.
func firstDiff(a, b []string) (int, bool) {
	for i := 0; i < max(len(a), len(b)); i++ {
		if skelAt(a, i) != skelAt(b, i) {
			return i, true
		}
	}
	return 0, false
}

func skelAt(s []string, i int) string {
	if i >= len(s) {
		return "<absent>"
	}
	return s[i]
}

func stringSetDiff(a, b []string) (onlyA, onlyB []string) {
	aSet, bSet := toSet(a), toSet(b)
	for _, s := range a {
		if !bSet[s] {
			onlyA = append(onlyA, s)
		}
	}
	for _, s := range b {
		if !aSet[s] {
			onlyB = append(onlyB, s)
		}
	}
	return onlyA, onlyB
}

func report(f *Findings, strict bool) {
	fmt.Println()
	for _, w := range f.Warnings {
		fmt.Printf("WARN  %s\n", w)
	}
	for _, e := range f.Errors {
		fmt.Printf("ERROR %s\n", e)
	}
	fmt.Println()

	failures := len(f.Errors)
	if strict {
		failures += len(f.Warnings)
	}
	if failures == 0 {
		fmt.Printf("OK — %d warning(s)\n", len(f.Warnings))
		return
	}
	fmt.Printf("FAIL — %d error(s), %d warning(s)\n", len(f.Errors), len(f.Warnings))
	os.Exit(1)
}

// capList keeps a finding list readable: a pack missing 900 keys should say 900 and show a
// window of them, not bury the summary under 900 lines.
func capList(items []string, max int) []string {
	if max <= 0 || len(items) <= max {
		return items
	}
	out := append([]string{}, items[:max]...)
	return append(out, fmt.Sprintf("… and %d more", len(items)-max))
}

func sortedStringKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedMapKeys[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// mustRepoRoot walks up from the CWD to the module root so the tool works from any subdirectory.
func mustRepoRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for i := 0; i < 12; i++ {
		if exists(filepath.Join(dir, "go.mod")) && exists(filepath.Join(dir, "prompts")) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "."
}
