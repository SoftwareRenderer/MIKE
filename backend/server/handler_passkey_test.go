package server

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mike/backend/auth"
	"mike/backend/startup"
	"mike/backend/testutil/testwebauthn"

	"github.com/go-webauthn/webauthn/protocol"
)

// The ceremony tests drive each route pair with a real credential: a key minted by testwebauthn, a
// self-attestation, and a signature over the challenge this server issued. What a browser holds between
// the two halves — the ticket — travels over the wire exactly as the browser echoes it.

const (
	// testCeremonyHost is a hostname, never an address: an address cannot be a relying-party id, so a
	// ceremony pointed at one is refused before it starts.
	testCeremonyHost   = "localhost:3000"
	testCeremonyRPID   = "localhost"
	testCeremonyOrigin = "http://localhost:3000"

	// registerFlags is what a platform passkey asserts about itself at registration: possession, a
	// verified person, and attested credential data (the bit that says a key was created). An assertion
	// carries the first two.
	registerFlags = protocol.FlagUserPresent | protocol.FlagUserVerified | protocol.FlagAttestedCredentialData
	assertFlags   = protocol.FlagUserPresent | protocol.FlagUserVerified
)

// ceremonyTransport is how a test says what the connection looked like: the ceremony's expected origin and
// the session cookie's Secure flag both come from the same answer, so they are said together.
type ceremonyTransport struct {
	TLS       bool   // TLS terminated here
	Forwarded string // the X-Forwarded-Proto a proxy set, honoured only under MIKE_TRUST_PROXY
	Secure    bool   // what the server is expected to conclude, which the credential must agree with

	// UserAgent and RemoteAddr are what the session opened at the end of the ceremony records: a
	// session row describes the device that signed in, and the only witness is this request.
	UserAgent  string
	RemoteAddr string
}

func (tp ceremonyTransport) origin() string {
	if tp.Secure {
		return "https://" + testCeremonyHost
	}
	return "http://" + testCeremonyHost
}

// decorate puts the transport back the way the browser reached it.
func (tp ceremonyTransport) decorate(req *http.Request) *http.Request {
	if tp.TLS {
		req.TLS = &tls.ConnectionState{}
	}
	if tp.Forwarded != "" {
		req.Header.Set("X-Forwarded-Proto", tp.Forwarded)
	}
	if tp.UserAgent != "" {
		req.Header.Set("User-Agent", tp.UserAgent)
	}
	if tp.RemoteAddr != "" {
		req.RemoteAddr = tp.RemoteAddr
	}
	return req
}

var plainCeremony = ceremonyTransport{}

// ceremonyPost builds a POST to a route so the request's own Host is the address the ceremony binds to —
// the value the expected origin is derived from, exactly as in a browser.
func ceremonyPost(t *testing.T, path string, body any, tp ceremonyTransport) *http.Request {
	t.Helper()
	return tp.decorate(testRequest(http.MethodPost, "http://"+testCeremonyHost+path, body))
}

// mintPairingCode mints a code directly. A test that is testing the code asks for one this way; a test
// that is testing something else goes through registerTestPasskey.
func mintPairingCode(t *testing.T, s *Server) string {
	t.Helper()
	code, err := s.pairing.Mint(auth.BootPairingTTL)
	if err != nil {
		t.Fatalf("mint pairing code: %v", err)
	}
	return code.Code
}

// beginCeremony runs the first half of a ceremony, returning its ticket, the challenge it issued, and the
// recorder so a refusal can be asserted from the status.
func beginCeremony(t *testing.T, s *Server, path string, body any, tp ceremonyTransport) (ticket, challenge string, rr *httptest.ResponseRecorder) {
	t.Helper()
	rr = testResponse(t, s, ceremonyPost(t, path, body, tp))
	if rr.Code != http.StatusOK {
		return "", "", rr
	}
	answer := mustJSON[map[string]any](t, rr.Body)
	ticket, _ = answer["ticket"].(string)
	// The options travel under `options.publicKey`, which is the shape navigator.credentials.* takes.
	options, _ := answer["options"].(map[string]any)
	publicKey, _ := options["publicKey"].(map[string]any)
	challenge, _ = publicKey["challenge"].(string)
	if ticket == "" || challenge == "" {
		t.Fatalf("%s answered without a ticket or a challenge: %v", path, answer)
	}
	return ticket, challenge, rr
}

