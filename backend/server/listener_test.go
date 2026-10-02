package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	stdtls "crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"mike/pkg/logging"
)

// The hybrid listener reads a connection's first byte to tell TLS from plain HTTP, and
// http.Server accepts serially. These tests cover the peers that must never reach it:
// one that connects and stays silent, and one that hangs up without sending a byte.

// hybridTestServer runs a trivial http.Server over a hybridListener on a loopback port,
// returning its address, the channel Serve's result lands on, and a teardown.
func hybridTestServer(t *testing.T, sniffTimeout time.Duration, stdCfg *stdtls.Config) (addr string, served chan error, stop func()) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	h := newHybridListener(ln, stdCfg, sniffTimeout)

	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, "ok")
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}

	served = make(chan error, 1)
	go func() { served <- srv.Serve(h) }()

	return ln.Addr().String(), served, func() { srv.Close() }
}

// silentPeer opens a TCP connection and sends nothing — a lost first packet, a
// cancelled preconnect, a health check or a port scan.
func silentPeer(t *testing.T, addr string) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// fetch does a plain HTTP GET and returns the body, failing on any error.
func fetch(t *testing.T, url string, timeout time.Duration, tlsCfg *stdtls.Config) (string, error) {
	t.Helper()
	client := &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{TLSClientConfig: tlsCfg},
	}
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// mustBeServed asserts a normal client is served promptly. The bound sits far below
// every sniff timeout used here, so a listener that waited on a stalled peer fails it.
func mustBeServed(t *testing.T, addr string, timeout time.Duration) {
	t.Helper()
	got, err := fetch(t, "http://"+addr+"/", timeout, nil)
	if err != nil {
		t.Fatalf("request while a peer was stalled: %v", err)
	}
	if got != "ok" {
		t.Fatalf("body = %q, want %q", got, "ok")
	}
}

// serveDied reports whether Serve has returned (i.e. the accept loop died).
func serveDied(served chan error) (error, bool) {
	select {
	case err := <-served:
		return err, true
	case <-time.After(300 * time.Millisecond):
		return nil, false
	}
}

// TestHybridListener_SilentPeerDoesNotBlockOtherClients: a connection that has not sent its first
// byte must not delay the next client.
func TestHybridListener_SilentPeerDoesNotBlockOtherClients(t *testing.T) {
	addr, served, stop := hybridTestServer(t, 5*time.Second, nil)
	defer stop()

	mustBeServed(t, addr, 2*time.Second) // sanity

	silent := silentPeer(t, addr)
	// Stays silent for the whole test: its 5s sniff deadline outlasts every request
	// bound below, so a listener that waits on it cannot pass.
	defer silent.Close()

	mustBeServed(t, addr, time.Second)

	// Many stalled peers at once: a fix that only caps concurrency is still caught.
	for i := 0; i < 12; i++ {
		other := silentPeer(t, addr)
		defer other.Close()
		mustBeServed(t, addr, time.Second)
	}

	if err, died := serveDied(served); died {
		t.Fatalf("Serve returned while still serving: %v", err)
	}
}

// TestHybridListener_ZeroBytePeerDoesNotEndServe: a peer that hangs up without sending a
// byte arrives as EOF or ECONNRESET, which net/http reads as a dead listener — Serve
// must not end on it.
func TestHybridListener_ZeroBytePeerDoesNotEndServe(t *testing.T) {
	for _, mode := range []string{"eof", "reset"} {
		t.Run(mode, func(t *testing.T) {
			addr, served, stop := hybridTestServer(t, 5*time.Second, nil)
			defer stop()

			for i := 0; i < 3; i++ {
				c, err := net.DialTimeout("tcp", addr, 2*time.Second)
				if err != nil {
					t.Fatalf("dial: %v", err)
				}
				if mode == "reset" {
					// SO_LINGER with 0 timeout: close() sends RST instead of FIN.
					if tc, ok := c.(*net.TCPConn); ok {
						if err := tc.SetLinger(0); err != nil {
							t.Fatalf("set linger: %v", err)
						}
					}
				}
				if err := c.Close(); err != nil {
					t.Fatalf("close: %v", err)
				}
			}

			// Still up in both senses: Serve has not returned, and new connections
			// are served.
			if err, died := serveDied(served); died {
				t.Fatalf("Serve returned after a zero-byte peer (%s): %v", mode, err)
			}
			mustBeServed(t, addr, 2*time.Second)
		})
	}
}

