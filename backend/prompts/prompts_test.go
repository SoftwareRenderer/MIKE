package prompts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"
)

// findTemplateFile walks up from the current directory to find the merge_conflict.tmpl.
func findTemplateFile() (string, bool) {
	dir, _ := os.Getwd()
	for i := 0; i < 10; i++ {
		candidate := filepath.Join(dir, "prompts", "merge_conflict.tmpl")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", false
}

// TestRenderMergeConflictPrompt_TaskToTarget verifies that the merge conflict
// prompt template renders the correct instructions for task→target direction.
func TestRenderMergeConflictPrompt_TaskToTarget(t *testing.T) {
	tmplPath, ok := findTemplateFile()
	if !ok {
		t.Skip("merge_conflict.tmpl not found (run from project root)")
	}

	tmpl, err := template.ParseFiles(tmplPath)
	if err != nil {
		t.Fatalf("parse template: %v", err)
	}

	data := MergeConflictData{
		TaskBranch:      "task/feature",
		TargetBranch:    "main",
		TaskTitle:       "Add feature",
		TaskDescription: "Implement the feature",
		ConflictedFiles: "FILE: test.go\n---BEGIN---\ncontent\n---END---",
		Direction:       "task_to_target",
	}

	var buf strings.Builder
	if err := tmpl.Execute(&buf, data); err != nil {
		t.Fatalf("execute template: %v", err)
	}

	// Should say "prefer the task branch's changes"
	if !strings.Contains(buf.String(), "Prefer the task branch's changes") {
		t.Error("task_to_target prompt should instruct to prefer task branch changes")
	}
}

// TestRenderMergeConflictPrompt_TargetToTask verifies that the merge conflict
// prompt template renders the correct instructions for target→task direction.
func TestRenderMergeConflictPrompt_TargetToTask(t *testing.T) {
	tmplPath, ok := findTemplateFile()
	if !ok {
		t.Skip("merge_conflict.tmpl not found (run from project root)")
	}

	tmpl, err := template.ParseFiles(tmplPath)
	if err != nil {
		t.Fatalf("parse template: %v", err)
	}

	data := MergeConflictData{
		TaskBranch:      "task/feature",
		TargetBranch:    "main",
		TaskTitle:       "Add feature",
		TaskDescription: "Implement the feature",
		ConflictedFiles: "FILE: test.go\n---BEGIN---\ncontent\n---END---",
		Direction:       "target_to_task",
	}

	var buf strings.Builder
	if err := tmpl.Execute(&buf, data); err != nil {
		t.Fatalf("execute template: %v", err)
	}

	// Should say "prefer the target branch's changes"
	if !strings.Contains(buf.String(), "Prefer the target branch's changes") {
		t.Error("target_to_task prompt should instruct to prefer target branch changes")
	}
	// Should mention adapting task changes to work with target
	if !strings.Contains(buf.String(), "Adapt the task's changes") {
		t.Error("target_to_task prompt should instruct to adapt task changes")
	}
}
