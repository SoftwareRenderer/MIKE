package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"mike/backend/httputil"
)

// These tests see the era pin and the capability decline from the outside: a stub answers JSON-RPC by
// hand and records every method it saw. It echoes the id it was given so the client is never left waiting.

// wireLog is what a probe server saw, request by request.
type wireLog struct {
	mu   sync.Mutex
	seen []wireRequest
}

type wireRequest struct {
	Method string
	Params map[string]any
}

func (w *wireLog) record(method string, params map[string]any) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.seen = append(w.seen, wireRequest{Method: method, Params: params})
}

func (w *wireLog) byMethod(method string) []wireRequest {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []wireRequest
	for _, r := range w.seen {
		if r.Method == method {
			out = append(out, r)
		}
	}
	return out
}

// probeServer answers each method with what `answer` returns; a nil result with a non-nil error is
// sent as a JSON-RPC error, which is how a server says "I have never heard of that method".
func probeServer(t *testing.T, answer func(method string) (map[string]any, map[string]any)) (*httptest.Server, *wireLog) {
	t.Helper()
	log := &wireLog{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params map[string]any  `json:"params"`
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		log.record(req.Method, req.Params)
		if len(req.ID) == 0 {
			// A notification gets the 202 an MCP server owes it, with no body.
			w.WriteHeader(http.StatusAccepted)
			return
		}
		result, rpcErr := answer(req.Method)
		body := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		if rpcErr != nil {
			body["error"] = rpcErr
		} else {
			body["result"] = result
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(ts.Close)
	return ts, log
}

// handshakeEra is a server that has never heard of server/discover and negotiates 2025-06-18 — the
// shape nearly every deployed MCP server has today.
func handshakeEra(method string) (map[string]any, map[string]any) {
	if method == "server/discover" {
		return nil, map[string]any{"code": -32601, "message": "Method not found"}
	}
	if method == "initialize" {
		return map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "legacy-probe", "version": "1"},
		}, nil
	}
	return map[string]any{}, nil
}

// statelessEra answers the pinned version and asks for nothing.
func statelessEra(method string) (map[string]any, map[string]any) {
	if method == "server/discover" {
		return map[string]any{
			"protocolVersion":   Era,
			"supportedVersions": []string{Era},
			"capabilities":      map[string]any{"tools": map[string]any{}},
			"serverInfo":        map[string]any{"name": "modern-probe", "version": "1"},
		}, nil
	}
	if method == "initialize" {
		return map[string]any{
			"protocolVersion": Era,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "modern-probe", "version": "1"},
		}, nil
	}
	return map[string]any{}, nil
}

// TestHandshakeEraServerIsRefusedByName is D2 against the shape that actually ships. The SDK falls
// back to the initialize handshake on its own, so what is under test is MIKE's refusal afterwards.
func TestHandshakeEraServerIsRefusedByName(t *testing.T) {
	ts, log := probeServer(t, handshakeEra)

	_, err := connect(context.Background(), registered(ts.URL, 2), testID)
	if err == nil {
		t.Fatal("a handshake-era server connected")
	}
	if kind := kindOf(t, err); kind != FailureWrongEra {
		t.Errorf("kind = %q, want %q (%v)", kind, FailureWrongEra, err)
	}
	if !strings.Contains(err.Error(), Era) {
		t.Errorf("the refusal must name the version MIKE speaks: %q", err)
	}
	// The fallback really happened, which is the point: nothing but MIKE's own check refused it.
	if len(log.byMethod("server/discover")) == 0 {
		t.Error("no server/discover on the wire, so the fallback was never exercised")
	}
	if len(log.byMethod("initialize")) == 0 {
		t.Error("no initialize on the wire: nothing was negotiated, so the refusal proves nothing")
	}
}

// The capability decline is only real if it is on the wire. The SDK's own default is to declare roots,
// so this fails the moment the explicit empty capability set is dropped.
func TestClientDeclinesEveryCapability(t *testing.T) {
	ts, log := probeServer(t, statelessEra)
	sess, err := connect(context.Background(), registered(ts.URL, 3), testID)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer sess.close()

	discovered := log.byMethod("server/discover")
	if len(discovered) == 0 {
		t.Fatal("no server/discover on the wire to read the declaration from")
	}
	// The SEP-2575 triple rides in _meta, which is the whole shape change of the stateless era.
	meta, _ := discovered[0].Params["_meta"].(map[string]any)
	if meta == nil {
		t.Fatalf("server/discover carried no _meta: %v", discovered[0].Params)
	}
	if got, _ := meta["io.modelcontextprotocol/protocolVersion"].(string); got != Era {
		t.Errorf("declared protocol version %q, want %q", got, Era)
	}
	caps, present := meta["io.modelcontextprotocol/clientCapabilities"]
	if !present {
		t.Fatal("no clientCapabilities on the wire, so the version check could not have run")
	}
	body, _ := json.Marshal(caps)
	for _, declined := range []string{"roots", "sampling", "elicitation"} {
		if strings.Contains(string(body), declined) {
			t.Errorf("the client declared %s, which MIKE declines: %s", declined, body)
		}
	}
	info, ok := meta["io.modelcontextprotocol/clientInfo"].(map[string]any)
	if !ok || info["name"] != testID.Name {
		t.Errorf("clientInfo = %v, want the identity MIKE was given", meta["io.modelcontextprotocol/clientInfo"])
	}
}

// A registered server is dialled with MIKE's own egress client, which is what keeps the address rule
// in force: the URL is a person's, and the address it names is still bounded.
func TestTheAddressRuleAppliesToARegisteredServer(t *testing.T) {
	for _, url := range []string{
		"http://169.254.169.254/latest/meta-data/", // the cloud metadata address
		"http://0.0.0.0:9999/mcp",
	} {
		_, err := connect(context.Background(), registered(url, 2), testID)
		if err == nil {
			t.Errorf("%s connected, so the address rule is not on this path", url)
			continue
		}
		// The refusal is the egress rule's own, not a message MIKE invented.
		if !errors.Is(err, httputil.ErrInternalAddress) {
			t.Errorf("%s: %v, want the non-public-address refusal", url, err)
		}
	}
}

// The host a person typed is the one address admitted at loopback, which is what makes a local MCP
// server usable while a name that resolves elsewhere stays refused.
func TestTheTypedHostIsGrantedAtLoopback(t *testing.T) {
	fake := fakeMCPServer(t, fakeOptions{Stateless: true})
	// 127.0.0.1 is loopback, and it is the host in the URL: the grant is what lets this connect at
	// all, so a failure here means the grant was dropped and every local server stopped working.
	if _, err := connect(context.Background(), registered(fake.URL, 3), testID); err != nil {
		t.Fatalf("the granted loopback host was refused: %v", err)
	}
}