// TestHybridListener_SniffDeadlineDropsStalledPeer: past the timeout the stalled peer
// is closed and the following request is served.
func TestHybridListener_SniffDeadlineDropsStalledPeer(t *testing.T) {
	addr, served, stop := hybridTestServer(t, 200*time.Millisecond, nil)
	defer stop()

	silent := silentPeer(t, addr)

	// The server must close it on its own: reading returns EOF.
	if err := silent.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	buf := make([]byte, 1)
	if _, err := silent.Read(buf); !errors.Is(err, io.EOF) {
		t.Fatalf("read after sniff timeout = %v, want EOF (peer was not dropped)", err)
	}

	if err, died := serveDied(served); died {
		t.Fatalf("Serve returned after dropping a stalled peer: %v", err)
	}
	mustBeServed(t, addr, 2*time.Second)
}

// TestHybridListener_ProtocolSniff checks the sniffed byte still reaches the protocol
// reader: net/http reads its request line from that byte, a TLS server its ClientHello
// from the 0x16 record type.
func TestHybridListener_ProtocolSniff(t *testing.T) {
	t.Run("plain http", func(t *testing.T) {
		addr, _, stop := hybridTestServer(t, 5*time.Second, nil)
		defer stop()
		mustBeServed(t, addr, 2*time.Second)
	})

	t.Run("tls", func(t *testing.T) {
		cert := selfSignedCert(t)
		addr, _, stop := hybridTestServer(t, 5*time.Second, &stdtls.Config{Certificates: []stdtls.Certificate{cert}})
		defer stop()

		got, err := fetch(t, "https://"+addr+"/", 3*time.Second, &stdtls.Config{InsecureSkipVerify: true})
		if err != nil {
			t.Fatalf("https request through the sniff: %v", err)
		}
		if got != "ok" {
			t.Fatalf("body = %q, want %q", got, "ok")
		}
	})
}

// TestHybridListener_ListenerCloseEndsServe: closing the listener must end Serve — it
// is the one error allowed to propagate — even with a stalled peer in flight, and
// without waiting for that peer's sniff to expire.
func TestHybridListener_ListenerCloseEndsServe(t *testing.T) {
	addr, served, stop := hybridTestServer(t, 10*time.Second, nil)

	silentPeer(t, addr) // occupy a sniff when the listener goes away

	stopped := make(chan struct{})
	go func() {
		stop()
		close(stopped)
	}()

	select {
	case err := <-served:
		if !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("Serve returned %v, want http.ErrServerClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after the listener was closed")
	}
	<-stopped
}

// TestSniffDropLogger_RateLimitsPerHost keeps a scan from burying the log while still
// reporting each new host once.
func TestSniffDropLogger_RateLimitsPerHost(t *testing.T) {
	buf, restore := logging.TestSink()
	defer restore()

	l := &sniffDropLogger{}
	cause := errors.New("i/o timeout")

	l.note(&net.TCPAddr{IP: net.ParseIP("203.0.113.9"), Port: 40000}, cause)
	l.note(&net.TCPAddr{IP: net.ParseIP("203.0.113.9"), Port: 40001}, cause) // same host, new port
	l.note(&net.TCPAddr{IP: net.ParseIP("198.51.100.7"), Port: 40002}, cause)

	logs := buf.String()
	if n := strings.Count(logs, "203.0.113.9"); n != 1 {
		t.Fatalf("host 203.0.113.9 reported %d times, want 1:\n%s", n, logs)
	}
	if !strings.Contains(logs, "198.51.100.7") {
		t.Fatalf("a different host was suppressed:\n%s", logs)
	}
}

// selfSignedCert mints a throwaway loopback certificate so the TLS branch can be
// exercised with a real handshake.
func selfSignedCert(t *testing.T) stdtls.Certificate {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{Organization: []string{"MIKE test"}, CommonName: "localhost"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	cert, err := stdtls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("key pair: %v", err)
	}
	return cert
}
