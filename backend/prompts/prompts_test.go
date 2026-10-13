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
		name        string
		planMode    bool
		exploreMode bool
	}{
		{name: "act mode"},
		{name: "plan mode", planMode: true},
		{name: "explore mode", exploreMode: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := baseSample()
			data.PlanMode = tc.planMode
			data.ExploreMode = tc.exploreMode

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
			if got := strings.Contains(out, "NEVER use create_task unless"); got != tc.exploreMode {
				t.Errorf("explore-mode section present = %v, want %v", got, tc.exploreMode)
			}
			if tc.exploreMode {
				for _, want := range []string{
					"NEVER use create_task unless the user explicitly asked for a separate task to be created.",
					"Run order: children run BEFORE parents.",
				} {
					if !strings.Contains(out, want) {
						t.Errorf("explore-mode section is missing %q", want)
					}
				}
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

func writeSkillDir(t *testing.T, root, dirName, body string) {
	t.Helper()
	skillDir := filepath.Join(root, dirName)
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", dirName, err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", dirName, err)
	}
}

// TestDiscoverSkills reads the frontmatter format the shipped skills actually use
// and keeps the Prefix remap honest: an agent in a sandbox is told to
// read the mounted path, never the host path.
func TestDiscoverSkills(t *testing.T) {
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills")
	skills := map[string]string{
		"plan-first": "---\nname: plan-first\ndescription: \"Quoted, with a comma\"\n---\n\nBody.\n",
		"plain":      "---\nname: plain\ndescription: Bare description\n---\n\nBody.\n",
		"incomplete": "---\nname: incomplete\n---\n\nNo description, so it is not a skill.\n",
	}
	for name, body := range skills {
		writeSkillDir(t, skillsDir, name, body)
	}
	// A directory with no SKILL.md is skipped, not an error.
	if err := os.MkdirAll(filepath.Join(skillsDir, "not-a-skill"), 0o700); err != nil {
		t.Fatalf("mkdir not-a-skill: %v", err)
	}

	got := DiscoverSkills(SkillRoot{Dir: skillsDir, Origin: "bundled"})
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
		if filepath.Base(s.Location) != "SKILL.md" || !strings.HasPrefix(s.Location, skillsDir) {
			t.Errorf("skill %q location %q is not the host SKILL.md under %s", s.Name, s.Location, skillsDir)
		}
	}

	// With a mount configured, every location moves under it.
	mounted := DiscoverSkills(SkillRoot{Dir: skillsDir, Prefix: "/mnt/skills", Origin: "bundled"})
	for _, s := range mounted {
		want := filepath.Join("/mnt/skills", s.Name, "SKILL.md")
		if s.Location != want {
			t.Errorf("mount remap: got %q, want %q", s.Location, want)
		}
	}
}

// TestDiscoverSkillsProjectRoot: the project's own .agents/skills/ is scanned, one level deep only.
func TestDiscoverSkillsProjectRoot(t *testing.T) {
	projectDir := t.TempDir()
	root := ProjectSkillsDir(projectDir)
	writeSkillDir(t, root, "ship-checklist", "---\nname: ship-checklist\ndescription: What to check before a release\n---\n\nBody.\n")
	if err := os.MkdirAll(filepath.Join(root, "ship-checklist", "sub"), 0o700); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "ship-checklist", "sub", "SKILL.md"),
		[]byte("---\nname: nested-only\ndescription: One level only\n---\n"), 0o600); err != nil {
		t.Fatalf("write nested SKILL.md: %v", err)
	}

	host := DiscoverSkills(SkillRoot{Dir: root, Origin: "project"})
	if len(host) != 1 || host[0].Name != "ship-checklist" {
		t.Fatalf("discovered %+v, want only the top-level ship-checklist", host)
	}
	want := filepath.Join(root, "ship-checklist", "SKILL.md")
	if host[0].Location != want {
		t.Errorf("host location %q, want %q", host[0].Location, want)
	}

	sandboxed := DiscoverSkills(SkillRoot{Dir: root, Prefix: "/mnt/project-skills", Origin: "project"})
	if want := filepath.Join("/mnt/project-skills", "ship-checklist", "SKILL.md"); sandboxed[0].Location != want {
		t.Errorf("sandbox location %q, want %q", sandboxed[0].Location, want)
	}

	// A project with no directory of its own contributes nothing, and does not fail the list.
	empty := DiscoverSkills(
		SkillRoot{Dir: filepath.Join(t.TempDir(), "absent"), Origin: "project"},
		SkillRoot{Dir: "", Origin: "project"},
	)
	if len(empty) != 0 {
		t.Errorf("absent roots discovered %+v, want none", empty)
	}
}

