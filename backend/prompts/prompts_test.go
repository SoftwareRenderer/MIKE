package prompts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- Skill Frontmatter Parsing Tests ---

func TestParseSkillFrontmatter(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantName   string
		wantDesc   string
		wantNoName bool
		wantNoDesc bool
	}{
		{
			name: "basic frontmatter",
			input: `---
name: my-skill
description: Does something useful.
---

# My Skill

Content here.
`,
			wantName: "my-skill",
			wantDesc: "Does something useful.",
		},
		{
			name: "quoted values",
			input: `---
name: "quoted-skill"
description: "A skill with quotes."
---

Content.
`,
			wantName: "quoted-skill",
			wantDesc: "A skill with quotes.",
		},
		{
			name: "extra frontmatter fields",
			input: `---
name: full-skill
description: Full featured skill.
license: MIT
compatibility: go 1.21+
---

Content.
`,
			wantName: "full-skill",
			wantDesc: "Full featured skill.",
		},
		{
			name: "missing name",
			input: `---
description: Has description but no name.
---
`,
			wantNoName: true,
			wantDesc:   "Has description but no name.",
		},
		{
			name: "missing description",
			input: `---
name: no-desc-skill
---
`,
			wantName:   "no-desc-skill",
			wantNoDesc: true,
		},
		{
			name:       "no frontmatter at all",
			input:      "# Just markdown content",
			wantNoName: true,
			wantNoDesc: true,
		},
		{
			name:       "empty frontmatter",
			input:      "---\n---\nContent",
			wantNoName: true,
			wantNoDesc: true,
		},
		{
			name: "whitespace in values",
			input: `---
name:   spaced-skill   
description:   Spaced description   
---
`,
			wantName: "spaced-skill",
			wantDesc: "Spaced description",
		},
		{
			name:     "crlf line endings",
			input:    "---\r\nname: crlf-skill\r\ndescription: CRLF skill.\r\n---\r\nContent",
			wantName: "crlf-skill",
			wantDesc: "CRLF skill.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, desc := parseSkillFrontmatter([]byte(tt.input))
			if tt.wantNoName && name != "" {
				t.Errorf("expected empty name, got %q", name)
			}
			if !tt.wantNoName && name != tt.wantName {
				t.Errorf("name = %q, want %q", name, tt.wantName)
			}
			if tt.wantNoDesc && desc != "" {
				t.Errorf("expected empty description, got %q", desc)
			}
			if !tt.wantNoDesc && desc != tt.wantDesc {
				t.Errorf("description = %q, want %q", desc, tt.wantDesc)
			}
		})
	}
}

// --- DiscoverSkills Tests ---

func TestDiscoverSkills(t *testing.T) {
	// Create a temp directory structure mimicking prompts/skills/
	tmpDir := t.TempDir()
	skillsDir := filepath.Join(tmpDir, "skills")

	// Skill 1: valid skill
	skill1Dir := filepath.Join(skillsDir, "alpha-skill")
	os.MkdirAll(skill1Dir, 0755)
	os.WriteFile(filepath.Join(skill1Dir, "SKILL.md"), []byte(`---
name: alpha-skill
description: Alpha does alpha things.
---

# Alpha Skill

Content.
`), 0644)

	// Skill 2: valid skill with extra fields
	skill2Dir := filepath.Join(skillsDir, "beta-skill")
	os.MkdirAll(skill2Dir, 0755)
	os.WriteFile(filepath.Join(skill2Dir, "SKILL.md"), []byte(`---
name: beta-skill
description: Beta does beta things.
license: MIT
---

# Beta
`), 0644)

	// Skill 3: missing description (should be skipped)
	skill3Dir := filepath.Join(skillsDir, "gamma-skill")
	os.MkdirAll(skill3Dir, 0755)
	os.WriteFile(filepath.Join(skill3Dir, "SKILL.md"), []byte(`---
name: gamma-skill
---

No description.
`), 0644)

	// Skill 4: no SKILL.md (should be skipped)
	skill4Dir := filepath.Join(skillsDir, "delta-skill")
	os.MkdirAll(skill4Dir, 0755)

	// Non-directory file (should be skipped)
	os.WriteFile(filepath.Join(skillsDir, "not-a-dir.md"), []byte("ignore me"), 0644)

	// Override promptDir to use our temp dir
	origPromptDir := promptDir
	promptDir = func() string { return tmpDir }
	defer func() { promptDir = origPromptDir }()

	skills := DiscoverSkills("")

	// Should find 2 valid skills (alpha and beta), sorted by name
	if len(skills) != 2 {
		t.Fatalf("expected 2 skills, got %d", len(skills))
	}

	if skills[0].Name != "alpha-skill" {
		t.Errorf("first skill name = %q, want %q", skills[0].Name, "alpha-skill")
	}
	if skills[0].Description != "Alpha does alpha things." {
		t.Errorf("first skill desc = %q", skills[0].Description)
	}

	if skills[1].Name != "beta-skill" {
		t.Errorf("second skill name = %q, want %q", skills[1].Name, "beta-skill")
	}
	if skills[1].Description != "Beta does beta things." {
		t.Errorf("second skill desc = %q", skills[1].Description)
	}
}

