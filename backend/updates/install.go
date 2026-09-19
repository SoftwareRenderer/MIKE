package updates

import (
	"os"
	"path/filepath"
)

// Kind is what this process could do about a new release, decided once at startup.
type Kind string

const (
	// KindPackaged is an unpacked release directory: the only shape that may replace its own files.
	KindPackaged Kind = "packaged"
	// KindContainer is an image: a swapped binary survives `docker restart` and is gone on the next
	// `compose up`.
	KindContainer Kind = "container"
	// KindSource is a development tree, or any binary whose version is not a release's.
	KindSource Kind = "source"
	// KindUnknown is a layout this build cannot name. Refusing is the answer for anything it cannot describe.
	KindUnknown Kind = "unknown"
)

// Install is the answer plus the reason a person reads when the answer is no.
type Install struct {
	Kind     Kind   `json:"kind"`
	Dir      string `json:"dir,omitempty"`
	Reason   string `json:"reason,omitempty"`
	CanStage bool   `json:"can_stage"`
}

// defaultContainerMarkers are the files a runtime puts in front of the process running inside it.
var defaultContainerMarkers = []string{"/.dockerenv", "/run/.containerenv"}

// classifyInstall decides the shape once, from arguments only, so it can be asked about a tree that is not
// the one running the test. An empty marker list means "no container"; nil means the defaults.
func classifyInstall(installDir, version string, markers []string) Install {
	if markers == nil {
		markers = defaultContainerMarkers
	}
	dir := installDir
	if dir == "" {
		dir = executableDir()
	}
	for _, marker := range markers {
		if _, err := os.Stat(marker); err == nil {
			return Install{
				Kind:   KindContainer,
				Dir:    dir,
				Reason: "this server runs inside a container, where a replaced binary is lost the next time the image is started; pull the image instead",
			}
		}
	}
	if !KnownVersion(version) {
		return Install{
			Kind:   KindSource,
			Dir:    dir,
			Reason: "this build reports its version as " + quoteOrEmpty(version) + ", so there is no release to compare it with",
		}
	}
	if dir == "" || !isReleaseLayout(dir) {
		return Install{
			Kind:   KindUnknown,
			Dir:    dir,
			Reason: "the directory this binary runs from is not shaped like an unpacked release, so nothing here can be safely replaced",
		}
	}
	return Install{Kind: KindPackaged, Dir: dir, CanStage: true}
}

// executableDir is the directory the running binary belongs to, symlinks resolved.
func executableDir() string {
	exe := executablePath()
	if exe == "" {
		return ""
	}
	return filepath.Dir(exe)
}

// executablePath is this process's binary with symlinks resolved: the file a staged release replaces.
func executablePath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return exe
}

// isReleaseLayout is the packaged shape: a prompts directory holding the templates the executor renders.
// It is read together with the version rule above, never on its own — a source checkout carries the same
// directory.
func isReleaseLayout(dir string) bool {
	matches, err := filepath.Glob(filepath.Join(dir, "prompts", "*.tmpl"))
	return err == nil && len(matches) > 0
}

func quoteOrEmpty(v string) string {
	if v == "" {
		return "(nothing)"
	}
	return "\"" + v + "\""
}
