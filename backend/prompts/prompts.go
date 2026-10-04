// Package prompts renders the LLM prompt templates under prompts/ and discovers agent
// skills, keeping template loading and path resolution out of the execution loop.
package prompts

import (
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
	ExploreMode  bool
	Tools        []ToolInfo
	AgentsMd     string  // contents of AGENTS.md from worktree root, empty if not found
	Skills       []Skill // discovered skills with name, description, and location
	Summary      string  // prior conversation summary
	Locale       string  `json:"-"`
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

// funcs are helper template functions.
var funcs = template.FuncMap{
	"inc": func(i int) int { return i + 1 },
}

// FuncMap returns a copy of the helper functions every prompt template is parsed with.
//
// It is exported for one reason: an unregistered function is a *parse* error in text/template,
// so anything that checks a template before the renderer runs has to know these names or it
// refuses a program the renderer accepts. A translation pack ships exactly such templates
// (capabilities.i18n.prompts_dir), and its install-time check parses with this map — see
// validateI18N in backend/extensions. Callers must not execute the results they parse with it.
func FuncMap() template.FuncMap {
	fm := make(template.FuncMap, len(funcs))
	for name, fn := range funcs {
		fm[name] = fn
	}
	return fm
}

// promptDir resolves the prompts directory. Tests override it.
var promptDir = func() string {
	return resolvePromptDir()
}

// resolvePromptDir is promptDir's default: the executable's directory first (installed binary),
// then the current working directory (go run).
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
	for i := range len(skills) {
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
//
// Every core template is parsed, because base.tmpl calls the plan-mode and explore-mode
// sub-templates; a pack's templates are then laid over them by name (see overlay.go), and core
// answers for the ones a pack left alone.
func RenderSystemPrompt(data PromptData) (string, error) {
	if data.Locale == "" {
		data.Locale = ActiveLocale()
	}
	files, err := coreGlob()
	if err != nil {
		return "", err
	}
	// Render by name: Execute() would render the alphabetically-first file, so
	// a new template could hijack the system prompt.
	return render(files, "base.tmpl", data)
}

// MergeConflictData holds the variables for the merge conflict resolution prompt.
type MergeConflictData struct {
	TaskBranch      string
	TargetBranch    string
	ReviewContext   string // what each request_review reported; when empty the two fields below are used instead
	TaskTitle       string
	TaskDescription string
	ConflictedFiles []string // list of conflicted file paths
}

// RenderMergeConflictPrompt renders the merge conflict resolution prompt.
//
// Parsed with FuncMap like every other entry point: a pack template using `inc` is accepted
// at install (validateI18N parses with FuncMap) and would otherwise fail here at render.
func RenderMergeConflictPrompt(data MergeConflictData) (string, error) {
	return render(coreFile("merge_conflict.tmpl"), "merge_conflict.tmpl", data)
}

// SummarizeData holds the variables for the context summarization prompt.
type SummarizeData struct {
	ChatHistory     string // formatted conversation history
	PreviousSummary string // summary from a prior summarization, empty if first
}

// RenderSummarizePrompt renders the context summarization prompt pair: the
// system prompt (summarization instructions) and the user message (previous
// summary + chat history).
func RenderSummarizePrompt(data SummarizeData) (string, string, error) {
	return renderPair("summarize.tmpl", "summarize_user.tmpl", data)
}

// AnswerQuestionData is the input for the automated answer prompt.
type AnswerQuestionData struct {
	ParentTitle  string   // parent task title
	ParentPrompt string   // parent task description/prompt (the context to answer from)
	Questions    []string // all questions asked in this turn, in order
}

// RenderAnswerQuestionPrompt renders the automated answer prompt pair: the
// system prompt (parent context + answer format) and the user message (the
// numbered questions). The questions live in the user message because some
// chat templates reject system-only requests.
func RenderAnswerQuestionPrompt(data AnswerQuestionData) (string, string, error) {
	return renderPair("answer_question.tmpl", "answer_question_user.tmpl", data)
}

// renderPair renders a prompt's two halves: the system template, and the user message that carries
// what the system message may not hold on its own.
func renderPair(systemFile, userFile string, data any) (string, string, error) {
	system, err := render(coreFile(systemFile), systemFile, data)
	if err != nil {
		return "", "", err
	}
	user, err := render(coreFile(userFile), userFile, data)
	if err != nil {
		return "", "", err
	}
	return system, user, nil
}
