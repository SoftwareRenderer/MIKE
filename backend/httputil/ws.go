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

// ReadLimit is the maximum size of a single frame from a client. If exceeded,
// the connection is closed.
const ReadLimit = 1 << 20

// AcceptWS upgrades an HTTP connection to a WebSocket using shared settings.
// logTag is used for logging failures. Returns ok=false if the upgrade failed.
//
// The Origin header is checked to prevent cross-site WebSocket hijacking. Requests
// with no Origin are accepted, as they typically come from non-browser clients.
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

// sameOriginHost checks if the Origin header matches the request host. The scheme
// is ignored, and ports are considered part of the origin.
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

// WriteTimeout is the maximum time allowed to deliver a single frame. An
// expired deadline closes the connection.
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

	// KeepaliveTimeout is the maximum time to wait for a pong before considering
	// the peer dead.
	KeepaliveTimeout = 10 * time.Second

	// KeepaliveFrame is a JSON payload sent as an application-level ping to
	// ensure the browser's connection timer is reset.
	KeepaliveFrame = `{"type":"ping"}`
)

// KeepaliveOption changes one aspect of the loop; see WithAppFrame.
type KeepaliveOption func(*keepaliveOptions)

type keepaliveOptions struct {
	appFrame bool
}

// WithAppFrame sends an application-level ping (KeepaliveFrame) after each
// control ping.
func WithAppFrame() KeepaliveOption {
	return func(o *keepaliveOptions) { o.appFrame = true }
}

// Keepalive pings the connection in a separate goroutine. If a ping fails,
// onDead is called and the loop stops. The caller must be reading from the
// connection to process pongs.
//
// If writeMu is provided, it is used to prevent concurrent writes. TryLock is
// used to avoid queuing pings behind slow writes.
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
