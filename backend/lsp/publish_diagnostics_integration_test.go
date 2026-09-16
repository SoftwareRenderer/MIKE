package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"mike/pkg/bridge"
)

// TestPublishDiagnosticsForEveryFileWithOneHandshake: against the real server, one connection and
// one handshake must produce a publishDiagnostics for every file opened — the shape of the
// turn-boundary pass.
//
// Skipped without typescript-language-server (PATH or MIKE_TSSERVER_BIN) and a typescript package
// it can resolve; see seedTypeScript.
func TestPublishDiagnosticsForEveryFileWithOneHandshake(t *testing.T) {
	tsserver := os.Getenv("MIKE_TSSERVER_BIN")
	if tsserver == "" {
		var err error
		if tsserver, err = exec.LookPath("typescript-language-server"); err != nil {
			t.Skip("typescript-language-server not available (set MIKE_TSSERVER_BIN)")
		}
	}

	const fileCount = 8
	const errorsPerFile = 8

	ws := t.TempDir()
	seedTypeScript(t, ws, tsserver)
	writeFile(t, filepath.Join(ws, "tsconfig.json"), `{
	"compilerOptions": {"target": "ES2022", "module": "ESNext", "moduleResolution": "bundler", "strict": true, "noEmit": true},
	"include": ["src"]
}`)
	src := filepath.Join(ws, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	uris := make([]string, 0, fileCount)
	for i := 0; i < fileCount; i++ {
		name := fmt.Sprintf("src/mod%d.ts", i)
		var body string
		for e := 0; e < errorsPerFile; e++ {
			// A string where a number belongs: TS2322, reported per occurrence.
			body += fmt.Sprintf("export const bad%d_%d: number = %q;\n", i, e, "not a number")
		}
		writeFile(t, filepath.Join(ws, name), body)
		abs, err := filepath.Abs(filepath.Join(ws, name))
		if err != nil {
			t.Fatal(err)
		}
		uris = append(uris, "file://"+abs)
	}

	client := startHostBridge(t, tsserver)
	watch := watchDiagnostics(client)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// One handshake for the whole session — the same initialize + initialized pair the
	// manager's lspInitialize sends, and never a second one.
	rootURI := "file://" + ws
	if _, err := client.LSPSend(ctx, mustJSON(t, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"rootUri": rootURI,
			"capabilities": map[string]any{
				"textDocument": map[string]any{
					"publishDiagnostics": map[string]any{"relatedInformation": true},
					"hover":              map[string]any{"contentFormat": []string{"markdown", "plaintext"}},
				},
				"workspace": map[string]any{"workspaceFolders": true},
			},
			"workspaceFolders": []map[string]any{{"uri": rootURI, "name": filepath.Base(ws)}},
		},
	})); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if err := client.LSPNotify(ctx, mustJSON(t, map[string]any{
		"jsonrpc": "2.0", "method": "initialized", "params": map[string]any{},
	})); err != nil {
		t.Fatalf("initialized: %v", err)
	}

	// Then one document at a time, exactly as the pass does it: didOpen, wait for that URI's
	// diagnostics, next file.
	for i, uri := range uris {
		content, err := os.ReadFile(filepath.Join(ws, fmt.Sprintf("src/mod%d.ts", i)))
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		if err := client.LSPNotify(ctx, mustJSON(t, map[string]any{
			"jsonrpc": "2.0", "method": "textDocument/didOpen",
			"params": map[string]any{
				"textDocument": map[string]any{
					"uri": uri, "languageId": "typescript", "version": 1, "text": string(content),
				},
			},
		})); err != nil {
			t.Fatalf("didOpen %s: %v", uri, err)
		}
		// The same allowance the pass gives one file's publishDiagnostics, so a server that
		// stops answering fails this test instead of hanging it.
		fileCtx, cancelFile := context.WithTimeout(ctx, 10*time.Second)
		got, err := watch.awaitHighWater(fileCtx, uri, errorsPerFile)
		cancelFile()
		if err != nil {
			t.Errorf("file %d (%s): no publishDiagnostics with %d errors after one handshake: %v",
				i, filepath.Base(uri), errorsPerFile, err)
			continue
		}
		t.Logf("file %d: %d error diagnostics after %v", i, got, time.Since(start).Round(time.Millisecond))
	}

	// A file that stops being broken must be answered again — the didChange reply that the
	// disk-vs-server comparison exists to get, without which a turn that fixed the errors would
	// keep being told about them.
	const fixedText = "export const fine: number = 1;\n"
	fixedPath := filepath.Join(ws, "src/mod0.ts")
	if err := os.WriteFile(fixedPath, []byte(fixedText), 0o644); err != nil {
		t.Fatal(err)
	}
	reportsBefore, _ := watch.reports(uris[0])
	if err := client.LSPNotify(ctx, mustJSON(t, map[string]any{
		"jsonrpc": "2.0", "method": "textDocument/didChange",
		"params": map[string]any{
			"textDocument":   map[string]any{"uri": uris[0], "version": 2},
			"contentChanges": []map[string]any{{"text": fixedText}},
		},
	})); err != nil {
		t.Fatalf("didChange: %v", err)
	}
	cleanCtx, cancelClean := context.WithTimeout(ctx, 10*time.Second)
	defer cancelClean()
	if err := watch.awaitClean(cleanCtx, uris[0], reportsBefore); err != nil {
		t.Errorf("after the file was fixed the server never reported it clean: %v", err)
	}
}

