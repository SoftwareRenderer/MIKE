package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// enTmpl is a miniature of the real prompt set: the same files, the same cross-file defines, the
// same control flow. Prose is short because the checker must never look at it.
var enTmpl = map[string]string{
	"base.tmpl": `You are MIKE, an agent working in a git worktree.

Worktree: {{ .WorktreePath }}
Today: {{ .CurrentDate }}
{{if .PlanMode}}{{template "plan-mode" .}}{{end}}
{{if .ExploreMode}}{{template "explore-mode" .}}{{end}}
Available tools:
{{range .Tools}}{{inc .Name}}. {{ .Name }}: {{ .Description }}
{{end}}
{{if .AgentsMd}}Project rules:
{{ .AgentsMd }}{{end}}
`,
	"plan-mode.tmpl":            "{{define \"plan-mode\"}}You are in plan mode for {{ .WorktreePath }}. Do not edit files.{{end}}\n",
	"explore-mode.tmpl":         "{{define \"explore-mode\"}}You are exploring {{ .WorktreePath }}. Create tasks only when asked.{{end}}\n",
	"merge_conflict.tmpl":       "Resolve {{ .TaskBranch }} into {{ .TargetBranch }}.\n{{range .ConflictedFiles}}- {{ . }}\n{{end}}",
	"summarize.tmpl":            "Summarise the conversation concisely.",
	"summarize_user.tmpl":       "Conversation:\n{{ .Text }}",
	"answer_question.tmpl":      "Answer the question asked.",
	"answer_question_user.tmpl": "Question: {{ .Question }}\n",
}

// zhTmpl is the same templates with the prose in Chinese and every directive untouched.
var zhTmpl = map[string]string{
	"base.tmpl": `你是 MIKE，一个在 git worktree 中工作的 agent。

工作目录：{{ .WorktreePath }}
今天：{{ .CurrentDate }}
{{if .PlanMode}}{{template "plan-mode" .}}{{end}}
{{if .ExploreMode}}{{template "explore-mode" .}}{{end}}
可用工具：
{{range .Tools}}{{inc .Name}}. {{ .Name }}：{{ .Description }}
{{end}}
{{if .AgentsMd}}项目规则：
{{ .AgentsMd }}{{end}}
`,
	"plan-mode.tmpl":            "{{define \"plan-mode\"}}你处于 plan mode，工作目录 {{ .WorktreePath }}。不要修改文件。{{end}}\n",
	"explore-mode.tmpl":         "{{define \"explore-mode\"}}你正在探索 {{ .WorktreePath }}。只有被要求时才创建任务。{{end}}\n",
	"merge_conflict.tmpl":       "将 {{ .TaskBranch }} 合并到 {{ .TargetBranch }}。\n{{range .ConflictedFiles}}- {{ . }}\n{{end}}",
	"summarize.tmpl":            "简要总结当前对话。",
	"summarize_user.tmpl":       "对话内容：\n{{ .Text }}",
	"answer_question.tmpl":      "回答用户提出的问题。",
	"answer_question_user.tmpl": "问题：{{ .Question }}\n",
}

