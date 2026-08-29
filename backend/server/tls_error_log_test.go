package server

import (
	"fmt"
	"log"
	"strings"
	"testing"
)

// captureLog redirects the standard logger for the duration of the test and
// returns a reader of everything written to it.
func captureLog(t *testing.T) *strings.Builder {
	t.Helper()
	var buf strings.Builder
	w, flags := log.Writer(), log.Flags()
	log.SetFlags(0)
	log.SetOutput(&buf)
	t.Cleanup(func() {
		log.SetOutput(w)
		log.SetFlags(flags)
	})
	return &buf
}

func TestClassifyTrustFailure(t *testing.T) {
	tests := []struct {
		name      string
		msg       string
		wantHost  string
		wantAlert string
		wantOK    bool
	}{
		{
			name:      "untrusted local CA (the reported noise)",
			msg:       "http: TLS handshake error from 192.168.1.190:61946: remote error: tls: unknown certificate authority",
			wantHost:  "192.168.1.190",
			wantAlert: "remote error: tls: unknown certificate authority",
			wantOK:    true,
		},
		{
			name:      "bad certificate (Safari/Firefox)",
			msg:       "http: TLS handshake error from 10.0.0.5:52341: remote error: tls: bad certificate",
			wantHost:  "10.0.0.5",
			wantAlert: "remote error: tls: bad certificate",
			wantOK:    true,
		},
		{
			name:      "unknown certificate",
			msg:       "http: TLS handshake error from [::1]:41000: remote error: tls: unknown certificate",
			wantHost:  "::1",
			wantAlert: "remote error: tls: unknown certificate",
			wantOK:    true,
		},
		{
			name:   "expired leaf stays visible",
			msg:    "http: TLS handshake error from 192.168.1.190:61946: remote error: tls: expired certificate",
			wantOK: false,
		},
		{
			name:   "no shared cipher stays visible",
			msg:    "http: TLS handshake error from 192.168.1.190:61946: remote error: tls: handshake failure",
			wantOK: false,
		},
		{
			name:   "server-side certificate problem stays visible",
			msg:    "http: TLS handshake error from 192.168.1.190:61946: tls: failed to find any PEM data in certificate input",
			wantOK: false,
		},
		{
			name:   "plaintext to the TLS port stays visible",
			msg:    "http: TLS handshake error from 192.168.1.190:61946: client sent an HTTP request to an HTTPS server",
			wantOK: false,
		},
		{
			name:   "unrelated http error",
			msg:    "http: superfluous response.WriteHeader call from x",
			wantOK: false,
		},
		{
			name:   "unrelated log line",
			msg:    "TLS: using existing certificate (/data/tls/mike.crt)",
			wantOK: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			host, alert, ok := classifyTrustFailure(tc.msg)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (host=%q alert=%q)", ok, tc.wantOK, host, alert)
			}
			if !tc.wantOK {
				return
			}
			if host != tc.wantHost {
				t.Errorf("host = %q, want %q", host, tc.wantHost)
			}
			if alert != tc.wantAlert {
				t.Errorf("alert = %q, want %q", alert, tc.wantAlert)
			}
		})
	}
}

// A device without the CA retries every request; only the first failure per
// host may be logged, and it must carry the hint (the CA path).
func TestTLSTrustNoiseFilterReportsOncePerHost(t *testing.T) {
	buf := captureLog(t)
	f := &tlsTrustNoiseFilter{caFile: "/data/tls/mike-ca.crt", seen: map[string]bool{}}

	noise := func(port int) string {
		return fmt.Sprintf("http: TLS handshake error from 192.168.1.190:%d: remote error: tls: unknown certificate authority", port)
	}
	for _, p := range []int{61946, 61947, 61948} {
		if _, err := f.Write([]byte(noise(p) + "\n")); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	// A second host gets its own single report.
	if _, err := f.Write([]byte("http: TLS handshake error from 192.168.1.7:40000: remote error: tls: unknown certificate authority\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d log lines, want 2:\n%s", len(lines), buf.String())
	}
	for i, want := range []string{"192.168.1.190", "192.168.1.7"} {
		if !strings.Contains(lines[i], want) {
			t.Errorf("line %d = %q, want it to mention %s", i, lines[i], want)
		}
		if !strings.Contains(lines[i], "/data/tls/mike-ca.crt") {
			t.Errorf("line %d = %q, want it to name the CA file to import", i, lines[i])
		}
		if !strings.Contains(lines[i], "suppressed") {
			t.Errorf("line %d = %q, want it to say further errors are suppressed", i, lines[i])
		}
	}
}

// Everything that is not client-side certificate noise must reach the standard
// logger byte-for-byte, including multi-line messages (panic traces).
func TestTLSTrustNoiseFilterPassesThrough(t *testing.T) {
	buf := captureLog(t)
	f := &tlsTrustNoiseFilter{caFile: "/data/tls/mike-ca.crt", seen: map[string]bool{}}

	msgs := []string{
		"http: TLS handshake error from 192.168.1.190:61946: remote error: tls: expired certificate",
		"http: TLS handshake error from 192.168.1.190:61946: client sent an HTTP request to an HTTPS server",
		"http: Accept error: accept tcp :::3000: too many open files; retrying in 5ms",
		"http: panic serving 1.2.3.4:5: boom\nmain.f()\n\t/app/main.go:1",
	}
	for _, msg := range msgs {
		if _, err := f.Write([]byte(msg + "\n")); err != nil {
			t.Fatalf("Write: %v", err)
		}
		if got := buf.String(); got != msg+"\n" {
			t.Errorf("after %q\ngot:\n%s\nwant:\n%s", msg, got, msg+"\n")
		}
		buf.Reset()
	}
}

// The remembered-host set is bounded; past the cap noise is still suppressed,
// it just stops producing hints for new hosts.
func TestTLSTrustNoiseFilterCapsReports(t *testing.T) {
	buf := captureLog(t)
	f := &tlsTrustNoiseFilter{seen: map[string]bool{}}

	for i := 0; i <= maxTrustReports; i++ {
		msg := fmt.Sprintf("http: TLS handshake error from 10.0.%d.%d:1234: remote error: tls: unknown certificate authority", i/256, i%256)
		if _, err := f.Write([]byte(msg + "\n")); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if got := strings.Count(buf.String(), "\n"); got != maxTrustReports {
		t.Fatalf("logged %d lines, want %d", got, maxTrustReports)
	}
	if len(f.seen) != maxTrustReports {
		t.Errorf("remembered %d hosts, want %d", len(f.seen), maxTrustReports)
	}
}
