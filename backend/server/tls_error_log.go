package server

import (
	"log"
	"net"
	"strings"
	"sync"

	"kanban/pkg/logging"
)

// net/http logs its own errors through Server.ErrorLog; with none it writes to the standard
// logger. One of them is pure noise here:
//
//	http: TLS handshake error from 192.168.1.190:61946: remote error: tls: unknown certificate authority
//
// It comes from the client, which could not chain our certificate to a root in its own trust store
// (never imported the local CA, or holds an older copy), and such a device retries every request, so
// one phone buries the log. httpErrorLog logs a certificate-trust rejection once per remote host —
// with the actionable hint — and drops the rest. Everything else passes through, including errors
// that do point at the server: an expired leaf (never renewed on expiry, by design) or no usable
// cipher ("remote error: tls: handshake failure").
func httpErrorLog(caFile string) *log.Logger {
	return log.New(&tlsTrustNoiseFilter{caFile: caFile, seen: map[string]bool{}}, "", 0)
}

// tlsHandshakeErrorPrefix is the message net/http builds in conn.serve:
// "http: TLS handshake error from %s: %v".
const tlsHandshakeErrorPrefix = "http: TLS handshake error from "

// clientTrustAlerts are the alerts a peer sends when it rejects our certificate
// on trust grounds. Every one of them means "this device has not imported the
// local CA", never a server fault. Deliberately absent: expired/revoked
// certificate, handshake failure, decrypt error — those can be real problems.
var clientTrustAlerts = map[string]bool{
	"remote error: tls: unknown certificate authority": true, // alert 48 — Chrome, curl
	"remote error: tls: unknown certificate":           true, // alert 46
	"remote error: tls: bad certificate":               true, // alert 42 — Safari, Firefox
}

// classifyTrustFailure reports whether msg is a net/http TLS handshake error
// caused by the client not trusting our certificate, and returns the remote
// host (without its ephemeral port, which changes every connection) plus the
// alert text.
func classifyTrustFailure(msg string) (host, alert string, ok bool) {
	rest, found := strings.CutPrefix(msg, tlsHandshakeErrorPrefix)
	if !found {
		return "", "", false
	}
	// The address is the only part before the first ": " — an IPv6 address is
	// rendered "[::1]:1234", so it never contains ": " itself.
	addr, reason, found := strings.Cut(rest, ": ")
	if !found || !clientTrustAlerts[reason] {
		return "", "", false
	}
	if h, _, err := net.SplitHostPort(addr); err == nil {
		addr = h
	}
	return addr, reason, true
}

// maxTrustReports caps how many remote hosts are remembered per process, so a
// server bound to a public interface can't be pushed into growing the set
// without bound. Past the cap the messages are still suppressed, only the hint
// stops being repeated for new hosts.
const maxTrustReports = 256

// tlsTrustNoiseFilter is the io.Writer behind net/http's error logger.
type tlsTrustNoiseFilter struct {
	caFile string

	mu   sync.Mutex
	seen map[string]bool
}

// Write implements io.Writer. Non-noise keeps the standard logger's format,
// which is what net/http would have used with no ErrorLog at all.
func (f *tlsTrustNoiseFilter) Write(p []byte) (int, error) {
	msg := strings.TrimSuffix(string(p), "\n")

	host, reason, noise := classifyTrustFailure(msg)
	if !noise {
		logging.For(logging.HTTP).Info(msg)
		return len(p), nil
	}

	if f.firstSeen(host) {
		hint := "the client does not trust this certificate"
		if f.caFile != "" {
			hint = "the client does not trust this certificate; import " + f.caFile + " on that device"
		}
		logging.For(logging.HTTP).Infof("TLS: %s from %s (%s) — further such handshake errors from this host are suppressed", strings.TrimPrefix(reason, "remote error: tls: "), host, hint)
	}
	return len(p), nil
}

// firstSeen reports whether this is the first filtered handshake from host.
func (f *tlsTrustNoiseFilter) firstSeen(host string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.seen[host] || len(f.seen) >= maxTrustReports {
		return false
	}
	f.seen[host] = true
	return true
}
