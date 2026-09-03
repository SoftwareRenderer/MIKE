package exec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kanban/backend/config"
	"kanban/backend/extensions"
	"kanban/backend/git"
	"kanban/backend/lsp"
	"kanban/backend/store"
	"kanban/backend/stream"
	"kanban/backend/types"
)

func newTestStreamMgr(s *store.Store) *stream.AppStreamManager {
	return stream.NewAppStreamManager(s, nil)
}

// The advisory channel: environment notes the model must see but the user never said.
// They must never be persisted as role "user" (a fake user bubble, and a summarizer that
// credits the user with them), and they must never land between a turn's tool_call rows
// (which used to cost the rebuild that turn's remaining tool results).

func diagTestSetup(t *testing.T, s *store.Store, projectID, taskID string) *taskExecSetup {
	t.Helper()
	createProject(t, s, projectID, "diagproj")
	now := time.Now().UTC()
	task := &types.Task{
		ID: taskID, ProjectID: projectID, Title: "diag", Prompt: "diag prompt",
		Status: types.StatusInProgress, OperatingMode: "act", LlamaServerURL: "http://test:8080",
		ModelName: "test-model", CreatedAt: now, UpdatedAt: now,
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	stored, err := s.GetTaskByID(taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	return &taskExecSetup{task: stored, project: &types.Project{ID: projectID, Name: "diagproj"}, projectID: projectID}
}

func TestFlushAdvisories_PersistsAdvisoryRoleAndKeepsOrder(t *testing.T) {
	s := testStore(t)
	te := &TaskExecutor{store: s, appStreamMgr: newTestStreamMgr(s), tasks: make(map[string]*executionState)}
	setup := diagTestSetup(t, s, "adv-proj", "adv-task")

	lc := &loopContext{messages: []types.ChatMessage{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "do the thing"},
		{Role: "assistant", Content: "edited files", ToolCalls: []types.ToolCallMsg{{ID: "A"}, {ID: "B"}}},
		{Role: "tool", Content: "ok", ToolCallID: "A"},
		{Role: "tool", Content: "ok", ToolCallID: "B"},
	}}
	queueAdvisory(lc, advisory{kind: advisoryDiagnostics, severity: "error", text: "LSP diagnostics: 3 errors"})
	queueAdvisory(lc, advisory{kind: advisoryMergeConflict, text: "Resolve the conflict in b.go"})
	te.flushAdvisories(setup, lc)

	// The notes come after the turn's own messages, in queue order.
	roles := make([]string, 0, len(lc.messages))
	for _, m := range lc.messages {
		roles = append(roles, m.Role)
	}
	want := "system user assistant tool tool advisory advisory"
	if got := strings.Join(roles, " "); got != want {
		t.Fatalf("in-memory roles = %q, want %q", got, want)
	}
	if lc.messages[len(lc.messages)-1].Content != "Resolve the conflict in b.go" {
		t.Errorf("last advisory = %q", lc.messages[len(lc.messages)-1].Content)
	}
	if len(lc.pendingAdvisories) != 0 {
		t.Errorf("queue not drained: %d left", len(lc.pendingAdvisories))
	}

	msgs, err := s.GetTaskMessages(setup.task.ID, "")
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	var advisories []types.TaskMessage
	for _, m := range msgs {
		if m.Role == "advisory" {
			advisories = append(advisories, m)
		}
		if m.Role == "user" {
			t.Errorf("an advisory was persisted as a user message: %q", m.Content)
		}
	}
	if len(advisories) != 2 {
		t.Fatalf("advisory rows = %d, want 2", len(advisories))
	}
	if advisories[0].Severity != "error" {
		t.Errorf("severity not persisted: %q", advisories[0].Severity)
	}
	// kind is what titles the note after a reload, so it has to be on the row, not only in
	// the live WS frame.
	if advisories[0].Kind != advisoryDiagnostics || advisories[1].Kind != advisoryMergeConflict {
		t.Errorf("kind not persisted: %q / %q", advisories[0].Kind, advisories[1].Kind)
	}
	if advisories[1].Severity != "" {
		t.Errorf("severity for the unmarked note = %q, want empty", advisories[1].Severity)
	}
	if advisories[0].ThreadID != setup.task.CurrentThreadID {
		t.Errorf("advisory ThreadID = %q, want %q", advisories[0].ThreadID, setup.task.CurrentThreadID)
	}

	// A rebuilt conversation carries them as advisories, not as user turns.
	rebuilt := rebuildRows(msgs)
	var rebuiltAdvisory int
	for _, m := range rebuilt {
		if m.Role == "advisory" && m.Content == "LSP diagnostics: 3 errors" {
			rebuiltAdvisory++
		}
	}
	if rebuiltAdvisory != 1 {
		t.Fatalf("rebuilt advisory messages = %d, want 1", rebuiltAdvisory)
	}
}

func TestFlushAdvisories_EmptyAndBlank(t *testing.T) {
	s := testStore(t)
	te := &TaskExecutor{store: s, appStreamMgr: newTestStreamMgr(s), tasks: make(map[string]*executionState)}
	setup := diagTestSetup(t, s, "adv-proj2", "adv-task2")

	lc := &loopContext{}
	te.flushAdvisories(setup, lc) // must not panic or write
	queueAdvisory(lc, advisory{kind: advisoryDiagnostics, text: "   \n  "})
	te.flushAdvisories(setup, lc)

	msgs, err := s.GetTaskMessages(setup.task.ID, "")
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("blank advisory written: %+v", msgs)
	}
	if len(lc.messages) != 0 {
		t.Fatalf("blank advisory appended to the conversation: %+v", lc.messages)
	}
}

