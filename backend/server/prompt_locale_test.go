package server

// The seam between "which pack is enabled" and "which templates render".
//
// The two halves are tested where they live — extensions/i18n_prompts_test.go proves the
// resolver, prompts/overlay_test.go proves that an installed overlay changes what renders.
// What is left, and what this file is about, is the wiring: that the server actually
// calls one with the answer to the other, and does it again when an extension changes.
// A derived language that nothing installs is exactly the bug `prompts_dir` had before
// this existed — declared, validated, projected into the registry, and read by nobody.

import (
	"os"
	"path/filepath"
	"testing"

	"mike/backend/extensions"
	"mike/backend/prompts"
)

// packWithFiles creates the on-disk half of an installed pack under the server's
// extensions dir and returns the row to store. `templates` names the files the pack
// ships inside its prompts_dir; pass none for a catalog-only pack.
func packWithFiles(t *testing.T, s *Server, name, locale string, enabled bool, templates ...string) *extensions.Extension {
	t.Helper()
	root := filepath.Join(s.extensionsDir, "testdev", name)
	if len(templates) > 0 {
		if err := os.MkdirAll(filepath.Join(root, "prompts"), 0o755); err != nil {
			t.Fatal(err)
		}
		for _, f := range templates {
			if err := os.WriteFile(filepath.Join(root, "prompts", f), []byte("translated"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	} else if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	promptsDir := ""
	if len(templates) > 0 {
		promptsDir = "prompts"
	}
	ext := &extensions.Extension{
		ID:      "ext-" + name,
		Name:    name,
		Author:  "TestDev",
		Version: "1.0.0",
		Type:    extensions.ExtensionTypeI18N,
		Enabled: enabled,
		Manifest: extensions.ExtensionManifest{
			DisplayName: name,
			Capabilities: &extensions.Capabilities{
				I18N: &extensions.I18NConfig{
					Locale:      locale,
					MessagesURL: "messages.json",
					PromptsDir:  promptsDir,
				},
			},
		},
	}
	if err := s.extStore.Create(ext); err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	return ext
}

// TestRefreshPromptLocaleFollowsTheEnabledPack is the live switch: enabling a pack
// installs its templates, disabling it returns to English, and no restart is involved.
func TestRefreshPromptLocaleFollowsTheEnabledPack(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	s.extensionsDir = t.TempDir()
	t.Cleanup(func() { prompts.SetOverlay("", "") })
	prompts.SetOverlay("", "")

	ext := packWithFiles(t, s, "pack-ja", "ja", false, "base.tmpl", "plan-mode.tmpl")

	// Installed but disabled: a disabled pack contributes no text, in the prompts
	// exactly as in the UI.
	s.refreshPromptLocale()
	if dir, locale := prompts.ActiveOverlay(); dir != "" || locale != "" {
		t.Errorf("a disabled pack set the overlay to dir=%q locale=%q", dir, locale)
	}

	// Enabled: the resolved directory is the pack's own, not a path it invented.
	if err := s.extStore.SetEnabled(ext.ID, true); err != nil {
		t.Fatal(err)
	}
	s.refreshPromptLocale()
	dir, locale := prompts.ActiveOverlay()
	if locale != "ja" {
		t.Errorf("locale = %q, want ja", locale)
	}
	want := filepath.Join(s.extensionsDir, "testdev", "pack-ja", "prompts")
	if resolved, err := filepath.EvalSymlinks(want); err == nil {
		want = resolved
	}
	if dir != want {
		t.Errorf("templates = %q, want %q", dir, want)
	}

	// Disabled again: back to English, with no leftovers of the pack's answer.
	if err := s.extStore.SetEnabled(ext.ID, false); err != nil {
		t.Fatal(err)
	}
	s.refreshPromptLocale()
	if dir, locale := prompts.ActiveOverlay(); dir != "" || locale != "" {
		t.Errorf("disabling the pack left dir=%q locale=%q — the language is derived, so it must go back", dir, locale)
	}
}

// TestRefreshPromptLocaleCatalogOnlyPack covers the pack that translates the UI and
// ships no prompt templates: the locale is still recorded, the templates stay core's.
func TestRefreshPromptLocaleCatalogOnlyPack(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	s.extensionsDir = t.TempDir()
	t.Cleanup(func() { prompts.SetOverlay("", "") })
	prompts.SetOverlay("", "")

	if _, err := os.Create(filepath.Join(s.extensionsDir, "nothing")); err != nil {
		t.Fatal(err)
	}
	packWithFiles(t, s, "pack-fr", "fr", true) // no templates
	s.refreshPromptLocale()

	dir, locale := prompts.ActiveOverlay()
	if dir != "" {
		t.Errorf("a pack with no prompts_dir set templates to %q", dir)
	}
	if locale != "fr" {
		t.Errorf("locale = %q, want fr — the locale is recorded whether or not the pack shipped templates", locale)
	}
}

// TestRefreshPromptLocaleRefusesAnEscapingPromptsDir is the security property at the
// seam: a manifest that names a directory outside its own pack must not get the
// server reading templates from it.
func TestRefreshPromptLocaleRefusesAnEscapingPromptsDir(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	s.extensionsDir = t.TempDir()
	t.Cleanup(func() { prompts.SetOverlay("", "") })
	prompts.SetOverlay("", "")

	// Somewhere a pack must never reach: a sibling of the pack's own directory.
	outside := filepath.Join(s.extensionsDir, "somebody-else", "templates")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "base.tmpl"), []byte("not yours"), 0o644); err != nil {
		t.Fatal(err)
	}

	ext := packWithFiles(t, s, "pack-xx", "xx", true)
	ext.Manifest.Capabilities.I18N.PromptsDir = "../../somebody-else/templates"
	if err := s.extStore.Update(ext.ID, ext); err != nil {
		t.Fatal(err)
	}
	s.refreshPromptLocale()

	dir, locale := prompts.ActiveOverlay()
	if dir != "" {
		t.Errorf("an escaping prompts_dir installed templates from %q", dir)
	}
	if locale != "xx" {
		t.Errorf("locale = %q, want the language to survive a refused directory", locale)
	}
}

// TestRefreshPromptLocalePicksTheWinningPack keeps the server on the same ordering
// rule the browser uses: priority, then name — the two ends of one rule.
func TestRefreshPromptLocalePicksTheWinningPack(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	s.extensionsDir = t.TempDir()
	t.Cleanup(func() { prompts.SetOverlay("", "") })
	prompts.SetOverlay("", "")

	low := packWithFiles(t, s, "pack-de", "de", true, "base.tmpl")
	low.Manifest.Priority = 20
	if err := s.extStore.Update(low.ID, low); err != nil {
		t.Fatal(err)
	}
	high := packWithFiles(t, s, "pack-ko", "ko", true, "base.tmpl")
	high.Manifest.Priority = 1
	if err := s.extStore.Update(high.ID, high); err != nil {
		t.Fatal(err)
	}

	s.refreshPromptLocale()
	dir, locale := prompts.ActiveOverlay()
	if locale != "ko" {
		t.Errorf("locale = %q, want ko (priority 1 beats 20)", locale)
	}
	if want := filepath.Join(s.extensionsDir, "testdev", "pack-ko", "prompts"); dir != want {
		t.Errorf("templates = %q, want the winning pack's %q", dir, want)
	}
}
