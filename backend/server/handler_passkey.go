package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/go-webauthn/webauthn/protocol"

	"mike/backend/auth"
	"mike/backend/httputil"
	"mike/backend/store"
)

// The passkey surface. A pairing code authorizes a registration, the registration opens the session on the
// browser that completed it, and every later sign-in is an assertion. The code never authenticates
// anything: docs/passkeys.md owns the reason.

const (
	// maxCeremonyBodyBytes bounds a ceremony request body. A real one — client data plus an attestation
	// object, or a signature — is a couple of kilobytes, and the verifier reads the body it is handed with
	// no bound of its own, so the bound goes on the reader here.
	maxCeremonyBodyBytes = 64 << 10

	// maxPasskeyNameLen is the device label's cap. It is a line in a settings list, not a document.
	maxPasskeyNameLen = 64
)

// A body that does not parse as a credential response is the person's browser having sent something the
// ceremony cannot use, so it is refused as a failed ceremony rather than answered with the parser's words.
func refusedBody(w http.ResponseWriter, operation string, err error) {
	refuseCeremony(w, operation, fmt.Errorf("%w: %v", auth.ErrCeremonyRefused, err))
}

// relyingHost is the (rpID, origin) pair this request's address implies. A credential is bound to it, so
// the same install reached at another name is a different set of passkeys.
func (s *Server) relyingHost(r *http.Request) (auth.RelyingHost, error) {
	return auth.NewRelyingHost(r.Host, requestIsHTTPS(r, s.settings.TrustProxy))
}

// refuseCeremony answers a ceremony that did not run. What the caller can act on — an expired ticket, an
// address that cannot carry passkeys, a signature that did not verify — gets MIKE's own sentence;
// everything else is a server fault and goes through the one place a status is chosen, with the cause left
// in the log rather than in the response.
func refuseCeremony(w http.ResponseWriter, operation string, err error) {
	switch {
	case errors.Is(err, auth.ErrTicket):
		JSONError(w, http.StatusUnauthorized, "the ceremony has expired; start it again")
	case errors.Is(err, auth.ErrNoPasskeyForHost):
		JSONError(w, http.StatusNotFound, "no passkey is registered for this address; pair this device with a code")
	case errors.Is(err, auth.ErrHostCannotHostPasskeys):
		JSONError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, auth.ErrCeremonyRefused):
		JSONError(w, http.StatusUnauthorized, "the passkey did not verify")
	default:
		httputil.Fault(w, operation, err)
	}
}

// ceremonyRequestPayload is the envelope both ceremony halves are posted in. `response` is the browser's
// credential, verbatim: it is signed bytes, and a client that had to reshape it would be a client that
// could not complete the ceremony.
type ceremonyRequestPayload struct {
	Ticket   string          `json:"ticket"`
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response"`
}

// decodeCeremonyRequest reads the envelope within the bound the verifier needs applied before its bytes
// are read.
func decodeCeremonyRequest(w http.ResponseWriter, r *http.Request) (ceremonyRequestPayload, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxCeremonyBodyBytes)
	var req ceremonyRequestPayload
	if !httputil.DecodeJSON(w, r, &req) {
		return req, false
	}
	if req.Ticket == "" || len(req.Response) == 0 {
		JSONError(w, http.StatusBadRequest, "ticket and response are required")
		return req, false
	}
	return req, true
}

// handlePasskeyRegisterBegin handles POST /api/auth/passkey/register/begin.
//
// The pairing code is spent here, before any options are issued: one code begins one registration, and a
// code that could begin twice could pair two devices.
func (s *Server) handlePasskeyRegisterBegin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxCeremonyBodyBytes)
	if !httputil.DecodeJSON(w, r, &req) {
		return
	}
	// The host is checked before the code is spent: a code burned by an address that cannot carry
	// passkeys would have to be minted again for nothing.
	host, err := s.relyingHost(r)
	if err != nil {
		refuseCeremony(w, "passkey registration", err)
		return
	}
	if err := s.pairing.Consume(req.Code); err != nil {
		JSONError(w, http.StatusUnauthorized, "pairing code is not valid")
		return
	}
	options, ticket, err := s.passkeys.BeginRegistration(r.Context(), host)
	if err != nil {
		refuseCeremony(w, "begin passkey registration", err)
		return
	}
	JSONResponse(w, http.StatusOK, map[string]any{"ticket": ticket, "options": options})
}

