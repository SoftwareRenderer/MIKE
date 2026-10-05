package git

// Environment and process construction for every git subprocess this server starts. Two rules:
//
//  1. A git subprocess is never handed the server's environment — that included
//     MIKE_ENCRYPTION_KEY.
//  2. A git subprocess never runs repository-supplied code: hooks are repo content, and projects
//     are routinely added from a path the operator already has.
//
// Enforced here rather than at call sites, and Command is the only way to start git.

import (
	"context"
	"os/exec"
	"time"

	"mike/pkg/subproc"
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
// be wrong; nothing secret is named GIT_*, and the execution fence below — which an inherited GIT_*
// cannot override — is what closes the execution surface.
var gitEnvPrefixes = []string{"LC_", "GIT_"}

// isGitSafeEnv reports whether an environment variable name may reach a git subprocess.
var isGitSafeEnv = subproc.Allows(gitEnvNames, gitEnvPrefixes)

// ExecutionFence stops a repository naming a program for git to run. Every entry is command-line
// configuration, which outranks the repository's own `.git/config` but not an environment variable the
// operator set themselves. `core.editor` is pinned to `true` rather than to a program that refuses,
// because `git merge --continue` still has to finish.
//
// No key disables `filter.<name>.clean`, which a repository selects by name in its own
// `.gitattributes`; the read-only git pointer is what stops it
// (sandboxruntime.WorktreeGitPointerMount).
func ExecutionFence() []string {
	return []string{
		"GIT_CONFIG_COUNT=5",
		"GIT_CONFIG_KEY_0=core.hooksPath",
		"GIT_CONFIG_VALUE_0=/dev/null",
		"GIT_CONFIG_KEY_1=core.fsmonitor",
		"GIT_CONFIG_VALUE_1=false",
		"GIT_CONFIG_KEY_2=core.editor",
		"GIT_CONFIG_VALUE_2=true",
		"GIT_CONFIG_KEY_3=core.pager",
		"GIT_CONFIG_VALUE_3=cat",
		"GIT_CONFIG_KEY_4=core.askPass",
		"GIT_CONFIG_VALUE_4=",
	}
}

// Env puts policy last: os/exec keeps the last occurrence of a duplicated key, so a caller cannot weaken the fence.
func Env(extra ...string) []string {
	env := subproc.Inherit(isGitSafeEnv, subproc.DropEmpty)
	env = append(env, extra...)
	// Policy last. GIT_TERMINAL_PROMPT=0 goes with the fence: an unattended server that hits a
	// credential prompt hangs the request forever.
	return append(env, append(ExecutionFence(), "GIT_TERMINAL_PROMPT=0")...)
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

// Run runs one git command to completion in dir and returns what git printed, because git writes its
// reason for refusing to stderr and an exit code alone tells the caller nothing. It is the only way
// this repo waits for a git subprocess.
func Run(dir string, args ...string) (string, error) {
	out, err := Command(dir, args...).CombinedOutput()
	return string(out), err
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
	return subproc.Bind(cmd, Env(), GitWaitDelay)
}