func tmplSetFrom(t *testing.T, bodies map[string]string) *TmplSet {
	t.Helper()
	dir := t.TempDir()
	for name, body := range bodies {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	set, err := LoadTmplSet(dir)
	if err != nil {
		t.Fatalf("LoadTmplSet: %v", err)
	}
	return set
}

func compareTmpl(t *testing.T, enBodies, zhBodies map[string]string) *Findings {
	t.Helper()
	f := &Findings{}
	ComparePromptSets(tmplSetFrom(t, enBodies), tmplSetFrom(t, zhBodies), f, 40)
	return f
}

func TestLoadTmplSetSeesCrossFileDefine(t *testing.T) {
	// base.tmpl invokes a define declared in plan-mode.tmpl and one declared in
	// explore-mode.tmpl; the set must resolve both, and `inc` must parse because
	// prompts.funcs provides it.
	set := tmplSetFrom(t, enTmpl)
	for _, mode := range []string{"plan-mode", "explore-mode"} {
		if !set.Declared[mode] {
			t.Fatalf("define %s not found; declared = %v", mode, set.Declared)
		}
		if !slices.Contains(set.Invoked["base.tmpl"], mode) {
			t.Fatalf("base.tmpl invoke of %s not recorded; invoked = %v", mode, set.Invoked["base.tmpl"])
		}
	}
	if got := set.DefineNames(); len(got) != 2 || got[0] != "explore-mode" || got[1] != "plan-mode" {
		t.Fatalf("DefineNames = %v, want [explore-mode plan-mode]", got)
	}
}

func TestLoadTmplSetReportsParseErrorPerFile(t *testing.T) {
	bad := map[string]string{}
	for k, v := range enTmpl {
		bad[k] = v
	}
	bad["summarize.tmpl"] = "{{if .X}} unclosed"
	dir := t.TempDir()
	for name, body := range bad {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := LoadTmplSet(dir); err == nil {
		t.Fatal("expected a parse error")
	} else if !strings.Contains(err.Error(), "summarize.tmpl") {
		t.Fatalf("error must name the file, got %v", err)
	}
}

func TestLoadTmplSetNoFiles(t *testing.T) {
	if _, err := LoadTmplSet(t.TempDir()); err == nil {
		t.Fatal("expected an error for an empty directory")
	}
}

func TestComparePromptSetsClean(t *testing.T) {
	f := compareTmpl(t, enTmpl, zhTmpl)
	requireNoErrors(t, f)
	if len(f.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", f.Warnings)
	}
}

func TestComparePromptSetsDroppedIf(t *testing.T) {
	// The highest-consequence edit in a prompt overlay: plan mode's instructions quietly vanish.
	mutated := map[string]string{}
	for k, v := range zhTmpl {
		mutated[k] = v
	}
	mutated["base.tmpl"] = strings.Replace(mutated["base.tmpl"], "{{if .PlanMode}}{{template \"plan-mode\" .}}{{end}}", "", 1)

	f := compareTmpl(t, enTmpl, mutated)
	requireErrorContaining(t, f, "base.tmpl: template logic differs")
}

func TestComparePromptSetsDroppedTemplateCall(t *testing.T) {
	mutated := map[string]string{}
	for k, v := range zhTmpl {
		mutated[k] = v
	}
	mutated["base.tmpl"] = strings.Replace(mutated["base.tmpl"], "{{if .PlanMode}}{{template \"plan-mode\" .}}{{end}}", "{{if .PlanMode}}Planning is recommended.{{end}}", 1)

	f := compareTmpl(t, enTmpl, mutated)
	requireErrorContaining(t, f, `plan-mode`)
}

// A pack base.tmpl that stops invoking the explore sub-template leaves that mode without its
// instructions.
func TestComparePromptSetsDroppedExploreCall(t *testing.T) {
	mutated := map[string]string{}
	for k, v := range zhTmpl {
		mutated[k] = v
	}
	mutated["base.tmpl"] = strings.Replace(mutated["base.tmpl"], "{{if .ExploreMode}}{{template \"explore-mode\" .}}{{end}}", "", 1)

	f := compareTmpl(t, enTmpl, mutated)
	requireErrorContaining(t, f, `base.tmpl no longer invokes {{template "explore-mode" .}}`)
}

func TestComparePromptSetsRenamedField(t *testing.T) {
	// `{{ .WorktreePath }}` is PromptData's field; renaming it in the overlay renders nothing.
	mutated := map[string]string{}
	for k, v := range zhTmpl {
		mutated[k] = v
	}
	mutated["base.tmpl"] = strings.Replace(mutated["base.tmpl"], "{{ .WorktreePath }}", "{{ .WorkspacePath }}", 1)

	f := compareTmpl(t, enTmpl, mutated)
	requireErrorContaining(t, f, "WorkspacePath")
}

func TestComparePromptSetsRenamedDefine(t *testing.T) {
	mutated := map[string]string{}
	for k, v := range zhTmpl {
		mutated[k] = v
	}
	mutated["plan-mode.tmpl"] = strings.Replace(mutated["plan-mode.tmpl"], `{{define "plan-mode"}}`, `{{define "plan_mode"}}`, 1)

	f := compareTmpl(t, enTmpl, mutated)
	// A renamed define parses. It fails when base.tmpl renders, which is inside a task.
	requireErrorContaining(t, f, `lost define "plan-mode"`)
	requireErrorContaining(t, f, `calls {{template "plan-mode"}}`)
}

func TestComparePromptSetsRangeBecomesWith(t *testing.T) {
	mutated := map[string]string{}
	for k, v := range zhTmpl {
		mutated[k] = v
	}
	mutated["base.tmpl"] = strings.Replace(mutated["base.tmpl"], "{{range .Tools}}", "{{with .Tools}}", 1)

	f := compareTmpl(t, enTmpl, mutated)
	requireErrorContaining(t, f, "template logic differs")
}

func TestComparePromptSetsMissingAndExtraFile(t *testing.T) {
	missing := map[string]string{}
	for k, v := range zhTmpl {
		missing[k] = v
	}
	delete(missing, "answer_question_user.tmpl")
	f := compareTmpl(t, enTmpl, missing)
	requireErrorContaining(t, f, "answer_question_user.tmpl")

	extra := map[string]string{}
	for k, v := range zhTmpl {
		extra[k] = v
	}
	extra["clever-extra.tmpl"] = "Extra prose {{ .X }}"
	f = compareTmpl(t, enTmpl, extra)
	requireErrorContaining(t, f, "clever-extra.tmpl")
}

func TestComparePromptSetsIdenticalFileWarns(t *testing.T) {
	copied := map[string]string{}
	for k, v := range zhTmpl {
		copied[k] = v
	}
	copied["summarize.tmpl"] = enTmpl["summarize.tmpl"]

	f := compareTmpl(t, enTmpl, copied)
	if len(f.Errors) != 0 {
		t.Fatalf("a copied file is a warning, not an error: %v", f.Errors)
	}
	if !strings.Contains(warnText(f), "copied, not translated") {
		t.Fatalf("expected the copied-file warning, got %v", f.Warnings)
	}
}

func TestComparePromptSetsStaleEnglishList(t *testing.T) {
	// If the contract grows or drops a template, the checker must complain rather than quietly
	// check a subset.
	thinned := map[string]string{}
	for k, v := range enTmpl {
		if k != "merge_conflict.tmpl" {
			thinned[k] = v
		}
	}
	f := compareTmpl(t, thinned, zhTmplWithout(t, "merge_conflict.tmpl"))
	requireErrorContaining(t, f, "out of date")
}

func zhTmplWithout(t *testing.T, name string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for k, v := range zhTmpl {
		if k != name {
			out[k] = v
		}
	}
	return out
}
