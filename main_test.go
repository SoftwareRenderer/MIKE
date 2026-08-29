package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestListenAddr(t *testing.T) {
	tests := []struct {
		name   string
		listen string
		port   int
		want   string
	}{
		// Empty is the default, and the default is loopback — never all interfaces.
		{name: "empty falls back to loopback", listen: "", port: 3000, want: "127.0.0.1:3000"},
		// Explicit any-address stays the all-interfaces escape hatch.
		{name: "explicit IPv4 any", listen: "0.0.0.0", port: 3000, want: ":3000"},
		{name: "explicit IPv6 any", listen: "::", port: 3000, want: ":3000"},
		{name: "localhost IPv4", listen: "127.0.0.1", port: 8080, want: "127.0.0.1:8080"},
		{name: "localhost IPv6 brackets", listen: "::1", port: 3000, want: "[::1]:3000"},
		{name: "hostname", listen: "localhost", port: 9090, want: "localhost:9090"},
		{name: "LAN IP", listen: "192.168.1.50", port: 3000, want: "192.168.1.50:3000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := listenAddr(tt.listen, tt.port); got != tt.want {
				t.Errorf("listenAddr(%q, %d) = %q, want %q", tt.listen, tt.port, got, tt.want)
			}
		})
	}
}

func TestIsLoopbackAddr(t *testing.T) {
	tests := []struct {
		listen string
		want   bool
	}{
		{listen: "", want: true},              // default = loopback
		{listen: "127.0.0.1", want: true},     //
		{listen: "127.0.0.53", want: true},    // whole 127/8 is loopback
		{listen: "::1", want: true},           //
		{listen: "[::1]", want: true},         // bracketed form
		{listen: "localhost", want: true},     //
		{listen: "0.0.0.0", want: false},      // explicit all interfaces
		{listen: "::", want: false},           //
		{listen: "192.168.1.50", want: false}, // LAN
		{listen: "10.0.0.5", want: false},     //
		{listen: "0.1.2.3", want: false},      // routable → treated as exposed
		{listen: "example.com", want: false},  //
	}
	for _, tt := range tests {
		if got := isLoopbackAddr(tt.listen); got != tt.want {
			t.Errorf("isLoopbackAddr(%q) = %v, want %v", tt.listen, got, tt.want)
		}
	}
}

func TestRefusesWeakSecret(t *testing.T) {
	tests := []struct {
		name     string
		listen   string
		password string
		want     bool
	}{
		// Loopback keeps the dev flow working, weak secret or not.
		{name: "loopback default + weak", listen: "", password: "test", want: false},
		{name: "loopback explicit + weak", listen: "127.0.0.1", password: "dev", want: false},
		{name: "loopback + empty", listen: "localhost", password: "", want: false},
		// Exposed listen addresses refuse weak secrets.
		{name: "all interfaces + short", listen: "0.0.0.0", password: "abc", want: true},
		{name: "all interfaces + empty", listen: "0.0.0.0", password: "", want: true},
		{name: "all interfaces + known default", listen: "0.0.0.0", password: "test", want: true},
		{name: "IPv6 any + known default", listen: "::", password: "Password", want: true},
		{name: "LAN ip + known default", listen: "192.168.1.50", password: "changeme", want: true},
		{name: "LAN ip + known default uppercase", listen: "192.168.1.50", password: "ADMIN", want: true},
		{name: "LAN ip + known default padded", listen: "192.168.1.50", password: "  pass  ", want: true},
		// Long enough and not a known default → accepted.
		{name: "all interfaces + strong", listen: "0.0.0.0", password: "correct horse battery", want: false},
		{name: "all interfaces + long default-ish", listen: "0.0.0.0", password: "test-password-1234", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := refusesWeakSecret(tt.listen, tt.password); got != tt.want {
				t.Errorf("refusesWeakSecret(%q, %q) = %v, want %v", tt.listen, tt.password, got, tt.want)
			}
		})
	}
}

// TestOpenTraceFileIsOwnerOnly covers L8: the flight recorder is always-on and
// holds ~30s of runtime internals, so both the initial file and the rotated
// replacement must be created 0600.
func TestOpenTraceFileIsOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kanban.trace")

	f, err := openTraceFile(path)
	if err != nil {
		t.Fatalf("openTraceFile: %v", err)
	}
	// Rotate exactly like the 30s ticker does: truncate the same path again.
	f.Close()
	f2, err := openTraceFile(path)
	if err != nil {
		t.Fatalf("openTraceFile (rotate): %v", err)
	}
	defer f2.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("trace file mode = %#o, want 0600", perm)
	}
}