// registerPasskeyOver pairs one device end to end over the connection the test describes, and returns the
// credential it minted with the response that finished the ceremony.
func registerPasskeyOver(t *testing.T, s *Server, name string, tp ceremonyTransport) (*testwebauthn.Credential, *httptest.ResponseRecorder) {
	t.Helper()
	ticket, challenge, rr := beginCeremony(t, s, "/api/auth/passkey/register/begin",
		map[string]string{"code": mintPairingCode(t, s)}, tp)
	if rr.Code != http.StatusOK {
		t.Fatalf("register/begin: %d %s", rr.Code, rr.Body.String())
	}
	credential, err := testwebauthn.NewCredential()
	if err != nil {
		t.Fatalf("mint credential: %v", err)
	}
	response, err := credential.RegistrationBody(testCeremonyRPID, tp.origin(), challenge, registerFlags)
	if err != nil {
		t.Fatalf("build registration response: %v", err)
	}
	finished := testResponse(t, s, ceremonyPost(t, "/api/auth/passkey/register/finish", map[string]any{
		"ticket":   ticket,
		"name":     name,
		"response": json.RawMessage(response),
	}, tp))
	if finished.Code != http.StatusOK {
		t.Fatalf("register/finish: %d %s", finished.Code, finished.Body.String())
	}
	return credential, finished
}

// registerTestPasskey pairs one device on a plain connection and returns the credential it minted plus
// the session cookie the finished registration set.
func registerTestPasskey(t *testing.T, s *Server) (*testwebauthn.Credential, string) {
	t.Helper()
	credential, finished := registerPasskeyOver(t, s, "test laptop", plainCeremony)
	return credential, finished.Header().Get("Set-Cookie")
}

// registerTestPasskeyNamed pairs one device with the label a person would have typed.
func registerTestPasskeyNamed(t *testing.T, s *Server, name string) (*testwebauthn.Credential, string) {
	t.Helper()
	credential, finished := registerPasskeyOver(t, s, name, plainCeremony)
	return credential, finished.Header().Get("Set-Cookie")
}

// signInOver runs the assertion ceremony, so a test can assert on both the answer and the session it
// opened.
func signInOver(t *testing.T, s *Server, credential *testwebauthn.Credential, counter uint32, tp ceremonyTransport) *httptest.ResponseRecorder {
	t.Helper()
	ticket, challenge, rr := beginCeremony(t, s, "/api/auth/passkey/assert/begin", nil, tp)
	if rr.Code != http.StatusOK {
		return rr
	}
	response, err := credential.AssertionBody(testCeremonyRPID, tp.origin(), challenge, counter, nil, assertFlags)
	if err != nil {
		t.Fatalf("build assertion response: %v", err)
	}
	return testResponse(t, s, ceremonyPost(t, "/api/auth/passkey/assert/finish", map[string]any{
		"ticket":   ticket,
		"response": json.RawMessage(response),
	}, tp))
}

// signInWithPasskey runs the assertion ceremony on a plain connection.
func signInWithPasskey(t *testing.T, s *Server, credential *testwebauthn.Credential, counter uint32) *httptest.ResponseRecorder {
	t.Helper()
	return signInOver(t, s, credential, counter, plainCeremony)
}

// TestPasskeyPairingOpensASessionOnTheBrowserThatFinishedIt is the whole first-run path: the code buys a
// registration, and the registration — not the code — is what sets the cookie.
func TestPasskeyPairingOpensASessionOnTheBrowserThatFinishedIt(t *testing.T) {
	s := testServer(t)

	_, cookie := registerTestPasskey(t, s)
	if !strings.Contains(cookie, sessionCookieName+"=") {
		t.Fatalf("no session cookie after a finished registration: %q", cookie)
	}
	for _, flag := range []string{"HttpOnly", "SameSite=Lax"} {
		if !strings.Contains(cookie, flag) {
			t.Errorf("the session cookie lost %s: %q", flag, cookie)
		}
	}
	if rr := testResponse(t, s, ceremonyPost(t, "/api/auth/passkey/assert/finish",
		map[string]any{"ticket": "forged", "response": json.RawMessage(`{}`)}, plainCeremony)); rr.Code == http.StatusOK {
		t.Error("a forged ticket opened a session")
	}

	// The session the ceremony opened is a real one: it answers the session check, and the install now
	// reports itself paired.
	body := mustJSON[map[string]any](t, testResponse(t, s,
		withAuth(testRequest(http.MethodGet, "/api/auth/session", nil), cookie)).Body)
	if body["authenticated"] != true {
		t.Errorf("the registration did not open a session: %v", body)
	}
	if body["has_passkey"] != true {
		t.Errorf("has_passkey = %v after registering; want true", body["has_passkey"])
	}
}

