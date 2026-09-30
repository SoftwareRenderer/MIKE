package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"mike/backend/config"
	"mike/backend/extensions"
	"mike/backend/git"
	"mike/backend/lsp"
	"mike/backend/store"
)

// recordingLSPManager is a language server the pass can be watched talking to. It answers a
// document sync the way a real server behaves: a document it has never been given opens, one
// whose content it already holds is left alone — and it publishes diagnostics only for a
// document it was actually told about.
type recordingLSPManager struct {
	registry *lsp.ExtensionRegistry

	mu           sync.Mutex
	initCalls    []string // language of each EnsureInitialized that reached the server
	syncCalls    []string // file each SyncDocument was asked about
	created      []string // language of each GetOrCreate
	released     []string // language of each Release
	held         map[string]string
	sub          chan json.RawMessage
	subscribes   int
	unsubscribes int
	// publish builds the notification for a synced document; "" means the server had
	// nothing to say about it.
	publish func(file string) []LSPDiagnostic
	// publishSequence, when set, wins over publish and supplies every notification in order.
	// A server that has not loaded the package publishes its loader state first and the real
	// findings a moment later, and the pass treats those two frames differently.
	publishSequence func(file string) [][]LSPDiagnostic
	reloadQuestions [][]string
}

func newRecordingLSPManager(registry *lsp.ExtensionRegistry) *recordingLSPManager {
	return &recordingLSPManager{registry: registry, held: map[string]string{}}
}

func (m *recordingLSPManager) GetRegistry() *lsp.ExtensionRegistry { return m.registry }

func (m *recordingLSPManager) FilesChanged(_ string, changed []string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(changed) > 0 {
		m.reloadQuestions = append(m.reloadQuestions, append([]string(nil), changed...))
	}
	return false
}

func (m *recordingLSPManager) GetOrCreate(_ context.Context, _, language string) (*lsp.LSPProcess, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.created = append(m.created, language)
	return &lsp.LSPProcess{Workspace: "/work/tree", Language: language}, nil
}

func (m *recordingLSPManager) Release(_, language string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.released = append(m.released, language)
}

func (m *recordingLSPManager) Subscribe() chan json.RawMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.subscribes++
	if m.sub == nil {
		m.sub = make(chan json.RawMessage, 32)
	}
	return m.sub
}

func (m *recordingLSPManager) Unsubscribe(ch chan json.RawMessage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.unsubscribes++
	if ch == m.sub {
		m.sub = nil
	}
}

func (m *recordingLSPManager) IsToolCallsEnabledForFile(string) bool { return true }

func (m *recordingLSPManager) LSPRequest(context.Context, string, string, string, any) (json.RawMessage, error) {
	return json.RawMessage(`{"result":null}`), nil
}

func (m *recordingLSPManager) EnsureInitialized(_ context.Context, _ string, cfg *lsp.ServerConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.initCalls = append(m.initCalls, cfg.Language)
	return nil
}

func (m *recordingLSPManager) SyncDocument(ctx context.Context, workspacePath, filePath string, cfg *lsp.ServerConfig) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	uri := "file://" + workspacePath + "/" + filePath

	m.mu.Lock()
	m.syncCalls = append(m.syncCalls, filePath)
	seen, known := m.held[uri]
	content := "content of " + filePath
	if known && seen == content {
		m.mu.Unlock()
		return uri, false, nil
	}
	m.held[uri] = content
	publish := m.publish
	sequence := m.publishSequence
	sub := m.sub
	m.mu.Unlock()

	if sequence != nil {
		for _, diags := range sequence(filePath) {
			if sub == nil {
				break
			}
			frame, _ := json.Marshal(map[string]any{
				"method": "textDocument/publishDiagnostics",
				"params": map[string]any{"uri": uri, "diagnostics": diags},
			})
			sub <- frame
		}
		return uri, true, nil
	}

	var diags []LSPDiagnostic
	if publish != nil {
		diags = publish(filePath)
		if diags == nil {
			diags = []LSPDiagnostic{} // the "nothing wrong here" answer a real server sends
		}
	}
	if diags != nil && sub != nil {
		frame, _ := json.Marshal(map[string]any{
			"method": "textDocument/publishDiagnostics",
			"params": map[string]any{"uri": uri, "diagnostics": diags},
		})
		sub <- frame
	}
	return uri, true, nil
}

