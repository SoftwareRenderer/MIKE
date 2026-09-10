// Package subproc is the mechanism behind every subprocess this server starts: inherit the
// allowlisted part of the environment, and bound the output pipes alongside it.
//
// Only the mechanism lives here. Each family keeps its own allowlist, as a readable set of names in
// its own package — git.Env, sandboxruntime.PodmanEnv, agent.SafeAgentEnv — because "what does this
// subprocess get" has to be answerable without leaving the package that decides it. What was
// duplicated is the filtering loop, three copies of it, and what drifted is the pairing: cmd.Env and
// cmd.WaitDelay are the two fields a subprocess must carry together, and one podman call site had
// already set the environment while forgetting the delay.
package subproc

import (
	"os"
	"os/exec"
	"strings"
	"time"
)

// EmptyPolicy decides what happens to a variable the server has set to an empty string. It is a
// parameter rather than a package rule because the three families genuinely differ: git and podman
// drop empty values, agent keeps them.
type EmptyPolicy int

const (
	// DropEmpty omits a set-but-empty variable. `SSH_AUTH_SOCK=` is not "unset" to ssh: it reads as a
	// configured-but-broken agent, so the fallback to key files never happens.
	DropEmpty EmptyPolicy = iota
	// KeepEmpty passes the variable through as NAME=. An adapter may distinguish "unset" from "cleared
	// on purpose", and the agent allowlist is a closed set of names that cannot smuggle anything in.
	KeepEmpty
)

// Inherit returns the part of this process's environment that keep accepts, in os.Environ's order.
// The allowlist belongs to the family that needs it; the rules shared by every family - matching a
// prefix case-insensitively, dropping anything not claimed - are here so there is one copy of them.
func Inherit(keep func(name string) bool, empty EmptyPolicy) []string {
	all := os.Environ()
	env := make([]string, 0, len(all))
	for _, kv := range all {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || name == "" || !keep(name) {
			continue
		}
		if value == "" && empty == DropEmpty {
			continue
		}
		env = append(env, kv)
	}
	return env
}

// Bind sets the two fields every subprocess in this server carries, from the constructors that build
// one. Setting them together is the point: a cancelled context kills the child but does not close its
// output pipes, so cmd.Wait still waits on them — and the process that never gets a WaitDelay is the
// one that holds a request goroutine forever after its own death.
//
// It returns the command so a constructor can end with it.
func Bind(cmd *exec.Cmd, env []string, waitDelay time.Duration) *exec.Cmd {
	cmd.Env = env
	cmd.WaitDelay = waitDelay
	return cmd
}

// SlowWait is the threshold a caller passes to Watch when it has no better answer: long past any
// healthy podman operation, short enough to be in the log while the operator is still looking.
const SlowWait = 30 * time.Second

// Watch reports a subprocess still running after `after`, once. Call it around the wait, not the start.
func Watch(what string, after time.Duration, report func(format string, args ...any)) func() {
	timer := time.AfterFunc(after, func() {
		report("%s has been running for %s — the child is alive and nobody is timing it out; "+
			"a wedged podman holds its own database lock and every later call behind it", what, after)
	})
	return func() { timer.Stop() }
}
