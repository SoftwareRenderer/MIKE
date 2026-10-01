package mcp

import (
	"context"
	"strings"
	"testing"

	"mike/backend/types"
)

// The Test action is the only place the classification reaches a person rather than the log, so what it
// promises is the same promise the offer makes — and it writes nothing down.

func probeForm(t *testing.T, reg *Registry, body TestRequest) (TestAnswer, error) {
	t.Helper()
	return reg.ProbeForm(context.Background(), body)
}

// The form's own values are what gets dialled: a URL typed and never saved is still the address the
// person means, and the tool count is the answer they came for.
func TestProbeFormDialsTheFormsValues(t *testing.T) {
	fake := fakeMCPServer(t, fakeOptions{Stateless: true})
	reg, _, _ := registryFixture(t)

	answer, err := probeForm(t, reg, TestRequest{Name: "Draft", URL: fake.URL, TimeoutSeconds: 5})
	if err != nil {
		t.Fatalf("a diallable form was refused: %v", err)
	}
	if !answer.OK {
		t.Fatalf("ok=false: %s (%s)", answer.Message, answer.Kind)
	}
	if answer.Tools != 2 {
		t.Errorf("tools = %d, want the fake's two", answer.Tools)
	}
	if fake.Requests() == 0 {
		t.Error("the draft's URL was never dialled")
	}
}

// A masked header stands for a stored one, and the probe resolves it server-side. A server that needs the
// token says so when the mask has nothing behind it.
func TestProbeFormUsesTheStoredTokenForAMaskedHeader(t *testing.T) {
	const token = "s3cret-token"
	fake := fakeMCPServer(t, fakeOptions{Stateless: true, RequireToken: token})
	value := "Bearer " + token
	stored := registered(fake.URL, 5)
	stored.Headers = []types.MCPHeader{{Name: "Authorization", Value: &value}}
	projects := fakeProjects{byID: map[string][]string{"p-on": {stored.ID}}}

	reg := NewRegistry(fakeSource{servers: []types.MCPServer{stored}}, projects, testID)
	answer, err := probeForm(t, reg, TestRequest{
		ID: stored.ID, Name: stored.Name, URL: stored.URL, TimeoutSeconds: 5,
		Headers: []TestHeader{{Name: "Authorization", Value: types.MaskedSecret}},
	})
	if err != nil {
		t.Fatalf("a masked header was refused outright: %v", err)
	}
	if !answer.OK {
		t.Errorf("the stored token was not used: %s (%s)", answer.Message, answer.Kind)
	}

	// The same form with nothing stored behind the mask: the refusal is the answer, and it names the
	// place a token is set.
	bare := NewRegistry(fakeSource{servers: []types.MCPServer{registered(fake.URL, 5)}}, projects, testID)
	answer, _ = probeForm(t, bare, TestRequest{
		ID: "ffffffffffffffff", Name: "Fake", URL: fake.URL, TimeoutSeconds: 5,
		Headers: []TestHeader{{Name: "Authorization", Value: types.MaskedSecret}},
	})
	if answer.OK || answer.Kind != string(FailureRefused) {
		t.Errorf("an unbacked mask answered %+v, want a refusal", answer)
	}
	if !strings.Contains(answer.Message, "Settings") {
		t.Errorf("message %q does not say where a token is set", answer.Message)
	}
}

// An address that cannot be dialled at all is a refusal naming the field — the same sentence saving it
// would get, from the same rules — and nothing reaches the network.
func TestProbeFormRefusesAnUndialableURLWithoutDialling(t *testing.T) {
	fake := fakeMCPServer(t, fakeOptions{Stateless: true})
	reg, _, _ := registryFixture(t)
	before := fake.Requests()

	for _, tc := range []struct{ url, wants string }{
		{"stdio://nothing", "http or https"},
		{"https://user:pw@mcp.example.com/mcp", "send them as a header"},
		{"", "url"},
		{"https://a.example.com", "timeout"},
	} {
		body := TestRequest{Name: "Bad", URL: tc.url}
		if tc.wants == "timeout" {
			body.TimeoutSeconds = -1
		}
		_, err := probeForm(t, reg, body)
		if err == nil {
			t.Errorf("%+v: accepted, want a refusal naming %q", body, tc.wants)
			continue
		}
		if !strings.Contains(strings.ToLower(err.Error()), tc.wants) {
			t.Errorf("%q: refused with %q, want it to name %q", tc.url, err, tc.wants)
		}
	}
	if fake.Requests() != before {
		t.Errorf("the fake was dialled %d more times for forms that cannot be dialled", fake.Requests()-before)
	}
}

