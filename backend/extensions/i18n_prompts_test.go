package extensions

// Tests for the prompt side of the active language: which pack's translated prompt
// templates the server renders, and the containment rule that keeps a manifest from
// naming a directory outside its own pack.

import (
	"os"
	"path/filepath"
	"testing"
)

// packAt builds an enabled pack row and materializes <extensionsDir>/<author>/<name>/
// on disk, with `promptsDir` as its declared template directory. It returns the row
// and the host path the declaration is expected to resolve to (which for the refused
// shapes is where the files actually are, outside the pack).
func packAt(t *testing.T, extensionsDir, author, name, locale string, priority int, declared string, layout func(root string)) Extension {
	t.Helper()
	root := filepath.Join(extensionsDir, author, name)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir pack: %v", err)
	}
	if layout != nil {
		layout(root)
	}
	return Extension{
		Name:    name,
		Author:  author,
		Enabled: true,
		Manifest: ExtensionManifest{
			Priority: priority,
			Capabilities: &Capabilities{I18N: &I18NConfig{
				Locale:      locale,
				MessagesURL: "messages.json",
				PromptsDir:  declared,
			}},
		},
	}
}

func withPrompts(root string) {
	dir := filepath.Join(root, "prompts")
	if err := os.MkdirAll(dir, 0o755); err == nil {
		_ = os.WriteFile(filepath.Join(dir, "base.tmpl"), []byte("tradotto"), 0o644)
	}
}

