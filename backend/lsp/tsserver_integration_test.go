package lsp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"kanban/backend/extensions"

	bridge "kanban/pkg/bridge"
)

// TestTSServerCrossFileViaBridge drives the real sandbox-bridge binary (stdin/stdout, as in the
// extension sandbox) and a real typescript-language-server through the product's request sequence,
// to prove cross-file resolution for .ts/.js actually works.
//
// Skipped without typescript-language-server (MIKE_TSSERVER_BIN or PATH) and a typescript package
// it can resolve; see seedTypeScript.
func TestTSServerCrossFileViaBridge(t *testing.T) {
	tsserver := tsserverBinary(t)
	bridgeBin := findBridgeBinary(t)

	// ── Test workspace: two files in one tsconfig project ──
	ws := t.TempDir()
	seedTypeScript(t, ws, tsserver)
	src := filepath.Join(ws, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(ws, "tsconfig.json"), `{
	"compilerOptions": {"target": "ES2022", "module": "ESNext", "moduleResolution": "bundler", "strict": true, "noEmit": true},
	"include": ["src"]
}`)
	utilPath := filepath.Join(src, "util.ts")
	chatPath := filepath.Join(src, "chat.ts")
	writeFile(t, utilPath, `export function getDraftText(taskId: string): string | null {
    return 'draft-' + taskId;
}
`)
	writeFile(t, chatPath, `import { getDraftText } from './util';

export function load(taskId: string) {
    return getDraftText(taskId);
}
`)

	// ── Start the bridge (stdio mode, as in the extension sandbox) ──
	var bridgeErr bytes.Buffer
	client := startBridge(t, bridgeBin, &bridgeErr)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if err := client.LSPStart(ctx, tsserver, []string{"--stdio"}); err != nil {
		t.Fatalf("LSPStart failed: %v (bridge stderr: %s)", err, bridgeErr.String())
	}

	uri := func(p string) string { return "file://" + p }

	initializeServer(ctx, t, client, ws, &bridgeErr)

	didOpen := func(p string) {
		content, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		notifyLSP(ctx, t, client, "textDocument/didOpen", map[string]any{
			"textDocument": map[string]any{
				"uri":        uri(p),
				"languageId": "typescript",
				"version":    1,
				"text":       string(content),
			},
		})
	}

	// ── Case 1: definition, cross-file (only the queried file is opened) ──
	didOpen(chatPath)
	waitForSettle(t, 2*time.Second)
	defResp, err := requestLSP(ctx, client, 2, "textDocument/definition", map[string]any{
		"textDocument": map[string]any{"uri": uri(chatPath)},
		"position":     map[string]any{"line": 3, "character": 11},
	})
	if err != nil {
		t.Fatalf("definition: %v", err)
	}
	var defEnvelope struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(defResp, &defEnvelope); err != nil {
		t.Fatalf("definition unmarshal: %v (%s)", err, defResp)
	}
	if !strings.Contains(string(defEnvelope.Result), uri(utilPath)) {
		t.Errorf("definition should resolve to %s, got: %s", utilPath, defEnvelope.Result)
	}

	// ── Case 2: references, cross-file (only the declaration file is opened) ──
	didOpen(utilPath)
	waitForSettle(t, 2*time.Second)
	refsResp, err := requestLSP(ctx, client, 3, "textDocument/references", map[string]any{
		"textDocument": map[string]any{"uri": uri(utilPath)},
		"position":     map[string]any{"line": 0, "character": 17},
		"context":      map[string]any{"includeDeclaration": true},
	})
	if err != nil {
		t.Fatalf("references: %v", err)
	}
	var refsEnvelope struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(refsResp, &refsEnvelope); err != nil {
		t.Fatalf("references unmarshal: %v (%s)", err, refsResp)
	}
	refs := string(refsEnvelope.Result)
	if !strings.Contains(refs, uri(utilPath)) || !strings.Contains(refs, uri(chatPath)) {
		t.Errorf("references should span both files, got: %s", refs)
	}
	// The import specifier in chat.ts counts as a reference, so expect 2:
	// the import plus the usage in load().
	if got := strings.Count(refs, uri(chatPath)); got != 2 {
		t.Errorf("references should show 2 refs in chat.ts (import + usage) before the on-disk edit, got %d: %s", got, refs)
	}

	// ── Case 3: rename, multi-file WorkspaceEdit ──
	renResp, err := requestLSP(ctx, client, 4, "textDocument/rename", map[string]any{
		"textDocument": map[string]any{"uri": uri(utilPath)},
		"position":     map[string]any{"line": 0, "character": 17},
		"newName":      "loadDraftText",
	})
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	var renEnvelope struct {
		Result struct {
			Changes map[string][]struct {
				NewText string `json:"newText"`
			} `json:"changes"`
		} `json:"result"`
	}
	if err := json.Unmarshal(renResp, &renEnvelope); err != nil {
		t.Fatalf("rename unmarshal: %v (%s)", err, renResp)
	}
	if len(renEnvelope.Result.Changes) != 2 {
		t.Fatalf("rename should touch exactly 2 files, got %d: %v", len(renEnvelope.Result.Changes), keys(renEnvelope.Result.Changes))
	}
	if n := len(renEnvelope.Result.Changes[uri(utilPath)]); n < 1 {
		t.Errorf("rename: expected >=1 edit in util.ts, got %d", n)
	}
	if n := len(renEnvelope.Result.Changes[uri(chatPath)]); n < 2 {
		t.Errorf("rename: expected >=2 edits in chat.ts (import + usage), got %d", n)
	}

	// ── Case 4: on-disk edit is re-synced via full-text didChange ──
	// tsserver does not watch the filesystem. ensureDocSync (LSPRequest) sends
	// exactly this notification when the on-disk content diverges; verify the
	// server picks up the new usage without a fresh didOpen.
	updatedChat := `import { getDraftText } from './util';

export function load(taskId: string) {
    return getDraftText(taskId);
}

export function reload(taskId: string) {
    return getDraftText(taskId);
}
`
	if err := os.WriteFile(chatPath, []byte(updatedChat), 0o644); err != nil {
		t.Fatal(err)
	}
	notifyLSP(ctx, t, client, "textDocument/didChange", map[string]any{
		"textDocument":   map[string]any{"uri": uri(chatPath), "version": 2},
		"contentChanges": []map[string]any{{"text": updatedChat}},
	})
	waitForSettle(t, 2*time.Second)
	refsResp2, err := requestLSP(ctx, client, 5, "textDocument/references", map[string]any{
		"textDocument": map[string]any{"uri": uri(utilPath)},
		"position":     map[string]any{"line": 0, "character": 17},
		"context":      map[string]any{"includeDeclaration": true},
	})
	if err != nil {
		t.Fatalf("references after didChange: %v", err)
	}
	var refs2Envelope struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(refsResp2, &refs2Envelope); err != nil {
		t.Fatalf("references after didChange unmarshal: %v", err)
	}
	if got := strings.Count(string(refs2Envelope.Result), uri(chatPath)); got != 3 {
		t.Errorf("after didChange re-sync, references should show 3 refs in chat.ts (import + 2 usages), got %d: %s", got, refs2Envelope.Result)
	}
}

