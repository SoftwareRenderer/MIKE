package httputil

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Tested against a real redirect: a client that follows one has already reached a host nothing validated.

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

// The one place a non-public address becomes reachable, and only for the host a person typed: that exact
// host on loopback, and nothing else is admitted even when named.

func TestGrantedEgressClientReachesLoopbackForTheGrantedHost(t *testing.T) {
	reached := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	client := NewExtensionEgressClientGranting(5*time.Second, []string{"127.0.0.1"})
	resp, err := client.Get(upstream.URL)
	if err != nil {
		t.Fatalf("the granted loopback host was refused: %v", err)
	}
	defer resp.Body.Close()
	if !reached {
		t.Fatal("the request never arrived")
	}
}

func TestGrantedEgressClientRefusesLoopbackForAnyOtherHost(t *testing.T) {
	reached := false
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	defer upstream.Close()

	for _, tc := range []struct {
		name    string
		granted []string
	}{
		{"no grants at all", nil},
		{"a different host", []string{"searx.test"}},
		// The grant matches the dial hostname, so loopback reached under another name is not named.
		{"another name for this machine", []string{"localhost"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := NewExtensionEgressClientGranting(5*time.Second, tc.granted)
			_, err := client.Get(upstream.URL)
			if err == nil {
				t.Fatal("loopback was reached without a grant for it")
			}
			if !errors.Is(err, ErrInternalAddress) {
				t.Errorf("err = %v, want the address refusal", err)
			}
			if reached {
				t.Error("the server was dialed despite the refusal")
			}
		})
	}
}

// The exception is loopback only, not every host a person names.
func TestGrantedEgressClientRefusesTheMetadataAddress(t *testing.T) {
	client := NewExtensionEgressClientGranting(2*time.Second, []string{"169.254.169.254"})
	_, err := client.Get("http://169.254.169.254/latest/meta-data/")
	if err == nil {
		t.Fatal("the link-local metadata address was reached")
	}
	if !errors.Is(err, ErrInternalAddress) {
		t.Errorf("err = %v, want the address refusal", err)
	}
}

// The widening is per call: with nothing granted, nothing changes.
func TestGrantedEgressClientWithNoGrantsIsTheStrictClient(t *testing.T) {
	if NewExtensionEgressClientGranting(time.Second, nil) == nil {
		t.Fatal("no client")
	}
	if NewExtensionEgressClientGranting(time.Second, nil).Transport != NewExtensionEgressClient(time.Second).Transport {
		t.Error("an ungranted call should share the strict transport rather than build another")
	}
	if NewExtensionEgressClientGranting(time.Second, []string{"x.test"}).Transport == NewExtensionEgressClient(time.Second).Transport {
		t.Error("a granted call must not share the strict pool — the address rule differs per call")
	}
}
