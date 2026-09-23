package exec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mike/backend/config"
	"mike/backend/extensions"
	"mike/backend/fsutil"
	"mike/backend/git"
	"mike/backend/llama"
	"mike/backend/store"
	"mike/backend/stream"
	"mike/backend/types"

	"mike/pkg/bridge/protocol"
	"mike/pkg/logging"
)

// toolRunner dispatches a tool call to the handler registered under its name.
//
// It holds the dependencies its handlers use, no lock and no executionState: a tool call is
// serialized by the worktree lock runToolProtected takes, and the state a call runs against arrives
// per call in toolEnv because one executor runs many tasks. The bridge-recovery latch executeTool
// clears after a successful call is taskMu-guarded, so clearing it stays on TaskExecutor.
type toolRunner struct {
	store        *store.Store
	configStore  *config.ConfigStore
	appStreamMgr *stream.AppStreamManager
	// The LSP manager belongs to TaskExecutor, which also hands it to the turn-boundary
	// diagnostics, so the runner reads it instead of holding a copy that can fall behind.
	lspMgr func() lspManager
	// A func value: storing a task belongs to the executor, not to the dispatcher.
	storeTask func(*types.Task, string) error
	// A func value: the executor owns the registry, so a copy here could fall behind it.
	extTools func() *extensions.ToolRegistry
	// A func value so a test here needs no network. The policy is tested in backend/extensions.
	egress egressFunc
	// One entry per name in llama.AllToolNames(); TestEveryDeclaredToolIsDispatched is what fails
	// when a name is missing from it.
	handlers map[string]toolHandler
}

// toolHandler runs one tool call in the environment its dispatch built. A handler takes from env
// only what it needs: a board tool reads env.task, a file tool resolves a reader and a writer, bash
// streams through env.output.
type toolHandler func(ctx context.Context, args map[string]any, env *toolEnv) (string, bool)

// One wiring definition, so every executor's dispatcher is wired the same way.
func (te *TaskExecutor) newToolRunner() *toolRunner {
	r := &toolRunner{
		store:        te.store,
		configStore:  te.configStore,
		appStreamMgr: te.appStreamMgr,
		lspMgr:       func() lspManager { return te.lspMgr },
		storeTask:    te.storeTask,
		extTools:     func() *extensions.ToolRegistry { return te.extTools },
		egress:       extensions.Egress,
	}
	r.handlers = map[string]toolHandler{
		llama.ToolReadFile:      r.runReadFile,
		llama.ToolWriteFile:     r.runWriteFile,
		llama.ToolEditFile:      r.runEditFile,
		llama.ToolBash:          r.runBash,
		llama.ToolAskQuestion:   r.runAskQuestion,
		llama.ToolRequestReview: r.runRequestReview,
		llama.ToolCreateTask:    r.runCreateTask,
		llama.ToolViewTasks:     r.runViewTasks,
		llama.ToolEditTask:      r.runEditTask,
		llama.ToolLSPReferences: r.runLSPReferences,
		llama.ToolLSPDefinition: r.runLSPDefinition,
		llama.ToolLSPHover:      r.runLSPHover,
		llama.ToolLSPRename:     r.runLSPRename,
	}
	return r
}

// run dispatches a tool call. Sandbox callbacks come from state; when they are nil the task's
// project runs without a sandbox and the tool call works on the host, and a sandboxed task with no
// callbacks is refused instead (toolEnv.hostGuard). ctx cancels long-running tools (bash) when the
// task stops, and outputCallback, if non-nil, receives incremental output from the streaming tools.
func (r *toolRunner) run(ctx context.Context, name string, args map[string]any, worktreePath string, task *types.Task, state *executionState, outputCallback func(toolID string, text string)) (string, bool) {
	env := newToolEnv(state, worktreePath, task, outputCallback)
	handler, ok := r.handlers[name]
	if !ok {
		// Not a core tool; it may be one an extension contributes.
		return r.runContributedTool(ctx, name, args, env)
	}
	return handler(ctx, args, env)
}

// failed is how a tool call that did not succeed answers. The message is the whole answer: a failed
// call that returns nothing is indistinguishable from one still running.
func failed(err error) (string, bool) {
	return fmt.Sprintf("Error: %v", err), false
}