func TestPromptOverlayFor(t *testing.T) {
	t.Parallel()
	// No pack enabled is English in both halves: no templates and no locale.
	got := PromptOverlayFor(nil, t.TempDir())
	if got.Dir != "" || got.Locale != "" {
		t.Errorf("no pack = %+v, want no templates and no locale", got)
	}
	got = PromptOverlayFor([]Extension{{Name: "theme", Enabled: true}}, t.TempDir())
	if got.Dir != "" || got.Locale != "" {
		t.Errorf("a non-pack extension = %+v, want no templates and no locale", got)
	}

	t.Run("a pack that ships no templates still sets the locale", func(t *testing.T) {
		dir := t.TempDir()
		pack := packAt(t, dir, "someone", "pack-ja", "ja", 0, "", nil)
		got := PromptOverlayFor([]Extension{pack}, dir)
		if got.Dir != "" {
			t.Errorf("Dir = %q, want empty for a catalog-only pack", got.Dir)
		}
		if got.Locale != "ja" {
			t.Errorf("Locale = %q, want ja — the directive does not need templates", got.Locale)
		}
	})

	t.Run("a shipped prompts_dir resolves inside the pack", func(t *testing.T) {
		dir := t.TempDir()
		pack := packAt(t, dir, "someone", "pack-it", "it", 0, "prompts", withPrompts)
		got := PromptOverlayFor([]Extension{pack}, dir)
		want := filepath.Join(dir, "someone", "pack-it", "prompts")
		if got.Dir != want {
			t.Errorf("Dir = %q, want %q", got.Dir, want)
		}
		if got.Locale != "it" {
			t.Errorf("Locale = %q, want it", got.Locale)
		}
	})

	t.Run("a nested prompts_dir is fine", func(t *testing.T) {
		dir := t.TempDir()
		pack := packAt(t, dir, "someone", "pack-ko", "ko", 0, "share/prompts", func(root string) {
			if err := os.MkdirAll(filepath.Join(root, "share", "prompts"), 0o755); err != nil {
				t.Fatal(err)
			}
		})
		if got := PromptOverlayFor([]Extension{pack}, dir); got.Dir != filepath.Join(dir, "someone", "pack-ko", "share", "prompts") {
			t.Errorf("nested prompts_dir = %q", got.Dir)
		}
	})

	// The refusal cases all keep the locale and lose the directory: the language is
	// derived from which pack is enabled, while the directory is a path a manifest
	// named, and a named path is only trusted as far as the pack's own folder.
	t.Run("refuses a prompts_dir that escapes the pack", func(t *testing.T) {
		dir := t.TempDir()
		outside := filepath.Join(dir, "somebody-elses-templates")
		if err := os.MkdirAll(outside, 0o755); err != nil {
			t.Fatal(err)
		}
		// packAt creates <dir>/<author>/<name>; `../..` from there is the temp root.
		pack := packAt(t, dir, "someone", "pack-xx", "xx", 0, "../../somebody-elses-templates", nil)
		got := PromptOverlayFor([]Extension{pack}, dir)
		if got.Dir != "" {
			t.Errorf("an escaping prompts_dir resolved to %q", got.Dir)
		}
		if got.Locale != "xx" {
			t.Errorf("Locale = %q, want the locale to survive a refused directory", got.Locale)
		}
	})

	t.Run("refuses an absolute prompts_dir", func(t *testing.T) {
		dir := t.TempDir()
		pack := packAt(t, dir, "someone", "pack-xx", "xx", 0, "/etc", nil)
		if got := PromptOverlayFor([]Extension{pack}, dir); got.Dir != "" {
			t.Errorf("an absolute prompts_dir resolved to %q", got.Dir)
		}
	})

	t.Run("refuses a prompts_dir that is not a directory", func(t *testing.T) {
		dir := t.TempDir()
		pack := packAt(t, dir, "someone", "pack-xx", "xx", 0, "messages.json", func(root string) {
			_ = os.WriteFile(filepath.Join(root, "messages.json"), []byte("{}"), 0o644)
		})
		if got := PromptOverlayFor([]Extension{pack}, dir); got.Dir != "" {
			t.Errorf("a file declared as prompts_dir resolved to %q", got.Dir)
		}
	})

	t.Run("refuses a prompts_dir that is missing", func(t *testing.T) {
		dir := t.TempDir()
		pack := packAt(t, dir, "someone", "pack-xx", "xx", 0, "prompts", nil)
		if got := PromptOverlayFor([]Extension{pack}, dir); got.Dir != "" {
			t.Errorf("a prompts_dir the pack does not ship resolved to %q", got.Dir)
		}
	})

	t.Run("refuses a symlinked prompts_dir that points outside the pack", func(t *testing.T) {
		dir := t.TempDir()
		outside := t.TempDir() // a second, unrelated directory
		if err := os.WriteFile(filepath.Join(outside, "base.tmpl"), []byte("not yours"), 0o644); err != nil {
			t.Fatal(err)
		}
		pack := packAt(t, dir, "someone", "pack-xx", "xx", 0, "prompts", func(root string) {
			if err := os.Symlink(outside, filepath.Join(root, "prompts")); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
		})
		got := PromptOverlayFor([]Extension{pack}, dir)
		if got.Dir != "" {
			t.Errorf("a symlink out of the pack resolved to %q — the lexical check alone is not the enforcement", got.Dir)
		}
		if got.Locale != "xx" {
			t.Errorf("Locale = %q, want the locale to survive", got.Locale)
		}
	})

	t.Run("the winning pack's directory is the one resolved", func(t *testing.T) {
		dir := t.TempDir()
		loser := packAt(t, dir, "someone", "pack-fr", "fr", 10, "prompts", withPrompts)
		winner := packAt(t, dir, "someone", "pack-ja", "ja", 1, "prompts", withPrompts)
		got := PromptOverlayFor([]Extension{loser, winner}, dir)
		if got.Locale != "ja" || got.Dir != filepath.Join(dir, "someone", "pack-ja", "prompts") {
			t.Errorf("overlay = %+v, want the priority-1 pack", got)
		}
	})

	t.Run("a disabled pack that would win contributes nothing", func(t *testing.T) {
		dir := t.TempDir()
		disabled := packAt(t, dir, "someone", "pack-ja", "ja", 1, "prompts", withPrompts)
		disabled.Enabled = false
		enabled := packAt(t, dir, "someone", "pack-fr", "fr", 9, "prompts", withPrompts)
		got := PromptOverlayFor([]Extension{disabled, enabled}, dir)
		if got.Locale != "fr" || got.Dir != filepath.Join(dir, "someone", "pack-fr", "prompts") {
			t.Errorf("overlay = %+v, want the enabled pack only", got)
		}
	})
}

// TestPromptDirInPackSymlinkedPackDir is the other direction of the same rule: a pack
// *directory* that is a symlink must not become a pass to the host, because the
// resolved path is what the template reader would be handed.
func TestPromptDirInPackSymlinkedPackDir(t *testing.T) {
	t.Parallel()
	realRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(realRoot, "prompts"), 0o755); err != nil {
		t.Fatal(err)
	}
	extensionsDir := t.TempDir()
	link := filepath.Join(extensionsDir, "someone", "pack-zh")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realRoot, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	// Both the link target and the pack root resolve inside… so containment holds
	// against the *resolved* pack directory, which is the honest answer here.
	got, ok := PromptDirInPack(link, "prompts")
	if !ok {
		t.Fatal("a symlinked pack directory whose target holds the prompts dir should resolve")
	}
	want, err := filepath.EvalSymlinks(filepath.Join(realRoot, "prompts"))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("got %q, want the symlink-free %q", got, want)
	}
	// And an escape through that same link is still refused.
	if _, ok := PromptDirInPack(link, "../prompts"); ok {
		t.Error("a relative escape out of the pack must not resolve")
	}
}