// TestPasskeySignInAfterRegistration is every later login: an assertion, and the same session shape.
func TestPasskeySignInAfterRegistration(t *testing.T) {
	s := testServer(t)
	credential, _ := registerTestPasskey(t, s)

	rr := signInWithPasskey(t, s, credential, 1)
	if rr.Code != http.StatusOK {
		t.Fatalf("assert/finish: %d %s", rr.Code, rr.Body.String())
	}
	if rr.Header().Get("X-CSRF-Token") == "" {
		t.Error("a finished sign-in set no CSRF token")
	}

	rows, err := s.store.ListPasskeys(t.Context())
	if err != nil || len(rows) != 1 {
		t.Fatalf("passkeys = %v, %v", rows, err)
	}
	if rows[0].LastUsedAt.IsZero() {
		t.Error("a verified sign-in did not stamp last_used_at")
	}
}

// A pairing code is spent by the begin half, so one code cannot pair two devices — and a wrong, expired
// or already-used code is one refusal, so the answer never tells a guesser how close they got.
func TestPairingCodeIsSingleUseAndRefusesIdentically(t *testing.T) {
	s := testServer(t)
	code := mintPairingCode(t, s)

	if _, _, rr := beginCeremony(t, s, "/api/auth/passkey/register/begin", map[string]string{"code": code}, plainCeremony); rr.Code != http.StatusOK {
		t.Fatalf("a live code did not begin a registration: %d %s", rr.Code, rr.Body.String())
	}
	_, _, spent := beginCeremony(t, s, "/api/auth/passkey/register/begin", map[string]string{"code": code}, plainCeremony)
	if spent.Code != http.StatusUnauthorized {
		t.Fatalf("a spent code = %d; want 401", spent.Code)
	}
	unknown := testResponse(t, s, ceremonyPost(t, "/api/auth/passkey/register/begin", map[string]string{"code": "NOSUCHCODE"}, plainCeremony))
	if spent.Body.String() != unknown.Body.String() {
		t.Errorf("a spent code answered %q and an unknown one %q; one refusal, or a guesser learns how close they got",
			spent.Body.String(), unknown.Body.String())
	}
}

// TestPairingCodeExpires: a code whose window has passed is refused, and the minting of a code that has
// already aged out is the only way to test the clock without waiting on it.
func TestPairingCodeExpires(t *testing.T) {
	s := testServer(t)
	expired, err := s.pairing.Mint(-time.Minute)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	rr := testResponse(t, s, ceremonyPost(t, "/api/auth/passkey/register/begin", map[string]string{"code": expired.Code}, plainCeremony))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("an expired code = %d; want 401", rr.Code)
	}
}

// TestPairingCodeIsTolerantOfHowItIsTyped: the shape a person reads off a terminal is not a second way to
// be refused.
func TestPairingCodeIsTolerantOfHowItIsTyped(t *testing.T) {
	s := testServer(t)
	code := mintPairingCode(t, s)
	typed := strings.ToLower(code[:4] + "-" + code[4:])

	if _, _, rr := beginCeremony(t, s, "/api/auth/passkey/register/begin", map[string]string{"code": typed}, plainCeremony); rr.Code != http.StatusOK {
		t.Fatalf("%q was refused: %d %s", typed, rr.Code, rr.Body.String())
	}
}