// TestDiscoverSkillsCollisionsKeepsBoth: a project's plan-first does not silence the bundled one.
func TestDiscoverSkillsCollisionsKeepsBoth(t *testing.T) {
	bundled := t.TempDir()
	project := t.TempDir()
	writeSkillDir(t, bundled, "plan-first", "---\nname: plan-first\ndescription: Bundled plan workflow\n---\n")
	writeSkillDir(t, project, "alpha", "---\nname: plan-first\ndescription: The project's own plan workflow\n---\n")
	writeSkillDir(t, project, "plan-first", "---\nname: plan-first\ndescription: Same name as its directory\n---\n")
	writeSkillDir(t, project, "zeta", "---\nname: plan-first\ndescription: Another package with that name\n---\n")

	got := DiscoverSkills(
		SkillRoot{Dir: bundled, Origin: "bundled"},
		SkillRoot{Dir: project, Origin: "project"},
	)
	byName := map[string]string{}
	for _, s := range got {
		byName[s.Name] = s.Description
	}
	want := map[string]string{
		"plan-first":           "Bundled plan workflow",
		"plan-first (project)": "The project's own plan workflow",
		"plan-first (2)":       "Same name as its directory",
		"plan-first (zeta)":    "Another package with that name",
	}
	if len(got) != len(want) {
		t.Fatalf("discovered %d of %d documents: %+v", len(got), len(want), got)
	}
	for name, description := range want {
		if byName[name] != description {
			t.Errorf("skill %q carries %q, want %q (each document keeps a name of its own)", name, byName[name], description)
		}
	}
}

// The repository's own bundled skill is what tells every task the plan route exists, and it ships in the
// release package and the image. A frontmatter edit that drops `name` or `description` takes it out of
// every system prompt without failing anything else, so this is the gate that notices.
func TestShippedPlanFirstSkillIsDiscoverable(t *testing.T) {
	dir := filepath.Join("..", "..", "prompts", "skills")
	skills := DiscoverSkills(SkillRoot{Dir: dir, Origin: SkillOriginBundled})

	var found *Skill
	for i, skill := range skills {
		if skill.Name == "plan-first" {
			found = &skills[i]
		}
	}
	if found == nil {
		t.Fatalf("%s discovers %+v, want the shipped plan-first: without it no task is told a plan route exists", dir, skills)
	}
	if found.Description == "" {
		t.Error("the shipped plan-first has no description, which is what a task selects it on")
	}
	if want := filepath.Join("plan-first", "SKILL.md"); !strings.HasSuffix(found.Location, want) {
		t.Errorf("the shipped plan-first is discovered at %q, want a path ending in %q", found.Location, want)
	}
}

// TestDiscoverSkillsSkipsLinkedAndBoundedSKILL: a linked or over-cap SKILL.md is skipped, not read.
func TestDiscoverSkillsSkipsLinkedAndBoundedSKILL(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()

	linked := filepath.Join(root, "linked")
	if err := os.MkdirAll(linked, 0o700); err != nil {
		t.Fatalf("mkdir linked: %v", err)
	}
	writeSkillDir(t, outside, "target", "---\nname: linked\ndescription: Reaches outside the root\n---\n")
	if err := os.Symlink(filepath.Join(outside, "target", "SKILL.md"), filepath.Join(linked, "SKILL.md")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	late := filepath.Join(root, "late")
	if err := os.MkdirAll(late, 0o700); err != nil {
		t.Fatalf("mkdir late: %v", err)
	}
	padding := strings.Repeat("x", skillReadLimit)
	if err := os.WriteFile(filepath.Join(late, "SKILL.md"),
		[]byte(padding+"\n---\nname: late\ndescription: Frontmatter past the cap\n---\n"), 0o600); err != nil {
		t.Fatalf("write late SKILL.md: %v", err)
	}

	if got := DiscoverSkills(SkillRoot{Dir: root, Origin: "project"}); len(got) != 0 {
		t.Fatalf("discovered %+v, want neither the linked nor the over-cap skill", got)
	}

	// A long body after the frontmatter is still a skill.
	writeSkillDir(t, root, "long-body",
		"---\nname: long-body\ndescription: Long body after the frontmatter\n---\n"+strings.Repeat("y", skillReadLimit*2))
	got := DiscoverSkills(SkillRoot{Dir: root, Origin: "project"})
	if len(got) != 1 || got[0].Name != "long-body" {
		t.Fatalf("discovered %+v, want the long-bodied skill alone", got)
	}
}

// TestProjectSkillsDirNamesTheMountedDirectory: the listed directory and the bound one are one path.
func TestProjectSkillsDirNamesTheMountedDirectory(t *testing.T) {
	if got := ProjectSkillsDir("/srv/repos/app"); got != filepath.Join("/srv/repos/app", ProjectSkillsSubdir) {
		t.Errorf("ProjectSkillsDir = %q", got)
	}
	for _, empty := range []string{"", "   "} {
		if got := ProjectSkillsDir(empty); got != "" {
			t.Errorf("ProjectSkillsDir(%q) = %q, want no directory to scan or mount", empty, got)
		}
	}
}
