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
}

// ToolInfo holds the name and description of a tool for rendering in prompts.
type ToolInfo struct {
	Name        string
	Description string
}

// promptDir returns the absolute path to the prompts directory.
// Resolved relative to the running executable.
func promptDir() string {
	exe, err := os.Executable()
	if err != nil {
		// Fallback: assume prompts/ is a sibling of backend/
		return filepath.Join("prompts")
	}
	return filepath.Join(filepath.Dir(exe), "prompts")
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