// orFail answers with a handler's own result, or with its error in the failed shape.
func orFail(result string, err error) (string, bool) {
	if err != nil {
		return failed(err)
	}
	return result, true
}

// toolEnv is what one tool call runs against: the task, its worktree, its output callback, and the
// sandbox callbacks it started with. It borrows the executionState for the call and owns none of it.
//
// The snapshot is load-bearing — a recreate swaps the callbacks mid-run, and mixing the old
// container's reader with the new one's writer looks like a broken pipe and gets retried. Two reads
// go back to live state instead, reader and recoverBridge, because a retry after a recovery must not
// reuse the connection that just died.
type toolEnv struct {
	state        *executionState
	task         *types.Task
	worktreePath string
	// output receives incremental output from a streaming tool; nil when nobody is watching.
	output func(toolID string, text string)
	sb     sandboxState
}

// newToolEnv takes the one snapshot every callback the call then uses comes from.
func newToolEnv(state *executionState, worktreePath string, task *types.Task, output func(toolID string, text string)) *toolEnv {
	return &toolEnv{
		state:        state,
		task:         task,
		worktreePath: worktreePath,
		output:       output,
		sb:           state.sandbox(),
	}
}

// recoverBridge detects bridge errors and attempts automatic recovery. It reports whether recovery
// succeeded and the callbacks were refreshed, and reads the live state to do it: recovery is where
// the snapshot is known to be stale.
func (e *toolEnv) recoverBridge(errStr string) bool {
	if !strings.Contains(errStr, "broken pipe") &&
		!strings.Contains(errStr, "client closed") &&
		!strings.Contains(errStr, "connection closed") {
		return false
	}
	recover := e.state.sandbox().recoverBridge
	if recover == nil {
		return false
	}
	return recover()
}

// hostGuard is the closed door behind the host fallbacks below: what a tool call uses when the task
// has no sandbox callbacks, which means its project runs without a sandbox. A sandboxed task must
// never reach one, so a missing callback fails closed here rather than working on the host behind
// the container.
func (e *toolEnv) hostGuard(what string) error {
	if e.state.sandboxEnabled {
		return fmt.Errorf("cannot %s on the host: this task's project is sandboxed and its sandbox bridge is not connected", what)
	}
	return nil
}

func (e *toolEnv) hostRead() readFunc {
	return func(_ context.Context, p string) ([]byte, error) {
		if err := e.hostGuard("read files"); err != nil {
			return nil, err
		}
		norm := normalizePath(e.state.projectName, p)
		if !filepath.IsAbs(norm) {
			norm = filepath.Join(e.worktreePath, norm)
		}
		return readFileCapped(norm)
	}
}

func (e *toolEnv) hostWrite() writeFunc {
	if err := e.hostGuard("write files"); err != nil {
		return func(_ context.Context, p, c string) error { return err }
	}
	return func(_ context.Context, p, c string) error {
		norm := normalizePath(e.state.projectName, p)
		if !filepath.IsAbs(norm) {
			norm = filepath.Join(e.worktreePath, norm)
		}
		if err := os.MkdirAll(filepath.Dir(norm), 0o700); err != nil {
			return err
		}
		// Atomic: an interrupted write costs the temporary, never the file. Both watchers skip
		// fsutil's temporary names.
		return fsutil.WriteFileAtomic(norm, []byte(c))
	}
}

// reader answers which reader a tool call uses: the container's when the task has a bridge, the host
// fallback otherwise. It reads live state, not the snapshot, so a retry after bridge recovery does
// not reuse the reader of the connection that just died.
func (e *toolEnv) reader() readFunc {
	if r := e.state.sandbox().read; r != nil {
		return r
	}
	return e.hostRead()
}

// writer is the write callback the call was dispatched with, or the host fallback when its project
// has no sandbox. Retrying after a recovery answers through this too: the snapshot is what the call
// committed to.
func (e *toolEnv) writer() writeFunc {
	if e.sb.write != nil {
		return e.sb.write
	}
	return e.hostWrite()
}

