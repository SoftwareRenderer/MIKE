package prompts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withRealTemplates points promptDir at the repository's prompts/ directory for the test.
// Under `go test` the working directory is this package, which holds no .tmpl files, so
// without this the resolver finds nothing to render.
func withRealTemplates(t *testing.T) {
	t.Helper()
	prev := promptDir
	promptDir = func() string {
		abs, err := filepath.Abs(filepath.Join("..", "..", "prompts"))
		if err != nil {
			t.Fatalf("resolve prompts dir: %v", err)
		}
		return abs
	}
	t.Cleanup(func() { promptDir = prev })
}

// baseSample is one PromptData carrying every field a template can read, so a render that
// reaches the end has exercised each branch's data path.
func baseSample() PromptData {
	return PromptData{
		WorktreePath: "/tmp/kanban-worktree",
		CurrentDate:  "2026-01-02",
		Tools:        []ToolInfo{{Name: "read_file", Description: "Read the contents of a file."}},
		AgentsMd:     "Never commit.",
		Skills: []Skill{{
			Name:        "plan-first",
			Description: "Structured planning workflow.",
			Location:    "/mnt/skills/plan-first/SKILL.md",
		}},
		Summary: "Earlier work: added the session list.",
	}
}

// TestRenderSystemPrompt_ShippedTemplates renders the templates as shipped: a template that
// no longer parses or executes now fails `go test` instead of the first LLM call of a real
// task.
func TestRenderSystemPrompt_ShippedTemplates(t *testing.T) {
	withRealTemplates(t)

	for _, tc := range []struct {
		name     string
		planMode bool
	}{
		{name: "act mode", planMode: false},
		{name: "plan mode", planMode: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := baseSample()
			data.PlanMode = tc.planMode

			out, err := RenderSystemPrompt(data)
			if err != nil {
				t.Fatalf("RenderSystemPrompt: %v", err)
			}
			if out == "" {
				t.Fatal("rendered an empty system prompt")
			}

			// Every field the base template reads has to come through: a typo in
			// a field name renders as nothing rather than failing, which is how
			// a prompt silently loses its tools.
			for _, want := range []string{
				"read_file",
				"Read the contents of a file.",
				"plan-first",
				"Structured planning workflow.",
				"/mnt/skills/plan-first/SKILL.md",
				"/tmp/kanban-worktree",
				"2026-01-02",
				"Never commit.",
				"Earlier work: added the session list.",
			} {
				if !strings.Contains(out, want) {
					t.Errorf("prompt is missing %q", want)
				}
			}

			// The mode branch is the reason base.tmpl is rendered by name:
			// Execute() would run the alphabetically-first file instead and a
			// new template could hijack the system prompt.
			if got := strings.Contains(out, "Plan-First Workflow"); got != tc.planMode {
				t.Errorf("plan-mode section present = %v, want %v", got, tc.planMode)
			}
		})
	}
}

