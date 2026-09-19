package httputil

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"mike/pkg/logging"
)

// ReadLimit bounds one frame a client sends. The library default of 32 KiB is reachable in
// ordinary use, and going over it closes the connection rather than refusing the frame, so
// the browser reconnects, re-sends, and is closed again.
const ReadLimit = 1 << 20

// AcceptWS upgrades a WebSocket with the settings every MIKE endpoint shares, so no endpoint
// hand-rolls its own accept. logTag names the endpoint in the failure log; on ok == false
// nothing was accepted.
//
// The Origin rule is stated here rather than inherited from coder/websocket, which accepts an
// empty Origin. SameSite=Lax blocks a cross-site request first; this is the second lock on the
// endpoints that hand out a shell, a workspace's files, or its language servers. A request with
// no Origin is accepted: only a non-browser client omits it, and that client can set the
// session cookie by hand anyway.
func AcceptWS(w http.ResponseWriter, r *http.Request, logTag string) (conn *websocket.Conn, ok bool) {
	if origin := r.Header.Get("Origin"); origin != "" && !sameOriginHost(origin, r.Host) {
		// The operator needs to see the mismatch, not just that something refused.
		logging.For(logging.HTTP).Warnf("websocket handshake at %s refused: Origin %q is not the request host %q",
			logTag, origin, r.Host)
		http.Error(w, "cross-origin websocket request rejected", http.StatusForbidden)
		return nil, false
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionContextTakeover,
	})
	if err != nil {
		logging.For(logging.HTTP).Warnf("websocket accept error at %s: %v", logTag, err)
		return nil, false
	}
	c.SetReadLimit(ReadLimit)
	return c, true
}

// sameOriginHost reports whether an Origin header names the host the request arrived at. The
// scheme is ignored, the comparison is case-insensitive, and a port counts: "localhost" and
// "localhost:3000" are different origins.
func sameOriginHost(origin, host string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false // a malformed Origin is not a same-origin one
	}
	if strings.EqualFold(u.Host, host) {
		return true
	}
	oHost, oPort := splitHostPort(u.Host)
	rHost, rPort := splitHostPort(host)
	if !strings.EqualFold(oHost, rHost) || oHost == "" {
		return false
	}
	// An address with no port is the scheme's default; both sides are compared as written.
	return oPort == rPort
}

// splitHostPort keeps the port text as written; an address without one yields "".
func splitHostPort(hostport string) (host, port string) {
	h, p, err := net.SplitHostPort(hostport)
	if err != nil {
		return hostport, ""
	}
	return h, p
}

// WriteTimeout bounds the delivery of one frame. coder/websocket takes a write deadline only
// from the context it is handed, so a write without one waits forever on a peer that stopped
// reading and every writer behind it waits too; an expired deadline also closes the
// connection, so the error means "this peer is gone".
const WriteTimeout = 10 * time.Second

// WriteText writes one text frame under a deadline built from ctx and timeout.
func WriteText(ctx context.Context, conn *websocket.Conn, frame []byte, timeout time.Duration) error {
	writeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return conn.Write(writeCtx, websocket.MessageText, frame)
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

// Keepalive pings conn until ctx is done, in its own goroutine. A failed ping calls onDead
// and stops; callers pass the cancel of the context their read loop is blocked on, because
// Read never returns for a peer that merely stopped talking. writeMu holds off the caller's
// own frames while pinging (nil if nothing else writes). The caller must also be reading conn:
// only Read() delivers a pong, so with no reader a live peer looks dead.
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
