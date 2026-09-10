package extensions

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"kanban/pkg/sandboxruntime"
)

// A stand-in podman for tests that need a build attempted without a daemon: it records the argv and a
// snapshot of the build context, which only it can do because the caller deletes that directory.

// fakePodmanShim is the script installed as `podman` on PATH.
const fakePodmanShim = `#!/bin/sh
LOG='__LOG__'
# Nothing is present locally, so a create path has to build its image.
if [ "$1" = "image" ] && [ "$2" = "exists" ]; then exit 1; fi
printf '===CALL\n' >> "$LOG"
for a in "$@"; do printf '%s\n' "$a" >> "$LOG"; done
last=''
for a in "$@"; do last="$a"; done
if [ -d "$last" ]; then
	printf 'CTX-DIR %s\n' "$last" >> "$LOG"
	find "$last" -mindepth 1 -type f 2>/dev/null | sed "s|^$last/||" | sort | while read -r f; do
		printf 'CTX-FILE %s\n' "$f" >> "$LOG"
	done
	if [ -x "$last/sandbox/sandbox-bridge" ]; then printf 'CTX-EXEC 1\n' >> "$LOG"; else printf 'CTX-EXEC 0\n' >> "$LOG"; fi
	if [ -e "$last/.dockerignore" ]; then printf 'CTX-IGNORE 1\n' >> "$LOG"; else printf 'CTX-IGNORE 0\n' >> "$LOG"; fi
fi
__OUTPUT__
exit __EXIT__
`

// fakePodman records what podman was asked to do. `podman image exists` always reports missing, and
// every other call succeeds or fails as configured.
type fakePodman struct {
	logPath string
}

// fakePodmanOK installs a podman that succeeds.
func fakePodmanOK(t *testing.T) *fakePodman {
	t.Helper()
	return fakePodmanExit(t, 0, `printf 'Successfully tagged fake\n'`)
}

// fakePodmanFails installs a podman that exits `code` after printing `output`.
func fakePodmanFails(t *testing.T, code int, output string) *fakePodman {
	t.Helper()
	return fakePodmanExit(t, code, `printf '`+strings.ReplaceAll(output, "'", `'\''`)+`\n'`)
}

func fakePodmanExit(t *testing.T, code int, outputCmd string) *fakePodman {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "podman.log")
	// Written empty so "podman was never called" is a readable assertion.
	if err := os.WriteFile(logPath, nil, 0o600); err != nil {
		t.Fatalf("write podman log: %v", err)
	}
	script := strings.NewReplacer(
		"__LOG__", logPath,
		"__OUTPUT__", outputCmd,
		"__EXIT__", strconv.Itoa(code),
	).Replace(fakePodmanShim)
	scriptPath := filepath.Join(dir, "podman")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake podman: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return &fakePodman{logPath: logPath}
}

// setBridgeEnv returns the absolute path of the committed bridge, which every building test stages into
// its context. It is a Boot field (bridgeBoot below), not MIKE_AGENT_PATH: the code that stages the
// binary reads the boot its client was built with, so the value cannot change underneath it.
func setBridgeEnv(t *testing.T) string {
	t.Helper()
	_, filename, _, _ := runtime.Caller(0)
	p, err := filepath.Abs(filepath.Join(filepath.Dir(filename), "..", "..", "sandbox", "sandbox-bridge"))
	if err != nil {
		t.Fatalf("resolve committed sandbox-bridge: %v", err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("committed sandbox-bridge is missing (%v) — a build has nothing to stage", err)
	}
	return p
}

// bridgeBoot is the boot a building test runs under: the committed bridge named by path.
func bridgeBoot(t *testing.T) sandboxruntime.Boot {
	t.Helper()
	return sandboxruntime.Boot{AgentPath: setBridgeEnv(t)}
}

// calls returns one entry per podman invocation: its argv (without the program name). A log with no
// "===CALL" marker means podman was never run, which several tests assert.
func (f *fakePodman) calls(t *testing.T) [][]string {
	t.Helper()
	data, err := os.ReadFile(f.logPath)
	if err != nil {
		t.Fatalf("read fake podman log: %v", err)
	}
	var calls [][]string
	inCall := false
	for _, line := range strings.Split(string(data), "\n") {
		switch {
		case line == "===CALL":
			calls = append(calls, nil)
			inCall = true
		case strings.HasPrefix(line, "CTX-"):
			inCall = false
		case inCall && line != "":
			calls[len(calls)-1] = append(calls[len(calls)-1], line)
		}
	}
	return calls
}

// context is what the shim saw of the build context of invocation `idx`.
type buildContext struct {
	Dir                string
	Files              []string
	BridgeIsExecutable bool
	HasDockerignore    bool
}

func (f *fakePodman) context(t *testing.T, idx int) buildContext {
	t.Helper()
	calls := f.calls(t)
	if len(calls) <= idx {
		t.Fatalf("podman was called %d times, want at least %d", len(calls), idx+1)
	}
	var ctxDir string
	// The recorded context belongs to the invocation at idx: re-read the log section for it.
	data, err := os.ReadFile(f.logPath)
	if err != nil {
		t.Fatalf("read fake podman log: %v", err)
	}
	sections := strings.Split(string(data), "===CALL\n")
	if len(sections) <= idx+1 {
		t.Fatalf("no log section for invocation %d", idx)
	}
	var bc buildContext
	for _, line := range strings.Split(sections[idx+1], "\n") {
		switch {
		case strings.HasPrefix(line, "CTX-DIR "):
			ctxDir = strings.TrimPrefix(line, "CTX-DIR ")
		case strings.HasPrefix(line, "CTX-FILE "):
			bc.Files = append(bc.Files, strings.TrimPrefix(line, "CTX-FILE "))
		case line == "CTX-EXEC 1":
			bc.BridgeIsExecutable = true
		case line == "CTX-IGNORE 1":
			bc.HasDockerignore = true
		}
	}
	bc.Dir = ctxDir
	return bc
}
