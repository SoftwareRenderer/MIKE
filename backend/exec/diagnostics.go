package exec

// Turn-boundary workspace diagnostics: one pass over the files the turn actually changed,
// after its tool results are known, instead of one fetch per edit inside the tool callback.
// Working from the changed file list also catches what `bash` did (sed -i, formatters,
// codegen) rather than guessing from the command line.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"kanban/backend/git"
	"kanban/backend/lsp"
)

// maxDiagnosticFilesPerPass bounds one pass: every file costs a language-server round trip,
// so a turn that touched dozens is diagnosed over several turns rather than stalling this one.
const maxDiagnosticFilesPerPass = 8

// lspDiagnosticsPassBudget bounds one pass, lspDiagnosticsBudget one file in it, and
// lspPublishBudget the publishDiagnostics that answers a file's didOpen or didChange.
// The initialize handshake is bounded in the LSP manager (lspInitReplyTimeout)
const (
	lspDiagnosticsPassBudget = 15 * time.Second
	lspDiagnosticsBudget     = 12 * time.Second
	lspPublishBudget         = 10 * time.Second
)

// lspLoaderSettleGrace is the extra room given to a file whose first publish was the server's own loader state
var lspLoaderSettleGrace = 2 * time.Second

// noteTouchedFile records a path a tool wrote or edited. Order is kept so the advisory
// reads in the order the model worked.
func (lc *loopContext) noteTouchedFile(path string) {
	for _, known := range lc.touchedFiles {
		if known == path {
			return
		}
	}
	lc.touchedFiles = append(lc.touchedFiles, path)
}

// noteShellMayHaveChangedFiles flags that a shell command can touch anything, so the
// boundary pass asks git what actually changed rather than trusting the command line.
func (lc *loopContext) noteShellMayHaveChangedFiles() {
	lc.shellMayHaveChangedFiles = true
}

// turnDiagnosticFiles returns the workspace-relative files worth asking a language server
// about: what the worktree reports changed, plus what the tools named, filtered to paths an
// installed LSP extension claims. It consumes the turn's bookkeeping so the next starts clean.
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
			log.Printf("[lsp-diag] task=%s git status failed, using tool paths: %v", setup.task.ID, err)
		} else {
			candidates = append(changed, candidates...)
		}
	}
	return filterDiagnosticFiles(registry, setup.projectName(), candidates)
}

