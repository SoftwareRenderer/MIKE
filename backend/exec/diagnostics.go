package exec

// Turn-boundary workspace diagnostics.
//
// Diagnostics used to be fetched inside the tool callback, per write/edit call, while the
// task's worktree mutex was held: a 3-edit turn paid one full language-server handshake
// per edit serialised against the remaining tools, and reported errors for a file state
// its own sibling edits had already repaired. They are now collected once, after the
// turn's tool results are known, over the files that actually ended up changed — which
// also catches what `bash` did (sed -i, formatters, codegen) instead of guessing from the
// command line.

import (
	"fmt"
	"log"
	"strings"

	"kanban/backend/git"
	"kanban/backend/lsp"
)

// maxDiagnosticFilesPerPass bounds one diagnostics pass: every file costs a language-server
// round trip, so a turn that touched dozens of files diagnoses the first N and leaves the
// rest for a later turn instead of stalling this one.
const maxDiagnosticFilesPerPass = 8

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
// about after this turn: what the worktree reports as changed (when a shell command ran)
// plus what the tools named directly, filtered to paths an installed LSP extension claims.
// It consumes the turn's bookkeeping so the next turn starts clean.
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

// collectTurnDiagnostics runs one diagnostics pass over everything this turn changed and
// queues the findings as a single advisory. It runs after the turn's tool messages are in
// the conversation and outside the worktree mutex.
func (te *TaskExecutor) collectTurnDiagnostics(setup *taskExecSetup, lc *loopContext) {
	files := te.turnDiagnosticFiles(setup, lc)
	if len(files) == 0 {
		return
	}
	var blocks []string
	hits := 0
	for _, file := range files {
		select {
		case <-setup.ctx.Done():
			return
		default:
		}
		hint := te.triggerLSPDiagnostics(setup.ctx, setup.projectID, setup.task.ID, file)
		if hint == "" {
			continue
		}
		blocks = append(blocks, hint)
		hits++
	}

	a, ok := composeDiagnosticsAdvisory(hits, blocks)
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