// handlePasskeyRegisterFinish handles POST /api/auth/passkey/register/finish.
//
// A finished registration is proof that this browser holds a new key and that the person verified
// themselves to it, which is why the session is opened here — the pairing code bought the ceremony, not
// the session.
func (s *Server) handlePasskeyRegisterFinish(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeCeremonyRequest(w, r)
	if !ok {
		return
	}
	host, err := s.relyingHost(r)
	if err != nil {
		refuseCeremony(w, "passkey registration", err)
		return
	}
	name, ok := passkeyName(w, req.Name, r.UserAgent())
	if !ok {
		return
	}
	credential, err := protocol.ParseCredentialCreationResponseBytes(req.Response)
	if err != nil {
		refusedBody(w, "register passkey", err)
		return
	}
	if _, err := s.passkeys.FinishRegistration(r.Context(), host, req.Ticket, credential, name); err != nil {
		refuseCeremony(w, "register passkey", err)
		return
	}
	s.openSession(w, r)
}

// handlePasskeyAssertBegin handles POST /api/auth/passkey/assert/begin. It offers only this host's
// credentials, so a key that could not be accepted here is not what the person is prompted for.
func (s *Server) handlePasskeyAssertBegin(w http.ResponseWriter, r *http.Request) {
	host, err := s.relyingHost(r)
	if err != nil {
		refuseCeremony(w, "passkey sign-in", err)
		return
	}
	options, ticket, err := s.passkeys.BeginAssertion(r.Context(), host)
	if err != nil {
		refuseCeremony(w, "begin passkey sign-in", err)
		return
	}
	JSONResponse(w, http.StatusOK, map[string]any{"ticket": ticket, "options": options})
}

// handlePasskeyAssertFinish handles POST /api/auth/passkey/assert/finish.
func (s *Server) handlePasskeyAssertFinish(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeCeremonyRequest(w, r)
	if !ok {
		return
	}
	host, err := s.relyingHost(r)
	if err != nil {
		refuseCeremony(w, "passkey sign-in", err)
		return
	}
	assertion, err := protocol.ParseCredentialRequestResponseBytes(req.Response)
	if err != nil {
		refusedBody(w, "passkey sign-in", err)
		return
	}
	if _, err := s.passkeys.FinishAssertion(r.Context(), host, req.Ticket, assertion); err != nil {
		refuseCeremony(w, "passkey sign-in", err)
		return
	}
	s.openSession(w, r)
}