// TestPrepareMergeConflictTurn_DeliversBeforeTheNextCall is the ordering regression: both
// merge-conflict sites sit before the turn-boundary flush and continue straight into the
// next LLM call, so a note left in the queue would reach the model one call late — it
// would start resolving the conflict before ever being told which files conflict.
func TestPrepareMergeConflictTurn_DeliversBeforeTheNextCall(t *testing.T) {
	s := testStore(t)
	te := &TaskExecutor{store: s, appStreamMgr: newTestStreamMgr(s), tasks: make(map[string]*executionState)}
	setup := diagTestSetup(t, s, "mc-proj", "mc-task")
	lc := &loopContext{
		messages: []types.ChatMessage{{Role: "system", Content: "sys"}, {Role: "user", Content: "merge it"}},
		detector: newLoopDetector(),
		// A half-built turn whose buffers the helper must drop along with its tool calls.
		pendingToolCalls: []types.ChatToolCall{{ID: "stale"}},
	}
	// Prompt templates are read from prompts/ relative to the process CWD (the server runs
	// from the repo root), so the render only works from that directory.
	chdirToRepoRoot(t)

	te.prepareMergeConflictTurn(setup, lc, []string{"src/b.go"})

	if len(lc.pendingAdvisories) != 0 {
		t.Fatalf("instructions still queued (%d): the next LLM call would not see them", len(lc.pendingAdvisories))
	}
	last := lc.messages[len(lc.messages)-1]
	if last.Role != "advisory" {
		t.Fatalf("last message in context = %q, want advisory", last.Role)
	}
	if !strings.Contains(last.Content, "src/b.go") {
		t.Errorf("instructions do not name the conflicted file:\n%s", last.Content)
	}
	if lc.pendingToolCalls != nil {
		t.Errorf("pendingToolCalls not reset: %+v", lc.pendingToolCalls)
	}

	rows, err := s.GetTaskMessages(setup.task.ID, "")
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	if len(rows) != 1 || rows[0].Role != "advisory" || rows[0].Kind != advisoryMergeConflict {
		t.Fatalf("persisted rows = %+v, want one advisory of kind %q", rows, advisoryMergeConflict)
	}
	// The full instructions are what gets persisted, so a run restarted mid-conflict
	// rebuilds them instead of the terse marker row the old code wrote.
	if !strings.Contains(rows[0].Content, "src/b.go") {
		t.Errorf("persisted advisory does not carry the instructions: %q", rows[0].Content)
	}
}