// TestTSServerWireLanguageIdOpensJsxFiles drives a real typescript-language-server through the
// bridge and opens a .tsx and a .jsx file with the languageId the product puts on the wire.
// The manifest language — "typescript", which is what routing keys on — is the wrong
// ScriptKind for both: tsserver then parses JSX without the JSX grammar and answers with a
// wall of "',' expected." for syntax the file never contained, and it keeps that verdict for
// the URI because only the first didOpen of a file ever reaches it.
func TestTSServerWireLanguageIdOpensJsxFiles(t *testing.T) {
	tsserver := tsserverBinary(t)

	ws := t.TempDir()
	seedTypeScript(t, ws, tsserver)
	src := filepath.Join(ws, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(ws, "tsconfig.json"), `{
	"compilerOptions": {"target": "ES2022", "module": "ESNext", "moduleResolution": "bundler", "jsx": "react-jsx", "noEmit": true},
	"include": ["src"]
}`)
	widget := filepath.Join(src, "Widget.tsx")
	writeFile(t, widget, `type Props = { name: string };

export function Widget({ name }: Props) {
    const items: string[] = [name];
    return <ul>{items.map(i => <li key={i}>{i}</li>)}</ul>;
}
`)
	legacy := filepath.Join(src, "legacy.jsx")
	writeFile(t, legacy, `export function Legacy(items) {
    return <ul>{items.map(i => <li key={i}>{i}</li>)}</ul>;
}
`)

	var bridgeErr bytes.Buffer
	client := startBridge(t, findBridgeBinary(t), &bridgeErr)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := client.LSPStart(ctx, tsserver, []string{"--stdio"}); err != nil {
		t.Fatalf("LSPStart failed: %v (bridge stderr: %s)", err, bridgeErr.String())
	}
	initializeServer(ctx, t, client, ws, &bridgeErr)

	// Only a grammar complaint is the failure under test, and only a published report makes
	// the assertion non-vacuous. A temp workspace has no @types/react, so the JSX errors that
	// prove the file WAS parsed as JSX are the fixture talking — they are expected, and asked
	// for by name below.
	var mu sync.Mutex
	reports := map[string][]string{}
	go func() {
		for raw := range client.LSPNotificationCh() {
			var msg struct {
				Method string `json:"method"`
				Params struct {
					URI         string `json:"uri"`
					Diagnostics []struct {
						Code    any    `json:"code"`
						Message string `json:"message"`
					} `json:"diagnostics"`
				} `json:"params"`
			}
			if err := json.Unmarshal(raw, &msg); err != nil || msg.Method != "textDocument/publishDiagnostics" {
				continue
			}
			var msgs []string
			for _, d := range msg.Params.Diagnostics {
				msgs = append(msgs, fmt.Sprintf("[%v] %s", d.Code, d.Message))
			}
			mu.Lock()
			reports[msg.Params.URI] = msgs
			mu.Unlock()
		}
	}()

	// The ids come from a manifest declaration, so dropping the per-file entry below (or collapsing
	// every id onto the manifest language) is a mis-parse this test sees, not a silent pass.
	declared := &extensions.LSPConfig{
		Language: "typescript",
		FileTypes: []extensions.LSPFileType{
			{Extension: "ts", LanguageID: "typescript"},
			{Extension: "tsx", LanguageID: "typescriptreact"},
			{Extension: "js", LanguageID: "javascript"},
			{Extension: "jsx", LanguageID: "javascriptreact"},
		},
	}

	for _, path := range []string{widget, legacy} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		notifyLSP(ctx, t, client, "textDocument/didOpen", map[string]any{
			"textDocument": map[string]any{
				"uri":        "file://" + path,
				"languageId": declared.LanguageIDFor(path),
				"version":    1,
				"text":       string(content),
			},
		})
	}

	// A report per file: "no grammar complaints" is also what a server that never saw the
	// file answers, so the test would pass vacuously without this.
	waitReport := func(path string) []string {
		t.Helper()
		deadline := time.NewTimer(45 * time.Second)
		defer deadline.Stop()
		for {
			uri := "file://" + path
			mu.Lock()
			msgs, reported := reports[uri]
			mu.Unlock()
			if reported {
				return msgs
			}
			select {
			case <-deadline.C:
				t.Fatalf("nothing was ever published for %s — cannot tell a clean parse from an ignored file", path)
			case <-ctx.Done():
				t.Fatalf("waiting for diagnostics on %s: %v", path, ctx.Err())
			case <-time.After(200 * time.Millisecond):
			}
		}
	}

	widgetMsgs := waitReport(widget)
	all := append(append([]string{}, widgetMsgs...), waitReport(legacy)...)
	for _, m := range all {
		if strings.Contains(m, "expected") || strings.Contains(m, "can only be used in TypeScript files") {
			t.Errorf("tsserver parsed a file with the wrong ScriptKind: %s", m)
		}
	}
	// The other half of the verdict: tsserver only complains about JSX types once it has
	// analyzed the file AS JSX. With the old id ("typescript", the manifest language) this
	// file instead produced 15 × "',' expected." and no JSX diagnostic at all.
	if !strings.Contains(strings.Join(widgetMsgs, "\n"), "JSX") {
		t.Errorf("expected a JSX diagnostic (proof the .tsx was parsed with the JSX grammar); got %v", widgetMsgs)
	}
}