// openSession is the answer both ceremonies end with: a session cookie and the CSRF token the next write
// will need. A session is state this leaves behind, so the write runs detached and a tab closing halfway
// through does not decide whether the person is signed in (sideeffects.go).
func (s *Server) openSession(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.Detached()
	defer cancel()
	_, token, csrfToken, err := s.sessionMgr.StartSession(ctx, clientIP(r, s.settings.TrustProxy), r.UserAgent())
	if err != nil {
		httputil.Fault(w, "open session", err)
		return
	}
	s.setSessionCookie(w, r, token, sessionMaxAge)
	w.Header().Set("X-CSRF-Token", csrfToken)
	JSONResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handlePairingMint handles POST /api/auth/pairing. A signed-in device decides that another one may
// register; the code it gets back authorizes exactly that.
func (s *Server) handlePairingMint(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireSession(w, r); !ok {
		return
	}
	host, err := s.relyingHost(r)
	if err != nil {
		refuseCeremony(w, "pair a device", err)
		return
	}
	code, err := s.pairing.Mint(auth.DevicePairingTTL)
	if err != nil {
		if errors.Is(err, auth.ErrTooManyPairingCodes) {
			JSONError(w, http.StatusConflict, "too many pairing codes are waiting; revoke them and try again")
			return
		}
		httputil.Fault(w, "mint pairing code", err)
		return
	}
	url := auth.PairingURL(host.Origin, code.Code)
	qr, err := auth.PairingQRPNG(url)
	if err != nil {
		httputil.Fault(w, "render pairing code", err)
		return
	}
	JSONResponse(w, http.StatusOK, map[string]any{
		"code":       auth.FormatPairingCode(code.Code),
		"url":        url,
		"qr_png":     qr,
		"expires_at": code.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

// handlePairingRevoke handles DELETE /api/auth/pairing: every code this server has minted and not spent
// stops working. The passkeys themselves are untouched — that is what revoking a device is for.
func (s *Server) handlePairingRevoke(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireSession(w, r); !ok {
		return
	}
	s.pairing.RevokeAll()
	JSONResponse(w, http.StatusOK, map[string]any{"revoked": true})
}

// passkeyResponse is one row of GET /api/auth/passkeys. The credential record — the key and the bytes
// that verify it — has no field here and gets none.
type passkeyResponse struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
	LastUsed  string `json:"last_used"`
}

// handlePasskeysList handles GET /api/auth/passkeys.
func (s *Server) handlePasskeysList(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireSession(w, r); !ok {
		return
	}
	rows, err := s.store.ListPasskeys(r.Context())
	if err != nil {
		httputil.Fault(w, "list passkeys", err)
		return
	}
	out := make([]passkeyResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, passkeyResponse{
			ID:        row.CredentialID,
			Name:      row.Name,
			CreatedAt: formatAuthTime(row.CreatedAt),
			LastUsed:  formatAuthTime(row.LastUsedAt),
		})
	}
	JSONResponse(w, http.StatusOK, out)
}

// handlePasskeyRename handles PATCH /api/auth/passkeys/{id}. A partial update on pointer fields:
// absent means leave alone, present means write, and a wrong type is named rather than written.
func (s *Server) handlePasskeyRename(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireSession(w, r); !ok {
		return
	}
	var req struct {
		Name *string `json:"name"`
	}
	if !httputil.DecodeJSON(w, r, &req) {
		return
	}
	if req.Name == nil {
		JSONError(w, http.StatusBadRequest, "name is required")
		return
	}
	name, ok := cleanPasskeyName(*req.Name)
	if !ok {
		JSONError(w, http.StatusBadRequest, "name must be 1 to 64 characters")
		return
	}
	ctx, cancel := s.Detached()
	defer cancel()
	renamed, err := s.store.RenamePasskey(ctx, r.PathValue("id"), name)
	if err != nil {
		httputil.Fault(w, "rename passkey", err)
		return
	}
	if !renamed {
		JSONError(w, http.StatusNotFound, "no passkey has that id")
		return
	}
	JSONResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handlePasskeyRevoke handles DELETE /api/auth/passkeys/{id}. The last remaining passkey cannot be
// revoked from here: a server nobody can sign in to is only repairable from the command line, so the GUI
// cannot be the thing that does it (store.ErrLastPasskey).
func (s *Server) handlePasskeyRevoke(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireSession(w, r); !ok {
		return
	}
	ctx, cancel := s.Detached()
	defer cancel()
	revoked, err := s.store.DeletePasskey(ctx, r.PathValue("id"))
	switch {
	case errors.Is(err, store.ErrLastPasskey):
		JSONError(w, http.StatusConflict, "this is the last passkey; pair another device first, or run mike --reset-passkeys")
		return
	case err != nil:
		httputil.Fault(w, "revoke passkey", err)
		return
	case !revoked:
		JSONError(w, http.StatusNotFound, "no passkey has that id")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// passkeyName is the label a freshly registered credential gets: what the person typed if they typed
// anything, and otherwise what the browser said it was.
func passkeyName(w http.ResponseWriter, typed, userAgent string) (string, bool) {
	if strings.TrimSpace(typed) == "" {
		return deviceLabel(userAgent), true
	}
	name, ok := cleanPasskeyName(typed)
	if !ok {
		JSONError(w, http.StatusBadRequest, "name must be 1 to 64 characters")
		return "", false
	}
	return name, true
}

// cleanPasskeyName is the one rule for a device label: control characters out, whitespace collapsed, and
// between one and maxPasskeyNameLen characters. It is a list line, so a name that needs more is refused
// rather than silently cut — the person would not be able to find the row they meant.
func cleanPasskeyName(raw string) (string, bool) {
	collapsed := strings.Join(strings.FieldsFunc(raw, func(r rune) bool {
		return unicode.IsSpace(r) || !unicode.IsPrint(r)
	}), " ")
	if collapsed == "" || len([]rune(collapsed)) > maxPasskeyNameLen {
		return "", false
	}
	return collapsed, true
}

// deviceLabel is what a person sees for a device they paired without naming. The User-Agent is the only
// thing the browser volunteered, and its product tokens are half theatre: a WebKit browser ends with
// `Safari/<engine version>` whatever it is, and Safari's own release rides the `Version/` token. So the
// engine token is only believed when nothing else said who the browser is, and Safari's release is read
// from where the browser actually writes it.
func deviceLabel(userAgent string) string {
	platform := ""
	if start, end := strings.Index(userAgent, "("), strings.Index(userAgent, ")"); start >= 0 && end > start+1 {
		platform = strings.TrimSpace(strings.Split(userAgent[start+1:end], ";")[0])
	}
	agent, version, safariRelease := "", "", ""
	for _, field := range strings.Fields(userAgent) {
		product, value, found := strings.Cut(field, "/")
		if !found {
			continue
		}
		major, _, _ := strings.Cut(value, ".")
		switch product {
		case "Version":
			safariRelease = major
		case "Chrome", "HeadlessChrome", "CriOS", "FxiOS", "Firefox", "Edg", "OPR", "DuckDuckGo":
			agent, version = product, major
		case "Safari":
			if agent == "" {
				agent = product
			}
		}
	}
	switch {
	case agent == "":
		return firstOf(platform, "Unknown device")
	case agent == "Safari" && safariRelease != "":
		version = safariRelease
	case agent == "Edg":
		agent = "Edge"
	case agent == "OPR":
		agent = "Opera"
	}
	if version == "" {
		return firstOf(agent, platform)
	}
	if platform == "" {
		return agent + " " + version
	}
	return agent + " " + version + " · " + platform
}

// firstOf is the first value that says something.
func firstOf(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
