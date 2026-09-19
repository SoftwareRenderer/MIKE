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

	// KeepaliveFrame is the app-level ping WithAppFrame sends beside the control ping. Control
	// frames are invisible to JavaScript, so they never fire onmessage and never reset the
	// browser's own stale-connection timer. Must stay valid JSON.
	KeepaliveFrame = `{"type":"ping"}`
)

// KeepaliveOption changes one aspect of the loop; see WithAppFrame.
type KeepaliveOption func(*keepaliveOptions)

type keepaliveOptions struct {
	appFrame bool
}

// WithAppFrame sends KeepaliveFrame after each control ping, for the endpoint whose client measures
// silence from onmessage. The LSP socket speaks JSON-RPC and the terminal its own message type, so
// both stay on the control ping alone: a frame neither declared is a foreign message there.
func WithAppFrame() KeepaliveOption {
	return func(o *keepaliveOptions) { o.appFrame = true }
}

// Keepalive pings conn until ctx is done, in its own goroutine. A failed ping calls onDead and
// stops; callers pass the cancel of the context their read loop is blocked on, because Read never
// returns for a peer that merely stopped talking, and the caller must be reading conn because only
// Read() delivers a pong.
//
// writeMu holds off the caller's own frames while pinging (nil if nothing else writes). It is taken
// with TryLock and the ping's deadline built after it, because a lock another writer holds is a
// frame on its way out — liveness evidence. Queuing instead lets KeepaliveTimeout expire in line,
// and the library closes the socket for a spent write context that wrote nothing.
func Keepalive(ctx context.Context, conn *websocket.Conn, writeMu *sync.Mutex, onDead func(), opts ...KeepaliveOption) {
	keepalive(ctx, conn, KeepaliveInterval, KeepaliveTimeout, writeMu, onDead, opts...)
}

// keepalive is Keepalive with the timings injected, so tests need not wait 27 s.
func keepalive(ctx context.Context, conn *websocket.Conn, interval, timeout time.Duration, writeMu *sync.Mutex, onDead func(), opts ...KeepaliveOption) {
	var options keepaliveOptions
	for _, opt := range opts {
		opt(&options)
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if writeMu != nil && !writeMu.TryLock() {
					continue
				}
				pingCtx, pingCancel := context.WithTimeout(ctx, timeout)
				err := conn.Ping(pingCtx)
				pingCancel()
				if err == nil && options.appFrame {
					_ = WriteText(ctx, conn, []byte(KeepaliveFrame), WriteTimeout)
				}
				if writeMu != nil {
					writeMu.Unlock()
				}
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