// TestCeremonyTicketCannotCrossPurposes: a ticket is sealed for one ceremony, so an unfinished sign-in is
// not a registration and neither can be replayed as the other.
func TestCeremonyTicketCannotCrossPurposes(t *testing.T) {
	s := testServer(t)
	credential, _ := registerTestPasskey(t, s)

	assertTicket, _, rr := beginCeremony(t, s, "/api/auth/passkey/assert/begin", nil, plainCeremony)
	if rr.Code != http.StatusOK {
		t.Fatalf("assert/begin: %d %s", rr.Code, rr.Body.String())
	}
	body, err := credential.RegistrationBody(testCeremonyRPID, testCeremonyOrigin, "whatever", registerFlags)
	if err != nil {
		t.Fatalf("build response: %v", err)
	}
	crossed := testResponse(t, s, ceremonyPost(t, "/api/auth/passkey/register/finish", map[string]any{
		"ticket":   assertTicket,
		"response": json.RawMessage(body),
	}, plainCeremony))
	if crossed.Code != http.StatusUnauthorized {
		t.Fatalf("a sign-in ticket presented to registration = %d; want 401", crossed.Code)
	}
}

// TestCeremonyRefusalIsMIKEsOwnMessage: a response this server did not ask for is refused in MIKE's own
// words — the verifier's text describes the bytes someone sent and is not for a browser to read.
func TestCeremonyRefusalIsMIKEsOwnMessage(t *testing.T) {
	s := testServer(t)
	registerTestPasskey(t, s)

	ticket, _, rr := beginCeremony(t, s, "/api/auth/passkey/assert/begin", nil, plainCeremony)
	if rr.Code != http.StatusOK {
		t.Fatalf("assert/begin: %d %s", rr.Code, rr.Body.String())
	}
	foreign, err := testwebauthn.NewCredential()
	if err != nil {
		t.Fatalf("mint credential: %v", err)
	}
	body, err := foreign.AssertionBody(testCeremonyRPID, testCeremonyOrigin, "a-challenge-nobody-issued", 1, nil, assertFlags)
	if err != nil {
		t.Fatalf("build response: %v", err)
	}
	refused := testResponse(t, s, ceremonyPost(t, "/api/auth/passkey/assert/finish", map[string]any{
		"ticket":   ticket,
		"response": json.RawMessage(body),
	}, plainCeremony))
	if refused.Code != http.StatusUnauthorized {
		t.Fatalf("a forged assertion = %d; want 401; body %s", refused.Code, refused.Body.String())
	}
	if !strings.Contains(refused.Body.String(), "did not verify") {
		t.Errorf("the refusal is the verifier's text, not MIKE's: %s", refused.Body.String())
	}
}

// TestCeremonyAtAnAddressIsRefusedByTheServer: a credential is bound to a hostname, so the refusal has to
// say "open MIKE by its hostname" rather than fail inside the browser's prompt.
func TestCeremonyAtAnAddressIsRefusedByTheServer(t *testing.T) {
	s := testServer(t)
	rr := testResponse(t, s, testRequest(http.MethodPost, "http://192.168.1.10:3000/api/auth/passkey/assert/begin", nil))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("a ceremony at an address = %d; want 400; body %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "hostname") {
		t.Errorf("the refusal does not say what to do instead: %s", rr.Body.String())
	}
}

// TestPairingMintAndRevoke is Settings' half of onboarding: a signed-in device hands out the right to
// register, and can take every outstanding code back.
func TestPairingMintAndRevoke(t *testing.T) {
	s := testServer(t)
	cookie, csrf := loginTestCSRF(t, s)

	rr := testResponse(t, s, withAuthCSRF(testRequest(http.MethodPost, "/api/auth/pairing", nil), cookie, csrf))
	if rr.Code != http.StatusOK {
		t.Fatalf("POST /api/auth/pairing: %d %s", rr.Code, rr.Body.String())
	}
	minted := mustJSON[map[string]any](t, rr.Body)
	code, _ := minted["code"].(string)
	url, _ := minted["url"].(string)
	qr, _ := minted["qr_png"].(string)
	if len(strings.ReplaceAll(code, "-", "")) != 8 {
		t.Errorf("code = %q; want the 8 characters, shown in two groups", code)
	}
	// The code rides in the fragment, so it reaches no access log and no Referer header.
	if !strings.Contains(url, "/#pair="+strings.ReplaceAll(code, "-", "")) {
		t.Errorf("the URL does not carry the code in the fragment: %q", url)
	}
	if !strings.HasPrefix(qr, "data:image/png;base64,") {
		t.Errorf("qr_png = %.24q; want a data URL the browser can drop straight into an <img src>", qr)
	}
	if expires, _ := minted["expires_at"].(string); expires == "" {
		t.Error("a minted code was answered without the expiry the panel counts down from")
	}

	if _, _, live := beginCeremony(t, s, "/api/auth/passkey/register/begin", map[string]string{"code": code}, plainCeremony); live.Code != http.StatusOK {
		t.Fatalf("a freshly minted code was refused: %d", live.Code)
	}

	drop := testResponse(t, s, func() *http.Request {
		r := testRequest(http.MethodDelete, "/api/auth/pairing", nil)
		withAuthCSRF(r, cookie, csrf)
		return r
	}())
	if drop.Code != http.StatusOK {
		t.Fatalf("DELETE /api/auth/pairing: %d %s", drop.Code, drop.Body.String())
	}
	if _, _, after := beginCeremony(t, s, "/api/auth/passkey/register/begin", map[string]string{"code": code}, plainCeremony); after.Code != http.StatusUnauthorized {
		t.Fatalf("a revoked code still worked: %d", after.Code)
	}
	// Revoking codes touches no credential.
	if rows, err := s.store.ListPasskeys(t.Context()); err != nil || len(rows) != 0 {
		t.Errorf("revoking codes changed the credential table: %v, %v", rows, err)
	}
}

