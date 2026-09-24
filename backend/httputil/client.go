package httputil

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// Outbound HTTP goes through this file so the redirect policy is decided once. Three clients, named
// for the property that differs. The first two share one connection pool; the streaming one keeps
// its own, so its keepalive floor does not reach extension egress or update checks.

// sharedTransport is the pool behind every client here. The numbers are the stdlib defaults,
// written out because the DialContext is not one of them: without a dial timeout, connecting to a
// host that accepts nothing waits on the OS TCP timeout, which is minutes.
var sharedTransport = &http.Transport{
	Proxy: http.ProxyFromEnvironment,
	DialContext: (&net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext,
	ForceAttemptHTTP2:     true,
	MaxIdleConns:          100,
	IdleConnTimeout:       90 * time.Second,
	TLSHandshakeTimeout:   10 * time.Second,
	ExpectContinueTimeout: 1 * time.Second,
}

// NewClient follows redirects. Use it for a URL the server itself chose, such as a configured
// provider endpoint. A zero timeout means no overall deadline, which is what a streaming LLM
// response needs; say so at the call site when you pass 0.
func NewClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: sharedTransport}
}

// streamTransport differs from sharedTransport in one way: how fast a peer that stopped existing
// shows up. sharedTransport inherits the OS keepalive defaults (75s between probes, 9 of them here),
// which notice a vanished host in ~12 minutes — invisible to an ordinary request, but the only thing
// that will ever end a stream whose response is deliberately unbounded in time.
var streamTransport = func() *http.Transport {
	t := sharedTransport.Clone()
	t.DialContext = (&net.Dialer{
		Timeout: 30 * time.Second,
		KeepAliveConfig: net.KeepAliveConfig{
			Enable:   true,
			Idle:     30 * time.Second,
			Interval: 15 * time.Second,
			Count:    3,
		},
	}).DialContext
	return t
}()

// NewStreamClient is for a response that stays open for minutes: no wall-clock timeout, because a
// completion is finished by its last token and not by the clock. The keepalive floor underneath is
// for a host that is GONE - it is not a test of whether a stream is alive. A provider that is up and
// saying nothing for a minute is normal; see llama.ErrStreamSilent for what silence actually means.
func NewStreamClient() *http.Client {
	return &http.Client{Transport: streamTransport}
}

// refuseRedirects is the single-attempt rule every client here carries: the domain allowlist is
// checked against the URL the caller asked for, so following a redirect would send the request, and
// any credential on it, to a host nothing validated. The 3xx is returned to the caller to report.
var refuseRedirects = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// NewSingleAttemptClient refuses redirects instead of following them. Use it for any URL an
// extension or a request influenced.
func NewSingleAttemptClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:       timeout,
		Transport:     sharedTransport,
		CheckRedirect: refuseRedirects,
	}
}

// egressTransport is sharedTransport with an address-judging dialer, on its own Transport so
// its connections stay out of the shared idle pool.
var egressTransport = func() *http.Transport {
	t := sharedTransport.Clone()
	t.DialContext = dialPublicOnly
	t.Proxy = nil
	return t
}()

// ErrInternalAddress says an egress target resolved to a non-public address.
var ErrInternalAddress = errors.New("target resolves to a non-public address")

// NewExtensionEgressClient is NewSingleAttemptClient plus the address rule in publicAddress,
// for egress an extension asked for rather than the operator: an allowlisted name can resolve
// to this host. RFC1918 stays allowed — a LAN service is a real extension target.
func NewExtensionEgressClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:       timeout,
		Transport:     egressTransport,
		CheckRedirect: refuseRedirects,
	}
}

// publicAddress: IsGlobalUnicast excludes loopback, link-local (the cloud metadata address),
// interface-local and multicast; unspecified needs its own clause because "dial 0.0.0.0" is a
// local dial.
func publicAddress(ip netip.Addr) bool {
	return ip.IsGlobalUnicast() && !ip.IsUnspecified()
}

// FetchSchemeAllowed reports whether a URL the operator named may be fetched at all: HTTPS always, plain
// HTTP only when the host is loopback. It guards the URLs whose bytes decide what runs next.
func FetchSchemeAllowed(u *url.URL) bool {
	switch u.Scheme {
	case "https":
		return true
	case "http":
		host := u.Hostname()
		if strings.EqualFold(host, "localhost") {
			return true
		}
		ip := net.ParseIP(host)
		return ip != nil && ip.IsLoopback()
	default:
		return false
	}
}

func dialPublicOnly(ctx context.Context, network, addr string) (net.Conn, error) {
	return dialAddresses(ctx, network, addr, func(_ string, ip netip.Addr) bool { return publicAddress(ip) })
}

// The resolved address is judged rather than the name: an allowlisted name can resolve to this host, and a
// name can be re-pointed after it was checked.
func dialAddresses(ctx context.Context, network, addr string, admit func(host string, ip netip.Addr) bool) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	dial := sharedTransport.DialContext
	var last = ErrInternalAddress
	for _, ip := range ips {
		if !admit(host, ip) {
			last = fmt.Errorf("%w: %s", ErrInternalAddress, ip)
			continue
		}
		conn, derr := dial(ctx, network, net.JoinHostPort(ip.String(), port))
		if derr == nil {
			return conn, nil
		}
		last = derr
	}
	return nil, last
}

// NewExtensionEgressClientGranting is NewExtensionEgressClient with loopback admitted for the hosts a
// person named: the target is an address they typed, not one an allowlisted name resolved to.
//
// Only loopback is admitted — the metadata address and every other non-public range stay refused even when
// named — and the grant is per call, so its transport is too, with keep-alives off.
func NewExtensionEgressClientGranting(timeout time.Duration, grantedHosts []string) *http.Client {
	if len(grantedHosts) == 0 {
		return NewExtensionEgressClient(timeout)
	}
	granted := make(map[string]bool, len(grantedHosts))
	for _, h := range grantedHosts {
		granted[strings.ToLower(h)] = true
	}
	transport := egressTransport.Clone()
	transport.DisableKeepAlives = true
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return dialAddresses(ctx, network, addr, func(host string, ip netip.Addr) bool {
			if publicAddress(ip) {
				return true
			}
			return ip.IsLoopback() && granted[strings.ToLower(host)]
		})
	}
	return &http.Client{
		Timeout:       timeout,
		Transport:     transport,
		CheckRedirect: refuseRedirects,
	}
}

// MaxUpstreamBodyBytes bounds an upstream body read into memory: it is somebody else's server.
const MaxUpstreamBodyBytes int64 = 8 << 20

// ErrUpstreamTooLarge says an upstream body went past MaxUpstreamBodyBytes.
var ErrUpstreamTooLarge = errors.New("upstream response too large")

// ReadUpstream reads an upstream body, refusing past the cap. Truncating would let a caller
// report "invalid response" instead of the truth.
func ReadUpstream(body io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, MaxUpstreamBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > MaxUpstreamBodyBytes {
		return nil, fmt.Errorf("%w: more than %d bytes", ErrUpstreamTooLarge, MaxUpstreamBodyBytes)
	}
	return data, nil
}
