package prompts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
		ConflictedFiles: "src/main.go, src/util.go",
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
	// Should instruct to call task_complete
	if !strings.Contains(prompt, "task_complete") {
		t.Error("prompt should instruct to call task_complete")
	}
	// Should instruct to ask about conflicting intent
	if !strings.Contains(prompt, "conflicting intent") {
		t.Error("prompt should instruct to ask about conflicting intent")
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
		ConflictedFiles: "",
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
