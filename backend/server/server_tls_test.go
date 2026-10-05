package server

import (
	"net"
	"strings"
	"testing"

	miketls "mike/backend/tls"
	"mike/pkg/logging"
)

// A pair that will not load must end Serve, not leave a server that binds the port and then fails
// every handshake it is offered.
func TestServeRefusesAnUnloadableCertificatePair(t *testing.T) {
	one, err := miketls.LoadWithCert(t.TempDir(), "", "")
	if err != nil {
		t.Fatalf("first pair: %v", err)
	}
	two, err := miketls.LoadWithCert(t.TempDir(), "", "")
	if err != nil {
		t.Fatalf("second pair: %v", err)
	}

	// The address stays taken, so a bind that happens before the check reads as "listen:" rather than
	// silence. No other Server field is set, so reaching the banner or the router would panic.
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()

	buf, restore := logging.TestSink()
	defer restore()

	s := &Server{
		tlsConfig: &miketls.Config{CertFile: one.CertFile, KeyFile: two.KeyFile},
		addr:      held.Addr().String(),
	}

	err = s.Serve()
	if err == nil {
		t.Fatal("Serve accepted a certificate pair that cannot complete a handshake")
	}
	if strings.Contains(err.Error(), "listen:") {
		t.Errorf("Serve bound a port before checking the pair: %v", err)
	}
	if logged := buf.String(); strings.Contains(logged, "PAIRING CODE") || strings.Contains(logged, "Server starting") {
		t.Errorf("Serve announced a server it cannot run: %s", logged)
	}
}
