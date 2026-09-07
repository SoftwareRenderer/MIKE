package lsp

// A language server that cannot load a project does not say so. It answers with confident
// specifics — "no required module provides package X", "no active builds contain Y" — and the
// model spends the next turns fixing code that was never broken.
//
// So an extension may declare how to check whether its server can answer at all, which codes mean
// "could not load", and one sentence to tell the user instead. Core only compares opaque strings.
// Nothing calls Environment today, so no verdict is recorded — see its comment.

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"strings"
	"time"

	"kanban/backend/extensions"
	"kanban/pkg/logging"
	"kanban/pkg/shlex"
)

const (
	// envCheckTTL is how long a verdict is reused. Each check costs a round trip
	// into the sandbox, and a diagnostics pass asks per file.
	envCheckTTL = 60 * time.Second
	// envCheckDefaultTimeout / envCheckMaxTimeout bound one check. The check runs
	// inline in the turn's diagnostics pass, so a hung command must cost seconds.
	envCheckDefaultTimeout = 5 * time.Second
	envCheckMaxTimeout     = 15 * time.Second
	// envCheckMaxOutput caps how much of a failing command's output is kept for
	// the log. Enough to diagnose, never enough to matter.
	envCheckMaxOutput = 4096
)

// envStatus is one workspace's last verdict, with the time it was reached.
type envStatus struct {
	healthy   bool
	note      string
	checkedAt time.Time
	// checked is false until a verdict was actually reached — "unknown" and
	// "broken" are different states, and only the second justifies suppressing.
	checked bool
}

// EnvironmentSummary reports this extension's known state across every workspace
// it is currently serving: unhealthy if any one of them cannot be resolved —
// which is the case worth telling the user about, because that is the workspace
// whose diagnostics are being withheld. An extension that never ran its check,
// or declares none, reports nothing at all.
func (m *ExtensionLSPManager) EnvironmentSummary(extensionID string) (healthy bool, note string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, sc := range m.clients {
		if sc.extensionID != extensionID || sc.gate == nil {
			continue
		}
		st := m.environments[key]
		if !st.checked || st.healthy {
			continue
		}
		if st.note != "" {
			return false, st.note
		}
		return false, sc.gate.Note
	}
	return true, ""
}

// EnvironmentReport is what the diagnostics path needs to decide what to forward
// to the model: whether the server can resolve this workspace, what to tell the
// user if it cannot, and which diagnostic codes mean "could not load" rather
// than "this code is wrong".
type EnvironmentReport struct {
	Healthy       bool     `json:"healthy"`
	Note          string   `json:"note,omitempty"`
	SuppressCodes []string `json:"suppress_codes,omitempty"`
}

// Environment answers for one file in one workspace.
//
// Not wired: no caller asks it, so no verdict is ever recorded and EnvironmentSummary always
// reports healthy, which means the Settings -> Extensions health note can never appear.
//
// Everything about it is permissive on purpose: no LSP session, no declared check, or a check
// that could not be run all report healthy, so wiring it can only restore the old behaviour,
// never hide a real error behind an unreachable probe.
func (m *ExtensionLSPManager) Environment(workspacePath, filePath string) EnvironmentReport {
	cfg := m.GetRegistry().Lookup(filePath)
	if cfg == nil {
		cfg = m.GetRegistry().Lookup(workspacePath)
	}
	if cfg == nil {
		return EnvironmentReport{Healthy: true}
	}

	m.mu.Lock()
	sc := m.clients[processKey(workspacePath, cfg.Language)]
	m.mu.Unlock()
	if sc == nil || sc.gate == nil || sc.bridgeClient == nil {
		return EnvironmentReport{Healthy: true}
	}
	gate := sc.gate

	key := processKey(workspacePath, cfg.Language)
	m.mu.Lock()
	cached := m.environments[key]
	m.mu.Unlock()
	if cached.checked && time.Since(cached.checkedAt) < envCheckTTL {
		return environmentResult(cached, gate)
	}

	healthy, note := m.runEnvCheck(sc, gate, workspacePath)
	m.mu.Lock()
	m.environments[key] = envStatus{healthy: healthy, note: note, checkedAt: time.Now(), checked: true}
	m.mu.Unlock()
	if !healthy {
		// Once per verdict, not once per turn: the user needs to know the state,
		// and the log does not need the same line for every file.
		logging.For(logging.LSP).Infof("workspace=%s lang=%s cannot resolve the project: %s", workspacePath, cfg.Language, note)
	}
	return environmentResult(envStatus{healthy: healthy, note: note, checked: true}, gate)
}

