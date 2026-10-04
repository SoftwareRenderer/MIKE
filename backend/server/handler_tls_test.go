package server

import (
	"bytes"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"mike/backend/tls"
)

// getTLSCA asks for the download the way an unpaired device does, with no credential: the route needs
// none, which is what TestTLSCAIsPublicBeforeAnySession holds.
func getTLSCA(t *testing.T, s *Server) *httptest.ResponseRecorder {
	t.Helper()
	return testResponse(t, s, testRequest(http.MethodGet, "/api/tls/ca", nil))
}

func TestTLSCADownloadServesCertificate(t *testing.T) {
	s := testServer(t)

	rr := getTLSCA(t, s)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	if ct := rr.Header().Get("Content-Type"); ct != "application/x-x509-ca-cert" {
		t.Errorf("unexpected Content-Type %q", ct)
	}
	if cd := rr.Header().Get("Content-Disposition"); cd != `attachment; filename="mike-ca.crt"` {
		t.Errorf("unexpected Content-Disposition %q", cd)
	}

	body := rr.Body.Bytes()
	block, _ := pem.Decode(body)
	if block == nil || block.Type != "CERTIFICATE" {
		t.Fatalf("body is not a CERTIFICATE PEM block: %q", body)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("served body is not a parseable certificate: %v", err)
	}
	if !cert.IsCA {
		t.Error("served certificate is not a CA certificate")
	}

	// Must be the CA that signed the leaf, not something else from the data dir.
	onDisk, err := os.ReadFile(s.tlsConfig.CAFile)
	if err != nil {
		t.Fatal(err)
	}
	onDiskBlock, _ := pem.Decode(onDisk)
	if !bytes.Equal(block.Bytes, onDiskBlock.Bytes) {
		t.Error("served certificate is not the CA that signed the server certificate")
	}
	if bytes.Contains(body, []byte("PRIVATE KEY")) {
		t.Error("private key material leaked through the download route")
	}
}

// The device that needs the certificate is the one that has no session: a browser the connection will
// not run a passkey prompt in is sent this certificate before it can pair. A bogus cookie changes
// nothing, because a public path has no credential to check.
func TestTLSCAIsPublicBeforeAnySession(t *testing.T) {
	s := testServer(t)

	if rr := getTLSCA(t, s); rr.Code != http.StatusOK {
		t.Errorf("no session: got %d %s; want the certificate", rr.Code, rr.Body.String())
	}
	bogus := testRequest(http.MethodGet, "/api/tls/ca", nil)
	withAuth(bogus, "PHPSESSID=not-a-session")
	if rr := testResponse(t, s, bogus); rr.Code != http.StatusOK {
		t.Errorf("a bogus session: got %d %s; want the certificate", rr.Code, rr.Body.String())
	}
}

// Public is one path, not one server: the Host pin still applies in front of it, so the download cannot
// be pointed at another deployment's certificate.
func TestTLSCAStillPinsTheHost(t *testing.T) {
	s := testServer(t)

	req := testRequest(http.MethodGet, "/api/tls/ca", nil)
	req.Host = "elsewhere.example"
	rr := httptest.NewRecorder()
	s.middlewareChain(s.mux).ServeHTTP(rr, req)
	if rr.Code != http.StatusMisdirectedRequest {
		t.Errorf("a foreign Host = %d %s; want 421", rr.Code, rr.Body.String())
	}
}

// A certificate the operator brought has no CA of ours behind it, so the download refuses and the
// advertised flag stays down rather than linking at a 404.
func TestTLSCAUnavailableWithoutLocalCA(t *testing.T) {
	s := testServer(t)
	cookie := loginTest(t, s)

	s.tlsConfig = &tls.Config{CertFile: "user.crt", KeyFile: "user.key"}
	if rr := getTLSCA(t, s); rr.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d: %s", rr.Code, rr.Body.String())
	}
	s.configStore.SetTLSCAAvailable(false)
	if raw := getConfigBody(t, s, cookie); raw["tls_ca_available"] == true {
		t.Errorf("/api/config still advertises a CA download")
	}
}

func TestTLSCAOnlyServesGET(t *testing.T) {
	s := testServer(t)
	cookie, csrf := loginTestCSRF(t, s)

	req := testRequest(http.MethodPost, "/api/tls/ca", nil)
	withAuthCSRF(req, cookie, csrf)
	if rr := testResponse(t, s, req); rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 for POST, got %d: %s", rr.Code, rr.Body.String())
	}
}

// The flag drives the link, so it must follow the same condition as the 404.
func TestConfigAdvertisesTLSCA(t *testing.T) {
	s := testServer(t)
	cookie := loginTest(t, s)

	if raw := getConfigBody(t, s, cookie); raw["tls_ca_available"] != true {
		t.Errorf("expected tls_ca_available true with the default certificate, got %v", raw["tls_ca_available"])
	}

	s.configStore.SetTLSCAAvailable(false)
	if raw := getConfigBody(t, s, cookie); raw["tls_ca_available"] != false {
		t.Errorf("expected tls_ca_available false after the flag is cleared, got %v", raw["tls_ca_available"])
	}
}

// getConfigBody reads /api/config as a map, to assert on one field.
func getConfigBody(t *testing.T, s *Server, cookie string) map[string]any {
	t.Helper()
	req := testRequest(http.MethodGet, "/api/config", nil)
	withAuth(req, cookie)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("get config: %d %s", rr.Code, rr.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	return out
}