func TestComposeDiagnosticsAdvisory_SilentWhenNothingSurvives(t *testing.T) {
	// The whole point of suppressing: no findings left means no note at all, not
	// "Fix issues in …" with nothing under it.
	if _, ok := composeDiagnosticsAdvisory(0, nil); ok {
		t.Error("an empty pass produced an advisory")
	}
	a, ok := composeDiagnosticsAdvisory(1, []string{"Fix issues in a.go:\n1. unused: x"})
	if !ok || !strings.Contains(a.text, "a.go") {
		t.Errorf("advisory = %+v", a)
	}
}

// --- diagnostics discovery -------------------------------------------------------

func lspGoExtension() *extensions.Extension {
	return &extensions.Extension{
		ID: "gopls", Name: "gopls", Version: "1.0.0", Type: extensions.ExtensionTypeLSP, Enabled: true,
		Manifest: extensions.ExtensionManifest{
			DisplayName: "Go Language Server", Version: "1.0.0",
			Capabilities: &extensions.Capabilities{
				LSP: &extensions.LSPConfig{Command: "gopls", Args: []string{"serve"},
					Language: "go", FileTypes: []extensions.LSPFileType{{Extension: "go", LanguageID: "go"}}},
			},
		},
	}
}

func lspRegistryWith(t *testing.T, exts ...*extensions.Extension) *lsp.ExtensionRegistry {
	t.Helper()
	s := testStore(t)
	es := extensions.NewExtensionStore(s)
	if err := es.InitSchema(); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	for _, ext := range exts {
		if err := es.Create(ext); err != nil {
			t.Fatalf("create ext: %v", err)
		}
	}
	return lsp.NewExtensionLSPManager(es, nil, s).GetRegistry()
}

func TestFilterDiagnosticFiles(t *testing.T) {
	registry := lspRegistryWith(t, lspGoExtension())

	got := filterDiagnosticFiles(registry, "proj", []string{
		"main.go", "main.go", // duplicate
		"/home/sandbox/proj/pkg/a.go", // sandbox absolute path
		"README.md",                   // no LSP extension claims it
		"", ".",                       // noise
		"internal/tool/vendor/deep.go",
	})
	want := []string{"main.go", "pkg/a.go", "internal/tool/vendor/deep.go"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("filterDiagnosticFiles = %v, want %v", got, want)
	}

	// The per-pass bound keeps one broad turn from paying one server round trip each.
	var many []string
	for i := 0; i < maxDiagnosticFilesPerPass+4; i++ {
		many = append(many, "f"+string(rune('a'+i))+".go")
	}
	if got := filterDiagnosticFiles(registry, "proj", many); len(got) != maxDiagnosticFilesPerPass {
		t.Fatalf("cap not applied: %d files returned", len(got))
	}
}

