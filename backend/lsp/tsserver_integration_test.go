package lsp

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	bridge "kanban/pkg/bridge"
)

// TestTSServerCrossFileViaBridge drives the real sandbox-bridge binary (host
// process, stdin/stdout — the same client path the extension sandbox uses via
// `podman exec`) and a real typescript-language-server through the product's
// exact LSP request sequence (initialize params, single-file didOpen, then
// definition/references/rename). It proves cross-file resolution is complete
// for .ts/.js with the protocol the LSP manager sends.
//
// Skipped when typescript-language-server is not available. Locate it with
// the MIKE_TSSERVER_BIN env var or on PATH.
func TestTSServerCrossFileViaBridge(t *testing.T) {
	tsserver := os.Getenv("MIKE_TSSERVER_BIN")
	if tsserver == "" {
		var err error
		tsserver, err = exec.LookPath("typescript-language-server")
		if err != nil {
			t.Skip("typescript-language-server not available (set MIKE_TSSERVER_BIN)")
		}
	}
	bridgeBin := findBridgeBinary(t)

	// ── Test workspace: two files in one tsconfig project ──
	ws := t.TempDir()
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
	bridgeCmd := exec.Command(bridgeBin)
	bridgeCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := bridgeCmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := bridgeCmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var bridgeErr bytes.Buffer
	bridgeCmd.Stderr = &bridgeErr
	if err := bridgeCmd.Start(); err != nil {
		t.Skipf("cannot start sandbox-bridge (env limitation?): %v", err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-bridgeCmd.Process.Pid, syscall.SIGKILL)
		_ = bridgeCmd.Wait()
	})

	client := bridge.NewClient(stdin, stdout)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if err := client.LSPStart(ctx, tsserver, []string{"--stdio"}); err != nil {
		t.Fatalf("LSPStart failed: %v (bridge stderr: %s)", err, bridgeErr.String())
	}

	uri := func(p string) string { return "file://" + p }

	// Mirror lspInitialize: same initialize params the product sends.
	initReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"processId": os.Getpid(),
			"rootUri":   uri(ws),
			"capabilities": map[string]any{
				"textDocument": map[string]any{
					"publishDiagnostics": map[string]any{"relatedInformation": true},
					"hover":              map[string]any{"contentFormat": []string{"markdown", "plaintext"}},
				},
				"workspace": map[string]any{"workspaceFolders": true},
			},
			"workspaceFolders": []map[string]any{{"uri": uri(ws), "name": filepath.Base(ws)}},
		},
	}
	if _, err := sendLSPJSONRPC(ctx, client, initReq); err != nil {
		t.Fatalf("initialize failed: %v (bridge stderr: %s)", err, bridgeErr.String())
	}
	notifyLSP(ctx, t, client, "initialized", map[string]any{})

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