// TestPairingMintRefusesHoardOfCodes: the cap is what the 409 is for, and the refusal says what to do.
func TestPairingMintRefusesHoardOfCodes(t *testing.T) {
	s := testServer(t)
	cookie, csrf := loginTestCSRF(t, s)
	for i := 0; i < 3; i++ {
		if rr := testResponse(t, s, withAuthCSRF(testRequest(http.MethodPost, "/api/auth/pairing", nil), cookie, csrf)); rr.Code != http.StatusOK {
			t.Fatalf("mint %d: %d %s", i, rr.Code, rr.Body.String())
		}
	}
	rr := testResponse(t, s, withAuthCSRF(testRequest(http.MethodPost, "/api/auth/pairing", nil), cookie, csrf))
	if rr.Code != http.StatusConflict {
		t.Fatalf("a fourth live code = %d; want 409", rr.Code)
	}
}

// TestPasskeyListingRenameAndRevoke is the Settings panel's data and its two actions, including the one
// refusal that keeps an install signable-in.
func TestPasskeyListingRenameAndRevoke(t *testing.T) {
	s := testServer(t)
	first, cookie := registerTestPasskey(t, s)
	csrf := sessionCSRF(t, s, cookie)
	second, _ := registerTestPasskeyNamed(t, s, "phone")

	list := func() []map[string]any {
		t.Helper()
		rr := testResponse(t, s, withAuth(testRequest(http.MethodGet, "/api/auth/passkeys", nil), cookie))
		if rr.Code != http.StatusOK {
			t.Fatalf("list: %d %s", rr.Code, rr.Body.String())
		}
		return mustJSON[[]map[string]any](t, rr.Body)
	}

	rows := list()
	if len(rows) != 2 {
		t.Fatalf("listed %d passkeys; want 2: %v", len(rows), rows)
	}
	for _, row := range rows {
		for _, leaked := range []string{"record", "public_key", "rp_id"} {
			if _, found := row[leaked]; found {
				t.Errorf("the device list carries %q", leaked)
			}
		}
	}
	firstID, secondID := credentialID(first), credentialID(second)
	// Both were minted inside one second, and the tie-break inside a created_at second is the credential
	// id, so the only order this test can assert is "the row the person paired first is listed".
	byID := map[string]map[string]any{}
	for _, row := range rows {
		byID[row["id"].(string)] = row
	}
	if got, _ := byID[firstID]["name"].(string); got != "test laptop" {
		t.Errorf("the label the ceremony was given did not reach the list: %v", byID[firstID])
	}
	if _, listed := byID[secondID]; !listed {
		t.Errorf("the second device is not listed: %v", rows)
	}

	renamed := testResponse(t, s, withAuthCSRF(testRequest(http.MethodPatch, "/api/auth/passkeys/"+firstID,
		map[string]any{"name": "work laptop"}), cookie, csrf))
	if renamed.Code != http.StatusOK {
		t.Fatalf("rename: %d %s", renamed.Code, renamed.Body.String())
	}
	renamedRows := map[string]any{}
	for _, row := range list() {
		if row["id"] == firstID {
			renamedRows = row
		}
	}
	if renamedRows["name"] != "work laptop" {
		t.Errorf("name after rename = %v", renamedRows["name"])
	}
	if renamedRows["created_at"] == "" || renamedRows["created_at"] == nil {
		t.Error("a device row was answered without the instant it was paired")
	}

	// A name that is not a name is refused rather than truncated: the person would not find the row they
	// meant on a device list.
	bad := testResponse(t, s, withAuthCSRF(testRequest(http.MethodPatch, "/api/auth/passkeys/"+firstID,
		map[string]any{"name": strings.Repeat("n", 65)}), cookie, csrf))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("a 65-character name = %d; want 400", bad.Code)
	}
	unknown := testResponse(t, s, withAuthCSRF(testRequest(http.MethodPatch, "/api/auth/passkeys/nosuchid",
		map[string]any{"name": "ghost"}), cookie, csrf))
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("renaming an unknown id = %d; want 404", unknown.Code)
	}

	revoked := testResponse(t, s, withAuthCSRF(testRequest(http.MethodDelete, "/api/auth/passkeys/"+secondID, nil),
		cookie, csrf))
	if revoked.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", revoked.Code, revoked.Body.String())
	}
	missing := testResponse(t, s, withAuthCSRF(testRequest(http.MethodDelete, "/api/auth/passkeys/nosuchid", nil),
		cookie, csrf))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("revoking an unknown id = %d; want 404", missing.Code)
	}

	// What is left is the last one, and the GUI cannot reach zero passkeys from here.
	last := testResponse(t, s, withAuthCSRF(testRequest(http.MethodDelete, "/api/auth/passkeys/"+firstID, nil),
		cookie, csrf))
	if last.Code != http.StatusConflict {
		t.Fatalf("revoking the last passkey = %d; want 409", last.Code)
	}
	if !strings.Contains(last.Body.String(), "--reset-passkeys") {
		t.Errorf("the refusal does not name the way out: %s", last.Body.String())
	}
	if rows = list(); len(rows) != 1 {
		t.Fatalf("the refused delete took a row anyway: %v", rows)
	}
}

