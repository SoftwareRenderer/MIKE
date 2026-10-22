package types

import (
	"net"
	"strings"
)

// CanonicalHost puts a host in the form two addresses are compared in: trimmed, lower-cased, without
// its port, and without the trailing dot a DNS name may carry.
func CanonicalHost(raw string) string {
	h := strings.TrimSpace(raw)
	// A bracketed IPv6 address keeps its colons, and its port follows the closing bracket.
	if strings.HasPrefix(h, "[") {
		if end := strings.IndexByte(h, ']'); end > 0 {
			h = h[1:end]
		}
	} else if i := strings.IndexByte(h, ':'); i >= 0 && !strings.Contains(h[i+1:], ":") {
		// One colon separates a port. Two or more mean a bare IPv6 address, which has no port.
		h = h[:i]
	}
	return strings.TrimSuffix(strings.ToLower(h), ".")
}

// SameHost reports whether two hosts name the same machine. Hostnames that CanonicalHost folds are
// equal, and so are any two loopback names, because one machine is one holder of a saved credential.
func SameHost(a, b string) bool {
	ha, hb := CanonicalHost(a), CanonicalHost(b)
	if ha == "" || hb == "" {
		return false
	}
	return ha == hb || (IsLoopbackHost(ha) && IsLoopbackHost(hb))
}

// IsLoopbackHost reports whether a host is this machine: the name every operating system resolves to
// it, or an address in a loopback range. Several paths decide from a URL whether plain HTTP may reach it.
func IsLoopbackHost(raw string) bool {
	canonical := CanonicalHost(raw)
	if canonical == "localhost" {
		return true
	}
	ip := net.ParseIP(canonical)
	return ip != nil && ip.IsLoopback()
}
