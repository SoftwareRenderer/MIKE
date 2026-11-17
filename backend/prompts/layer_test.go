package prompts

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func useDir(t *testing.T, dir string) {
	t.Helper()
	prev := promptDir
	promptDir = func() string { return dir }
	t.Cleanup(func() { promptDir = prev })
}

func mapFS(files map[string]string) fstest.MapFS {
	out := make(fstest.MapFS, len(files))
	for name, body := range files {
		out[name] = &fstest.MapFile{Data: []byte(body)}
	}
	return out
}

// useCompiledIn installs a stand-in for the compiled-in copy. A test that injects one must take it back
// out, or every later render in this binary reads it.
func useCompiledIn(t *testing.T, files map[string]string) {
	t.Helper()
	SetCoreFS(mapFS(files))
	t.Cleanup(func() { SetCoreFS(nil) })
}

// TestThreeLayersAnswerOneTemplateName: three layers answer one template — the pack's directory, the
// prompts directory beside the binary, the copy compiled into the binary — and they are consulted per
// name, not per directory. What an inner layer holds alone is what it answers; a name no layer holds is
// an error rather than an empty prompt; and an absent directory is only another layer that answers
// nothing.
func TestThreeLayersAnswerOneTemplateName(t *testing.T) {
	render := func() string {
		t.Helper()
		got, err := RenderSystemPrompt(baseSample())
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		return got
	}

	for _, tc := range []struct {
		name   string
		pack   map[string]string
		disk   map[string]string
		absent bool // the directory itself is missing, not just the file
		binary map[string]string
		want   string
	}{
		{
			name:   "the pack beats the directory and the binary",
			pack:   map[string]string{"base.tmpl": "PACK"},
			disk:   map[string]string{"base.tmpl": "DISK"},
			binary: map[string]string{"base.tmpl": "BINARY"},
			want:   "PACK",
		},
		{
			name:   "with no pack the directory answers",
			disk:   map[string]string{"base.tmpl": "DISK"},
			binary: map[string]string{"base.tmpl": "BINARY"},
			want:   "DISK",
		},
		{
			name:   "with no file in the directory the binary answers",
			disk:   map[string]string{"summarize.tmpl": "DISK SUMMARIZE"},
			binary: map[string]string{"base.tmpl": "BINARY"},
			want:   "BINARY",
		},
		{
			name:   "an absent directory leaves the binary answering",
			absent: true,
			binary: map[string]string{"base.tmpl": "BINARY BASE"},
			want:   "BINARY BASE",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "nowhere")
			if !tc.absent {
				dir = writePack(t, tc.disk)
			}
			useDir(t, dir)
			useCompiledIn(t, tc.binary)
			resetOverlay(t)
			if tc.pack != nil {
				SetOverlay(writePack(t, tc.pack), "sv")
			}

			if got := render(); got != tc.want {
				t.Errorf("base.tmpl answered %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("an override shadows only its own name", func(t *testing.T) {
		useDir(t, writePack(t, map[string]string{"base.tmpl": "FROM THE DISK"}))
		useCompiledIn(t, map[string]string{
			"base.tmpl":           "FROM THE BINARY",
			"merge_conflict.tmpl": "BINARY MERGE",
		})
		resetOverlay(t)

		if got := render(); got != "FROM THE DISK" {
			t.Errorf("the overlaid name rendered %q, want the disk copy", got)
		}
		if got, err := RenderMergeConflictPrompt(MergeConflictData{}); err != nil || got != "BINARY MERGE" {
			t.Errorf("a name the directory left alone rendered %q (%v), want the binary's copy", got, err)
		}
		if _, _, err := RenderSummarizePrompt(SummarizeData{}); err == nil {
			t.Error("RenderSummarizePrompt succeeded although neither layer holds summarize.tmpl, want an error")
		}
	})

	// The directory is read on every render, so an install that loses a file mid-life falls through
	// rather than answering from what it last saw.
	t.Run("deleting the disk copy falls through to the binary", func(t *testing.T) {
		dir := writePack(t, map[string]string{"base.tmpl": "DISK"})
		useDir(t, dir)
		useCompiledIn(t, map[string]string{"base.tmpl": "BINARY"})
		resetOverlay(t)

		if got := render(); got != "DISK" {
			t.Fatalf("before the removal: %q, want the directory's template", got)
		}
		if err := os.Remove(filepath.Join(dir, "base.tmpl")); err != nil {
			t.Fatalf("remove: %v", err)
		}
		if got := render(); got != "BINARY" {
			t.Errorf("after deleting the disk copy: %q, want the compiled-in template", got)
		}
	})
}

// fs.FS wants an error for a missing name, and coreGlob's fall-through depends on it.
func TestEmptyLayerAnswersNothingAsMissing(t *testing.T) {
	if _, err := (layerFS{}).Open("base.tmpl"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Open on an empty layer returned %v, want fs.ErrNotExist", err)
	}
}

// Each name is listed once, sorted, because ParseFS parses in this order.
func TestLayerListingUnionsBothLayers(t *testing.T) {
	layer := layerFS{
		top:  mapFS(map[string]string{"base.tmpl": "top", "plan-mode.tmpl": "top"}),
		base: mapFS(map[string]string{"base.tmpl": "base", "summarize.tmpl": "base"}),
	}

	names, err := fs.Glob(layer, "*.tmpl")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if want := "base.tmpl plan-mode.tmpl summarize.tmpl"; strings.Join(names, " ") != want {
		t.Errorf("globbed %v, want %q (each name once, sorted)", names, want)
	}
	body, err := fs.ReadFile(layer, "base.tmpl")
	if err != nil || string(body) != "top" {
		t.Errorf("base.tmpl read %q (%v), want the top layer's bytes", body, err)
	}
}

// failingFS is a file that exists but cannot be read. A mode cannot fake that here: tests run as root.
type failingFS struct{ err error }

func (f failingFS) Open(name string) (fs.File, error) { return nil, f.err }

func TestUnreadableOverrideIsNotSilentlyReplaced(t *testing.T) {
	unreadable := errors.New("permission denied")
	layer := layerFS{
		top:  failingFS{err: unreadable},
		base: mapFS(map[string]string{"base.tmpl": "BINARY"}),
	}

	if _, err := layer.Open("base.tmpl"); !errors.Is(err, unreadable) {
		t.Errorf("Open returned %v, want the override's own failure rather than the binary's copy", err)
	}
}

func TestSetCoreFSSaysWhichCopyAnswers(t *testing.T) {
	useDir(t, writePack(t, map[string]string{"base.tmpl": "DISK", "local-only.tmpl": "DISK"}))

	out := captureLog(t, func() {
		SetCoreFS(mapFS(map[string]string{"base.tmpl": "BINARY", "summarize.tmpl": "BINARY"}))
	})
	t.Cleanup(func() { SetCoreFS(nil) })

	for _, want := range []string{"2 compiled in", "1 overriding"} {
		if !strings.Contains(out, want) {
			t.Errorf("the boot report omits %q; it said: %s", want, out)
		}
	}
	if !strings.Contains(out, "prompts") {
		t.Errorf("the boot report does not name the directory it looked in: %s", out)
	}
}
