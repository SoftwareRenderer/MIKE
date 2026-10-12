package exec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mike/backend/prompts"
	"mike/backend/sandbox"
	"mike/backend/types"
)

// writeProjectSkill puts one package in the project's skill root and returns that root.
func writeProjectSkill(t *testing.T, projectPath, dirName, name string) string {
	t.Helper()
	root := prompts.ProjectSkillsDir(projectPath)
	skillDir := filepath.Join(root, dirName)
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", dirName, err)
	}
	body := "---\nname: " + name + "\ndescription: What this project's skill does\n---\n\nBody.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o600); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	return root
}

// TestComposeSystemPrompt_ListsProjectSkills: the prompt names the project directory, or its mount
// when the task is sandboxed.
func TestComposeSystemPrompt_ListsProjectSkills(t *testing.T) {
	chdirRepoRoot(t)
	task := &types.Task{OperatingMode: types.ModeAct}
	projectDir := t.TempDir()
	root := writeProjectSkill(t, projectDir, "release-check", "release-check")

	host := composeSystemPrompt(task, "/host/worktree", promptScope{projectName: "proj", projectPath: projectDir}, "S")
	if want := filepath.Join(root, "release-check", "SKILL.md"); !strings.Contains(host, want) {
		t.Errorf("host prompt does not list the project skill at %s:\n%s", want, host)
	}

	sandboxed := composeSystemPrompt(task, "/host/worktree",
		promptScope{projectName: "proj", projectPath: projectDir, sandboxEnabled: true}, "S")
	want := filepath.Join(sandbox.ProjectSkillsMount, "release-check", "SKILL.md")
	if !strings.Contains(sandboxed, want) {
		t.Errorf("sandboxed prompt does not list the project skill at %s:\n%s", want, sandboxed)
	}
	if strings.Contains(sandboxed, filepath.Join(root, "release-check", "SKILL.md")) {
		t.Errorf("sandboxed prompt advertises the host path of a project skill:\n%s", sandboxed)
	}
}

// TestComposeSystemPrompt_NoProjectSkills: a project that ships none adds nothing to the list.
func TestComposeSystemPrompt_NoProjectSkills(t *testing.T) {
	chdirRepoRoot(t)
	task := &types.Task{OperatingMode: types.ModeAct}
	prompt := composeSystemPrompt(task, "/host/worktree",
		promptScope{projectName: "proj", projectPath: t.TempDir()}, "S")
	if strings.Contains(prompt, prompts.ProjectSkillsSubdir) {
		t.Errorf("a project with no skill packages was told about one:\n%s", prompt)
	}
	if !strings.Contains(prompt, "/host/worktree") {
		t.Errorf("the prompt stopped rendering:\n%s", prompt)
	}
}