func (m *recordingLSPManager) counts() (initCalls, syncCalls, created, released []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.initCalls...), append([]string(nil), m.syncCalls...),
		append([]string(nil), m.created...), append([]string(nil), m.released...)
}

// lspLanguageExtension is a minimal LSP manifest that names its language, the way both
// installed language-server manifests do, and opts into the post-edit pass the way the pass
// tests need it to.
func lspLanguageExtension(id, language, fileExt string) *extensions.Extension {
	return &extensions.Extension{
		ID: id, Name: id, Version: "1.0.0", Type: extensions.ExtensionTypeLSP, Enabled: true,
		Manifest: extensions.ExtensionManifest{
			DisplayName: id, Version: "1.0.0",
			Capabilities: &extensions.Capabilities{
				LSP: &extensions.LSPConfig{
					Command: id, Args: []string{"--stdio"}, Language: language,
					FileTypes:              []extensions.LSPFileType{{Extension: fileExt, LanguageID: language}},
					ChatDiagnosticsEnabled: boolRef(true),
				},
			},
		},
	}
}

func errorDiagnostic(msg string) LSPDiagnostic {
	return LSPDiagnostic{
		Range: LSPDiagnosticRange{
			Start: LSPDiagnosticPosition{Line: 3, Character: 1},
			End:   LSPDiagnosticPosition{Line: 3, Character: 9},
		},
		Message:  msg,
		Severity: 1,
		Source:   "bench",
	}
}

// loaderDiagnostic is a language server talking about itself instead of about the file: gopls'
// exact wording, source and severity for a document it cannot place in anything it has loaded.
func loaderDiagnostic(path string) LSPDiagnostic {
	return LSPDiagnostic{
		Range: LSPDiagnosticRange{
			Start: LSPDiagnosticPosition{Line: 0, Character: 8},
			End:   LSPDiagnosticPosition{Line: 0, Character: 11},
		},
		Message:  "No active builds contain " + path + ": consider opening a new workspace folder containing it",
		Severity: 2,
		Source:   "go list",
	}
}

