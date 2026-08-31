package sandbox

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// TaskForWorktree returns the task whose sandbox container has the given host
// workspace path mounted. It answers the question a caller holding only a host
// path has: "is this workspace running in a container right now, and which one?".
// Returns false when no live container mounts that exact path — a task that has
// not started, one whose container was stopped, or a project with sandboxing
// disabled (which never creates a container).
func (sm *SandboxManager) TaskForWorktree(hostPath string) (string, bool) {
	if sm == nil {
		return "", false
	}
	abs, err := filepath.Abs(hostPath)
	if err != nil {
		return "", false
	}
	abs = filepath.Clean(abs)

	sm.mu.Lock()
	defer sm.mu.Unlock()
	for taskID, mounted := range sm.worktreePaths {
		if _, active := sm.active[taskID]; !active {
			continue // stopped container: its mounts are gone
		}
		if filepath.Clean(mounted) == abs {
			return taskID, true
		}
	}
	return "", false
}

// TaskProbe runs one short, read-only command inside the sandbox container that
// already belongs to a workspace, and returns its output.
//
// It exists so a language extension can ask its own question about the
// environment its workspace really builds in — where the Go module cache lives,
// where node resolves packages from — without the core knowing what a module
// cache is, and without ever running an extension-declared command on the host.
// The container is the one the task is already using; nothing is created here.
type TaskProbe struct {
	sm *SandboxManager
}

// NewTaskProbe wires a probe to a sandbox manager. Returns nil for a nil
// manager so an unwired server simply reports "no detection".
func NewTaskProbe(sm *SandboxManager) *TaskProbe {
	if sm == nil {
		return nil
	}
	return &TaskProbe{sm: sm}
}

// ExecInTaskSandbox runs argv (never a shell string built from user text: each
// element is quoted as a single word) in the container that has workspacePath
// mounted, and returns its combined output.
//
// The call is bounded twice over: ctx cancels the caller, and timeout bounds the
// command itself, because podman exec has no deadline of its own — a wedged
// container must slow one diagnostics pass, never park a goroutine.
func (p *TaskProbe) ExecInTaskSandbox(ctx context.Context, workspacePath string, argv []string, timeout time.Duration) (string, error) {
	if p == nil || len(argv) == 0 {
		return "", fmt.Errorf("no probe command")
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	taskID, ok := p.sm.TaskForWorktree(workspacePath)
	if !ok {
		return "", fmt.Errorf("no running sandbox container for workspace %s", workspacePath)
	}

	type result struct {
		out string
		err error
	}
	done := make(chan result, 1) // buffered: a late answer must not leak the goroutine
	go func() {
		out, err := p.sm.Exec(taskID, shellJoin(argv), nil)
		done <- result{out, err}
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-timer.C:
		// The podman exec is still running; it cannot be cancelled from here, but
		// its output is no longer wanted. Abandoning it is what keeps the turn moving.
		return "", fmt.Errorf("probe timed out after %s in task %s", timeout, taskID)
	case r := <-done:
		return r.out, r.err
	}
}

// shellJoin renders argv as a command line for `sh -c` (which is how
// SandboxManager.Exec runs its command). Every word is single-quoted, so a
// path with a space stays one argument and no metacharacter is live.
func shellJoin(argv []string) string {
	quoted := make([]string, len(argv))
	for i, a := range argv {
		quoted[i] = shellQuote(a)
	}
	return strings.Join(quoted, " ")
}

// shellQuote single-quotes one word. A literal quote inside the word closes the
// quoting, adds an escaped quote, and reopens it — the standard trick, and the
// reason no argument can break out of its own word.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