// initializeServer sends the handshake the product sends (mirror of lspInitialize) plus the
// "initialized" notification. typescript-language-server accepts no document — and publishes
// no diagnostic — before it has been initialized, so every test that wants a server answer
// starts here.
func initializeServer(ctx context.Context, t *testing.T, client *bridge.Client, ws string, stderr *bytes.Buffer) {
	t.Helper()
	initReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"rootUri": "file://" + ws,
			"capabilities": map[string]any{
				"textDocument": map[string]any{
					"publishDiagnostics": map[string]any{"relatedInformation": true},
					"hover":              map[string]any{"contentFormat": []string{"markdown", "plaintext"}},
				},
				"workspace": map[string]any{"workspaceFolders": true},
			},
			"workspaceFolders": []map[string]any{{"uri": "file://" + ws, "name": filepath.Base(ws)}},
		},
	}
	if _, err := sendLSPJSONRPC(ctx, client, initReq); err != nil {
		t.Fatalf("initialize failed: %v (bridge stderr: %s)", err, stderr.String())
	}
	notifyLSP(ctx, t, client, "initialized", map[string]any{})
}

// tsserverBinary resolves a typescript-language-server to drive, or skips the test.
func tsserverBinary(t *testing.T) string {
	t.Helper()
	if bin := os.Getenv("MIKE_TSSERVER_BIN"); bin != "" {
		return bin
	}
	bin, err := exec.LookPath("typescript-language-server")
	if err != nil {
		t.Skip("typescript-language-server not available (set MIKE_TSSERVER_BIN)")
	}
	return bin
}