// TestPasskeyRoutesNeedTheirVerb: a route reached on the wrong verb is answered by the mux, so no handler
// ever has to inspect the method — and a methodless call never reaches a state change.
func TestPasskeyRoutesNeedTheirVerb(t *testing.T) {
	s := testServer(t)
	cookie, csrf := loginTestCSRF(t, s)

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/auth/passkey/register/begin"},
		{http.MethodGet, "/api/auth/passkey/register/finish"},
		{http.MethodGet, "/api/auth/passkey/assert/begin"},
		{http.MethodPost, "/api/auth/passkeys"},
		{http.MethodPut, "/api/auth/passkeys/someid"},
		{http.MethodGet, "/api/auth/pairing"},
	} {
		rr := testResponse(t, s, func() *http.Request {
			r := testRequest(tc.method, tc.path, nil)
			withAuthCSRF(r, cookie, csrf)
			return r
		}())
		if rr.Code == http.StatusOK {
			t.Errorf("%s %s ran a handler on the wrong verb", tc.method, tc.path)
		}
	}
}

// TestPasskeyStoreFaultAnswersMIKEsOwnMessage: with the store gone, the browser still gets MIKE's sentence
// and the cause stays in the log.
func TestPasskeyStoreFaultAnswersMIKEsOwnMessage(t *testing.T) {
	s := testServer(t)
	code := mintPairingCode(t, s)
	// The ceremony route is the one that reads the store with no session in front of it, so the handler's
	// own fault is what the recorder shows rather than the middleware's "invalid session".
	if err := s.store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	rr := testResponse(t, s, ceremonyPost(t, "/api/auth/passkey/register/begin",
		map[string]string{"code": code}, plainCeremony))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("a store fault answered %d; want 500; body %s", rr.Code, rr.Body.String())
	}
	if body := rr.Body.String(); strings.Contains(body, "sql") || strings.Contains(body, "closed") {
		t.Errorf("the underlying error reached the browser: %s", body)
	}
}

