package lsp

import (
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"

	"kanban/backend/extensions"

	sandbox "kanban/pkg/bridge"
	"kanban/pkg/bridge/protocol"
)

// captureBridge stands in for sandbox-bridge: it answers every request with a fixed reply and keeps
// the parameters of the initialize request, so what the language server is actually told — and what
// the handshake does with the answer — can be inspected.
type captureBridge struct {
	mu         sync.Mutex
	initialize map[string]any
	lspReqs    int
}

func (c *captureBridge) record(params []byte) {
	// sandbox-bridge wraps the JSON-RPC message in an LSPParams envelope ({"raw": …}).
	var wrapper struct {
		Raw json.RawMessage `json:"raw"`
	}
	if err := json.Unmarshal(params, &wrapper); err != nil || len(wrapper.Raw) == 0 {
		return
	}
	var envelope struct {
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
	}
	if err := json.Unmarshal(wrapper.Raw, &envelope); err != nil || envelope.Method != "initialize" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.initialize = envelope.Params
}

func (c *captureBridge) snapshot() (map[string]any, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.initialize, c.lspReqs
}

const okInitializeReply = `{"jsonrpc":"2.0","id":1,"result":{}}`

func capturingClient(t *testing.T, captured *captureBridge, reply string) *sandbox.Client {
	t.Helper()
	bridgeRead, hostWrite := io.Pipe()
	hostRead, bridgeWrite := io.Pipe()
	cl := sandbox.NewClient(hostWrite, hostRead)

	go func() {
		for {
			frame, err := protocol.ReadFrame(bridgeRead)
			if err != nil {
				return
			}
			if frame.Type != protocol.TypeRequest {
				continue
			}
			req, err := protocol.ParseRequest(frame)
			if err != nil || req.Method != protocol.MethodLSP {
				continue
			}
			captured.mu.Lock()
			captured.lspReqs++
			captured.mu.Unlock()
			captured.record(req.Params)
			_ = protocol.WriteFrame(bridgeWrite, protocol.NewResult(frame.MsgID, []byte(reply)))
		}
	}()
	t.Cleanup(func() {
		_ = cl.Close()
		_ = hostWrite.Close()
		_ = bridgeWrite.Close()
	})
	return cl
}

// TestInitializeParamsAreOnlyWhatWeIntendToSend pins the shape of initialize: the workspace, the
// capabilities, and whatever the manifest asks to have merged in — nothing else.
//
// An allowlist rather than a banned-key check, because any parameter can be a promise the sandbox
// cannot keep: a host `processId` made typescript-language-server decide its client had died.
func TestInitializeParamsAreOnlyWhatWeIntendToSend(t *testing.T) {
	// The declared floor. Anything else in the request came from somewhere nobody decided on.
	allowed := map[string]bool{"rootUri": true, "capabilities": true, "workspaceFolders": true}

	cases := []struct {
		name string
		cfg  *ServerConfig
		also map[string]bool
	}{
		{
			name: "plain server config",
			cfg:  tsLikeCfg(),
		},
		{
			// The manifest's initParamsOverride is the one sanctioned addition, and it is also the
			// only way an extension configures its server, so it must survive the handshake intact.
			name: "manifest override merged",
			cfg: func() *ServerConfig {
				cfg := tsLikeCfg()
				cfg.InitParamsOverride = map[string]any{"initializationOptions": map[string]any{"tsserver": map[string]any{"path": "/opt/ts"}}}
				return cfg
			}(),
			also: map[string]bool{"initializationOptions": true},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			captured := &captureBridge{}
			cl := capturingClient(t, captured, okInitializeReply)
			mgr := NewExtensionLSPManager(nil, nil, nil)

			sc := newSandboxClient(cl, t.TempDir(), "typescript", "ext-1", map[string]string{})
			if err := mgr.lspInitialize(t.Context(), sc, tc.cfg); err != nil {
				t.Fatalf("lspInitialize: %v", err)
			}

			params, reqs := captured.snapshot()
			if params == nil {
				t.Fatalf("no initialize request reached the bridge (lsp requests seen: %d)", reqs)
			}
			for key := range params {
				if !allowed[key] && !tc.also[key] {
					t.Errorf("initialize sends %q, which nothing decided to send", key)
				}
			}
			for key := range tc.also {
				if _, ok := params[key]; !ok {
					t.Errorf("initialize dropped the manifest's %q", key)
				}
			}
		})
	}
}

// TestInitializeReportsAServerRefusal covers the other half of the same decision: sending fewer
// parameters is only safe if a server that minds says so where someone can read it. The reply body
// used to be discarded, so a JSON-RPC error to initialize looked exactly like acceptance, and every
// request after it failed with nothing in the log to explain why.
func TestInitializeReportsAServerRefusal(t *testing.T) {
	captured := &captureBridge{}
	cl := capturingClient(t, captured, `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"missing required parameter"}}`)
	mgr := NewExtensionLSPManager(nil, nil, nil)

	sc := newSandboxClient(cl, t.TempDir(), "typescript", "ext-1", map[string]string{})
	err := mgr.lspInitialize(t.Context(), sc, tsLikeCfg())
	if err == nil {
		t.Fatal("a JSON-RPC error answer to initialize was reported as success")
	}
	if !strings.Contains(err.Error(), "missing required parameter") {
		t.Errorf("error = %q, want it to carry the server's own message", err)
	}
}

// tsLikeCfg is the ServerConfig the registry hands back for a .ts file.
func tsLikeCfg() *ServerConfig {
	return &ServerConfig{Language: "typescript", Command: "typescript-language-server",
		FileTypes: []extensions.LSPFileType{{Extension: "ts", LanguageID: "typescript"}, {Extension: "tsx", LanguageID: "typescriptreact"}}}
}
