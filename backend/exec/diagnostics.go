package exec

// Turn-boundary workspace diagnostics: one pass over changed files after tool
// results are known. Catches changes made by bash commands (e.g. sed -i).

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"mike/backend/git"
	"mike/backend/lsp"
	"mike/pkg/logging"
)

// maxDiagnosticFilesPerPass is the maximum files per diagnostic pass to avoid stalling the turn.
const maxDiagnosticFilesPerPass = 8

// LSP diagnostic budgets for the pass, individual files, and publication responses.
const (
	lspDiagnosticsPassBudget = 15 * time.Second
	lspDiagnosticsBudget     = 12 * time.Second
	lspPublishBudget         = 10 * time.Second
)

// lspLoaderSettleGrace is extra time given to files whose first publish is the server's loader state.
var lspLoaderSettleGrace = 2 * time.Second

// noteTouchedFile records a path a tool wrote or edited, preserving order.
func (lc *loopContext) noteTouchedFile(path string) {
	for _, known := range lc.touchedFiles {
		if known == path {
			return
		}
	}
	lc.touchedFiles = append(lc.touchedFiles, path)
}

// noteShellMayHaveChangedFiles flags that a shell command may have changed files; the boundary pass will use git status.
func (lc *loopContext) noteShellMayHaveChangedFiles() {
	lc.shellMayHaveChangedFiles = true
}

// turnDiagnosticFiles returns workspace-relative files for LSP diagnostics, based on
// worktree changes and tool edits. Consumes bookkeeping for the turn.
func (te *TaskExecutor) turnDiagnosticFiles(setup *taskExecSetup, lc *loopContext) []string {
	toolPaths := lc.touchedFiles
	shellRan := lc.shellMayHaveChangedFiles
	lc.touchedFiles = nil
	lc.shellMayHaveChangedFiles = false

	if (!shellRan && len(toolPaths) == 0) || te.lspMgr == nil {
		return nil
	}
	registry := te.lspMgr.GetRegistry()
	if registry == nil {
		return nil
	}

	candidates := toolPaths
	if shellRan {
		changed, err := te.worktreeChangedFiles(setup.task.ID)
		if err != nil {
			// The command line is not a reliable substitute for asking the worktree, but
			// the paths the tools named directly still are.
			logging.For(logging.LSP).With(logging.TaskID, setup.task.ID).Warnf("git status failed, using tool paths: %v", err)
		} else {
			candidates = append(changed, candidates...)
		}
	}
	return filterDiagnosticFiles(registry, setup.projectName(), candidates)
}

// filterDiagnosticFiles normalizes candidate paths, filters by LSP extension claim and
// opt-in, and bounds the result.
func filterDiagnosticFiles(registry *lsp.ExtensionRegistry, projectName string, candidates []string) []string {
	seen := make(map[string]bool, len(candidates))
	files := make([]string, 0, len(candidates))
	for _, c := range candidates {
		rel := normalizePath(projectName, c)
		if rel == "" || rel == "." || seen[rel] {
			continue
		}
		cfg := registry.Lookup(rel)
		if cfg == nil || !cfg.ChatDiagnostics {
			continue
		}
		seen[rel] = true
		files = append(files, rel)
	}
	if len(files) > maxDiagnosticFilesPerPass {
		files = files[:maxDiagnosticFilesPerPass]
	}
	return files
}

func (te *TaskExecutor) worktreeChangedFiles(taskID string) ([]string, error) {
	out, err := te.worktreeMgr.GitRunInWorktreeOutput(taskID, "status", "--porcelain")
	if err != nil {
		return nil, err
	}
	return git.ParseStatusPaths(out), nil
}

// composeDiagnosticsAdvisory turns per-file findings into a single advisory.
// ok is false if nothing is reportable.
func composeDiagnosticsAdvisory(filesWithFindings int, blocks []string) (advisory, bool) {
	if len(blocks) == 0 {
		return advisory{}, false
	}
	plural := "s"
	if filesWithFindings == 1 {
		plural = ""
	}
	return advisory{
		kind:     advisoryDiagnostics,
		severity: "error",
		text: fmt.Sprintf("LSP diagnostics for this turn's changes (%d file%s with findings):\n\n%s",
			filesWithFindings, plural, strings.Join(blocks, "\n\n")),
	}, true
}

