package server

import (
	"os/exec"
	"strings"
	"testing"

	"kanban/backend/git"
)

// canaryPath is used to detect command injection. If any malicious payload
// breaks out of shell quoting in gitAuthorFlagCmd, this file is created.
const canaryPath = "/tmp/gitauthorflagcmd_pwned_canary"

func cleanCanary() {
	_ = exec.Command("rm", "-f", canaryPath).Run()
}

// canaryCreated reports whether the injection canary file exists (i.e. an
// injection succeeded).
func canaryCreated() bool {
	return exec.Command("test", "-f", canaryPath).Run() == nil
}

// shellEchoArgs runs `printf '%s\n' <args...>` via a POSIX shell so that each
// shell-quoted token in command is printed on its own line. This validates that
// shellQuote groups each token into exactly one shell argument.
func shellEchoArgs(command string) (string, error) {
	out, err := exec.Command("sh", "-c", "printf '%s\n' "+command).Output()
	return string(out), err
}

// TestGitAuthorFlagCmd_Empty verifies that when no author identity is configured
// (the current default state), the flag command is empty so git falls back to
// its .gitconfig defaults.
func TestGitAuthorFlagCmd_Empty(t *testing.T) {
	origName, origEmail := git.GitAuthorName, git.GitAuthorEmail
	git.GitAuthorName = ""
	git.GitAuthorEmail = ""
	defer func() { git.GitAuthorName, git.GitAuthorEmail = origName, origEmail }()

	got := gitAuthorFlagCmd()
	if got != "" {
		t.Fatalf("expected empty flagCmd when identity unset, got %q", got)
	}
}

// TestGitAuthorFlagCmd_Format verifies the flag command produces properly
// shell-quoted -c pairs for each configured identity component.
func TestGitAuthorFlagCmd_Format(t *testing.T) {
	origName, origEmail := git.GitAuthorName, git.GitAuthorEmail
	git.GitAuthorName = "Test Author"
	git.GitAuthorEmail = "test@example.com"
	defer func() { git.GitAuthorName, git.GitAuthorEmail = origName, origEmail }()

	got := gitAuthorFlagCmd()
	want := "'-c' 'user.name=Test Author' '-c' 'user.email=test@example.com'"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

// TestGitAuthorFlagCmd_NameOnly verifies behavior with only the name set.
func TestGitAuthorFlagCmd_NameOnly(t *testing.T) {
	origName, origEmail := git.GitAuthorName, git.GitAuthorEmail
	git.GitAuthorName = "Only Name"
	git.GitAuthorEmail = ""
	defer func() { git.GitAuthorName, git.GitAuthorEmail = origName, origEmail }()

	got := gitAuthorFlagCmd()
	want := "'-c' 'user.name=Only Name'"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

// TestGitAuthorFlagCmd_EmailOnly verifies behavior with only the email set.
func TestGitAuthorFlagCmd_EmailOnly(t *testing.T) {
	origName, origEmail := git.GitAuthorName, git.GitAuthorEmail
	git.GitAuthorName = ""
	git.GitAuthorEmail = "only@x.com"
	defer func() { git.GitAuthorName, git.GitAuthorEmail = origName, origEmail }()

	got := gitAuthorFlagCmd()
	want := "'-c' 'user.email=only@x.com'"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

// TestGitAuthorFlagCmd_ShellSafety verifies that malicious author identity
// values cannot break out of shell quoting when the flagCmd is interpolated
// into the same command-string pattern used by handleFSInitGit's sandbox path.
// This is the core regression test for the latent command injection.
func TestGitAuthorFlagCmd_ShellSafety(t *testing.T) {
	payloads := []string{
		"'; touch " + canaryPath + "; '",
		"';touch " + canaryPath + ";'",
		"$(touch " + canaryPath + ")",
		"`touch " + canaryPath + "`",
		"'; touch " + canaryPath + "; ' &&",
		"'; touch " + canaryPath + " #",
		"; touch " + canaryPath,
		"| touch " + canaryPath,
		"'; touch " + canaryPath + "; echo '",
		"'; touch " + canaryPath + " ; ' -c user.name=evil",
	}

	origName, origEmail := git.GitAuthorName, git.GitAuthorEmail
	defer func() { git.GitAuthorName, git.GitAuthorEmail = origName, origEmail }()

	for _, payload := range payloads {
		t.Run(payload, func(t *testing.T) {
			cleanCanary()
			// Inject the payload into both name and email so every component
			// produced by GitAuthorFlags() / shellQuote is exercised.
			git.GitAuthorName = payload
			git.GitAuthorEmail = payload + "@x.com"

			flagCmd := gitAuthorFlagCmd()

			// Reproduce the exact command-string pattern from handleFSInitGit's
			// sandbox branch. `git -C <path> <flags> commit` will exit non-zero
			// (no repo / no staged changes), which is harmless — we only care
			// whether the injected `touch` executes during shell parsing.
			sandboxPath := t.TempDir()
			shellCmd := "git -C " + shellQuote(sandboxPath) + " " + flagCmd + " commit -m 'Initial commit' || true"

			cmd := exec.Command("sh", "-c", shellCmd)
			if err := cmd.Run(); err != nil {
				t.Fatalf("sh -c failed unexpectedly: %v", err)
			}

			if canaryCreated() {
				t.Errorf("command injection detected! payload %q created canary. flagCmd was: %q", payload, flagCmd)
			}
			cleanCanary()
		})
	}
}

// TestGitAuthorFlagCmd_RoundTrip verifies that each shell-quoted token, when
// parsed back by a POSIX shell, reconstructs the original author identity value
// as a single argument.
func TestGitAuthorFlagCmd_RoundTrip(t *testing.T) {
	origName, origEmail := git.GitAuthorName, git.GitAuthorEmail
	defer func() { git.GitAuthorName, git.GitAuthorEmail = origName, origEmail }()

	cases := []struct {
		name  string
		email string
	}{
		{"simple", "a@b.com"},
		{"name with spaces", "a b@x.com"},
		{"quote in name", "o'brien@x.com"},
		{"semicolon in name", "a;ls;@x.com"},
		{"dollar in name", "a$(whoami)@x.com"},
		{"backtick in name", "a`whoami`@x.com"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			git.GitAuthorName = c.name
			git.GitAuthorEmail = c.email

			flagCmd := gitAuthorFlagCmd()

			// `printf '%s\n' <flagCmd>` prints each shell-quoted token on its
			// own line, proving each token survived as a single argument.
			out, err := shellEchoArgs(flagCmd)
			if err != nil {
				t.Fatalf("printf failed: %v", err)
			}
			lines := strings.Split(strings.TrimRight(out, "\n"), "\n")

			// flagCmd is a series of ("-c", "user.name=<name>", "email parts")
			// pairs: ["-c", "user.name=NAME", "-c", "user.email=EMAIL"].
			wantArgs := []string{"-c", "user.name=" + c.name, "-c", "user.email=" + c.email}
			if len(lines) != len(wantArgs) {
				t.Fatalf("expected %d args, got %d: %v", len(wantArgs), len(lines), lines)
			}
			for i, want := range wantArgs {
				if lines[i] != want {
					t.Errorf("arg %d: expected %q, got %q", i, want, lines[i])
				}
			}
		})
	}
}
