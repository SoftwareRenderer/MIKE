package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mike/backend/types"

	"github.com/coder/websocket"
)

// The stream package's tests inject a fake reading; this one proves the real probe is wired.
func TestTheStreamGreetsAConnectionWithTheFilesystemsReading(t *testing.T) {
	s := testServer(t)
	cookie := loginTest(t, s)

	handler := s.CORSMiddleware(s.SessionMiddleware(s.CSRFMiddleware(CompressMiddleware(s.mux))))
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/api/stream", &websocket.DialOptions{
		HTTPHeader: http.Header{"Cookie": []string{cookie}},
	})
	if err != nil {
		t.Fatalf("dial the app stream: %v", err)
	}
	defer conn.CloseNow()

	for range 20 {
		_, raw, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read a frame: %v", err)
		}
		var frame map[string]any
		if json.Unmarshal(raw, &frame) != nil {
			continue
		}
		if frame["type"] != string(types.WSTypeStorageUsage) {
			continue
		}
		total, _ := frame["total_bytes"].(float64)
		if total <= 0 {
			t.Fatalf("greeting frame carries no filesystem size: %v", frame)
		}
		return
	}
	t.Fatal("the connection was never greeted with a filesystem reading")
}
