package git

import (
	"context"
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
	out, err := CloneContext(context.Background(), src, dst, func(pct int) {
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

	if _, err := CloneContext(context.Background(), src, dst, nil); err != nil {
		t.Fatalf("Clone with nil callback: %v", err)
	}
	if !IsGitRepo(dst) {
		t.Fatal("clone target is not a git repository")
	}
}

func TestClone_Error(t *testing.T) {
	tmp := t.TempDir()
	missing := filepath.Join(tmp, "does-not-exist")

	out, err := CloneContext(context.Background(), missing, filepath.Join(tmp, "dst"), nil)
	if err == nil {
		t.Fatal("expected error cloning a missing source")
	}
	if !strings.Contains(out, "does-not-exist") && !strings.Contains(out, "fatal") {
		t.Fatalf("error output does not mention the failure: %q", out)
	}
}

// TestValidateCloneURL is the shared gate every clone of a caller-supplied source
// passes through — project creation and an extension checkout alike.
func TestValidateCloneURL(t *testing.T) {
	tests := []struct {
		name    string
		gitURL  string
		wantErr bool
	}{
		// Accepted: network transports on the allowlist
		{"https url", "https://github.com/user/repo.git", false},
		{"https url no suffix", "http://example.com/repo", false},
		{"http url", "http://example.com/repo", false},
		{"ssh url", "ssh://git@example.com:22/repo.git", false},
		{"git url", "git://example.com/repo.git", false},
		{"uppercase scheme", "HTTPS://example.com/repo.git", true}, // git's allowlist is case-sensitive
		// Accepted: scp-like SSH form (no scheme)
		{"scp-like", "git@github.com:org/repo.git", false},
		{"scp-like no user", "github.com:org/repo.git", false},
		{"scp-like abs remote path", "deploy@build.example.com:/srv/git/repo.git", false},
		// Accepted: local cloning is a documented, supported workflow
		{"file scheme abs", "file:///tmp/local-repo", false},
		{"bare absolute path", "/tmp/local-repo", false},
		{"bare relative path", "repos/local-repo", false},
		{"dot relative path", "./local-repo", false},
		{"path with space", "/tmp/my repos/local-repo", false},
		{"path with colon after slash", "/tmp/local:repo", false},
		// Rejected: empty / argument injection
		{"empty", "", true},
		{"whitespace only", "   ", true},
		{"leading dash short", "-f", true},
		{"leading dash flag", "-evil-flag", true},
		{"leading dash upload-pack", "--upload-pack=touch /tmp/pwn", true},
		{"leading dash reference", "--reference=/etc", true},
		// Rejected: remote-helper transports (`<helper>::<args>`)
		{"ext shell", "ext::sh -c 'id'", true},
		{"ext touch", "ext::touch /tmp/pwn", true},
		{"fd helper", "fd::11", true},
		{"python helper", "python::Foo", true},
		{"rc helper", "rc::/etc/passwd", true},
		// Rejected: schemes outside the allowlist
		{"unknown scheme", "foo://x", true},
		{"ext scheme form", "ext:///tmp/x", true},
		{"smb scheme", "smb://host/share/repo", true},
		// Rejected: malformed scp-like forms (the shape git treats as a remote)
		{"scp-like with command", "git@host:sh -c 'id'", true},
		{"scp-like double colon", "host::repo", true},
		{"scp-like empty path", "git@host:", true},
		{"scp-like space in host", "git@ho st:repo", true},
		// Rejected: control characters
		{"newline in url", "https://example.com/repo\nx", true},
		{"nul in url", "https://example.com/repo\x00x", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCloneURL(tt.gitURL)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateCloneURL(%q) = %v, wantErr %v", tt.gitURL, err, tt.wantErr)
			}
		})
	}
}

// TestValidateCloneURL_AllowlistMatchesCloneEnv keeps the two halves of the
// clone restriction in sync: every scheme the validator accepts must also be one
// git is told to allow, and no helper transport may appear in either.
func TestValidateCloneURL_AllowlistMatchesCloneEnv(t *testing.T) {
	for _, scheme := range CloneAllowedProtocols {
		if err := ValidateCloneURL(scheme + "://example.com/repo.git"); err != nil {
			t.Errorf("allowed protocol %q rejected by validateGitCloneURL: %v", scheme, err)
		}
	}
	for _, helper := range []string{"ext", "fd", "python", "rc", "internal"} {
		if err := ValidateCloneURL(helper + "::payload"); err == nil {
			t.Errorf("helper transport %q accepted", helper)
		}
	}
}
