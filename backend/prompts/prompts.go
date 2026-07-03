package prompts

import (
	"bytes"
	"os"
	"path/filepath"
	"text/template"
)

// PromptData holds the variables passed to system prompt templates.
type PromptData struct {
	WorktreePath string
	PlanMode     bool
	Tools        []ToolInfo
	AgentsMd     string // contents of AGENTS.md from worktree root, empty if not found
}

// ToolInfo holds the name and description of a tool for rendering in prompts.
type ToolInfo struct {
	Name        string
	Description string
}

// promptDir returns the absolute path to the prompts directory.
// Tries the executable directory first (compiled binary), then falls back
// to the current working directory (go run).
func promptDir() string {
	candidates := []string{}

	// 1. Relative to the running executable (works for compiled binary)
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "prompts"))
	}

	// 2. Current working directory (works for `go run .`)
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, "prompts"))
	}

	// Return the first candidate that actually contains .tmpl files
	for _, dir := range candidates {
		matches, err := filepath.Glob(filepath.Join(dir, "*.tmpl"))
		if err == nil && len(matches) > 0 {
			return dir
		}
	}

	// Last resort: relative path
	return "prompts"
}

// RenderSystemPrompt renders the system prompt from templates loaded from disk.
func RenderSystemPrompt(data PromptData) (string, error) {
	dir := promptDir()
	pattern := filepath.Join(dir, "*.tmpl")

	matches, err := filepath.Glob(pattern)
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", os.ErrNotExist
	}

	tmpl, err := template.ParseFiles(matches...)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}

	return buf.String(), nil
}

// MergeConflictData holds the variables for the merge conflict resolution prompt.
type MergeConflictData struct {
	TaskBranch      string
	TargetBranch    string
	TaskTitle       string
	TaskDescription string
	ConflictedFiles string
}

// RenderMergeConflictPrompt renders the merge conflict resolution prompt from a template file.
func RenderMergeConflictPrompt(data MergeConflictData) (string, error) {
	dir := promptDir()
	path := filepath.Join(dir, "merge_conflict.tmpl")

	tmpl, err := template.ParseFiles(path)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}

	return buf.String(), nil
}

// SummarizeData holds the variables for the context summarization prompt.
type SummarizeData struct {
	ChatHistory     string // formatted conversation history
	PreviousSummary string // summary from a prior summarization, empty if first
}

// RenderSummarizePrompt renders the context summarization system prompt from a template file.
func RenderSummarizePrompt(data SummarizeData) (string, error) {
	dir := promptDir()
	path := filepath.Join(dir, "summarize.tmpl")

	tmpl, err := template.ParseFiles(path)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}

	return buf.String(), nil
}

// ValidateTemplates parses and dry-runs the templates to check for errors.
// Returns the list of template file basenames and any error encountered.
func ValidateTemplates() ([]string, error) {
	dir := promptDir()
	pattern := filepath.Join(dir, "*.tmpl")

	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, os.ErrNotExist
	}

	tmpl, err := template.ParseFiles(matches...)
	if err != nil {
		return nil, err
	}

	// Dry-run render with minimal data to catch execution errors
	data := PromptData{
		WorktreePath: "/tmp/test",
		PlanMode:     true,
		Tools: []ToolInfo{
			{Name: "test_tool", Description: "test description"},
		},
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, err
	}

	// Return basenames of loaded template files
	names := make([]string, len(matches))
	for i, m := range matches {
		names[i] = filepath.Base(m)
	}
	return names, nil
}
