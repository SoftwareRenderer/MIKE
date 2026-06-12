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

// AutoGenerateTitle creates a short title from the first line of a prompt.
func AutoGenerateTitle(prompt string) string {
	line := strings.Split(prompt, "\n")[0]
	line = strings.TrimSpace(line)
	if len(line) > 80 {
		line = line[:77] + "..."
	}
	return line
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