func TestTurnDiagnosticFiles_AskGitWhatTheShellChanged(t *testing.T) {
	te, _, projectID, _ := setupExecutorWithGit(t)
	es := extensions.NewExtensionStore(te.rawStore())
	if err := es.InitSchema(); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	if err := es.Create(lspGoExtension()); err != nil {
		t.Fatalf("create lsp extension: %v", err)
	}
	te.lspMgr = lsp.NewExtensionLSPManager(es, nil, te.rawStore())

	now := time.Now().UTC()
	task := &types.Task{
		ID: "shell-task", ProjectID: projectID, Title: "diag", Prompt: "diag prompt",
		Status: types.StatusInProgress, OperatingMode: "act", LlamaServerURL: "http://test:8080",
		ModelName: "test-model", Branch: "task/shell-task", CreatedAt: now, UpdatedAt: now,
	}
	if err := te.rawStore().CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	setup := &taskExecSetup{task: task, projectID: projectID}

	if _, _, err := te.worktreeMgr.CreateWorktree(projectID, task.ID, task.Branch, ""); err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	wtPath, err := te.worktreeMgr.WorktreePath(task.ID)
	if err != nil {
		t.Fatalf("worktree path: %v", err)
	}
	// Neither path below is named by any tool: only the worktree knows them.
	for _, rel := range []string{"generated.go", "notes.md"} {
		if err := os.WriteFile(filepath.Join(wtPath, rel), []byte("package main\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	lc := &loopContext{shellMayHaveChangedFiles: true}
	got := te.turnDiagnosticFiles(setup, lc)
	if strings.Join(got, ",") != "generated.go" {
		t.Fatalf("turnDiagnosticFiles = %v, want [generated.go] (git status, LSP-claimed only)", got)
	}
	// The footprint is consumed so the next turn starts clean.
	if lc.shellMayHaveChangedFiles || len(lc.touchedFiles) != 0 {
		t.Errorf("turn footprint not reset: %+v", lc)
	}

	// Nothing ran that could change a file: no git call, no files.
	if files := te.turnDiagnosticFiles(setup, &loopContext{}); files != nil {
		t.Errorf("turnDiagnosticFiles with no footprint = %v, want none", files)
	}
}

func TestCollectTurnDiagnostics_QueuesOneAdvisoryAndSuppressesRepeats(t *testing.T) {
	// Without a reachable language server the pass has nothing to say; assert the shape of
	// the decision instead of the round trip: compose + suppress.
	lc := &loopContext{}
	a, ok := composeDiagnosticsAdvisory(2, []string{"a.go: 1 error", "b.go: 2 errors"})
	if !ok {
		t.Fatal("findings produced no advisory")
	}
	if a.kind != advisoryDiagnostics || a.severity != "error" {
		t.Errorf("advisory metadata = %+v", a)
	}
	if !strings.HasPrefix(a.text, "LSP diagnostics for this turn's changes (2 files with findings):") {
		t.Fatalf("advisory text = %q", a.text)
	}
	if _, ok := composeDiagnosticsAdvisory(0, nil); ok {
		t.Fatal("empty findings produced an advisory")
	}
	if one, _ := composeDiagnosticsAdvisory(1, []string{"x"}); !strings.Contains(one.text, "(1 file with findings)") {
		t.Fatalf("singular wording = %q", one.text)
	}

	if !diagnosticsChanged(lc, a.text) {
		t.Fatal("first occurrence of findings must be injected")
	}
	if diagnosticsChanged(lc, a.text) {
		t.Fatal("identical findings must not be injected twice")
	}
	if !diagnosticsChanged(lc, a.text+"\nnew") {
		t.Fatal("changed findings must be injected")
	}
}

func TestAdvisoriesWrittenAfterToolRowsSurviveTheRebuild(t *testing.T) {
	// The regression this change rests on: a note written from inside tool execution landed
	// between the turn's tool_call rows. The turn boundary is now the only writer, so the
	// rows a turn produces read tool_call*, advisory — and the rebuild stays complete.
	s := testStore(t)
	te := &TaskExecutor{store: s, appStreamMgr: newTestStreamMgr(s), tasks: make(map[string]*executionState)}
	setup := diagTestSetup(t, s, "boundary-proj", "boundary-task")
	lc := &loopContext{}

	// An assistant row written first, then its two tool_call rows — what the loop persists.
	start := time.Now().UTC().Add(-time.Hour)
	if err := s.CreateTaskMessage(&types.TaskMessage{
		ID: "as-1", TaskID: setup.task.ID, Role: "assistant", Content: "editing two files", CreatedAt: start,
	}); err != nil {
		t.Fatalf("persist assistant row: %v", err)
	}
	for i, id := range []string{"A", "B"} {
		row := toolRow(t, "as-1", id, "edit_file", `{"path":"`+strings.ToLower(id)+`.go"}`, "ok")
		row.ID = "row-" + id
		row.TaskID = setup.task.ID
		row.CreatedAt = start.Add(time.Duration(i) * time.Second)
		if err := s.CreateTaskMessage(&row); err != nil {
			t.Fatalf("persist tool row: %v", err)
		}
	}
	queueAdvisory(lc, advisory{kind: advisoryDiagnostics, severity: "error", text: "diagnostics"})
	te.flushAdvisories(setup, lc)

	rows, err := s.GetTaskMessages(setup.task.ID, "")
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	if rows[len(rows)-1].Role != "advisory" {
		t.Fatalf("last persisted row = %q, want advisory (written at the turn boundary)", rows[len(rows)-1].Role)
	}

	rebuilt := rebuildRows(rows)
	var toolMsgs, advisoryMsgs int
	for _, m := range rebuilt {
		switch m.Role {
		case "tool":
			toolMsgs++
		case "advisory":
			advisoryMsgs++
		}
	}
	if toolMsgs != 2 || advisoryMsgs != 1 {
		t.Fatalf("rebuilt tool=%d advisory=%d, want 2/1", toolMsgs, advisoryMsgs)
	}
}

// wedgedLSPManager blocks in GetOrCreate until its context ends, recording that context.
// This is what a bridge that accepts `lsp_start` and never answers looks like from here.
type wedgedLSPManager struct {
	lspManager
	registry *lsp.ExtensionRegistry
	gotCtx   chan context.Context
}

func (m *wedgedLSPManager) GetRegistry() *lsp.ExtensionRegistry { return m.registry }

// The rest of the session contract is not what this fake is about — it exists to
// be wedged in `lsp_start` — so each answers the benign thing: nothing to
// recycle, and no handshake completed yet.
func (m *wedgedLSPManager) FilesChanged(string, []string) bool { return false }

func (m *wedgedLSPManager) SessionInitialized(string, string) bool { return false }
func (m *wedgedLSPManager) MarkSessionInitialized(string, string)  {}

func (m *wedgedLSPManager) Release(string, string) {}

func (m *wedgedLSPManager) GetOrCreate(ctx context.Context, _, _ string, _ *lsp.ServerConfig) (*lsp.LSPProcess, error) {
	select {
	case m.gotCtx <- ctx:
	default:
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

// installTsLSPExtension makes the pass chase app.ts instead of filtering it out.
func installTsLSPExtension(t *testing.T, s *store.Store) *lsp.ExtensionRegistry {
	t.Helper()
	es := extensions.NewExtensionStore(s)
	if err := es.InitSchema(); err != nil {
		t.Fatalf("init extension schema: %v", err)
	}
	ext := &extensions.Extension{
		ID: "typescript", Name: "typescript", Version: "1.0.0",
		Type: extensions.ExtensionTypeLSP, Enabled: true,
		Manifest: extensions.ExtensionManifest{
			DisplayName: "TypeScript LSP", Version: "1.0.0",
			Capabilities: &extensions.Capabilities{
				LSP: &extensions.LSPConfig{
					Command:   "typescript-language-server",
					Args:      []string{"--stdio"},
					FileTypes: []extensions.LSPFileType{{Extension: "ts", LanguageID: "typescript"}},
					Language:  "typescript",
				},
			},
		},
	}
	if err := es.Create(ext); err != nil {
		t.Fatalf("create LSP extension: %v", err)
	}
	return lsp.NewExtensionRegistry(es)
}

// TestCollectTurnDiagnostics_BoundedAndCancellable: the pass runs on the executor
// goroutine, so it must bound the context it hands down and end when the task stops.
func TestCollectTurnDiagnostics_BoundedAndCancellable(t *testing.T) {
	s := testStore(t)
	reg := installTsLSPExtension(t, s)

	// WorktreePath looks the task up in the store, so everything here shares s.
	cs := config.NewConfigStore(s, "", "test")
	cs.SetWorkspaceDirOverride(t.TempDir())
	fake := &wedgedLSPManager{registry: reg, gotCtx: make(chan context.Context, 4)}
	te := &TaskExecutor{
		store:        s,
		configStore:  cs,
		appStreamMgr: newTestStreamMgr(s),
		worktreeMgr:  git.NewWorktreeManager(s, cs),
		lspMgr:       fake,
	}

	setup := diagTestSetup(t, s, "diag-proj", "diag-task")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	setup.ctx = ctx

	lc := &loopContext{}
	lc.noteTouchedFile("app.ts")

	done := make(chan struct{})
	go func() {
		te.collectTurnDiagnostics(setup, lc)
		close(done)
	}()

	var got context.Context
	select {
	case got = <-fake.gotCtx:
	case <-time.After(10 * time.Second):
		t.Fatal("diagnostics pass never reached GetOrCreate")
	}

	dl, ok := got.Deadline()
	if !ok {
		t.Fatal("GetOrCreate was handed a context with no deadline; a silent language server would park the executor forever")
	}
	if left := time.Until(dl); left > lspDiagnosticsBudget+5*time.Second {
		t.Errorf("per-file deadline = %v, want at most %v", left, lspDiagnosticsBudget)
	}

	// Stopping the task has to end the pass, not leave it waiting on the sandbox.
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("collectTurnDiagnostics kept waiting after the task context was cancelled")
	}
}
