package main

import "testing"

func TestListenAddr(t *testing.T) {
	tests := []struct {
		name string
		bind string
		port int
		want string
	}{
		{name: "empty binds all interfaces", bind: "", port: 3000, want: ":3000"},
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