// startHostBridge runs the sandbox-bridge binary on the host over stdio — the same transport
// the sandbox uses through `podman exec`, minus the container.
func startHostBridge(t *testing.T, command string) *bridge.Client {
	t.Helper()
	bridgeBin := findBridgeBinary(t)
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
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start sandbox-bridge: %v", err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})

	client := bridge.NewClient(stdin, stdout)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := client.LSPStart(ctx, command, []string{"--stdio"}); err != nil {
		t.Fatalf("LSPStart %s: %v", command, err)
	}
	return client
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// diagnosticsWatch sorts the publishDiagnostics a server pushes by document URI, so a test can
// wait for one particular document without stealing another's notification.
type diagnosticsWatch struct {
	mu sync.Mutex
	// published counts reports per document, so a test can wait for the next one instead of
	// for any that ever happened. An empty diagnostics list counts: that is a server saying
	// the problems are gone.
	published map[string]int
	// counts holds the latest report per document, worst the most errors ever reported for
	// it. They differ because a server reports a document in several passes: the syntactic
	// diagnostics land first, and waiting for "8 errors" on the latest report alone would
	// race a mid-flight one that has fewer.
	counts map[string]int
	worst  map[string]int
	notify chan struct{}
}

func watchDiagnostics(client *bridge.Client) *diagnosticsWatch {
	w := &diagnosticsWatch{
		counts:    map[string]int{},
		worst:     map[string]int{},
		published: map[string]int{},
		notify:    make(chan struct{}, 1),
	}
	go func() {
		for raw := range client.LSPNotificationCh() {
			var msg struct {
				Method string `json:"method"`
				Params struct {
					URI         string `json:"uri"`
					Diagnostics []struct {
						Severity int `json:"severity"`
					} `json:"diagnostics"`
				} `json:"params"`
			}
			if err := json.Unmarshal(raw, &msg); err != nil || msg.Method != "textDocument/publishDiagnostics" {
				continue
			}
			errors := 0
			for _, d := range msg.Params.Diagnostics {
				if d.Severity == 1 {
					errors++
				}
			}
			w.mu.Lock()
			w.published[msg.Params.URI]++
			w.counts[msg.Params.URI] = errors
			if errors > w.worst[msg.Params.URI] {
				w.worst[msg.Params.URI] = errors
			}
			w.mu.Unlock()
			select {
			case w.notify <- struct{}{}:
			default:
			}
		}
	}()
	return w
}

// reports returns how many times the server has published this document and the error count of
// its latest report.
func (w *diagnosticsWatch) reports(uri string) (published, errors int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.published[uri], w.counts[uri]
}

// await blocks until the document has been reported with at least want errors, on the best
// report seen so far.
func (w *diagnosticsWatch) awaitHighWater(ctx context.Context, uri string, want int) (int, error) {
	for {
		w.mu.Lock()
		got := w.worst[uri]
		w.mu.Unlock()
		if got >= want {
			return got, nil
		}
		select {
		case <-ctx.Done():
			return got, fmt.Errorf("%w (have %d of %d diagnostics)", ctx.Err(), got, want)
		case <-w.notify:
		}
	}
}

// awaitClean waits for a report after `seen` that says the document has no errors — the answer
// a server owes a client that fixed the file. Anything older is not that answer.
func (w *diagnosticsWatch) awaitClean(ctx context.Context, uri string, seen int) error {
	for {
		published, errors := w.reports(uri)
		if published > seen && errors == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w (reports=%d want more than %d, latest error count=%d)", ctx.Err(), published, seen, errors)
		case <-w.notify:
		}
	}
}