// environmentResult pairs a verdict with the codes it governs.
func environmentResult(st envStatus, gate *extensions.EnvironmentGate) EnvironmentReport {
	if !st.checked || st.healthy {
		return EnvironmentReport{Healthy: true}
	}
	note := st.note
	if note == "" {
		note = gate.Note
	}
	return EnvironmentReport{Healthy: false, Note: note, SuppressCodes: gate.SuppressCodes}
}

// runEnvCheck runs the declared check inside the language server's sandbox, in that workspace's
// directory. It inherits the container's environment — where a granted dependency assignment lives —
// so it asks the toolchain the question the server would answer.
func (m *ExtensionLSPManager) runEnvCheck(sc *sandboxClient, gate *extensions.EnvironmentGate, workspacePath string) (bool, string) {
	cwd := containerWorkspacePath(mustAbs(workspacePath), sc.projectMounts)
	timeout := gate.Check.Timeout(envCheckDefaultTimeout, envCheckMaxTimeout)
	cmd, ok := envCheckCommand(gate, cwd)
	if !ok {
		return true, ""
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout+2*time.Second)
	defer cancel()
	holder, err := sc.bridgeClient.Bash(ctx, cmd, int64((timeout+time.Second)/time.Second))
	if err != nil {
		logging.For(logging.LSP).Warnf("check could not run for %s: %v", workspacePath, err)
		return true, ""
	}

	// The output stream must be drained or the bridge's writer blocks, so it is
	// copied into a bounded buffer regardless of whether the code is interesting.
	var buf bytes.Buffer
	drained := make(chan struct{})
	go func() {
		_, _ = io.Copy(&limitWriter{w: &buf, n: envCheckMaxOutput}, holder.Output())
		close(drained)
	}()

	select {
	case <-ctx.Done():
		logging.For(logging.LSP).Warnf("check timed out for %s after %s", workspacePath, timeout)
		return false, gate.Note
	case code := <-holder.ExitCode():
		<-drained
		out := strings.TrimSpace(buf.String())
		if code == 0 {
			return true, ""
		}
		if out != "" {
			logging.For(logging.LSP).Warnf("check failed for %s (exit %d): %s", workspacePath, code, out)
		}
		return false, gate.Note
	}
}

// envCheckCommand renders the check as one shell line: enter the workspace, then run the
// declared command. Every word is quoted through the shell-lexer rules, so nothing a manifest
// declares can turn into shell syntax — the command is run inside the extension's own sandbox,
// but a manifest is still somebody else's text.
func envCheckCommand(gate *extensions.EnvironmentGate, cwd string) (string, bool) {
	argv, ok := gate.Check.Argv()
	if !ok {
		return "", false
	}
	line := shlex.Join(argv)
	if cwd == "" {
		return line, true
	}
	return "cd " + shlex.Quote(cwd) + " && " + line, true
}

// limitWriter is an io.Writer that stops after n bytes.
type limitWriter struct {
	w io.Writer
	n int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.n <= 0 {
		return len(p), nil // report success so the copy does not abort early
	}
	if len(p) > l.n {
		_, _ = l.w.Write(p[:l.n])
		written := len(p)
		l.n = 0
		return written, nil
	}
	l.n -= len(p)
	return l.w.Write(p)
}

// mustAbs resolves a path, keeping the original when it cannot — callers only use
// the result for display and mount lookups, both of which tolerate it.
func mustAbs(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}
