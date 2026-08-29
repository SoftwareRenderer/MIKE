package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// makeCloneSource creates a temporary non-bare git repo with `commits`
// commits of a growing file (enough objects for git to emit
// "Receiving objects" progress lines).
func makeCloneSource(t *testing.T, path string, commits int) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	runGit(t, path, "init")
	runGit(t, path, "config", "user.email", "test@test.com")
	runGit(t, path, "config", "user.name", "Test")
	for i := 0; i < commits; i++ {
		// ~1KB per commit with unique content so the pack is large enough
		// for git to emit "Receiving objects" progress lines.
		line := fmt.Sprintf("line %d %s", i, strings.Repeat("x", 1020))
		writeFile(t, path, "data.txt", line)
		runGit(t, path, "add", "data.txt")
		runGit(t, path, "commit", "-m", "commit")
	}
}

func TestClone(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "source")
	makeCloneSource(t, srcDir, 20)
	// Use a file:// URL: cloning a plain local path uses git's hardlink
	// optimization (no pack transfer, no progress output), while file://
	// goes through the normal upload-pack protocol.
	src := "file://" + srcDir
	dst := filepath.Join(tmp, "workspace", "source")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}

	var events []int
	out, err := Clone(src, dst, func(pct int) {
		events = append(events, pct)
	})
	if err != nil {
		t.Fatalf("Clone: %v (output: %s)", err, out)
	}
	if !IsGitRepo(dst) {
		t.Fatal("clone target is not a git repository")
	}
	if len(events) == 0 {
		t.Fatalf("no progress events reported (output: %q)", out)
	}
	for i, pct := range events {
		if pct < 0 || pct > 100 {
			t.Fatalf("progress out of range: %d", pct)
		}
		if i > 0 && pct <= events[i-1] {
			t.Fatalf("progress not strictly increasing: %v", events)
		}
	}
	if events[len(events)-1] != 100 {
		t.Fatalf("final progress = %d, want 100 (events: %v)", events[len(events)-1], events)
	}
}

// TestCloneArgs verifies the argv used for every clone: the transport lockdown
// (-c protocol.ext.allow=never) must come BEFORE the `clone` subcommand, so the
// process that parses the client-supplied URL is the one that carries it.
func TestCloneArgs(t *testing.T) {
	args := CloneArgs("https://example.com/x/y.git", "/tmp/dst")

	// Locate the subcommand and the config override.
	cloneIdx, cfgIdx := -1, -1
	for i, a := range args {
		if a == "clone" && cloneIdx == -1 {
			cloneIdx = i
		}
		if a == "-c" {
			cfgIdx = i
		}
	}
	if cloneIdx == -1 {
		t.Fatalf("args missing `clone` subcommand: %v", args)
	}
	if cfgIdx == -1 {
		t.Fatalf("args missing `-c` config override: %v", args)
	}
	if cfgIdx > cloneIdx {
		t.Errorf("`-c protocol.ext.allow=never` must precede `clone`, got %v", args)
	}
	if cfgIdx+1 >= len(args) || args[cfgIdx+1] != "protocol.ext.allow=never" {
		t.Errorf("expected `-c protocol.ext.allow=never`, got %v", args)
	}
	// Source and destination are the two trailing arguments, in order.
	if args[len(args)-2] != "https://example.com/x/y.git" || args[len(args)-1] != "/tmp/dst" {
		t.Errorf("src/dst not passed last in order: %v", args)
	}
	if !containsExact(args, "--progress") {
		t.Errorf("progress reporting lost: %v", args)
	}
}

// TestCloneEnv verifies the clone environment: no interactive credential prompt,
// plus an explicit transport allowlist so helper transports (ext:, fd:, …) are
// refused even on a host whose git config enables them.
func TestCloneEnv(t *testing.T) {
	env := CloneEnv()

	if !containsExact(env, "GIT_TERMINAL_PROMPT=0") {
		t.Errorf("GIT_TERMINAL_PROMPT=0 missing from clone env: %v", env)
	}
	want := "GIT_ALLOW_PROTOCOL=" + strings.Join(CloneAllowedProtocols, ":")
	if !containsExact(env, want) {
		t.Errorf("%s missing from clone env: %v", want, env)
	}
	if !containsExact(env, "GIT_ALLOW_PROTOCOL=http:https:git:ssh:file") {
		t.Errorf("GIT_ALLOW_PROTOCOL must be exactly http:https:git:ssh:file, got %v", env)
	}
	// The allowlist must not silently grow a helper transport.
	for _, p := range CloneAllowedProtocols {
		switch p {
		case "ext", "fd", "python", "rc", "internal":
			t.Errorf("helper transport %q must not be allowed", p)
		}
	}
}

// TestClone_HostileEnvironment proves the restriction does not depend on the
// host's git config: even when the environment opens GIT_ALLOW_PROTOCOL to the
// `ext` helper and an earlier `-c protocol.ext.allow=always` is present, the
// flags CloneArgs/CloneEnv add win and `ext::` never executes.
func TestClone_HostileEnvironment(t *testing.T) {
	tmp := t.TempDir()
	canary := filepath.Join(tmp, "pwned")

	// Later -c wins in git, so our `protocol.ext.allow=never` (from CloneArgs)
	// overrides the hostile one placed ahead of it.
	args := append([]string{"-c", "protocol.ext.allow=always"}, CloneArgs("ext::sh -c 'touch "+canary+"'", filepath.Join(tmp, "dst"))...)
	cmd := exec.Command("git", args...)
	// Go keeps the LAST duplicate of an env var, so this opens the allowlist...
	cmd.Env = append(CloneEnv(), "GIT_ALLOW_PROTOCOL=ext:http:https:git:ssh:file")
	out, err := cmd.CombinedOutput()

	if err == nil {
		t.Fatalf("expected the ext:: clone to fail, got success: %s", out)
	}
	if _, statErr := os.Stat(canary); statErr == nil {
		t.Fatalf("ext:: transport executed a command despite the restrictions: %s", out)
	}
}

func containsExact(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func TestClone_NilCallback(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "source")
	makeCloneSource(t, src, 3)
	dst := filepath.Join(tmp, "workspace", "source")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}

	if _, err := Clone(src, dst, nil); err != nil {
		t.Fatalf("Clone with nil callback: %v", err)
	}
	if !IsGitRepo(dst) {
		t.Fatal("clone target is not a git repository")
	}
}

func TestClone_Error(t *testing.T) {
	tmp := t.TempDir()
	missing := filepath.Join(tmp, "does-not-exist")

	out, err := Clone(missing, filepath.Join(tmp, "dst"), nil)
	if err == nil {
		t.Fatal("expected error cloning a missing source")
	}
	if !strings.Contains(out, "does-not-exist") && !strings.Contains(out, "fatal") {
		t.Fatalf("error output does not mention the failure: %q", out)
	}
}