func (r *toolRunner) runReadFile(ctx context.Context, args map[string]any, env *toolEnv) (string, bool) {
	path, _ := args["path"].(string)
	data, err := env.reader()(ctx, path)
	if err != nil {
		if env.recoverBridge(err.Error()) {
			if data, err = env.reader()(ctx, path); err != nil {
				return failed(err)
			}
		} else {
			return failed(err)
		}
	}
	content, refuse := windowLines(string(data), args)
	if refuse != "" {
		return refuse, false
	}
	// Truncate to keep a huge file out of the WebSocket frames: the LLM doesn't need all of it, and
	// the notice tells it more exists.
	const maxReadSize = 50 * 1024 // 50KB
	if len(content) > maxReadSize {
		content = content[:maxReadSize] + "\n\n... (file truncated at 50KB, use offset/limit parameters to read remaining lines) ..."
	}
	return content, true
}

// windowLines applies a read_file offset/limit window so the LLM can read any region of a large
// file. A non-empty second result is the message for a window the file cannot satisfy.
func windowLines(content string, args map[string]any) (string, string) {
	lines := strings.Split(content, "\n")
	offset, hasOffset := args["offset"].(float64)
	if !hasOffset || int(offset) <= 0 {
		limit, hasLimit := args["limit"].(float64)
		if !hasLimit || int(limit) <= 0 {
			return content, ""
		}
		// limit without offset: take the first N lines, silently clamping an oversized limit.
		if int(limit) < len(lines) {
			lines = lines[:int(limit)]
		}
		return strings.Join(lines, "\n"), ""
	}
	offsetIdx := int(offset) - 1 // 1-indexed to 0-indexed
	if offsetIdx >= len(lines) {
		// An offset past the end is answered explicitly so the LLM can correct its request, rather
		// than silently returning empty.
		return "", fmt.Sprintf("Error: offset %d is past the end of the file (file has %d line(s)). Use a valid offset.", int(offset), len(lines))
	}
	lines = lines[offsetIdx:]
	// A valid offset silently clamps an oversized limit to the remaining lines.
	if limit, ok := args["limit"].(float64); ok && int(limit) > 0 && int(limit) < len(lines) {
		lines = lines[:int(limit)]
	}
	return strings.Join(lines, "\n"), ""
}

func (r *toolRunner) runWriteFile(ctx context.Context, args map[string]any, env *toolEnv) (string, bool) {
	path, _ := args["path"].(string)
	content, _ := args["content"].(string)
	write := env.writer()
	if err := write(ctx, path, content); err != nil {
		if env.recoverBridge(err.Error()) {
			write = env.writer()
			if err := write(ctx, path, content); err != nil {
				return failed(err)
			}
		} else {
			return failed(err)
		}
	}
	return fmt.Sprintf("Wrote %d bytes to %s", len(content), path), true
}

// textEdit is one edit_file replacement as the provider sends it.
type textEdit struct {
	OldText string `json:"oldText"`
	NewText string `json:"newText"`
}

func (r *toolRunner) runEditFile(ctx context.Context, args map[string]any, env *toolEnv) (string, bool) {
	path, _ := args["path"].(string)
	editsRaw, ok := args["edits"].(json.RawMessage)
	if !ok {
		// Try to marshal from the interface{}
		editsRaw, _ = json.Marshal(args["edits"])
	}
	var edits []textEdit
	if err := json.Unmarshal(editsRaw, &edits); err != nil {
		return failed(fmt.Errorf("failed to parse edits: %v", err))
	}
	if len(edits) == 0 {
		return failed(errors.New("no edits provided"))
	}

	// In sandbox mode, delegate the read-modify-write to the sandbox agent via MethodEdit. This
	// keeps the file content inside the container and applies the replacements atomically under a
	// per-file lock. The edit callback is nil in non-sandbox mode, falling back to read+edit+write
	// below.
	if edit := env.sb.edit; edit != nil {
		editsProto := make([]protocol.Edit, len(edits))
		for i, e := range edits {
			editsProto[i] = protocol.Edit{OldText: e.OldText, NewText: e.NewText}
		}
		if err := edit(ctx, path, editsProto); err != nil {
			if env.recoverBridge(err.Error()) {
				if err := edit(ctx, path, editsProto); err != nil {
					return failed(err)
				}
			} else {
				return failed(err)
			}
		}
		return fmt.Sprintf("Edited %s (%d edit(s) applied)", path, len(edits)), true
	}

	read := env.reader()
	data, err := read(ctx, path)
	if err != nil {
		if env.recoverBridge(err.Error()) {
			read = env.reader()
			if data, err = read(ctx, path); err != nil {
				return failed(err)
			}
		} else {
			return failed(err)
		}
	}
	result, refuse := applyEditReplacements(string(data), edits)
	if refuse != "" {
		return refuse, false
	}
	write := env.writer()
	if err := write(ctx, path, result); err != nil {
		if env.recoverBridge(err.Error()) {
			write = env.writer()
			if err := write(ctx, path, result); err != nil {
				return failed(err)
			}
		} else {
			return failed(err)
		}
	}
	return fmt.Sprintf("Edited %s (%d edit(s) applied)", path, len(edits)), true
}