// filterDiagnosticFiles normalizes candidate paths, drops duplicates and everything no
// installed LSP extension claims, and bounds the result.
func filterDiagnosticFiles(registry *lsp.ExtensionRegistry, projectName string, candidates []string) []string {
	seen := make(map[string]bool, len(candidates))
	files := make([]string, 0, len(candidates))
	for _, c := range candidates {
		rel := normalizePath(projectName, c)
		if rel == "" || rel == "." || seen[rel] {
			continue
		}
		if registry.Lookup(rel) == nil {
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

// worktreeChangedFiles lists the paths the worktree reports as changed.
func (te *TaskExecutor) worktreeChangedFiles(taskID string) ([]string, error) {
	out, err := te.worktreeMgr.GitRunInWorktreeOutput(taskID, "status", "--porcelain")
	if err != nil {
		return nil, err
	}
	return git.ParseStatusPaths(out), nil
}

// composeDiagnosticsAdvisory turns one pass's per-file findings into the single note the
// model gets. ok is false when nothing was severe enough to report.
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

// groupByServerLanguage buckets files by the server that owns them, in the order the model
// worked in. The key is cfg.Language — the key the connection is stored under — so grouping
// and attaching cannot disagree.
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

// turnLanguageServer is one language server, attached and handshaked once, shared by every
// file of that language in the pass. Attaching per file would let the pass's own Release be the
// last reference, tearing down the server the next file is about to use.
type turnLanguageServer struct {
	proc      *lsp.LSPProcess
	sub       chan json.RawMessage
	workspace string
	language  string
	cfg       *lsp.ServerConfig
	// held records that this pass still owes the reference GetOrCreate took, so close hands it
	// back exactly once.
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

// fileOutcome is what one file's question came back as. The pass keeps them apart because
// "did not change" is silence on purpose and "never answered" is a broken language server.
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

// diagnosticsPassReport is what one pass says about itself afterwards.
type diagnosticsPassReport struct {
	asked     int
	servers   int // language servers attached and handshaked
	reported  int
	clean     int
	unchanged int
	failed    int
	silent    []string
	// unloaded counts files whose only answer was the server saying it has no view of them:
	// answered, but not clean and not silent.
	unloaded int
	unasked  int
	stopped  bool // the task itself ended mid-pass
}

// warning names a shortfall an operator has to be told about, or "" when there is nothing to
// say. Unchanged files and a pass the user stopped are quiet on purpose; everything else means
// a language server stopped being useful, and a turn silently losing its diagnostics is a
// failure nobody would notice.
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

// diagnosticsPass is one turn-boundary pass over the files this turn changed. It holds both
// contexts because the task stopping is expected and the budget running out is not.
type diagnosticsPass struct {
	te       *TaskExecutor
	mgr      lspManager
	taskID   string
	worktree string
	passCtx  context.Context
	taskCtx  context.Context
}

// stopped reports whether the task itself is over.
func (p *diagnosticsPass) stopped() bool { return p.taskCtx.Err() != nil }

// run asks every relevant language server about its share of the files and returns the
// findings beside what the pass had to give up getting them.
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
			log.Printf("[lsp-diag] task=%s WARNING: no usable %s language server for %d file(s): %v",
				p.taskID, group.language, len(group.files), err)
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

// open attaches to one language's server and performs the handshake the whole pass shares.
// Release happens exactly once, in close, whether the pass read one file or eight.
func (p *diagnosticsPass) open(cfg *lsp.ServerConfig) (*turnLanguageServer, error) {
	proc, err := p.mgr.GetOrCreate(p.passCtx, p.worktree, cfg.Language, cfg)
	if err != nil {
		return nil, err
	}
	srv := &turnLanguageServer{proc: proc, sub: p.mgr.Subscribe(), workspace: p.worktree, language: cfg.Language, cfg: cfg, held: true}
	if err := p.mgr.EnsureInitialized(p.passCtx, p.worktree, cfg); err != nil {
		srv.close(p.mgr)
		return nil, err
	}
	return srv, nil
}

// diagnoseFile asks one file's language server about it, on the file's own share of the pass
// budget so one silent document cannot spend the whole pass.
func (p *diagnosticsPass) diagnoseFile(srv *turnLanguageServer, relPath string) (string, fileOutcome) {
	fileCtx, cancel := context.WithTimeout(p.passCtx, lspDiagnosticsBudget)
	defer cancel()

	uri, synced, err := p.mgr.SyncDocument(fileCtx, srv.workspace, relPath, srv.cfg)
	if err != nil {
		if p.stopped() {
			return "", fileStopped
		}
		log.Printf("[lsp-diag] task=%s %s: document sync failed: %v", p.taskID, relPath, err)
		return "", fileFailed
	}
	if !synced {
		// The server holds this exact text already, so no notification is coming and there
		// is nothing new for the model to hear. This is the no-change case, not a failure.
		return "", fileUnchanged
	}
	return p.awaitPublishDiagnostics(fileCtx, srv, uri, relPath)
}

// loaderStateMessages are what gopls (source "go list") says about itself when it cannot place
// the open document in anything it has loaded. They are warnings, so they would be handed to the
// model as problems to fix, and a server with no view publishes nothing else about that file:
//
//   - "No active builds contain <path>" — no views, so likely an unfinished handshake.
//   - "No packages found for open file <path>" — the file is outside every loaded module.
//   - "This file is within module \"<other>\"" — a different module was loaded.
var loaderStateMessages = []string{
	"No active builds contain",
	"No packages found for open file",
	"This file is within module",
}

// isLoaderStateDiagnostic reports whether a diagnostic is the loader describing itself. Source
// alone is not enough: BrokenImport is also "go list", and that one is a real finding.
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

// awaitPublishDiagnostics waits for the publish naming this exact document; findings for other
// documents are skipped, not attributed here. A publish of pure loader state does not end the
// wait, it starts the settle grace, and its entries are dropped from a publish that has findings.
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
					Uri         string          `json:"uri"`
					Diagnostics []LSPDiagnostic `json:"diagnostics"`
				} `json:"params"`
			}
			if err := json.Unmarshal(raw, &envelope); err != nil {
				continue
			}
			if envelope.Method != "textDocument/publishDiagnostics" || envelope.Params.Uri != uri {
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
					grace = time.NewTimer(lspLoaderSettleGrace)
					graceC = grace.C
				}
				continue
			}
			// An empty publish: the server loaded the file and found nothing to report.
			return "", fileClean
		}
	}
}

// collectTurnDiagnostics runs one pass over everything this turn changed and queues the
// findings as a single advisory, after the turn's tool messages and outside the worktree mutex.
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
		log.Printf("[lsp-diag] task=%s worktree path error: %v", setup.task.ID, err)
		return
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
	// Every pass says what it did. A pass that asked about files and reported nothing looks
	// exactly like a pass that never ran, and "the model got no hint" is otherwise
	// indistinguishable from "there was nothing to say" — which is the difference between a
	// clean file and a language server nobody is listening to.
	log.Printf("[lsp-diag] task=%s pass: asked=%d servers=%d reported=%d clean=%d unchanged=%d failed=%d silent=%d unloaded=%d unasked=%d",
		setup.task.ID, report.asked, report.servers, report.reported, report.clean, report.unchanged,
		report.failed, len(report.silent), report.unloaded, report.unasked)
	if w := report.warning(); w != "" {
		log.Printf("[lsp-diag] task=%s WARNING: %s", setup.task.ID, w)
	}

	a, ok := composeDiagnosticsAdvisory(report.reported, blocks)
	if !ok {
		return
	}
	if !diagnosticsChanged(lc, a.text) {
		// Same findings as the previous turn — re-sending them only teaches the model to
		// skim the channel.
		log.Printf("[lsp-diag] task=%s diagnostics unchanged, not re-injecting", setup.task.ID)
		return
	}
	queueAdvisory(lc, a)
}

// diagnosticsChanged reports whether a pass says anything the model has not been shown
// already, and records the text when it does.
func diagnosticsChanged(lc *loopContext, text string) bool {
	if text == lc.lastDiagnosticsText {
		return false
	}
	lc.lastDiagnosticsText = text
	return true
}
