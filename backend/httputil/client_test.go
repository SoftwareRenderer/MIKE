package httputil

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Tested against a real redirect: a client that follows one has already reached a host that nothing
// validated.

func TestNewSingleAttemptClientRefusesRedirects(t *testing.T) {
	var escapeHits int
	escapes := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		escapeHits++
		w.WriteHeader(http.StatusTeapot)
	}))
	defer escapes.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, escapes.URL, http.StatusFound)
	}))
	defer srv.Close()

	resp, err := NewSingleAttemptClient(5 * time.Second).Get(srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// ErrUseLastResponse reports the redirect as the response, not as an error.
	if resp.StatusCode != http.StatusFound {
		t.Errorf("status = %d, want the 307 itself — a client that refused the redirect reports it", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != escapes.URL {
		t.Errorf("Location = %q, want %q", loc, escapes.URL)
	}
	if escapeHits != 0 {
		t.Errorf("redirect target contacted %d time(s)", escapeHits)
	}
}

func TestNewClientFollowsRedirects(t *testing.T) {
	want := "arrived"
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, want)
	}))
	defer target.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer srv.Close()

	resp, err := NewClient(5 * time.Second).Get(srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK || string(body) != want {
		t.Errorf("status=%d body=%q, want 200 %q", resp.StatusCode, strings.TrimSpace(string(body)), want)
	}
}

// TestClientsShareOneTransport: one pool, one dial timeout, per-caller budgets preserved.
func TestClientsShareOneTransport(t *testing.T) {
	a := NewClient(time.Second)
	b := NewSingleAttemptClient(2 * time.Second)
	if a.Transport != b.Transport {
		t.Error("clients do not share a transport, so pooling is per call site again")
	}
	if a.Transport != sharedTransport {
		t.Error("a client bypassed sharedTransport")
	}
	if a.Timeout != time.Second || b.Timeout != 2*time.Second {
		t.Errorf("per-caller budgets were not preserved: %v / %v", a.Timeout, b.Timeout)
	}
	if sharedTransport.DialContext == nil {
		t.Error("sharedTransport has no DialContext, so connects use the OS TCP timeout")
	}
}