// applyEditReplacements applies every edit to content in one pass. A non-empty second result is the
// message naming the edit it refused, and nothing was written.
//
// Positions are all found in the original content, so an oldText must be present exactly once and no
// two edits may overlap. Building the result once is O(file size); rebuilding a full string per edit
// would be O(edits × file size).
func applyEditReplacements(content string, edits []textEdit) (string, string) {
	type editPos struct {
		index   int
		start   int
		end     int
		newText string
	}
	var positions []editPos
	for i, e := range edits {
		if e.OldText == "" {
			return "", fmt.Sprintf("Error: edits[%d].oldText is empty", i)
		}
		idx := strings.Index(content, e.OldText)
		if idx == -1 {
			return "", fmt.Sprintf("Error: edits[%d].oldText not found in file. Ensure it matches exactly (whitespace, indentation). Got %d lines in file.", i, len(strings.Split(content, "\n")))
		}
		if strings.Contains(content[idx+1:], e.OldText) {
			return "", fmt.Sprintf("Error: edits[%d].oldText appears multiple times in file. Make it more specific by including surrounding context.", i)
		}
		positions = append(positions, editPos{index: i, start: idx, end: idx + len(e.OldText), newText: e.NewText})
	}
	sort.Slice(positions, func(i, j int) bool {
		return positions[i].start < positions[j].start
	})
	for i := 0; i < len(positions)-1; i++ {
		if positions[i].end > positions[i+1].start {
			return "", fmt.Sprintf("Error: edits[%d] and edits[%d] overlap. Merge them into a single edit or ensure they don't overlap.", positions[i].index, positions[i+1].index)
		}
	}

	var b strings.Builder
	b.Grow(len(content) + 64)
	prev := 0
	for _, p := range positions {
		b.WriteString(content[prev:p.start])
		b.WriteString(p.newText)
		prev = p.end
	}
	b.WriteString(content[prev:])
	return b.String(), ""
}

func (r *toolRunner) runBash(ctx context.Context, args map[string]any, env *toolEnv) (string, bool) {
	cmd, _ := args["command"].(string)
	if strings.TrimSpace(cmd) == "" {
		// An empty command is a call that lost its argument, not a command that printed nothing.
		return failed(errors.New("command is required"))
	}
	timeout := int64(5)
	if t, ok := args["timeout"].(float64); ok {
		timeout = int64(t)
	}
	if timeout > 300 {
		timeout = 300
	}

	// The snapshot, until a recovery replaces both callbacks together.
	run := func(exec execFunc, stream execStreamFunc) (string, error) {
		// Bridge errors mean the command never reached the agent, so retrying is safe.
		if stream != nil && env.output != nil {
			return stream(ctx, cmd, timeout, func(text string) { env.output(env.task.ID, text) })
		} else if exec != nil {
			// ctx propagates task cancellation
			return exec(ctx, cmd, timeout)
		}
		if err := env.hostGuard("run commands"); err != nil {
			return "", err
		}
		// Direct execution (no sandbox), streaming output line by line. The timeout is enforced
		// there, so a command that never exits can't pin the tool.
		var onLine func(text string)
		if env.output != nil {
			onLine = func(text string) { env.output(env.task.ID, text) }
		}
		return execCommandStream(ctx, env.worktreePath, cmd, timeout, onLine)
	}

	out, err := run(env.sb.exec, env.sb.execStream)
	if err != nil && env.recoverBridge(err.Error()) {
		refreshed := env.state.sandbox()
		out, err = run(refreshed.exec, refreshed.execStream)
	}

	// Strip ANSI escape codes so the LLM sees clean text and the frontend displays it without
	// garbled sequences.
	out = stripANSI(out)
	if err != nil {
		return fmt.Sprintf("Failed: %v\n%s", err, out), false
	}
	const maxOutputSize = 50 * 1024 // 50KB
	if len(out) > maxOutputSize {
		out = out[:maxOutputSize] + "\n\n... (output truncated at 50KB) ..."
	}
	return out, true
}

