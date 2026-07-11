package prompts

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

// PromptData holds the variables passed to system prompt templates.
type PromptData struct {
	WorktreePath string
	CurrentDate  string // e.g. "2025-07-08"
	PlanMode     bool
	Tools        []ToolInfo
	AgentsMd     string  // contents of AGENTS.md from worktree root, empty if not found
	Skills       []Skill // discovered skills with name, description, and location
	Summary      string  // prior conversation summary
}

// ToolInfo holds the name and description of a tool for rendering in prompts.
type ToolInfo struct {
	Name        string
	Description string
}

// Skill holds the metadata of a discovered skill for rendering in prompts.
type Skill struct {
	Name        string // skill name from frontmatter
	Description string // skill description from frontmatter
	Location    string // path to the SKILL.md file
}

// promptDir is a function variable that returns the absolute path to the prompts directory.
// It can be overridden in tests. By default it tries the executable directory first,
// then falls back to the current working directory.
var promptDir = func() string {
	return resolvePromptDir()
}

// resolvePromptDir is the default implementation of promptDir.
// Tries the executable directory first (compiled binary), then falls back
// to the current working directory (go run).
func resolvePromptDir() string {
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

// SkillsDir returns the absolute path to the prompts/skills/ directory.
// Returns empty string if the directory does not exist.
func SkillsDir() string {
	skillsDir := filepath.Join(promptDir(), "skills")
	if _, err := os.Stat(skillsDir); err != nil {
		return ""
	}
	return skillsDir
}

// DiscoverSkills scans the prompts/skills/ directory for skill packages.
// Each skill is a directory containing a SKILL.md file with YAML frontmatter.
// If containerMount is non-empty (e.g. "/mnt/skills"), skill locations use
// that mount path instead of the host filesystem path.
// Returns a sorted slice of Skill metadata (name, description, location).
func DiscoverSkills(containerMount string) []Skill {
	dir := promptDir()
	skillsDir := filepath.Join(dir, "skills")

	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		return nil // no skills directory is not an error
	}

	var skills []Skill
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skillPath := filepath.Join(skillsDir, entry.Name(), "SKILL.md")
		data, err := os.ReadFile(skillPath)
		if err != nil {
			continue // skip directories without SKILL.md
		}

		name, description := parseSkillFrontmatter(data)
		if name == "" || description == "" {
			continue // skip skills missing required frontmatter fields
		}

		loc := skillPath
		if containerMount != "" {
			loc = filepath.Join(containerMount, entry.Name(), "SKILL.md")
		}

		skills = append(skills, Skill{
			Name:        name,
			Description: description,
			Location:    loc,
		})
	}

	// Sort by name for deterministic output
	for i := 0; i < len(skills); i++ {
		for j := i + 1; j < len(skills); j++ {
			if skills[i].Name > skills[j].Name {
				skills[i], skills[j] = skills[j], skills[i]
			}
		}
	}

	return skills
}

// parseSkillFrontmatter extracts the name and description from YAML frontmatter
// in a SKILL.md file. Expects frontmatter delimited by --- at the start of the file.
func parseSkillFrontmatter(data []byte) (name, description string) {
	text := string(data)

	// Must start with ---
	if !strings.HasPrefix(text, "---") {
		return "", ""
	}

	// Find the closing ---
	rest := text[3:]
	// Handle \r\n after opening ---
	if strings.HasPrefix(rest, "\r\n") {
		rest = rest[2:]
	} else if strings.HasPrefix(rest, "\n") {
		rest = rest[1:]
	}

	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return "", ""
	}

	frontmatter := rest[:idx]

	// Parse key: value lines (simple YAML subset)
	for _, line := range strings.Split(frontmatter, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "name:") {
			name = strings.TrimSpace(strings.TrimPrefix(line, "name:"))
			name = stripQuotes(name)
		} else if strings.HasPrefix(line, "description:") {
			description = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
			description = stripQuotes(description)
		}
	}

	return name, description
}

// stripQuotes removes matching surrounding quotes (double or single) from a string.
func stripQuotes(s string) string {
	if len(s) >= 2 && s[0] == s[len(s)-1] {
		if s[0] == '"' || s[0] == '\'' {
			return s[1 : len(s)-1]
		}
	}
	return s
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
	ConflictedFiles []string // list of conflicted file paths
}

// RenderMergeConflictPrompt renders the merge conflict resolution prompt.
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
		CurrentDate:  "2025-01-01",
		PlanMode:     true,
		Tools: []ToolInfo{
			{Name: "test_tool", Description: "test description"},
		},
		Skills: []Skill{
			{Name: "test-skill", Description: "A test skill", Location: "/tmp/test-skill/SKILL.md"},
		},
		Summary: "Previous work: implemented authentication system.",
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