// languageGroup is the set of files one language server owns. A turn can touch Go and
// TypeScript files at once, and each language has its own server, handshake and sandbox.
type languageGroup struct {
	language string
	cfg      *lsp.ServerConfig
	files    []string
}

// groupByServerLanguage buckets files by the server that owns them. The key is cfg.Language, matching the connection store.
func groupByServerLanguage(registry *lsp.ExtensionRegistry, files []string) []languageGroup {
	var groups []languageGroup
	index := make(map[string]int, len(groups))
	for _, f := range files {
		cfg := registry.Lookup(f)
		if cfg == nil {
			continue
		}
		if i, seen := index[cfg.Language]; seen {
			groups[i].files = append(groups[i].files, f)
			continue
		}
		index[cfg.Language] = len(groups)
		groups = append(groups, languageGroup{language: cfg.Language, cfg: cfg, files: []string{f}})
	}
	return groups
}

// turnLanguageServer is one language server shared by all files of that language in a pass. This prevents the server from being torn down between files.
type turnLanguageServer struct {
	sub       chan json.RawMessage
	workspace string
	language  string
	cfg       *lsp.ServerConfig
// held tracks if the pass owes a reference from GetOrCreate.
	held bool
}

// close ends the subscription and hands back the pass's reference. Idempotent, so bailing out
// mid-file can neither leak a reference nor drop one that another holder still needs.
func (s *turnLanguageServer) close(mgr lspManager) {
	if s.sub != nil {
		mgr.Unsubscribe(s.sub)
		s.sub = nil
	}
	if s.held {
		s.held = false
		mgr.Release(s.workspace, s.language)
	}
}

// fileOutcome is the outcome of a file's diagnostic request.
type fileOutcome int

const (
	fileUnchanged fileOutcome = iota // the server already held this exact text — nothing was asked
	fileClean                        // the server answered and had nothing reportable
	fileReported                     // the server answered with findings worth a hint
	fileSilent                       // no answer inside the budget the file was given
	fileFailed                       // the document could not be handed to the server
	fileStopped                      // the task ended first, which is the user asking for it
	fileUnloaded                     // the server only ever said it cannot load this file
)

type diagnosticsPassReport struct {
	asked     int
	servers   int // language servers attached and handshaked
	reported  int
	clean     int
	unchanged int
	failed    int
	silent    []string
	// unloaded counts files where the server has no view of the package.
	unloaded int
	unasked  int
	stopped  bool // the task itself ended mid-pass
}

// warning describes failures in language server utility.
func (r diagnosticsPassReport) warning() string {
	if r.stopped {
		return ""
	}
	var parts []string
	if len(r.silent) > 0 {
		parts = append(parts, fmt.Sprintf("%d of %d file(s) got no diagnostics within their budget (%s) — a server that stops answering is dead, not slow",
			len(r.silent), r.asked, strings.Join(r.silent, ", ")))
	}
	if r.unloaded > 0 {
		parts = append(parts, fmt.Sprintf("%d of %d file(s) got only the language server's own \"no active builds\" state — it has no view of those packages, so what it did not report there is unknown, not clean",
			r.unloaded, r.asked))
	}
	if r.unasked > 0 {
		parts = append(parts, fmt.Sprintf("%d of %d file(s) were never asked about — the pass spent its whole budget before reaching them",
			r.unasked, r.asked))
	}
	return strings.Join(parts, "; ")
}

// diagnosticsPass is a turn-boundary pass over changed files.
type diagnosticsPass struct {
	te       *TaskExecutor
	mgr      lspManager
	taskID   string
	worktree string
	passCtx  context.Context
	taskCtx  context.Context
	// 0 means lspLoaderSettleGrace. A pass that wants a shorter one sets this, because several passes
	// run at once and the package default is shared by all of them.
	loaderSettleGrace time.Duration
}

// settleGrace is this pass's loader-state grace.
func (p *diagnosticsPass) settleGrace() time.Duration {
	if p.loaderSettleGrace > 0 {
		return p.loaderSettleGrace
	}
	return lspLoaderSettleGrace
}

func (p *diagnosticsPass) stopped() bool { return p.taskCtx.Err() != nil }

