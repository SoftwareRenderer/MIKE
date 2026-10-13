package main

// Tests for the templates compiled into this binary. An embed pattern that stops matching a file is
// invisible until an install loses that file from its own directory and a turn fails.

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"mike/backend/prompts"
)

func TestEmbeddedTemplatesAreTheRepositorySet(t *testing.T) {
	embedded, err := embeddedPromptTemplates()
	if err != nil {
		t.Fatalf("embeddedPromptTemplates: %v", err)
	}
	disk := os.DirFS("prompts")

	onDisk, err := fs.Glob(disk, "*.tmpl")
	if err != nil || len(onDisk) == 0 {
		t.Fatalf("the repository holds %v templates (%v); this test would compare nothing", len(onDisk), err)
	}
	for _, name := range onDisk {
		want, err := fs.ReadFile(disk, name)
		if err != nil {
			t.Fatalf("read %s from the repository: %v", name, err)
		}
		got, err := fs.ReadFile(embedded, name)
		if err != nil {
			t.Errorf("%s is not compiled in, so an install that loses its copy has nothing to render: %v", name, err)
			continue
		}
		if string(got) != string(want) {
			t.Errorf("%s is compiled in with different bytes than the repository holds", name)
		}
	}

	// The binary carries nothing that the repository's prompts directory does not.
	inBinary, err := fs.Glob(embedded, "*")
	if err != nil {
		t.Fatalf("list the compiled-in set: %v", err)
	}
	for _, name := range inBinary {
		if filepath.Ext(name) != ".tmpl" || !slices.Contains(onDisk, name) {
			t.Errorf("the binary carries %q, want exactly the repository's *.tmpl", name)
		}
	}
}

// The case the compiled-in copy exists for: an install with no prompts directory at all.
func TestEveryPromptRendersFromTheBinaryAlone(t *testing.T) {
	// go test runs this package from the repository root, where ./prompts would answer every name.
	t.Chdir(t.TempDir())

	templates, err := embeddedPromptTemplates()
	if err != nil {
		t.Fatalf("embeddedPromptTemplates: %v", err)
	}
	prompts.SetCoreFS(templates)
	t.Cleanup(func() { prompts.SetCoreFS(nil) })

	system, err := prompts.RenderSystemPrompt(prompts.PromptData{
		WorktreePath: "/tmp/kanban-worktree",
		CurrentDate:  "2026-01-02",
		Tools:        []prompts.ToolInfo{{Name: "read_file", Description: "Read the contents of a file."}},
	})
	if err != nil {
		t.Fatalf("RenderSystemPrompt: %v", err)
	}
	if !strings.Contains(system, "expert autonomous coding assistant") {
		t.Errorf("the system prompt does not read like base.tmpl: %.120q", system)
	}
	if !strings.Contains(system, "read_file") {
		t.Errorf("the system prompt lost the tools it was given: %.120q", system)
	}

	conflict, err := prompts.RenderMergeConflictPrompt(prompts.MergeConflictData{TaskBranch: "task-1", TargetBranch: "main"})
	if err != nil {
		t.Fatalf("RenderMergeConflictPrompt: %v", err)
	}
	if !strings.Contains(conflict, `merging "task-1" into "main"`) {
		t.Errorf("the merge prompt lost its branches: %.120q", conflict)
	}

	summarizeSystem, summarizeUser, err := prompts.RenderSummarizePrompt(prompts.SummarizeData{
		ChatHistory: "user: fix the sidebar\n", PreviousSummary: "Moved the count into the store.",
	})
	if err != nil {
		t.Fatalf("RenderSummarizePrompt: %v", err)
	}
	if summarizeSystem == "" || summarizeUser == "" {
		t.Errorf("summarize rendered an empty half: system=%.60q user=%.60q", summarizeSystem, summarizeUser)
	}
	if !strings.Contains(summarizeUser, "fix the sidebar") {
		t.Errorf("the summarize user message lost the history it was given: %.120q", summarizeUser)
	}

	answerSystem, answerUser, err := prompts.RenderAnswerQuestionPrompt(prompts.AnswerQuestionData{
		ParentTitle:  "Fix the sidebar",
		ParentPrompt: "The sidebar count is stale after a move.",
		Questions:    []string{"Which view should own the count?"},
	})
	if err != nil {
		t.Fatalf("RenderAnswerQuestionPrompt: %v", err)
	}
	if answerSystem == "" || !strings.Contains(answerUser, "Which view should own the count?") {
		t.Errorf("the answer prompt lost the question: system=%.60q user=%.60q", answerSystem, answerUser)
	}
}