func (r *toolRunner) runAskQuestion(_ context.Context, args map[string]any, _ *toolEnv) (string, bool) {
	question, _ := args["question"].(string)
	if question == "" {
		return failed(errors.New("question is required"))
	}
	return question, true
}

func (r *toolRunner) runRequestReview(_ context.Context, _ map[string]any, env *toolEnv) (string, bool) {
	// In plan mode, require at least one task created via create_task
	if env.task.IsPlanMode() {
		createdIDs := r.planCreatedTaskIDs(env.task, env.state)
		if len(createdIDs) == 0 {
			return "Error: Cannot complete planning without creating tasks. Use create_task() to add at least one task first.", false
		}
		// Count independent tasks vs nested subtasks
		independentCount := 0
		nestedCount := 0
		for _, id := range createdIDs {
			t, err := r.store.GetTaskByID(id)
			if err != nil {
				continue
			}
			if t.ParentTaskID == "" {
				independentCount++
			} else {
				nestedCount++
			}
		}
		// Build accurate summary
		var summary string
		if independentCount > 0 && nestedCount > 0 {
			summary = fmt.Sprintf("Plan created with %d task%s and %d subtask%s. Awaiting approval.",
				independentCount, git.PluralS(independentCount), nestedCount, git.PluralS(nestedCount))
		} else if independentCount > 0 {
			summary = fmt.Sprintf("Plan created with %d task%s. Awaiting approval.",
				independentCount, git.PluralS(independentCount))
		} else {
			summary = fmt.Sprintf("Plan created with %d subtask%s. Awaiting approval.",
				nestedCount, git.PluralS(nestedCount))
		}
		logging.For(logging.Tools).Infof("plan mode: %s", summary)
	}
	return "Task complete", true
}

func (r *toolRunner) runCreateTask(_ context.Context, args map[string]any, env *toolEnv) (string, bool) {
	return orFail(r.handleCreateTask(args, env.task, env.state))
}

func (r *toolRunner) runViewTasks(_ context.Context, _ map[string]any, env *toolEnv) (string, bool) {
	return orFail(r.handleViewTasks(env.task))
}

func (r *toolRunner) runEditTask(_ context.Context, args map[string]any, env *toolEnv) (string, bool) {
	return orFail(r.handleEditTask(args, env.task))
}

func (r *toolRunner) runLSPReferences(ctx context.Context, args map[string]any, env *toolEnv) (string, bool) {
	return orFail(r.handleLSPSymbolRequest(ctx, args, env.worktreePath, "textDocument/references", map[string]any{"includeDeclaration": true}, env.reader()))
}

func (r *toolRunner) runLSPDefinition(ctx context.Context, args map[string]any, env *toolEnv) (string, bool) {
	return orFail(r.handleLSPSymbolRequest(ctx, args, env.worktreePath, "textDocument/definition", nil, env.reader()))
}

func (r *toolRunner) runLSPHover(ctx context.Context, args map[string]any, env *toolEnv) (string, bool) {
	return orFail(r.handleLSPSymbolRequest(ctx, args, env.worktreePath, "textDocument/hover", nil, env.reader()))
}

// runLSPRename writes through the same callbacks write_file uses, so a rename lands as one
// checkpoint's worth of changes in the task diff.
func (r *toolRunner) runLSPRename(ctx context.Context, args map[string]any, env *toolEnv) (string, bool) {
	return orFail(r.handleLSPRename(ctx, args, env.worktreePath, env.reader(), env.writer()))
}