// run asks language servers about files and returns findings and report.
func (p *diagnosticsPass) run(registry *lsp.ExtensionRegistry, files []string) ([]string, diagnosticsPassReport) {
	report := diagnosticsPassReport{asked: len(files)}
	var blocks []string

	for _, group := range groupByServerLanguage(registry, files) {
		if p.passCtx.Err() != nil {
			break
		}
		srv, err := p.open(group.cfg)
		if err != nil {
			// Nothing this language's files can be asked about. Other languages still get
			// their own server, their own handshake and their own chance.
			logging.For(logging.LSP).With(logging.TaskID, p.taskID).Infof("WARNING: no usable %s language server for %d file(s): %v", group.language, len(group.files), err)
			report.failed += len(group.files)
			continue
		}
		report.servers++
		for _, file := range group.files {
			if p.stopped() {
				break
			}
			hint, outcome := p.diagnoseFile(srv, file)
			switch outcome {
			case fileReported:
				blocks = append(blocks, hint)
				report.reported++
			case fileClean:
				report.clean++
			case fileUnchanged:
				report.unchanged++
			case fileFailed:
				report.failed++
			case fileSilent:
				report.silent = append(report.silent, file)
			case fileUnloaded:
				report.unloaded++
			case fileStopped:
			}
			if p.stopped() {
				break
			}
		}
		srv.close(p.mgr)
	}

	report.stopped = p.stopped()
	if !report.stopped {
		// Whatever is left was never reached: the pass budget is what ended the loop.
		report.unasked = report.asked - (report.reported + report.clean + report.unchanged + report.failed + report.unloaded + len(report.silent))
		if report.unasked < 0 {
			report.unasked = 0
		}
	}
	return blocks, report
}

// open attaches to a language server and performs handshake.
func (p *diagnosticsPass) open(cfg *lsp.ServerConfig) (*turnLanguageServer, error) {
	if _, err := p.mgr.GetOrCreate(p.passCtx, p.worktree, cfg.Language); err != nil {
		return nil, err
	}
	srv := &turnLanguageServer{sub: p.mgr.Subscribe(), workspace: p.worktree, language: cfg.Language, cfg: cfg, held: true}
	if err := p.mgr.EnsureInitialized(p.passCtx, p.worktree, cfg); err != nil {
		srv.close(p.mgr)
		return nil, err
	}
	return srv, nil
}

// diagnoseFile asks language server about a file using its own budget.
func (p *diagnosticsPass) diagnoseFile(srv *turnLanguageServer, relPath string) (string, fileOutcome) {
	fileCtx, cancel := context.WithTimeout(p.passCtx, lspDiagnosticsBudget)
	defer cancel()

	uri, synced, err := p.mgr.SyncDocument(fileCtx, srv.workspace, relPath, srv.cfg)
	if err != nil {
		if p.stopped() {
			return "", fileStopped
		}
		logging.For(logging.LSP).With(logging.TaskID, p.taskID).Warnf("%s: document sync failed: %v", relPath, err)
		return "", fileFailed
	}
	if !synced {
		// The server holds this exact text already, so no notification is coming and there
		// is nothing new for the model to hear. This is the no-change case, not a failure.
		return "", fileUnchanged
	}
	return p.awaitPublishDiagnostics(fileCtx, srv, uri, relPath)
}

// loaderStateMessages are gopls warnings for documents not in any loaded package.
var loaderStateMessages = []string{
	"No active builds contain",
	"No packages found for open file",
	"This file is within module",
}

// isLoaderStateDiagnostic reports if a diagnostic is a loader state message.
func isLoaderStateDiagnostic(d LSPDiagnostic) bool {
	if d.Source != "go list" {
		return false
	}
	for _, prefix := range loaderStateMessages {
		if strings.HasPrefix(d.Message, prefix) {
			return true
		}
	}
	return false
}

