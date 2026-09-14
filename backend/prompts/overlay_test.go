package prompts

// Tests for the prompt-template overlay (overlay.go): the pack wins per template,
// core answers for every template the pack did not ship or could not render, and
// with no pack at all nothing changes — the last of these is asserted by comparing
// against what core renders when parsed the plain way, so "byte for byte" is a
// failing test rather than a claim.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"text/template"

	"kanban/pkg/logging"
)

// resetOverlay returns the package to the no-pack state after a test, so overlay
// state cannot leak into the next test in the binary.
func resetOverlay(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { SetOverlay("", "") })
	SetOverlay("", "")
}

// writePack materializes a pack's template directory and returns its path.
func writePack(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", name, err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

// captureLog sends the standard logger into a buffer for the duration of fn and
// returns what was written. The failure policy is "log once, then render English",
// and the only way to test the first half of that sentence is to read it.
func captureLog(t *testing.T, fn func()) string {
	t.Helper()
	buf, restore := logging.TestSink()
	defer restore()
	fn()
	return buf.String()
}

// coreRendered renders one core template the way this package did before the
// overlay existed, so the no-pack path can be held against it.
func coreRendered(t *testing.T, files []string, name string, data any) string {
	t.Helper()
	set, err := template.New("prompts").Funcs(FuncMap()).ParseFiles(files...)
	if err != nil {
		t.Fatalf("parse core: %v", err)
	}
	var buf bytes.Buffer
	if err := set.ExecuteTemplate(&buf, name, data); err != nil {
		t.Fatalf("execute core: %v", err)
	}
	return buf.String()
}

// TestNoOverlayRendersCoreBytesForEveryEntryPoint is the compatibility promise:
// with no pack installed, every entry point answers exactly what parsing core's
// files plainly answers — same bytes, not "similar output".
func TestNoOverlayRendersCoreBytesForEveryEntryPoint(t *testing.T) {
	withRealTemplates(t)
	resetOverlay(t)
	dir := promptDir()

	t.Run("system prompt (act)", func(t *testing.T) {
		got, err := RenderSystemPrompt(baseSample())
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		files, err := filepath.Glob(filepath.Join(dir, "*.tmpl"))
		if err != nil || len(files) == 0 {
			t.Fatalf("core glob: %v (%d files)", err, len(files))
		}
		if want := coreRendered(t, files, "base.tmpl", baseSample()); got != want {
			t.Errorf("no-pack system prompt differs from a plain core render")
		}
	})

	t.Run("system prompt (plan mode)", func(t *testing.T) {
		data := baseSample()
		data.PlanMode = true
		got, err := RenderSystemPrompt(data)
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		files, _ := filepath.Glob(filepath.Join(dir, "*.tmpl"))
		if want := coreRendered(t, files, "base.tmpl", data); got != want {
			t.Errorf("no-pack plan-mode prompt differs from a plain core render")
		}
	})

	t.Run("merge conflict", func(t *testing.T) {
		data := MergeConflictData{TaskBranch: "task/a", TargetBranch: "main", TaskTitle: "T", ConflictedFiles: []string{"x.go"}}
		got, err := RenderMergeConflictPrompt(data)
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		want := coreRendered(t, []string{filepath.Join(dir, "merge_conflict.tmpl")}, "merge_conflict.tmpl", data)
		if got != want {
			t.Errorf("no-pack merge-conflict prompt differs from a plain core render")
		}
	})

	t.Run("summarize pair", func(t *testing.T) {
		data := SummarizeData{ChatHistory: "H", PreviousSummary: "P"}
		sys, user, err := RenderSummarizePrompt(data)
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		if want := coreRendered(t, []string{filepath.Join(dir, "summarize.tmpl")}, "summarize.tmpl", data); sys != want {
			t.Errorf("no-pack summarize system prompt differs from a plain core render")
		}
		if want := coreRendered(t, []string{filepath.Join(dir, "summarize_user.tmpl")}, "summarize_user.tmpl", data); user != want {
			t.Errorf("no-pack summarize user prompt differs from a plain core render")
		}
	})

	t.Run("answer question pair", func(t *testing.T) {
		data := AnswerQuestionData{ParentTitle: "PT", SubtaskTitle: "ST", Questions: []string{"Q1"}}
		sys, user, err := RenderAnswerQuestionPrompt(data)
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		if want := coreRendered(t, []string{filepath.Join(dir, "answer_question.tmpl")}, "answer_question.tmpl", data); sys != want {
			t.Errorf("no-pack answer system prompt differs from a plain core render")
		}
		if want := coreRendered(t, []string{filepath.Join(dir, "answer_question_user.tmpl")}, "answer_question_user.tmpl", data); user != want {
			t.Errorf("no-pack answer user prompt differs from a plain core render")
		}
	})
}

// TestNoOverlayCarriesNoDirective pins that core holds no language instruction at
// all: the answer's language is something a translation pack says in its own
// base.tmpl, never something English asks for on a pack's behalf.
func TestNoOverlayCarriesNoDirective(t *testing.T) {
	withRealTemplates(t)
	resetOverlay(t)

	out, err := RenderSystemPrompt(baseSample())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(out, "Answer in the user's language") {
		t.Errorf("no-pack prompt carries the answer-language directive:\n%.200s", out)
	}
	if !strings.HasPrefix(out, "You are an expert autonomous coding assistant.") {
		t.Errorf("prompt no longer opens with its first line: %.80s", out)
	}
}

// TestNoCoreTemplateReadsTheLocale is the pack contract in both directions: a pack
// that translates the UI and ships no templates leaves the render byte-for-byte English
// core's, and no core template reads the locale the overlay carries.
func TestNoCoreTemplateReadsTheLocale(t *testing.T) {
	withRealTemplates(t)
	resetOverlay(t)

	SetOverlay("", "ko")
	out, err := RenderSystemPrompt(baseSample())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(out, "`ko`") {
		t.Error("core template read the locale; English must not mention a language")
	}
	// Every template is core's, because the pack shipped none — and the locale the
	// overlay carries does not reach any of them.
	files, _ := filepath.Glob(filepath.Join(promptDir(), "*.tmpl"))
	withLocale := baseSample()
	withLocale.Locale = "ko"
	if out != coreRendered(t, files, "base.tmpl", baseSample()) {
		t.Errorf("a pack with no templates changed the templates:\n got %.200q\nwant %.200q", out, coreRendered(t, files, "base.tmpl", baseSample()))
	}
	if coreRendered(t, files, "base.tmpl", withLocale) != coreRendered(t, files, "base.tmpl", baseSample()) {
		t.Error("a core template reads .Locale; English must answer identically whatever the overlay carries")
	}
	plan := baseSample()
	plan.PlanMode = true
	planOut, err := RenderSystemPrompt(plan)
	if err != nil {
		t.Fatalf("plan render: %v", err)
	}
	if !strings.Contains(planOut, "Plan-First Workflow") {
		t.Error("core's plan-mode section must still be there for a locale-only pack")
	}
}

// TestPackTemplateWinsPerTemplate is the overlay rule itself: a same-named pack
// file redefines the core one, and every template the pack said nothing about
// stays core's — a partial pack is the normal state.
func TestPackTemplateWinsPerTemplate(t *testing.T) {
	withRealTemplates(t)
	resetOverlay(t)

	pack := writePack(t, map[string]string{
		"summarize_user.tmpl": "PACKED SUMMARY FOR {{.PreviousSummary}}",
	})
	SetOverlay(pack, "fr")

	// The pack's own template wins…
	_, user, err := RenderSummarizePrompt(SummarizeData{ChatHistory: "H", PreviousSummary: "P"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if user != "PACKED SUMMARY FOR P" {
		t.Errorf("pack template did not win: %q", user)
	}
	// …and the one it never shipped is still core's English.
	dir := promptDir()
	sys, _, err := RenderSummarizePrompt(SummarizeData{ChatHistory: "H", PreviousSummary: "P"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if want := coreRendered(t, []string{filepath.Join(dir, "summarize.tmpl")}, "summarize.tmpl", SummarizeData{ChatHistory: "H", PreviousSummary: "P"}); sys != want {
		t.Errorf("a template the pack is silent about must stay core's:\n got %.200q\nwant %.200q", sys, want)
	}
}

// TestPackBaseTemplateResolvesCoreSubTemplates is the reason the whole core glob
// is parsed for the system prompt: base.tmpl calls {{template "plan-mode" .}}, and
// a translated base.tmpl has to keep working against core's English plan-mode when
// the pack is silent about it. It also pins the trap — the sub-template resolves by
// the name plan-mode.tmpl *defines*, so a pack that translates plan-mode itself has
// to keep {{define "plan-mode"}}.
func TestPackBaseTemplateResolvesCoreSubTemplates(t *testing.T) {
	withRealTemplates(t)
	resetOverlay(t)

	pack := writePack(t, map[string]string{
		"base.tmpl": `PACK BASE mode={{if .PlanMode}}plan{{else}}act{{end}} sub={{template "plan-mode" .}}`,
	})
	SetOverlay(pack, "ja")

	data := baseSample()
	data.PlanMode = true
	out, err := RenderSystemPrompt(data)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.HasPrefix(out, "PACK BASE mode=plan sub=") {
		t.Fatalf("pack base.tmpl did not win: %.80s", out)
	}
	if !strings.Contains(out, "Plan-First Workflow") {
		t.Error("core's plan-mode sub-template did not resolve inside the pack's base.tmpl")
	}
}

// TestPackTemplateThatCannotParseFallsBackToCoreAndLogsOnce is the failure policy for a broken
// translation: core's English answers for that template, the pack's other templates still win, and the
// report is printed once rather than per turn.
func TestPackTemplateThatCannotParseFallsBackToCoreAndLogsOnce(t *testing.T) {
	withRealTemplates(t)
	resetOverlay(t)

	pack := writePack(t, map[string]string{
		// Broken: unclosed action.
		"merge_conflict.tmpl": "BROKEN {{ inc ",
		// Fine: proves one bad file does not cost the pack its good ones.
		"summarize_user.tmpl": "PACKED {{.PreviousSummary}}",
	})
	SetOverlay(pack, "de")

	data := MergeConflictData{TaskBranch: "task/a", TargetBranch: "main", TaskTitle: "T"}
	want := coreRendered(t, []string{filepath.Join(promptDir(), "merge_conflict.tmpl")}, "merge_conflict.tmpl", data)

	var got string
	logged := captureLog(t, func() {
		out, err := RenderMergeConflictPrompt(data)
		if err != nil {
			t.Fatalf("render with a broken pack template: %v", err)
		}
		got = out
	})

	if got != want {
		t.Errorf("broken pack template did not fall back to core:\n got %.200q\nwant %.200q", got, want)
	}
	if n := strings.Count(logged, "core English answers for it"); n != 1 {
		t.Errorf("expected exactly one report, got %d:\n%s", n, logged)
	}
	if !strings.Contains(logged, "merge_conflict.tmpl") {
		t.Errorf("the report must name the template that failed:\n%s", logged)
	}

	// The pack's good template still wins…
	if _, user, err := RenderSummarizePrompt(SummarizeData{PreviousSummary: "P"}); err != nil || user != "PACKED P" {
		t.Errorf("a broken sibling cost the pack its good template: %q err=%v", user, err)
	}
	// …and rendering the broken one again says nothing new.
	again := captureLog(t, func() {
		if _, err := RenderMergeConflictPrompt(data); err != nil {
			t.Fatalf("second render: %v", err)
		}
	})
	if again != "" {
		t.Errorf("the same failure logged twice: %q", again)
	}
}

// TestPackTemplateThatFailsToExecuteFallsBackToCore covers the other failure: a
// template that parses and then breaks on the real turn's data. Losing the turn
// would be the worst possible outcome of somebody's translation.
func TestPackTemplateThatFailsToExecuteFallsBackToCore(t *testing.T) {
	withRealTemplates(t)
	resetOverlay(t)

	pack := writePack(t, map[string]string{
		// Parses cleanly, then fails at execute: no such field on SummarizeData.
		"summarize.tmpl": `BROKEN {{.NoSuchField.Now}}`,
	})
	SetOverlay(pack, "es")

	data := SummarizeData{ChatHistory: "H", PreviousSummary: "P"}
	want := coreRendered(t, []string{filepath.Join(promptDir(), "summarize.tmpl")}, "summarize.tmpl", data)

	var got string
	var err error
	logged := captureLog(t, func() {
		s, _, e := RenderSummarizePrompt(data)
		got, err = s, e
	})
	if err != nil {
		t.Fatalf("an unrenderable pack template must never fail the turn: %v", err)
	}
	if got != want {
		t.Errorf("execute failure did not fall back to core:\n got %.200q\nwant %.200q", got, want)
	}
	if !strings.Contains(logged, "execute") || !strings.Contains(logged, "summarize.tmpl") {
		t.Errorf("expected one execute report naming the template, got %q", logged)
	}
}

// TestPackTemplateCanUseFuncMapFunctions is the parse-at-install/render-at-turn
// split: validateI18N accepts a pack template parsed with FuncMap(), so every
// render entry point has to parse with the same functions or `inc` would parse at
// install and die at render.
func TestPackTemplateCanUseFuncMapFunctions(t *testing.T) {
	withRealTemplates(t)
	resetOverlay(t)

	pack := writePack(t, map[string]string{
		"merge_conflict.tmpl":       "count={{inc 41}}",
		"summarize.tmpl":            "also={{inc 1}}",
		"answer_question.tmpl":      "again={{inc 0}}",
		"answer_question_user.tmpl": "once more={{inc 2}}",
	})
	SetOverlay(pack, "it")

	if out, err := RenderMergeConflictPrompt(MergeConflictData{}); err != nil || out != "count=42" {
		t.Errorf("merge_conflict overlay: %q err=%v", out, err)
	}
	sys, _, err := RenderSummarizePrompt(SummarizeData{})
	if err != nil || sys != "also=2" {
		t.Errorf("summarize overlay: %q err=%v", sys, err)
	}
	qs, qu, err := RenderAnswerQuestionPrompt(AnswerQuestionData{})
	if err != nil || qs != "again=1" || qu != "once more=3" {
		t.Errorf("answer overlay: %q/%q err=%v", qs, qu, err)
	}
}

// TestSetOverlaySwapIsRaceClean runs renders while the overlay changes underneath
// them. Run with -race: a render must read one consistent (dir, locale) pair, and
// the once-only failure report must not be torn by concurrent renderers.
func TestSetOverlaySwapIsRaceClean(t *testing.T) {
	withRealTemplates(t)
	resetOverlay(t)

	packA := writePack(t, map[string]string{"summarize_user.tmpl": "A {{.PreviousSummary}}"})
	packB := writePack(t, map[string]string{"summarize_user.tmpl": "B {{.PreviousSummary}}", "summarize.tmpl": "B broken {{"})

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for n := 0; n < 40; n++ {
				if _, _, err := RenderSummarizePrompt(SummarizeData{PreviousSummary: "P"}); err != nil {
					t.Errorf("render during swap: %v", err)
					return
				}
				_ = ActiveLocale()
			}
		}(i)
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for n := 0; n < 40; n++ {
				if n%2 == 0 {
					SetOverlay(packA, "fr")
				} else {
					SetOverlay(packB, "de")
				}
			}
		}(i)
	}
	wg.Wait()
}

// TestMissingCoreDirStillErrors keeps the old contract: a prompts directory with
// no templates is os.ErrNotExist, overlay or no overlay.
func TestMissingCoreDirStillErrors(t *testing.T) {
	prev := promptDir
	promptDir = func() string { return filepath.Join(t.TempDir(), "nowhere") }
	t.Cleanup(func() { promptDir = prev })
	resetOverlay(t)
	SetOverlay(writePack(t, map[string]string{"base.tmpl": "pack"}), "sv")

	if _, err := RenderSystemPrompt(baseSample()); err == nil {
		t.Fatal("expected an error when core has no templates at all")
	}
}
