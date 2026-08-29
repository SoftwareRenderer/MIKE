package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestListenAddr(t *testing.T) {
	tests := []struct {
		name string
		bind string
		port int
		want string
	}{
		// Empty is the default, and the default is loopback — never all interfaces.
		{name: "empty binds loopback", bind: "", port: 3000, want: "127.0.0.1:3000"},
		// Explicit any-address stays the all-interfaces escape hatch.
		{name: "explicit IPv4 any", bind: "0.0.0.0", port: 3000, want: ":3000"},
		{name: "explicit IPv6 any", bind: "::", port: 3000, want: ":3000"},
		{name: "localhost IPv4", bind: "127.0.0.1", port: 8080, want: "127.0.0.1:8080"},
		{name: "localhost IPv6 brackets", bind: "::1", port: 3000, want: "[::1]:3000"},
		{name: "hostname", bind: "localhost", port: 9090, want: "localhost:9090"},
		{name: "LAN IP", bind: "192.168.1.50", port: 3000, want: "192.168.1.50:3000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := listenAddr(tt.bind, tt.port); got != tt.want {
				t.Errorf("listenAddr(%q, %d) = %q, want %q", tt.bind, tt.port, got, tt.want)
			}
		})
	}
}

func TestIsLoopbackBind(t *testing.T) {
	tests := []struct {
		bind string
		want bool
	}{
		{bind: "", want: true},              // default = loopback
		{bind: "127.0.0.1", want: true},     //
		{bind: "127.0.0.53", want: true},    // whole 127/8 is loopback
		{bind: "::1", want: true},           //
		{bind: "[::1]", want: true},         // bracketed form
		{bind: "localhost", want: true},     //
		{bind: "0.0.0.0", want: false},      // explicit all interfaces
		{bind: "::", want: false},           //
		{bind: "192.168.1.50", want: false}, // LAN
		{bind: "10.0.0.5", want: false},     //
		{bind: "0.1.2.3", want: false},      // routable → treated as exposed
		{bind: "example.com", want: false},  //
	}
	for _, tt := range tests {
		if got := isLoopbackBind(tt.bind); got != tt.want {
			t.Errorf("isLoopbackBind(%q) = %v, want %v", tt.bind, got, tt.want)
		}
	}
}

func TestRefusesWeakSecret(t *testing.T) {
	tests := []struct {
		name     string
		bind     string
		password string
		want     bool
	}{
		// Loopback keeps the dev flow working, weak secret or not.
		{name: "loopback default + weak", bind: "", password: "test", want: false},
		{name: "loopback explicit + weak", bind: "127.0.0.1", password: "dev", want: false},
		{name: "loopback + empty", bind: "localhost", password: "", want: false},
		// Exposed binds refuse weak secrets.
		{name: "all interfaces + short", bind: "0.0.0.0", password: "abc", want: true},
		{name: "all interfaces + empty", bind: "0.0.0.0", password: "", want: true},
		{name: "all interfaces + known default", bind: "0.0.0.0", password: "test", want: true},
		{name: "IPv6 any + known default", bind: "::", password: "Password", want: true},
		{name: "LAN ip + known default", bind: "192.168.1.50", password: "changeme", want: true},
		{name: "LAN ip + known default uppercase", bind: "192.168.1.50", password: "ADMIN", want: true},
		{name: "LAN ip + known default padded", bind: "192.168.1.50", password: "  pass  ", want: true},
		// Long enough and not a known default → accepted.
		{name: "all interfaces + strong", bind: "0.0.0.0", password: "correct horse battery", want: false},
		{name: "all interfaces + long default-ish", bind: "0.0.0.0", password: "test-password-1234", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := refusesWeakSecret(tt.bind, tt.password); got != tt.want {
				t.Errorf("refusesWeakSecret(%q, %q) = %v, want %v", tt.bind, tt.password, got, tt.want)
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