// awaitPublishDiagnostics waits for publish diagnostics, handling loader state grace.
func (p *diagnosticsPass) awaitPublishDiagnostics(ctx context.Context, srv *turnLanguageServer, uri, relPath string) (string, fileOutcome) {
	timeout := time.NewTimer(lspPublishBudget)
	defer timeout.Stop()
	var grace *time.Timer
	var graceC <-chan time.Time
	defer func() {
		if grace != nil {
			grace.Stop()
		}
	}()

	for {
		select {
		case <-ctx.Done():
			if p.stopped() {
				return "", fileStopped
			}
			return "", fileSilent
		case <-timeout.C:
			return "", fileSilent
		case <-graceC:
			return "", fileUnloaded
		case raw, ok := <-srv.sub:
			if !ok {
				return "", fileSilent
			}
			var envelope struct {
				Method string `json:"method"`
				Params struct {
					URI         string          `json:"uri"`
					Diagnostics []LSPDiagnostic `json:"diagnostics"`
				} `json:"params"`
			}
			if err := json.Unmarshal(raw, &envelope); err != nil {
				continue
			}
			if envelope.Method != "textDocument/publishDiagnostics" || envelope.Params.URI != uri {
				continue
			}

			var findings []LSPDiagnostic
			sawLoaderState := false
			for _, d := range envelope.Params.Diagnostics {
				if isLoaderStateDiagnostic(d) {
					sawLoaderState = true
					continue
				}
				findings = append(findings, d)
			}
			if len(findings) > 0 {
				hint := p.te.formatLSPDiagnostics(relPath, findings)
				if hint == "" {
					return "", fileClean // findings below the severity worth reporting
				}
				return hint, fileReported
			}
			if sawLoaderState {
				if grace == nil {
					grace = time.NewTimer(p.settleGrace())
					graceC = grace.C
				}
				continue
			}
			// An empty publish: the server loaded the file and found nothing to report.
			return "", fileClean
		}
	}
}

// collectTurnDiagnostics runs diagnostic pass over changed files and queues findings as an advisory.
func (te *TaskExecutor) collectTurnDiagnostics(setup *taskExecSetup, lc *loopContext) {
	files := te.turnDiagnosticFiles(setup, lc)
	if len(files) == 0 {
		return
	}
	registry := te.lspMgr.GetRegistry()
	if registry == nil {
		return
	}
	worktreePath, err := te.worktreeMgr.WorktreePath(setup.task.ID)
	if err != nil {
		logging.For(logging.LSP).With(logging.TaskID, setup.task.ID).Warnf("worktree path error: %v", err)
		return
	}
	// Manifest changes must be reported to the LSP manager so it can restart affected sessions and update the project graph.
	if recycled := te.lspMgr.FilesChanged(worktreePath, files); recycled {
		logging.For(logging.LSP).With(logging.TaskID, setup.task.ID).Infof("reloaded the language server after a manifest change")
	}
	// One budget for the pass, so eight slow files cannot add up to a stalled turn.
	passCtx, cancel := context.WithTimeout(setup.ctx, lspDiagnosticsPassBudget)
	defer cancel()

	pass := &diagnosticsPass{
		te:       te,
		mgr:      te.lspMgr,
		taskID:   setup.task.ID,
		worktree: worktreePath,
		passCtx:  passCtx,
		taskCtx:  setup.ctx,
	}
	blocks, report := pass.run(registry, files)
	// Every pass reports its stats. This helps distinguish between clean files and silent servers.
	logging.For(logging.LSP).With(logging.TaskID, setup.task.ID).Warnf("pass: asked=%d servers=%d reported=%d clean=%d unchanged=%d failed=%d silent=%d unloaded=%d unasked=%d", report.asked, report.servers, report.reported, report.clean, report.unchanged, report.failed, len(report.silent), report.unloaded, report.unasked)
	if w := report.warning(); w != "" {
		logging.For(logging.LSP).With(logging.TaskID, setup.task.ID).Infof("WARNING: %s", w)
	}

	a, ok := composeDiagnosticsAdvisory(report.reported, blocks)
	if !ok {
		return
	}
	if !diagnosticsChanged(lc, a.text) {
		// Same findings as the previous turn — re-sending them only teaches the model to
		// skim the channel.
		logging.For(logging.LSP).With(logging.TaskID, setup.task.ID).Infof("diagnostics unchanged, not re-injecting")
		return
	}
	queueAdvisory(lc, a)
}

// diagnosticsChanged reports if diagnostics have changed since last pass.
func diagnosticsChanged(lc *loopContext, text string) bool {
	if text == lc.lastDiagnosticsText {
		return false
	}
	lc.lastDiagnosticsText = text
	return true
}