// TestRenderHelpers_ShippedTemplates covers the other four renderers for the
// same reason: they are built from separate files and parse nothing else, so
// nothing else loads them until a task hits that code path.
func TestRenderHelpers_ShippedTemplates(t *testing.T) {
	withRealTemplates(t)

	t.Run("merge conflict", func(t *testing.T) {
		out, err := RenderMergeConflictPrompt(MergeConflictData{
			TaskBranch:      "task/alpha",
			TargetBranch:    "main",
			ReviewContext:   "Listed the signed-in sessions with revoke buttons",
			ConflictedFiles: []string{"backend/server/handler_sessions.go"},
		})
		if err != nil {
			t.Fatalf("RenderMergeConflictPrompt: %v", err)
		}
		for _, want := range []string{"task/alpha", "main", "Listed the signed-in sessions with revoke buttons", "handler_sessions.go"} {
			if !strings.Contains(out, want) {
				t.Errorf("merge conflict prompt is missing %q", want)
			}
		}
		if strings.Contains(out, "Task title:") || strings.Contains(out, "Task description:") {
			t.Errorf("review context did not replace the task title and description:\n%s", out)
		}
	})

	t.Run("merge conflict without a review", func(t *testing.T) {
		// Nothing reviewed yet: the title and description are what the resolver gets.
		out, err := RenderMergeConflictPrompt(MergeConflictData{
			TaskBranch:      "task/alpha",
			TargetBranch:    "main",
			TaskTitle:       "Add sessions",
			TaskDescription: "List the signed-in sessions",
			ConflictedFiles: []string{"backend/server/handler_sessions.go"},
		})
		if err != nil {
			t.Fatalf("RenderMergeConflictPrompt: %v", err)
		}
		for _, want := range []string{"Add sessions", "List the signed-in sessions"} {
			if !strings.Contains(out, want) {
				t.Errorf("fallback prompt is missing %q", want)
			}
		}
	})

	t.Run("summarize pair", func(t *testing.T) {
		system, user, err := RenderSummarizePrompt(SummarizeData{
			ChatHistory:     "user: hello\nassistant: hi",
			PreviousSummary: "Built the board.",
		})
		if err != nil {
			t.Fatalf("RenderSummarizePrompt: %v", err)
		}
		if system == "" {
			t.Error("summarize system prompt is empty")
		}
		// The history and the prior summary ride on the user message.
		for _, want := range []string{"hello", "Built the board."} {
			if !strings.Contains(user, want) {
				t.Errorf("summarize user message is missing %q", want)
			}
		}
	})

	t.Run("answer question pair", func(t *testing.T) {
		system, user, err := RenderAnswerQuestionPrompt(AnswerQuestionData{
			ParentTitle:  "Ship sessions",
			ParentPrompt: "Add the revoke endpoint",
			Questions:    []string{"Which column?", "Auth by cookie?"},
		})
		if err != nil {
			t.Fatalf("RenderAnswerQuestionPrompt: %v", err)
		}
		if system == "" {
			t.Error("answer-question system prompt is empty")
		}
		// Questions go in the user message because some chat templates reject
		// system-only requests, so a render that drops them breaks the feature.
		for _, want := range []string{"Which column?", "Auth by cookie?"} {
			if !strings.Contains(user, want) {
				t.Errorf("answer-question user message is missing %q", want)
			}
		}
	})
}

// TestDiscoverSkills reads the frontmatter format the shipped skills actually use
// and keeps the containerMount remap honest: an agent in a sandbox is told to
// read the mounted path, never the host path.
func TestDiscoverSkills(t *testing.T) {
	dir := t.TempDir()
	skills := map[string]string{
		"plan-first": "---\nname: plan-first\ndescription: \"Quoted, with a comma\"\n---\n\nBody.\n",
		"plain":      "---\nname: plain\ndescription: Bare description\n---\n\nBody.\n",
		"incomplete": "---\nname: incomplete\n---\n\nNo description, so it is not a skill.\n",
	}
	for name, body := range skills {
		skillDir := filepath.Join(dir, "skills", name)
		if err := os.MkdirAll(skillDir, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	// A directory with no SKILL.md is skipped, not an error.
	if err := os.MkdirAll(filepath.Join(dir, "skills", "not-a-skill"), 0o700); err != nil {
		t.Fatalf("mkdir not-a-skill: %v", err)
	}

	prev := promptDir
	promptDir = func() string { return dir }
	t.Cleanup(func() { promptDir = prev })

	got := DiscoverSkills("")
	if len(got) != 2 {
		t.Fatalf("discovered %d skills, want 2 (frontmatter-less dirs are skipped): %+v", len(got), got)
	}
	byName := map[string]Skill{}
	for _, s := range got {
		byName[s.Name] = s
	}
	if s, ok := byName["plan-first"]; !ok || s.Description != "Quoted, with a comma" {
		t.Errorf("quoted frontmatter description not unwrapped: %+v", s)
	}
	if s, ok := byName["plain"]; !ok || s.Description != "Bare description" {
		t.Errorf("bare frontmatter description mangled: %+v", s)
	}
	for _, s := range got {
		if filepath.Base(s.Location) != "SKILL.md" || !strings.HasPrefix(s.Location, dir) {
			t.Errorf("skill %q location %q is not the host SKILL.md under %s", s.Name, s.Location, dir)
		}
	}

	// With a mount configured, every location moves under it.
	mounted := DiscoverSkills("/mnt/skills")
	for _, s := range mounted {
		want := filepath.Join("/mnt/skills", s.Name, "SKILL.md")
		if s.Location != want {
			t.Errorf("mount remap: got %q, want %q", s.Location, want)
		}
	}
}
