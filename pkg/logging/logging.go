// Package logging gives every diagnostic the same shape:
//
//	logging.For(logging.Merge).With(logging.TaskID, task.ID).Infof("merged into %s", branch)
//	time=... level=INFO component=merge task_id=frost-amber-quartz msg="merged into main"
//
// Call sites used to type their own [bracket] prefix - 91 distinct tags - and put ids in the
// message, so one subsystem answered to several names and "everything about this task" was a
// grep. Subsystem and ids are fields now; the message is only the message. Output is a text
// handler on stderr, so the log stays greppable.
//
// Levels, in order of how much they should be noticed:
//
//	Error - the fault reaches the user: an error event, an HTTP error response, a failed start.
//	Warn  - something went wrong and the run continued: a retry, a fallback in use.
//	Info  - a state change worth keeping: a task started, a sandbox was created, a merge landed.
//	Debug - chatter nobody acts on, but that explains a surprise: per-request plumbing.
//
// MIKE_LOG_LEVEL or --log-level picks the threshold; default info.
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
)

// Attribute keys. Using the constants means a filter survives a rename.
const (
	TaskID    = "task_id"
	ProjectID = "project_id"
)

// Components is the whole vocabulary of subsystem names. Copy one; do not invent one. The merges
// below are deliberate -- old tags each one replaced:
//
//	merge      <- merge, sync-merge, commit-and-done, move, move-descendants,
//	             ensure-parent-branch, propagate-subtasks-auto-merge, merge-msg, auto-commit
//	worktree   <- gc, prune, worktree, stage, checkpoint, snapshots, diff-snapshot
//	startup    <- auto-start, lazy-start, start, start-parent, start-subtask, init, connect
//	tools      <- tool, ask_question, create_task, edit_task, view_tasks, request_review
//	generate   <- title-gen, commit-message, checkpoint-label, commit-preview-stream
//	recovery   <- recover, panic, PANIC, agent-panic   (bridge-recovery -> bridge)
//	thread     <- thread, thread-fork, thread-switch, thread-name
//	watcher    <- watcher, branch-watcher, project-watcher
//	lsp        <- lsp, lsp-diag, lsp-env
//	sandbox    <- sandbox, sandbox-config, ext-sandbox   (sandboxruntime -> runtime)
//	agent      <- agent, wasm-adapter, agent-registry
//
// Name the subsystem a reader would grep for, and prefer an entry that already exists.
const (
	Agent      = "agent"
	Auth       = "auth"
	Bridge     = "bridge"
	Exec       = "exec"
	Extensions = "extensions"
	Generate   = "generate"
	Git        = "git"
	Host       = "host"
	HTTP       = "http"
	LLM        = "llm"
	LSP        = "lsp"
	// MCP is the client side of a registered Model Context Protocol server: connecting, listing its
	// tools, calling one, and the registry row that holds its address and headers.
	MCP       = "mcp"
	Main      = "main"
	Managers  = "managers"
	Merge     = "merge"
	Prompts   = "prompts"
	Recovery  = "recovery"
	Runtime   = "runtime"
	Sandbox   = "sandbox"
	Startup   = "startup"
	Store     = "store"
	Stream    = "stream"
	Summarize = "summarize"
	Terminal  = "terminal"
	Thread    = "thread"
	TLS       = "tls"
	Tools     = "tools"
	Trace     = "trace"
	// Updates is the release check and the staged upgrade (docs/auto-update.md).
	Updates  = "updates"
	Watcher  = "watcher"
	Worktree = "worktree"
)

var (
	mu       sync.RWMutex
	levelVar slog.LevelVar
	base     = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: &levelVar}))
	cache    = map[string]*Logger{}
)

// Logger is bound to one component and a set of attributes. The f-methods format eagerly, as
// log.Printf did, so a converted call site says the same thing at the same time.
type Logger struct {
	inner *slog.Logger
}

// For returns the logger for a component. Repeated calls share one logger.
func For(component string) *Logger {
	mu.RLock()
	l, ok := cache[component]
	mu.RUnlock()
	if ok {
		return l
	}
	mu.Lock()
	defer mu.Unlock()
	if l, ok := cache[component]; ok {
		return l
	}
	l = &Logger{inner: base.With(slog.String("component", component))}
	cache[component] = l
	return l
}

// With returns a logger carrying one more attribute. It takes a single pair so a key cannot be
// left without its value: For(Merge).With(TaskID, task.ID).
func (l *Logger) With(key string, value any) *Logger {
	if key == "" {
		return l
	}
	return &Logger{inner: l.inner.With(slog.Any(key, value))}
}

// There is no WithSub. A second free-form label re-opens the per-call naming this package closes:
// detail goes in the message, and detail worth filtering becomes a component.

func (l *Logger) logf(level slog.Level, format string, args ...any) {
	// Skip the Sprintf when the level is off.
	if !l.inner.Enabled(context.Background(), level) {
		return
	}
	l.inner.Log(context.Background(), level, fmt.Sprintf(format, args...))
}

// Debugf logs at Debug.
func (l *Logger) Debugf(format string, args ...any) { l.logf(slog.LevelDebug, format, args...) }

// Infof logs at Info.
func (l *Logger) Infof(format string, args ...any) { l.logf(slog.LevelInfo, format, args...) }

// Warnf logs at Warn.
func (l *Logger) Warnf(format string, args ...any) { l.logf(slog.LevelWarn, format, args...) }

// Errorf logs at Error.
func (l *Logger) Errorf(format string, args ...any) { l.logf(slog.LevelError, format, args...) }

// Info logs a message with no formatting.
func (l *Logger) Info(msg string) { l.inner.Info(msg) }

// Warn takes a message with no format and no args, for a string that is already the sentence — a
// resolved-configuration warning, say. The f-suffixed form is what go vet checks; this one exists so
// a caller does not write Warnf("%s", x) to get the same line.
func (l *Logger) Warn(msg string) { l.inner.Warn(msg) }

// ParseLevel reports an error for a name that is not a level, so startup can refuse instead of
// quietly changing how much of the log the operator sees.
func ParseLevel(name string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info", "":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return slog.LevelInfo, fmt.Errorf("unknown log level %q (want debug, info, warn or error)", name)
}

// SetLevel sets verbosity from a name; an unknown name means Info. Startup uses ParseLevel and
// refuses instead of guessing.
func SetLevel(name string) {
	lvl, _ := ParseLevel(name)
	levelVar.Set(lvl)
}

// setOutput redirects the log and returns what to restore it to. It is process-wide and unexported:
// only TestSink may re-point the shared log.
func setOutput(w io.Writer) (restore func()) {
	mu.Lock()
	defer mu.Unlock()
	prev := base
	base = slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: &levelVar}))
	for k := range cache {
		delete(cache, k)
	}
	return func() {
		mu.Lock()
		defer mu.Unlock()
		base = prev
		for k := range cache {
			delete(cache, k)
		}
	}
}

// TestSink captures the log for the duration of a test. It replaces log.SetOutput(&buf), which no
// longer sees these lines.
func TestSink() (buf *SafeBuffer, restore func()) {
	b := &SafeBuffer{}
	return b, setOutput(b)
}

// SafeBuffer is written concurrently: the exec loop, websocket goroutines and shutdown steps overlap
// at shutdown.
type SafeBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *SafeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// String returns everything written so far.
func (b *SafeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Reset discards what has been captured so far.
func (b *SafeBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
}

// Lines returns the captured output split into lines.
func (b *SafeBuffer) Lines() []string {
	s := strings.TrimSpace(b.String())
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