// Something answering with a different protocol era is refused by name, and the person sees the era that
// is supported — the one thing the offer can only report in the log.
func TestProbeFormNamesTheEraOfAHandshakeServer(t *testing.T) {
	stub, _ := probeServer(t, handshakeEra)
	reg, _, _ := registryFixture(t)

	answer, err := probeForm(t, reg, TestRequest{Name: "Old", URL: stub.URL, TimeoutSeconds: 3})
	if err != nil {
		t.Fatalf("a diallable form was refused: %v", err)
	}
	if answer.OK {
		t.Fatal("a handshake-era server was accepted")
	}
	if answer.Kind != string(FailureWrongEra) {
		t.Fatalf("kind = %q, want %q (%s)", answer.Kind, FailureWrongEra, answer.Message)
	}
	if !strings.Contains(answer.Message, Era) {
		t.Errorf("message %q does not name the supported era", answer.Message)
	}
}

// A successful Test primes the cache the offer reads, so the next turn does not pay for that dial again.
// A draft with no id is not cached: nothing is keyed on it.
func TestProbeFormPrimesTheOfferForARegisteredRow(t *testing.T) {
	fake := fakeMCPServer(t, fakeOptions{Stateless: true})
	ctx := context.Background()

	// A draft: no id, so no entry, so the offer still has to make its own dial.
	draft := NewRegistry(fakeSource{servers: nil}, fakeProjects{}, testID)
	if _, err := draft.ProbeForm(ctx, TestRequest{Name: "Draft", URL: fake.URL, TimeoutSeconds: 5}); err != nil {
		t.Fatalf("probe of a draft: %v", err)
	}
	if len(draft.entries) != 0 {
		t.Errorf("a draft probe left %d cache entries, want none", len(draft.entries))
	}

	// A registered row, on a registry that has taken no listing: the probe's listing is the only one
	// the offer can read.
	row := registered(fake.URL, 5)
	reg := NewRegistry(fakeSource{servers: []types.MCPServer{row}},
		fakeProjects{byID: map[string][]string{"p-on": {row.ID}}}, testID)
	if _, err := reg.ProbeForm(ctx, TestRequest{ID: row.ID, Name: row.Name, URL: row.URL, TimeoutSeconds: 5}); err != nil {
		t.Fatalf("probe of the registered row: %v", err)
	}
	dials := fake.Requests()
	if specs := reg.SpecsForProject(ctx, "p-on"); len(specs) == 0 {
		t.Fatalf("the offer found nothing after a successful probe of server %s", row.ID)
	}
	if fake.Requests() != dials {
		t.Errorf("the offer dialled %d more times after the probe listed the same row", fake.Requests()-dials)
	}
}

// The name a failure is attributed to is the one the form holds; with none, it is the address, because
// the sentence has to name who failed.
func TestProbeFormReportsAnUnnamedServerByItsAddress(t *testing.T) {
	reg, _, _ := registryFixture(t)

	answer, err := probeForm(t, reg, TestRequest{URL: "http://127.0.0.1:1/mcp", TimeoutSeconds: 1})
	if err != nil {
		t.Fatalf("a diallable form was refused: %v", err)
	}
	if !strings.Contains(answer.Message, "127.0.0.1:1") {
		t.Errorf("message %q names nobody — the host and port are all the form carried", answer.Message)
	}
}