// TestBootPairingCodeIsMintedOnlyForAnUnpairedInstall is the boot decision the operator reads: a code
// while nothing can sign in, and silence once something can.
func TestBootPairingCodeIsMintedOnlyForAnUnpairedInstall(t *testing.T) {
	s := testServer(t)

	s.pairAtBoot(t.Context())
	if s.pairing.Live() != 1 {
		t.Fatalf("an unpaired install booted with %d live codes; want 1", s.pairing.Live())
	}
	registerTestPasskey(t, s)
	// Forget the boot code, so what a second boot mints is the only thing Live() can be counting.
	s.pairing.RevokeAll()

	s.pairAtBoot(t.Context())
	if s.pairing.Live() != 0 {
		t.Errorf("a paired install announced a fresh code into a log nobody is reading")
	}
}

// TestBootPairingURLNamesAHostThatCanCarryACredential: the printed URL is where an operator without a
// scannable terminal looks, so it must not point at an address the ceremony will refuse.
func TestBootPairingURLNamesAHostThatCanCarryACredential(t *testing.T) {
	for _, tc := range []struct {
		name       string
		listen     string
		allowed    []string
		tlsOff     bool
		port       int
		wantOrigin string
	}{
		{name: "loopback address becomes localhost", listen: "127.0.0.1", tlsOff: true, port: 3000, wantOrigin: "http://localhost:3000"},
		{name: "ipv6 any", listen: "::", tlsOff: true, port: 3000, wantOrigin: "http://localhost:3000"},
		{name: "wildcard with tls", listen: "0.0.0.0", port: 3000, wantOrigin: "https://localhost:3000"},
		{name: "a pinned host wins", listen: "0.0.0.0", allowed: []string{"mike.example.com"}, port: 8443, wantOrigin: "https://mike.example.com:8443"},
		{name: "the default port is not spelled", listen: "localhost", port: 443, wantOrigin: "https://localhost"},
	} {
		s := testServerWith(t, func(c *startup.Config) {
			c.Listen, c.AllowedHosts, c.TLSDisabled, c.Port = tc.listen, tc.allowed, tc.tlsOff, tc.port
		})
		if got := s.bootPairingOrigin(); got != tc.wantOrigin {
			t.Errorf("%s: origin = %q; want %q", tc.name, got, tc.wantOrigin)
		}
		if _, err := auth.NewRelyingHost(s.bootPairingAuthority(), !tc.tlsOff); err != nil {
			t.Errorf("%s: the printed URL points at a host that cannot carry a passkey: %v", tc.name, err)
		}
	}
}

// credentialID is the wire form a test asks a route for by id.
func credentialID(credential *testwebauthn.Credential) string {
	return auth.EncodeCredentialID(credential.ID())
}

// sessionCSRF reads the CSRF token a session was issued, the way the app does on first paint.
func sessionCSRF(t *testing.T, s *Server, cookie string) string {
	t.Helper()
	body := mustJSON[map[string]any](t, testResponse(t, s,
		withAuth(testRequest(http.MethodGet, "/api/auth/session", nil), cookie)).Body)
	token, _ := body["csrf_token"].(string)
	if token == "" {
		t.Fatal("the session answer carried no CSRF token")
	}
	return token
}

// TestUnnamedDeviceIsLabelledByWhatItSaidItIs pins the reading of a User-Agent whose product tokens are
// cosmetic: a headless Chromium pairs as Chrome, not as the `Safari/537` engine token it also carries, and
// Safari's release comes from the `Version/` token rather than from its engine number.
func TestUnnamedDeviceIsLabelledByWhatItSaidItIs(t *testing.T) {
	cases := []struct{ userAgent, want string }{
		{"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) HeadlessChrome/141.0.7390.37 Safari/537.36", "HeadlessChrome 141 · X11"},
		{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36", "Chrome 141 · Macintosh"},
		{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.4 Safari/605.1.15", "Safari 17 · Macintosh"},
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:130.0) Gecko/20100101 Firefox/130.0", "Firefox 130 · Windows NT 10.0"},
		{"Mozilla/5.0 (iPhone; CPU iPhone OS 18_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/141.0.7390.35 Mobile/15E148 Safari/604.1", "CriOS 141 · iPhone"},
		{"", "Unknown device"},
	}
	for _, tc := range cases {
		if got := deviceLabel(tc.userAgent); got != tc.want {
			t.Errorf("deviceLabel(%q) = %q, want %q", tc.userAgent, got, tc.want)
		}
	}
}