func TestDiscoverSkills_NoDirectory(t *testing.T) {
	// Override promptDir to return a dir with no skills/ subdirectory
	tmpDir := t.TempDir()
	origPromptDir := promptDir
	promptDir = func() string { return tmpDir }
	defer func() { promptDir = origPromptDir }()

	skills := DiscoverSkills("")
	if skills != nil {
		t.Errorf("expected nil when no skills dir exists, got %d skills", len(skills))
	}
}

func TestDiscoverSkills_ContainerMount(t *testing.T) {
	// Create a temp directory with a skill
	tmpDir := t.TempDir()
	skillsDir := filepath.Join(tmpDir, "skills")
	skillDir := filepath.Join(skillsDir, "my-skill")
	os.MkdirAll(skillDir, 0755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: my-skill
description: A test skill.
---

Content.
`), 0644)

	origPromptDir := promptDir
	promptDir = func() string { return tmpDir }
	defer func() { promptDir = origPromptDir }()

	// Without container mount: uses host path
	skills := DiscoverSkills("")
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	if !filepath.IsAbs(skills[0].Location) || skills[0].Location == "/mnt/skills/my-skill/SKILL.md" {
		t.Errorf("host mode: expected host path, got %q", skills[0].Location)
	}

	// With container mount: uses container path
	skills = DiscoverSkills("/mnt/skills")
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	if skills[0].Location != "/mnt/skills/my-skill/SKILL.md" {
		t.Errorf("sandbox mode: location = %q, want %q", skills[0].Location, "/mnt/skills/my-skill/SKILL.md")
	}
}

// findProjectRoot walks up from the current directory to find the project root
// by looking for the prompts/merge_conflict.tmpl file.
func findProjectRoot() (string, bool) {
	dir, _ := os.Getwd()
	for i := 0; i < 10; i++ {
		candidate := filepath.Join(dir, "prompts", "merge_conflict.tmpl")
		if _, err := os.Stat(candidate); err == nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", false
}

// TestRenderMergeConflictPrompt verifies that the merge conflict
// prompt template renders correctly with all fields populated.
func TestRenderMergeConflictPrompt(t *testing.T) {
	root, ok := findProjectRoot()
	if !ok {
		t.Skip("merge_conflict.tmpl not found (run from project root)")
	}

	// Change to project root so promptDir() finds the template
	origWd, _ := os.Getwd()
	os.Chdir(root)
	defer os.Chdir(origWd)

	data := MergeConflictData{
		TaskBranch:      "task/feature",
		TargetBranch:    "main",
		TaskTitle:       "Add feature",
		TaskDescription: "Implement the feature",
		ConflictedFiles: []string{"src/main.go", "src/util.go"},
	}

	prompt, err := RenderMergeConflictPrompt(data)
	if err != nil {
		t.Fatalf("render prompt: %v", err)
	}

	// Should include branch names
	if !strings.Contains(prompt, "task/feature") {
		t.Error("prompt should include task branch name")
	}
	if !strings.Contains(prompt, "main") {
		t.Error("prompt should include target branch name")
	}
	// Should include task context
	if !strings.Contains(prompt, "Add feature") {
		t.Error("prompt should include task title")
	}
	// Should include conflicted files
	if !strings.Contains(prompt, "src/main.go") {
		t.Error("prompt should include conflicted files")
	}
	// Should instruct to use tools
	if !strings.Contains(prompt, "your tools") {
		t.Error("prompt should instruct to use tools")
	}
	// Should instruct to call request_review
	if !strings.Contains(prompt, "request_review") {
		t.Error("prompt should instruct to call request_review")
	}
	// Should instruct to ask about conflicting intent
	if !strings.Contains(prompt, "conflicting intent") {
		t.Error("prompt should instruct to ask about conflicting intent")
	}
}

func TestRenderAnswerQuestionPrompt(t *testing.T) {
	root, ok := findProjectRoot()
	if !ok {
		t.Skip("answer_question.tmpl not found (run from project root)")
	}
	origWd, _ := os.Getwd()
	os.Chdir(root)
	defer os.Chdir(origWd)

	prompt, err := RenderAnswerQuestionPrompt(AnswerQuestionData{
		ParentTitle:  "Build Auth",
		ParentPrompt: "Implement login with OAuth.",
		SubtaskTitle: "Add login endpoint",
		Questions:    []string{"Which auth flow?", "Token expiry?"},
	})
	if err != nil {
		t.Fatalf("render prompt: %v", err)
	}
	if !strings.Contains(prompt, "Build Auth") {
		t.Error("prompt should include parent task title")
	}
	if !strings.Contains(prompt, "Implement login with OAuth.") {
		t.Error("prompt should include parent task prompt context")
	}
	if !strings.Contains(prompt, "Which auth flow?") {
		t.Error("prompt should include the first question")
	}
	if !strings.Contains(prompt, "Token expiry?") {
		t.Error("prompt should include the second question")
	}
	if !strings.Contains(prompt, "1) answer to first question 2) answer to second") {
		t.Error("prompt should specify the simple numbered answer format")
	}
}

// TestRenderSystemPromptStillBase verifies that adding answer_question.tmpl does
// not hijack the main system prompt — RenderSystemPrompt must render base.tmpl.
func TestRenderSystemPromptStillBase(t *testing.T) {
	root, ok := findProjectRoot()
	if !ok {
		t.Skip("templates not found (run from project root)")
	}
	origWd, _ := os.Getwd()
	os.Chdir(root)
	defer os.Chdir(origWd)

	prompt, err := RenderSystemPrompt(PromptData{
		WorktreePath: "/tmp",
		CurrentDate:  "2026-01-01",
		Tools:        []ToolInfo{{Name: "bash", Description: "run a command"}},
	})
	if err != nil {
		t.Fatalf("render system prompt: %v", err)
	}
	if !strings.HasPrefix(prompt, "You are an expert autonomous coding assistant.") {
		t.Errorf("system prompt should start with the base template, got: %.80q", prompt)
	}
}

// TestRenderMergeConflictPrompt_EmptyConflictedFiles verifies that the template
// renders correctly when no conflicted files are provided.
func TestRenderMergeConflictPrompt_EmptyConflictedFiles(t *testing.T) {
	root, ok := findProjectRoot()
	if !ok {
		t.Skip("merge_conflict.tmpl not found (run from project root)")
	}

	// Change to project root so promptDir() finds the template
	origWd, _ := os.Getwd()
	os.Chdir(root)
	defer os.Chdir(origWd)

	data := MergeConflictData{
		TaskBranch:      "task/feature",
		TargetBranch:    "main",
		TaskTitle:       "Add feature",
		TaskDescription: "Implement the feature",
		ConflictedFiles: nil,
	}

	prompt, err := RenderMergeConflictPrompt(data)
	if err != nil {
		t.Fatalf("render prompt: %v", err)
	}

	// Should not have a blank "Conflicted files:" line
	if strings.Contains(prompt, "Conflicted files: ") {
		t.Error("prompt should not include empty conflicted files line")
	}
}
