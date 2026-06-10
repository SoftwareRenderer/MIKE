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

func generateUUID() string {
	return uuid.New().String()
}

func generateTaskID() string {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	w1 := taskWords[r.Intn(len(taskWords))]
	w2 := taskWords[r.Intn(len(taskWords))]
	w3 := taskWords[r.Intn(len(taskWords))]
	return w1 + "-" + w2 + "-" + w3
}

func autoGenerateTitle(prompt string) string {
	line := strings.Split(prompt, "\n")[0]
	line = strings.TrimSpace(line)
	if len(line) > 80 {
		line = line[:77] + "..."
	}
	return line
}

func isGitRepo(path string) bool {
	cmd := exec.Command("git", "-C", path, "rev-parse", "--git-dir")
	cmd.Dir = path
	return cmd.Run() == nil
}

func getEnv(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}

func basename(path string) string {
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