// startBridge runs the sandbox-bridge in stdio mode (as the extension sandbox does) and
// returns the host side of its stdin/stdout. The process belongs to the test: it is killed
// with the process group, so a language server spawned under it never outlives the run.
func startBridge(t *testing.T, bridgeBin string, stderr *bytes.Buffer) *bridge.Client {
	t.Helper()
	cmd := exec.Command(bridgeBin)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start sandbox-bridge (env limitation?): %v", err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})
	return bridge.NewClient(stdin, stdout)
}

// findBridgeBinary locates the sandbox-bridge binary: the repo's prebuilt
// sandbox/sandbox-bridge first, building from source as a fallback.
func findBridgeBinary(t *testing.T) string {
	t.Helper()
	prebuilt := filepath.Join("..", "..", "sandbox", "sandbox-bridge")
	if _, err := os.Stat(prebuilt); err == nil {
		abs, _ := filepath.Abs(prebuilt)
		return abs
	}
	tmp := filepath.Join(t.TempDir(), "sandbox-bridge")
	out, err := exec.Command("go", "build", "-o", tmp, "kanban/pkg/bridge/agent").CombinedOutput()
	if err != nil {
		t.Skipf("sandbox-bridge not found and cannot build: %v (%s)", err, out)
	}
	return tmp
}

func sendLSPJSONRPC(ctx context.Context, client *bridge.Client, msg map[string]any) ([]byte, error) {
	raw, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return client.LSPSend(ctx, raw)
}

func requestLSP(ctx context.Context, client *bridge.Client, id int, method string, params map[string]any) ([]byte, error) {
	return sendLSPJSONRPC(ctx, client, map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	})
}

func notifyLSP(ctx context.Context, t *testing.T, client *bridge.Client, method string, params any) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.LSPNotify(ctx, raw); err != nil {
		t.Fatalf("notify %s: %v", method, err)
	}
}

// seedTypeScript links a typescript package into the test workspace: typescript-language-server
// looks in the workspace's node_modules first, and when nothing resolves it answers every request
// with null/[] — indistinguishable from a product bug.
//
// Search order: MIKE_TYPESCRIPT_DIR, the server binary's own node_modules, frontend/node_modules.
func seedTypeScript(t *testing.T, ws, tsserver string) {
	t.Helper()

	var candidates []string
	if env := os.Getenv("MIKE_TYPESCRIPT_DIR"); env != "" {
		candidates = append(candidates, env)
	}
	if resolved, err := filepath.EvalSymlinks(tsserver); err == nil {
		for dir := filepath.Dir(resolved); dir != "/" && dir != "."; dir = filepath.Dir(dir) {
			candidates = append(candidates, filepath.Join(dir, "node_modules", "typescript"))
		}
	}
	candidates = append(candidates, filepath.Join("..", "..", "frontend", "node_modules", "typescript"))

	for _, c := range candidates {
		if _, err := os.Stat(filepath.Join(c, "lib", "tsserver.js")); err != nil {
			continue
		}
		abs, err := filepath.Abs(c)
		if err != nil {
			continue
		}
		if err := os.MkdirAll(filepath.Join(ws, "node_modules"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(abs, filepath.Join(ws, "node_modules", "typescript")); err != nil {
			t.Fatalf("link typescript from %s: %v", abs, err)
		}
		t.Logf("typescript package: %s", abs)
		return
	}
	t.Skipf("no resolvable typescript package (looked in %v; set MIKE_TYPESCRIPT_DIR) — "+
		"typescript-language-server would answer every request with null", candidates)
}

func waitForSettle(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case <-time.After(d):
	case <-t.Context().Done():
	}
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
