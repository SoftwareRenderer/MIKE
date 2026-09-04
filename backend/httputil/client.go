package httputil

import (
	"net"
	"net/http"
	"time"
)

// Outbound HTTP goes through this file so the redirect policy is decided once. Two clients, named
// for the property that differs, sharing one connection pool.

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

// NewSingleAttemptClient refuses redirects instead of following them. Use it for any URL an
// extension or a request influenced.
//
// The domain allowlist is checked against the URL the caller asked for, so following a redirect
// would send the request, and any credential on it, to a host nothing validated. The 3xx is
// returned to the caller to report.
func NewSingleAttemptClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: sharedTransport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}
