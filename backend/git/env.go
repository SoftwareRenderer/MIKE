package git

// Environment and process construction for every git subprocess this server starts.
//
// Two rules, both previously false:
//
//  1. A git subprocess is never handed the server's environment. Spawns either set
//     cmd.Env = append(os.Environ(), ...) or left it nil, which os/exec resolves to os.Environ().
//     That included MIKE_LOGIN_PASSWORD and MIKE_ENCRYPTION_KEY.
//  2. A git subprocess never runs repository-supplied code. Hooks are repo content, and projects
//     are routinely added from a path the operator already has.
//
// Enforced here, not at call sites: 71 spawns across 13 files, and Command is now the only way to
// start git.

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

// gitBinary is a variable so a test can point a subprocess at a stub.
var gitBinary = "git"

// gitEnvNames is what a git subprocess is given: enough to find its config, its credential helpers
// and the operator's SSH agent, and nothing else. An allowlist, not a filter — a variable added to
// the server tomorrow is not passed until it appears here.
var gitEnvNames = map[string]bool{
	"PATH":                true, // git is resolved through it, as are the credential helpers it shells out to
	"HOME":                true, // ~/.gitconfig, ~/.config/git, and the credential helpers' own state
	"USER":                true,
	"LOGNAME":             true,
	"SHELL":               true, // git's pager and some credential helpers honour $SHELL
	"TERM":                true,
	"LANG":                true,
	"TZ":                  true,
	"TMPDIR":              true,
	"TMP":                 true,
	"TEMP":                true,
	"SSH_AUTH_SOCK":       true, // the operator's private remotes authenticate through their agent
	"SSH_AGENT_PID":       true,
	"XDG_RUNTIME_DIR":     true,
	"XDG_CONFIG_HOME":     true,
	"XDG_DATA_HOME":       true,
	"CREDENTIAL_USER":     true,
	"NODE_EXTRA_CA_CERTS": true,
	"SSL_CERT_FILE":       true, // git's own HTTPS (git-remote-https) verifies TLS with these
	"SSL_CERT_DIR":        true,
	"GIT_ASKPASS_REQUIRE": true,
	"GIT_SSH":             true,
	"GIT_SSH_COMMAND":     true, // an operator's bespoke transport, set outside git config
	"GIT_CONFIG_GLOBAL":   true,
	"GIT_CONFIG_SYSTEM":   true,
	"GIT_EXEC_PATH":       true,
	"GIT_REMOTE_HTTPS":    true,
	"GIT_SSL_CAINFO":      true,
	"GIT_SSL_NO_VERIFY":   true,
	"GIT_AUTHOR_NAME":     true, // author identity is set explicitly (GitAuthorFlags); these
	"GIT_AUTHOR_EMAIL":    true, // are the mirror for environments that read env not flags
	"GIT_COMMITTER_NAME":  true,
	"GIT_COMMITTER_EMAIL": true,
}

// gitEnvPrefixes are families allowed in bulk. LC_* is the family SafeAgentEnv allows too. GIT_* is
// in bulk because git reads its behaviour out of its own ~500 variables and a hand-picked list would
// be wrong; nothing secret is named GIT_*, and the hook fence below — which an inherited GIT_*
// cannot override — is what closes the execution surface.
var gitEnvPrefixes = []string{"LC_", "GIT_"}

// proxyEnvNames: a private remote behind an egress proxy stops resolving without them.
var proxyEnvNames = map[string]bool{
	"HTTP_PROXY":  true,
	"HTTPS_PROXY": true,
	"NO_PROXY":    true,
	"http_proxy":  true,
	"https_proxy": true,
	"no_proxy":    true,
}

// isGitSafeEnv reports whether an environment variable name may reach a git subprocess.
func isGitSafeEnv(name string) bool {
	if gitEnvNames[name] || proxyEnvNames[name] {
		return true
	}
	for _, p := range gitEnvPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// HookFence returns the config that stops git running repository-supplied hooks, as environment
// entries.
//
// Env-based rather than a flag, for one measured reason: --no-verify does not exist for every
// hook-capable operation. git worktree add has no such flag and runs post-checkout, which is a MIKE
// hot path. Verified on git 2.55. core.hooksPath covers pre-commit, commit-msg, post-commit,
// post-merge and post-checkout at once, and /dev/null is never an executable directory.
//
// GIT_CONFIG_COUNT/KEY_n/VALUE_0 is git's own "config as environment", so it has the precedence of
// -c but cannot be reordered or forgotten at a call site.
func HookFence() []string {
	return []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=core.hooksPath",
		"GIT_CONFIG_VALUE_0=/dev/null",
	}
}

// Env returns the environment for a git subprocess: the allowlisted part of the server's, then
// extras, then policy.
//
// Policy comes last because os/exec keeps the LAST occurrence of a duplicated key, so a caller
// cannot weaken the hook fence by passing its own core.hooksPath. That ordering is the security
// argument; TestEnv_PolicySurvivesAnExtra pins it.
//
// Variables the server lacks or has set empty are omitted, not emitted as VAR=. Git and the tools it
// shells out to treat those differently, and here empty is worse: SSH_AUTH_SOCK= reads as a
// configured but broken agent instead of falling back to the key files.
func Env(extra ...string) []string {
	env := make([]string, 0, 32+len(extra))
	for _, kv := range os.Environ() {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || name == "" || value == "" || !isGitSafeEnv(name) {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, extra...)
	// Policy last. GIT_TERMINAL_PROMPT=0 goes with the fence: an unattended server that hits a
	// credential prompt hangs the request forever.
	return append(env, append(HookFence(), "GIT_TERMINAL_PROMPT=0")...)
}

// Command builds the *exec.Cmd for one git invocation. It is the only place in this repository that
// constructs a git process; TestNoGitSubprocessSpawnsOutsideGit fails the build if another appears.
//
// dir is the working directory ("" means the server's). Callers passing -C <path> in args are
// unaffected, since git resolves -C itself. Extra configuration goes through Env, never by assigning
// cmd.Env:
//
//	cmd := git.Command(worktreePath, "commit", "-m", msg)
//	cmd.Env = git.Env("GIT_DIR="+gitDir)
func Command(dir string, args ...string) *exec.Cmd {
	return configureGit(exec.Command(gitBinary, args...), dir)
}

// CommandContext is Command with a cancellable context, so a detached caller passes no context.
// backend/extensions/registry.go already spawned git under a request context three times; routing
// those through Command alone would have made them uncancellable.
func CommandContext(ctx context.Context, dir string, args ...string) *exec.Cmd {
	return configureGit(exec.CommandContext(ctx, gitBinary, args...), dir)
}

// GitWaitDelay bounds how long a git subprocess may keep its output pipes open after it exits, or
// after a cancelled context kills it. A cancelled context is not the bound: cmd.Wait waits for the
// pipes, so a git wedged on a locked index keeps the request goroutine with it. Generous because
// there is no container on the other side — the process is dead well before this fires.
const GitWaitDelay = 30 * time.Second

// configureGit applies the invariants for both constructors. It takes a built *exec.Cmd rather than
// a context so Command can share it without passing a nil context.
func configureGit(cmd *exec.Cmd, dir string) *exec.Cmd {
	cmd.Dir = dir
	cmd.Env = Env()
	cmd.WaitDelay = GitWaitDelay
	return cmd
}