// TestIsLoaderStateDiagnostic pins the line between the server describing its own loader and a
// dependency problem the model has to be told about. Both arrive with source "go list", and
// mistaking the second for the first is how a real broken import gets swallowed.
func TestIsLoaderStateDiagnostic(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		diag LSPDiagnostic
		want bool
	}{
		{"no active builds", loaderDiagnostic("/mnt/wt/main.go"), true},
		{"no packages found", LSPDiagnostic{Source: "go list", Severity: 2, Message: "No packages found for open file /tmp/x.go."}, true},
		{"wrong module", LSPDiagnostic{Source: "go list", Severity: 2, Message: `This file is within module "../mod", which is not included in your workspace.`}, true},
		{"missing dependency is a finding", LSPDiagnostic{Source: "go list", Severity: 1, Message: `could not import github.com/google/uuid (no required module provides package "github.com/google/uuid")`}, false},
		{"same words from another source", LSPDiagnostic{Source: "compiler", Severity: 2, Message: "No active builds contain /mnt/wt/main.go"}, false},
	}
	for _, tc := range cases {
		if got := isLoaderStateDiagnostic(tc.diag); got != tc.want {
			t.Errorf("%s: isLoaderStateDiagnostic = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// passSetup is diagTestSetup plus the turn context the pass derives its budget from.
func passSetup(t *testing.T, s *store.Store, projectID, taskID string) *taskExecSetup {
	t.Helper()
	setup := diagTestSetup(t, s, projectID, taskID)
	setup.ctx = context.Background()
	return setup
}

// passExecutor wires just enough of the executor for collectTurnDiagnostics: a store for the
// task rows, a workspace dir for the worktree path, and the recording server.
func passExecutor(t *testing.T, s *store.Store, mgr lspManager) *TaskExecutor {
	t.Helper()
	cs := config.NewConfigStore(s, "", "test", nil)
	cs.SetWorkspaceDirOverride(t.TempDir())
	te := newTestExecutorWith(t, Deps{
		Store:       s,
		ConfigStore: cs,
		WorktreeMgr: git.NewWorktreeManager(s, cs),
	})
	te.lspMgr = mgr
	return te
}

// TestCollectTurnDiagnostics_OneHandshakePerLanguage: a pass attaches, handshakes and releases
// once per language, whatever number of files it reads. Attaching per file let each file's
// Release be the last reference, so the next file cold-started a server that reported nothing.
func TestCollectTurnDiagnostics_OneHandshakePerLanguage(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	setup := passSetup(t, s, "diag-proj", "diag-task")
	// lspGoExtension leaves `language` out, which a manifest is allowed to do: the pass must
	// still attach one server and handshake it once.
	registry := lspRegistryWith(t, lspGoExtension())
	fake := newRecordingLSPManager(registry)
	fake.publish = func(file string) []LSPDiagnostic { return []LSPDiagnostic{errorDiagnostic("bad " + file)} }
	te := passExecutor(t, s, fake)

	lc := &loopContext{}
	for _, f := range []string{"a.go", "b.go", "c.go", "d.go", "e.go"} {
		lc.noteTouchedFile(f)
	}
	te.collectTurnDiagnostics(setup, lc)

	initCalls, syncCalls, created, released := fake.counts()
	if len(initCalls) != 1 {
		t.Errorf("initialize handshakes = %d (%v), want 1 for a five-file pass", len(initCalls), initCalls)
	}
	if len(created) != 1 {
		t.Errorf("servers attached = %v, want exactly one for five files of one language", created)
	}
	if len(released) != 1 {
		t.Errorf("releases = %d (%v), want 1 — the sandbox reference is taken once per server", len(released), released)
	}
	if len(syncCalls) != 5 {
		t.Errorf("documents synced = %d (%v), want all five files", len(syncCalls), syncCalls)
	}
	if len(lc.pendingAdvisories) != 1 {
		t.Fatalf("advisories = %d, want the pass's findings in one note", len(lc.pendingAdvisories))
	}
	if got := lc.pendingAdvisories[0].text; got == "" || !strings.Contains(got, "(5 files with findings)") {
		t.Errorf("advisory = %q, want all five files reported", got)
	}
}

// TestCollectTurnDiagnostics_UnchangedFilesProduceNoHint: the server already has the text, so
// it will not report again and the pass must not wait for a notification that is not coming —
// and with nothing new to say, the model must get nothing.
func TestCollectTurnDiagnostics_UnchangedFilesProduceNoHint(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	setup := passSetup(t, s, "diag-proj", "diag-task")
	registry := lspRegistryWith(t, lspGoExtension())
	fake := newRecordingLSPManager(registry)
	fake.publish = func(file string) []LSPDiagnostic { return []LSPDiagnostic{errorDiagnostic("bad " + file)} }
	te := passExecutor(t, s, fake)

	lc := &loopContext{}
	lc.noteTouchedFile("a.go")
	te.collectTurnDiagnostics(setup, lc)
	if len(lc.pendingAdvisories) != 1 {
		t.Fatalf("first pass advisories = %d, want 1", len(lc.pendingAdvisories))
	}
	// Drain the queue and clear the unchanged-findings memory, so the second pass is
	// measured on its own behaviour rather than on the dedupe.
	lc.pendingAdvisories = nil
	lc.lastDiagnosticsText = ""

	// Nothing was edited between the two turns: every file is already in the server.
	start := time.Now()
	lc.noteTouchedFile("a.go")
	te.collectTurnDiagnostics(setup, lc)

	if len(lc.pendingAdvisories) != 0 {
		t.Errorf("advisories on unchanged files = %d, want 0 — there is nothing new to tell the model", len(lc.pendingAdvisories))
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("unchanged pass took %v: it waited for publishDiagnostics that was never going to arrive", elapsed)
	}
}

// TestCollectTurnDiagnostics_OneSessionPerLanguage: a turn that touched Go and TypeScript must
// attach, handshake and release each server once.
func TestCollectTurnDiagnostics_OneSessionPerLanguage(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	setup := passSetup(t, s, "diag-proj", "diag-task")
	registry := lspRegistryWith(t, lspLanguageExtension("gopls", "go", "go"), lspLanguageExtension("typescript", "typescript", "ts"))
	fake := newRecordingLSPManager(registry)
	fake.publish = func(file string) []LSPDiagnostic { return []LSPDiagnostic{errorDiagnostic("bad " + file)} }
	te := passExecutor(t, s, fake)

	lc := &loopContext{}
	lc.noteTouchedFile("main.go")
	lc.noteTouchedFile("app.ts")
	lc.noteTouchedFile("util.ts")
	te.collectTurnDiagnostics(setup, lc)

	initCalls, _, created, released := fake.counts()
	if len(created) != 2 || created[0] != "go" || created[1] != "typescript" {
		t.Errorf("servers attached = %v, want [go typescript]", created)
	}
	if len(initCalls) != 2 {
		t.Errorf("handshakes = %v, want one per language", initCalls)
	}
	if len(released) != 2 {
		t.Errorf("releases = %v, want one per attached server", released)
	}
	if len(lc.pendingAdvisories) != 1 {
		t.Fatalf("advisories = %d, want one note carrying both languages", len(lc.pendingAdvisories))
	}
	if got := lc.pendingAdvisories[0].text; !strings.Contains(got, "(3 files with findings)") {
		t.Errorf("advisory = %q, want findings from both languages", got)
	}
}

// TestCollectTurnDiagnostics_ReleasesEvenWhenNothingIsReported: every attached server gives
// back its sandbox reference, or the extension sandbox never gets to stop.
func TestCollectTurnDiagnostics_ReleasesEvenWhenNothingIsReported(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	setup := passSetup(t, s, "diag-proj", "diag-task")
	registry := lspRegistryWith(t, lspGoExtension())
	fake := newRecordingLSPManager(registry)
	fake.publish = func(string) []LSPDiagnostic { return nil } // answers every file with "no problems"
	te := passExecutor(t, s, fake)

	lc := &loopContext{}
	lc.noteTouchedFile("a.go")
	lc.noteTouchedFile("b.go")
	te.collectTurnDiagnostics(setup, lc)

	_, _, created, released := fake.counts()
	if len(created) != 1 {
		t.Fatalf("servers attached = %v, want one", created)
	}
	if len(released) != 1 {
		t.Errorf("releases = %d, want 1 even with nothing reported", len(released))
	}
	if len(lc.pendingAdvisories) != 0 {
		t.Errorf("advisories = %d, want 0 when the server says nothing", len(lc.pendingAdvisories))
	}
}

// TestGroupByServerLanguage keeps the grouping honest: order preserved, one entry per
// language, files no extension claims dropped rather than guessed at.
func TestGroupByServerLanguage(t *testing.T) {
	t.Parallel()
	registry := lspRegistryWith(t, lspLanguageExtension("gopls", "go", "go"), lspLanguageExtension("typescript", "typescript", "ts"))

	groups := groupByServerLanguage(registry, []string{"main.go", "app.ts", "util.ts", "pkg/a.go", "README.md"})
	if len(groups) != 2 {
		t.Fatalf("groups = %d, want 2 (go, typescript)", len(groups))
	}
	if groups[0].language != "go" || fmt.Sprint(groups[0].files) != "[main.go pkg/a.go]" {
		t.Errorf("first group = %+v, want go with [main.go pkg/a.go] in worked order", groups[0])
	}
	if groups[1].language != "typescript" || fmt.Sprint(groups[1].files) != "[app.ts util.ts]" {
		t.Errorf("second group = %+v, want typescript with [app.ts util.ts]", groups[1])
	}
	if groups[0].cfg == nil || groups[0].cfg.Command != "gopls" {
		t.Errorf("first group carries cfg %+v, want the gopls config", groups[0].cfg)
	}
}

// TestDiagnosticsPassReportWarning pins when a pass is allowed to complain. Silence about an
// unchanged file is the design; silence about a file the server was asked about means the
// server is dead, and the turn loses its diagnostics with no other trace.
func TestDiagnosticsPassReportWarning(t *testing.T) {
	t.Parallel()
	quiet := []struct {
		name   string
		report diagnosticsPassReport
	}{
		{"nothing changed", diagnosticsPassReport{asked: 3, unchanged: 3, servers: 1}},
		{"everything answered", diagnosticsPassReport{asked: 3, reported: 2, clean: 1, servers: 1}},
		{"task stopped", diagnosticsPassReport{asked: 3, stopped: true, silent: []string{"a.go"}, unasked: 2}},
	}
	for _, tc := range quiet {
		if w := tc.report.warning(); w != "" {
			t.Errorf("%s: warning = %q, want none", tc.name, w)
		}
	}

	w := diagnosticsPassReport{asked: 3, reported: 1, silent: []string{"pkg/dead.go"}, unasked: 1}.warning()
	if !strings.Contains(w, "pkg/dead.go") {
		t.Errorf("warning %q does not name the file that went unanswered", w)
	}
	if !strings.Contains(w, "1 of 3 file(s) were never asked about") {
		t.Errorf("warning %q does not count the files the pass never reached", w)
	}

	// A file whose only answer was the loader's own state is not clean, and a pass full of
	// those must not look like a pass that found nothing.
	if w := (diagnosticsPassReport{asked: 2, unloaded: 2, servers: 1}).warning(); !strings.Contains(w, "unknown, not clean") {
		t.Errorf("warning for unloaded files = %q, want it to say the state is unknown", w)
	}
}

// A server that answers the handshake and then nothing else: the pass must say so, per file,
// rather than ending the turn as if the code were clean.
func TestDiagnosticsPass_SilentServerIsReported(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	setup := passSetup(t, s, "diag-proj", "diag-task")
	registry := lspRegistryWith(t, lspGoExtension())
	fake := newRecordingLSPManager(registry) // handshakes fine, never publishes anything
	te := passExecutor(t, s, fake)

	// A short pass budget stands in for the 15s one expiring; the file budget derives from it.
	passCtx, cancel := context.WithTimeout(setup.ctx, 150*time.Millisecond)
	defer cancel()
	pass := &diagnosticsPass{te: te, mgr: fake, taskID: setup.task.ID, worktree: "/work/tree", passCtx: passCtx, taskCtx: setup.ctx}

	blocks, report := pass.run(registry, []string{"pkg/dead.go", "pkg/next.go"})
	if len(blocks) != 0 {
		t.Errorf("blocks = %v, want none from a silent server", blocks)
	}
	if len(report.silent) != 1 || report.silent[0] != "pkg/dead.go" {
		t.Errorf("silent = %v, want the file that was asked and never answered", report.silent)
	}
	w := report.warning()
	if w == "" {
		t.Fatal("a server that answered nothing produced no warning; the turn would have lost its diagnostics silently")
	}
	if !strings.Contains(w, "pkg/dead.go") {
		t.Errorf("warning = %q, want it to name the silent file", w)
	}
}

// Stopping the task is not a language server failure: the pass that ends because the user
// stopped the task has to stay quiet.
func TestDiagnosticsPass_StoppedTaskStaysQuiet(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	setup := passSetup(t, s, "diag-proj", "diag-task")
	registry := lspRegistryWith(t, lspGoExtension())
	fake := newRecordingLSPManager(registry)
	fake.publish = func(file string) []LSPDiagnostic { return []LSPDiagnostic{errorDiagnostic("bad " + file)} }
	te := passExecutor(t, s, fake)

	taskCtx, cancel := context.WithCancel(setup.ctx)
	cancel() // the task is already stopped when the pass runs
	pass := &diagnosticsPass{te: te, mgr: fake, taskID: setup.task.ID, worktree: "/work/tree", passCtx: taskCtx, taskCtx: taskCtx}

	blocks, report := pass.run(registry, []string{"a.go"})
	if len(blocks) != 0 {
		t.Errorf("blocks = %v, want nothing after the task stopped", blocks)
	}
	if !report.stopped {
		t.Error("report does not record that the task ended the pass")
	}
	if w := report.warning(); w != "" {
		t.Errorf("a pass ended by the user warned: %q", w)
	}
}

// TestDiagnosticsPass_LoaderStateYieldsToTheSettledPublish: a server that has not loaded the
// package yet publishes a complaint about itself, then the real findings tens of milliseconds
// later. Taking the first frame told the model to "open a new workspace folder" and withheld
// the syntax error it had just written — the reason this pass exists at all.
func TestDiagnosticsPass_LoaderStateYieldsToTheSettledPublish(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	setup := passSetup(t, s, "diag-proj", "diag-task")
	registry := lspRegistryWith(t, lspGoExtension())
	fake := newRecordingLSPManager(registry)
	fake.publishSequence = func(file string) [][]LSPDiagnostic {
		uri := "file:///work/tree/" + file
		return [][]LSPDiagnostic{
			{loaderDiagnostic(uri)},                           // where the loader stands right now
			{errorDiagnostic("expected operand, found '++'")}, // what it found once loaded
		}
	}
	te := passExecutor(t, s, fake)

	pass := &diagnosticsPass{te: te, mgr: fake, taskID: setup.task.ID, worktree: "/work/tree", passCtx: setup.ctx, taskCtx: setup.ctx}
	blocks, report := pass.run(registry, []string{"a.go"})

	if len(blocks) != 1 || !strings.Contains(blocks[0], "found '++'") {
		t.Fatalf("blocks = %v, want the settled finding the second publish carried", blocks)
	}
	if strings.Contains(blocks[0], "No active builds") {
		t.Errorf("advisory carries the server's own loader state as a problem to fix: %q", blocks[0])
	}
	if report.reported != 1 || report.unloaded != 0 {
		t.Errorf("report = %+v, want one reported file and nothing unloaded", report)
	}
}

// A server that never gets past its own loader state has told us nothing about the file. That
// is unknown, and unknown has to be reported as such — counting it clean is how a whole
// workspace loses its diagnostics and looks healthy.
func TestDiagnosticsPass_LoaderStateOnlyIsUnknownNotClean(t *testing.T) {
	t.Parallel()

	s := testStore(t)
	setup := passSetup(t, s, "diag-proj", "diag-task")
	registry := lspRegistryWith(t, lspGoExtension())
	fake := newRecordingLSPManager(registry)
	fake.publishSequence = func(file string) [][]LSPDiagnostic {
		return [][]LSPDiagnostic{{loaderDiagnostic("/mnt/wt/" + file)}}
	}
	te := passExecutor(t, s, fake)

	pass := &diagnosticsPass{te: te, mgr: fake, taskID: setup.task.ID, worktree: "/work/tree", passCtx: setup.ctx, taskCtx: setup.ctx, loaderSettleGrace: 80 * time.Millisecond}
	blocks, report := pass.run(registry, []string{"a.go"})

	if len(blocks) != 0 {
		t.Errorf("blocks = %v, want nothing injected for a file the server cannot load", blocks)
	}
	if report.clean != 0 || report.reported != 0 || report.unloaded != 1 {
		t.Errorf("report = %+v, want one unloaded file and no clean/reported", report)
	}
	if report.unasked != 0 {
		t.Errorf("unasked = %d, want the unloaded file counted as asked", report.unasked)
	}
	w := report.warning()
	if !strings.Contains(w, "1 of 1 file(s) got only the language server's own") {
		t.Errorf("warning = %q, want it to name the unloaded file for the operator", w)
	}
}

// Loader state travelling alongside a real finding must not smuggle itself into the advisory.
func TestDiagnosticsPass_LoaderStateIsFilteredOutOfARealPublish(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	setup := passSetup(t, s, "diag-proj", "diag-task")
	registry := lspRegistryWith(t, lspGoExtension())
	fake := newRecordingLSPManager(registry)
	fake.publish = func(file string) []LSPDiagnostic {
		return []LSPDiagnostic{
			loaderDiagnostic("/mnt/wt/" + file),
			errorDiagnostic("cannot use \"x\" as int value"),
		}
	}
	te := passExecutor(t, s, fake)

	pass := &diagnosticsPass{te: te, mgr: fake, taskID: setup.task.ID, worktree: "/work/tree", passCtx: setup.ctx, taskCtx: setup.ctx}
	blocks, report := pass.run(registry, []string{"a.go"})

	if len(blocks) != 1 || !strings.Contains(blocks[0], "as int value") {
		t.Fatalf("blocks = %v, want the real finding reported", blocks)
	}
	if strings.Contains(blocks[0], "No active builds") {
		t.Errorf("advisory carries loader state: %q", blocks[0])
	}
	if report.reported != 1 {
		t.Errorf("report = %+v, want one reported file", report)
	}
}

// TestTurnLanguageServerReleasesExactlyOnce: the reference this pass took on the connection is
// handed back once. A second Release would take a reference that an editor reading the same
// workspace still needs, and the third would be worse.
func TestTurnLanguageServerReleasesExactlyOnce(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	registry := lspRegistryWith(t, lspGoExtension())
	fake := newRecordingLSPManager(registry)
	te := passExecutor(t, s, fake)
	srv := &turnLanguageServer{sub: fake.Subscribe(), workspace: "/work/tree", language: "go", held: true}

	srv.close(te.lspMgr)
	srv.close(te.lspMgr)
	srv.close(te.lspMgr)

	if _, _, _, released := fake.counts(); len(released) != 1 {
		t.Errorf("releases = %d (%v), want exactly one per attach", len(released), released)
	}
	if fake.unsubscribes != 1 {
		t.Errorf("unsubscribes = %d, want 1", fake.unsubscribes)
	}
}
