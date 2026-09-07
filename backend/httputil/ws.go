package httputil

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"kanban/pkg/logging"
)

// AcceptWS upgrades a WebSocket with the settings every MIKE endpoint shares, so no
// endpoint hand-rolls its own accept.
//
// Origin checking is left on: SameSite=Lax blocks a cross-site request first, and this
// is the second lock on endpoints that hand out a shell, a workspace's files, or its
// language servers.
//
// logTag names the endpoint in the failure log. On ok == false nothing was accepted.
func AcceptWS(w http.ResponseWriter, r *http.Request, logTag string) (conn *websocket.Conn, ok bool) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionContextTakeover,
	})
	if err != nil {
		logging.For(logging.HTTP).Warnf("websocket accept error at %s: %v", logTag, err)
		return nil, false
	}
	return c, true
}

const (
	// KeepaliveInterval stays under the 30-60 s idle timeout a proxy or VPN enforces.
	KeepaliveInterval = 27 * time.Second

	// KeepaliveTimeout bounds one ping so a silent peer cannot wedge the pinger.
	KeepaliveTimeout = 10 * time.Second

	// KeepaliveFrame is an app-level ping sent beside the control ping. Control frames
	// are invisible to JavaScript, so they never fire onmessage and never reset the
	// browser's own stale-connection timer. Must stay valid JSON.
	KeepaliveFrame = `{"type":"ping"}`
)

// Keepalive pings conn until ctx is done, in its own goroutine.
//
// A failed ping calls onDead and stops. Callers pass the cancel of the context their
// read loop is blocked on, because Read never returns for a peer that merely stopped
// talking.
//
// writeMu holds off the caller's own frames while pinging; pass nil if nothing else
// writes to this connection.
//
// The caller must also be reading conn concurrently: only Read() delivers a pong, so
// with no reader every ping times out and a live peer looks dead.
func Keepalive(ctx context.Context, conn *websocket.Conn, writeMu *sync.Mutex, onDead func()) {
	keepalive(ctx, conn, KeepaliveInterval, KeepaliveTimeout, writeMu, onDead)
}

// keepalive is Keepalive with the timings injected, so tests need not wait 27 s.
func keepalive(ctx context.Context, conn *websocket.Conn, interval, timeout time.Duration, writeMu *sync.Mutex, onDead func()) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				pingCtx, pingCancel := context.WithTimeout(ctx, timeout)
				if writeMu != nil {
					writeMu.Lock()
				}
				err := conn.Ping(pingCtx)
				if err == nil {
					_ = conn.Write(pingCtx, websocket.MessageText, []byte(KeepaliveFrame))
				}
				if writeMu != nil {
					writeMu.Unlock()
				}
				pingCancel()
				if err != nil {
					if onDead != nil {
						onDead()
					}
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
}
