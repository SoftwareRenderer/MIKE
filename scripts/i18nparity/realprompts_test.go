package main

import (
	"path/filepath"
	"testing"
)

// The fixtures prove the rules; this proves them against the real templates in prompts/, which are
// what a locale overlay must mirror. A template this checker cannot model shows up here rather than
// as a false pass on a locale pack.
func TestRealRepoPromptsParse(t *testing.T) {
	root := mustRepoRoot()
	dir := filepath.Join(root, "prompts")

	set, err := LoadTmplSet(dir)
	if err != nil {
		t.Fatalf("the repo's own prompts must parse: %v", err)
	}

	for _, name := range promptTemplates {
		if _, ok := set.Skeletons[name]; !ok {
			t.Errorf("prompts/%s not found in %s", name, dir)
		}
	}
	for _, mode := range []string{"plan-mode", "explore-mode"} {
		if !set.Declared[mode] {
			t.Errorf(`prompts/%s.tmpl must declare %q; declared = %v`, mode, mode, set.Declared)
		}
		if !contains(set.Invoked["base.tmpl"], mode) {
			t.Errorf(`base.tmpl must invoke {{template %q}}; invoked = %v`, mode, set.Invoked["base.tmpl"])
		}
	}
	for _, name := range promptTemplates {
		for _, invoked := range set.Invoked[name] {
			if !set.Declared[invoked] {
				t.Errorf("%s invokes %q, which prompts/ never declares", name, invoked)
			}
		}
	}
}

// The prompt set is also the one thing the checker must be able to compare with itself: a
// self-comparison is the "clean overlay" case and must produce no findings at all.
func TestRealRepoPromptsSelfCompareIsClean(t *testing.T) {
	root := mustRepoRoot()
	set, err := LoadTmplSet(filepath.Join(root, "prompts"))
	if err != nil {
		t.Skipf("repo prompts unavailable: %v", err)
	}
	f := &Findings{}
	stats := ComparePromptSets(set, set, f, 40)
	// Byte-identical files are the point here, so the "copied, not translated" warning is expected
	// and everything else is not.
	if len(f.Errors) != 0 {
		t.Fatalf("self-comparison produced errors:\n%s", errText(f))
	}
	if stats.ENFiles != 8 {
		t.Fatalf("expected the eight English templates, got %+v", stats)
	}
}

func TestRealRepoEnglishCatalogueAbsentIsReported(t *testing.T) {
	// Honest statement of the current state: T3 has not landed, so Run() must say "nothing to
	// compare" rather than invent a contract or pass vacuously.
	root := mustRepoRoot()
	en := filepath.Join(root, "frontend/src/lib/i18n/en.ts")
	if exists(en) {
		t.Skip("en.ts exists now — T3 has landed, so this expectation is obsolete")
	}
	f := &Findings{}
	missing, _, _ := Run(Options{
		EN:        en,
		ENPrompts: filepath.Join(root, "prompts"),
		Pack:      filepath.Join(root, "..", "MIKE-extensions", "i18n", "zh"),
		MaxList:   40,
	}, f)
	if !missing {
		t.Fatal("expected inputsMissing while the English contract does not exist")
	}
}
