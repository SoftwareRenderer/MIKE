package httputil

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"
)

// The library accepts a request with no Origin at all, so AcceptWS states the rule itself
// rather than relying on the library's default.

func TestAcceptWSRefusesACrossOriginHandshake(t *testing.T) {
	for _, tc := range []struct {
		name   string
		origin string
		host   string
		ok     bool
	}{
		{name: "same origin", origin: "http://localhost:3000", host: "localhost:3000", ok: true},
		{name: "case differs", origin: "http://LocalHost:3000", host: "localhost:3000", ok: true},
		{name: "other port", origin: "http://localhost:9999", host: "localhost:3000"},
		{name: "no port against a port", origin: "http://localhost", host: "localhost:3000"},
		{name: "other host", origin: "https://evil.example", host: "localhost:3000"},
		{name: "suffix trick", origin: "http://localhost:3000.evil.example", host: "localhost:3000"},
		{name: "malformed", origin: "not a url", host: "localhost:3000"},
		{name: "absent origin", origin: "", host: "localhost:3000", ok: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/ws", nil)
			req.Host = tc.host
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			// Not a real upgrade: a refused Origin must never reach websocket.Accept, so the
			// accept error a non-upgrade produces proves it was not refused for the origin reason.
			conn, ok := AcceptWS(rec, req, "test")
			if ok || conn != nil {
				t.Fatalf("AcceptWS accepted a request that is not a WebSocket upgrade")
			}
			body := rec.Body.String()
			if tc.ok {
				if strings.Contains(body, "cross-origin") {
					t.Errorf("a %s handshake was refused: %s", tc.name, body)
				}
				return
			}
			if rec.Code != http.StatusForbidden || !strings.Contains(body, "cross-origin") {
				t.Errorf("%s: status = %d, body = %q, want 403 cross-origin", tc.name, rec.Code, body)
			}
		})
	}
}

// The check must not break the real handshake.
func TestAcceptWSStillAcceptsTheBrowserCase(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, ok := AcceptWS(w, r, "test")
		if !ok {
			return
		}
		_ = c.Close(websocket.StatusNormalClosure, "bye")
	}))
	defer srv.Close()

	wsURL := "ws" + srv.URL[len("http"):] + "/ws"
	client, resp, err := websocket.Dial(t.Context(), wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if resp == nil || resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("handshake status = %v", resp)
	}
	_ = client.CloseNow()
}
