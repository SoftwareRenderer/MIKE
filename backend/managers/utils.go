package managers

import (
	"math/rand"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/google/uuid"
)

var taskWords = []string{
	"ocean", "river", "mountain", "forest", "desert", "island", "valley", "meadow",
	"garden", "stream", "canyon", "glacier", "volcano", "waterfall", "sunrise", "sunset",
	"thunder", "lightning", "rainbow", "aurora", "breeze", "storm", "frost", "snow",
	"eagle", "wolf", "bear", "fox", "hawk", "owl", "deer", "lion",
	"tiger", "shark", "dolphin", "whale", "panda", "falcon", "raven", "heron",
	"crimson", "azure", "emerald", "amber", "violet", "coral", "sapphire", "ruby",
	"gold", "silver", "bronze", "copper", "ivory", "pearl", "onyx", "jade",
	"nova", "comet", "meteor", "galaxy", "nebula", "planet", "star", "cosmos",
	"orbit", "pulse", "zenith", "eclipse", "lunar", "solar", "stellar",
	"flame", "stone", "iron", "steel", "crystal", "diamond", "obsidian", "marble",
	"granite", "basalt", "quartz", "flint", "coal", "ash", "ember", "spark",
	"dream", "vision", "spirit", "soul", "mind", "heart", "voice", "echo",
	"shadow", "light", "truth", "wisdom", "power", "grace", "honor", "valor",
}

// GenerateUUID returns a new UUID string.
func GenerateUUID() string {
	return uuid.New().String()
}

// GenerateTaskID returns a human-readable task ID (e.g., "frost-amber-quartz").
func GenerateTaskID() string {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	w1 := taskWords[r.Intn(len(taskWords))]
	w2 := taskWords[r.Intn(len(taskWords))]
	w3 := taskWords[r.Intn(len(taskWords))]
	return w1 + "-" + w2 + "-" + w3
}

// GenerateThreadID returns a human-readable thread ID (e.g., "voice-ash").
func GenerateThreadID() string {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	w1 := taskWords[r.Intn(len(taskWords))]
	w2 := taskWords[r.Intn(len(taskWords))]
	return w1 + "-" + w2
}

// AutoGenerateTitle creates a short title from the first line of a prompt.
func AutoGenerateTitle(prompt string) string {
	line := strings.Split(prompt, "\n")[0]
	line = strings.TrimSpace(line)
	if len(line) > 80 {
		line = line[:77] + "..."
	}
	return line
}

// IsShortTitle returns true if the first line of the prompt has 5 or fewer words.
func IsShortTitle(prompt string) bool {
	line := strings.Split(prompt, "\n")[0]
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	return len(strings.Fields(line)) <= 5
}

// IsGitRepo checks if a directory contains a .git subdirectory.
func IsGitRepo(path string) bool {
	cmd := exec.Command("git", "-C", path, "rev-parse", "--git-dir")
	cmd.Dir = path
	return cmd.Run() == nil
}

// GetEnv returns the environment variable value or a default.
func GetEnv(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}

// Basename returns the last component of a path.
func Basename(path string) string {
	if len(path) > 0 && (path[len(path)-1] == '/' || path[len(path)-1] == '\\') {
		path = path[:len(path)-1]
	}
	parts := strings.Split(path, "/")
	if last := parts[len(parts)-1]; last != "" {
		return last
	}
	parts = strings.Split(path, "\\")
	return parts[len(parts)-1]
}

// GenerateProjectID returns a human-readable project ID derived from the folder path.
// Uses the basename by default; falls back to a slugified full path if the basename
// collides with an existing project ID.
func GenerateProjectID(path string, existingIDs []string) string {
	base := Basename(path)
	if !contains(existingIDs, base) {
		return base
	}
	// Collision — slugify the full path
	slug := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '-'
	}, path)
	// Collapse multiple dashes and trim leading/trailing dashes
	for strings.Contains(slug, "--") {
		slug = strings.ReplaceAll(slug, "--", "-")
	}
	slug = strings.Trim(slug, "-")
	return strings.ToLower(slug)
}

// contains returns true if the slice contains the given string.
func contains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}
