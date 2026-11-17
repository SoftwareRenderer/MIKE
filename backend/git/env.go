package git

// Construction of git subprocesses. Two rules:
//
//  1. Never pass the server's environment (contains MIKE_ENCRYPTION_KEY).
//  2. Never run repository-supplied code (hooks).
//
// Enforced here rather than at call sites, and Command is the only way to start git.

import (
	"context"
	"os/exec"
	"slices"
	"strings"
	"time"

	"mike/pkg/subproc"
)

// gitBinary allows tests to use a stub.
var gitBinary = "git"

// gitEnvNames allowlist of environment variables passed to git subprocesses
// (config, credentials, SSH agent). A variable added to the server is not passed until it appears here.
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
	"GIT_AUTHOR_NAME":     true, // how a command with no Identity of its own — a stash writes a commit
	"GIT_AUTHOR_EMAIL":    true, // object and has no person naming one — gets an ident at all
	"GIT_COMMITTER_NAME":  true,
	"GIT_COMMITTER_EMAIL": true,
}

// gitEnvPrefixes are environment prefixes allowed in bulk. GIT_* is included
// because git uses hundreds of variables; the ExecutionFence closes the surface.
var gitEnvPrefixes = []string{"LC_", "GIT_"}

// isGitSafeEnv reports if an environment variable name is allowed for git.
var isGitSafeEnv = subproc.Allows(gitEnvNames, gitEnvPrefixes)

// ExecutionFence prevents a repository from specifying programs for git to run.
// Pinned configurations outrank `.git/config`. `core.editor=true` ensures `git merge --continue` can finish.
// Read-only git pointers stop `filter.<name>.clean`.
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

// Env appends policy last so callers cannot weaken the fence (os/exec keeps the last occurrence).
func Env(extra ...string) []string {
	env := subproc.Inherit(isGitSafeEnv, subproc.DropEmpty)
	env = append(env, extra...)
	// Policy last. GIT_TERMINAL_PROMPT=0 goes with the fence: an unattended server that hits a
	// credential prompt hangs the request forever.
	return append(env, append(ExecutionFence(), "GIT_TERMINAL_PROMPT=0")...)
}

// Command constructs a git process. It is the only place in the repo where this happens.
// dir is the working directory (empty means server's). Use Env() for extra config.
func Command(dir string, args ...string) *exec.Cmd {
	return configureGit(exec.Command(gitBinary, args...), dir)
}

// CommandContext is Command with a cancellable context.
func CommandContext(ctx context.Context, dir string, args ...string) *exec.Cmd {
	return configureGit(exec.CommandContext(ctx, gitBinary, args...), dir)
}

// Run runs a git command to completion and returns the combined output (stdout/stderr).
func Run(dir string, args ...string) (string, error) {
	out, err := Command(dir, args...).CombinedOutput()
	return string(out), err
}

// CommandWithIdentity is Command for a command that creates a commit in one project's name: the
// identity goes on the command line as -c arguments, and into the environment in place of whatever
// this process carries there, because git reads GIT_AUTHOR_* and GIT_COMMITTER_* before it reads any
// config and an inherited one would outrank the pair the command was asked to commit as.
func CommandWithIdentity(dir string, id Identity, args ...string) *exec.Cmd {
	cmd := Command(dir, append(id.flags(), args...)...)
	cmd.Env = withIdentity(Env(), id.envEntries())
	return cmd
}

// RunWithIdentity is Run with a commit identity.
func RunWithIdentity(dir string, id Identity, args ...string) (string, error) {
	out, err := CommandWithIdentity(dir, id, args...).CombinedOutput()
	return string(out), err
}

// identityEnvNames are the variables git reads for a commit identity before it reads any config, so
// one this process inherited outranks the -c arguments a command carries. A command built with an
// Identity has these taken out of its environment and that identity's own put back.
var identityEnvNames = []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL"}

// withIdentity returns env with git's identity variables replaced by entries. They are dropped rather
// than overwritten in place so a half the identity does not name reaches the repository's config
// instead of the value this process happened to inherit.
func withIdentity(env []string, entries []string) []string {
	out := make([]string, 0, len(env)+len(entries))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if slices.Contains(identityEnvNames, name) {
			continue
		}
		out = append(out, kv)
	}
	return append(out, entries...)
}

// GitWaitDelay bounds how long a git subprocess may keep output pipes open after exit or cancellation.
const GitWaitDelay = 30 * time.Second

// configureGit applies construction invariants to an *exec.Cmd.
func configureGit(cmd *exec.Cmd, dir string) *exec.Cmd {
	cmd.Dir = dir
	return subproc.Bind(cmd, Env(), GitWaitDelay)
}
