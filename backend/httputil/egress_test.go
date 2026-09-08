package httputil

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// TestPublicAddress is the egress rule as a table: the shapes a manifest could name.
func TestPublicAddress(t *testing.T) {
	for _, tc := range []struct {
		ip string
		ok bool
	}{
		{"93.184.216.34", true}, // public
		{"2606:2800:220:1:248:1893:25c8:1942", true},
		{"127.0.0.1", false}, // loopback
		{"::1", false},
		{"169.254.169.254", false}, // cloud metadata
		{"fe80::1", false},         // link-local
		{"0.0.0.0", false},         // unspecified
		{"::", false},
		{"224.0.0.5", false}, // multicast
		{"10.0.0.1", true},   // RFC1918 stays allowed: a LAN service is a real target
		{"192.168.1.10", true},
	} {
		ip, err := netip.ParseAddr(tc.ip)
		if err != nil {
			t.Fatalf("bad address in table: %s", tc.ip)
		}
		if got := publicAddress(ip); got != tc.ok {
			t.Errorf("publicAddress(%s) = %v, want %v", tc.ip, got, tc.ok)
		}
	}
}

// TestEgressClientRefusesLoopbackTarget: the judgment is on the address, not the name.
func TestEgressClientRefusesLoopbackTarget(t *testing.T) {
	var reached bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
	}))
	defer upstream.Close()

	resp, err := NewExtensionEgressClient(5 * time.Second).Get(upstream.URL)
	if err == nil {
		resp.Body.Close()
		t.Fatalf("egress reached a loopback service: %s", upstream.URL)
	}
	if reached {
		t.Fatal("the request reached the loopback listener despite the refusal")
	}
	if !errors.Is(err, ErrInternalAddress) {
		t.Errorf("the refusal should name the rule, got: %v", err)
	}
}

// TestReadUpstreamCapsBody: a body past the cap is refused, not truncated.
func TestReadUpstreamCapsBody(t *testing.T) {
	if _, err := ReadUpstream(strings.NewReader(strings.Repeat("x", 1<<20))); err != nil {
		t.Fatalf("an ordinary body was refused: %v", err)
	}

	over := strings.NewReader(strings.Repeat("x", int(MaxUpstreamBodyBytes)+1))
	if _, err := ReadUpstream(over); err == nil {
		t.Fatal("a body past the cap was read into memory")
	} else if !errors.Is(err, ErrUpstreamTooLarge) {
		t.Errorf("the refusal should name the cap, got: %v", err)
	}
}
