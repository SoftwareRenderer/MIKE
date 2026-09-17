package httputil

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
)

// A request whose URL someone else chose. client.go decides how it may be dialled; this decides how it is
// made: one attempt, the caller's headers, the answer read under the upstream cap.

type OutboundRequest struct {
	Method string
	URL    string
	// Set as given. A header wanted only conditionally is left out rather than sent empty.
	Headers map[string]string
	// Sent as-is. An empty ContentType labels a nil body as JSON.
	Body        io.Reader
	ContentType string
	// Zero leaves only ctx bounding the call.
	Timeout time.Duration
	// GrantedHosts are hosts the operator named. Naming one is the only way a loopback address is dialled.
	GrantedHosts []string
}

type OutboundAnswer struct {
	StatusCode  int
	ContentType string
	Body        []byte
}

// DoOutbound makes one attempt, under the egress address rule. Redirects are not followed: the permission
// covers the URL the caller named, and a 3xx would carry the request, and any credential on it, to a host
// nothing validated.
func DoOutbound(ctx context.Context, r OutboundRequest) (*OutboundAnswer, error) {
	return DoOutboundWith(ctx, NewExtensionEgressClientGranting(r.Timeout, r.GrantedHosts), r)
}

// DoOutboundWith makes the call with a client the caller chose.
func DoOutboundWith(ctx context.Context, client *http.Client, r OutboundRequest) (*OutboundAnswer, error) {
	method := strings.ToUpper(strings.TrimSpace(r.Method))
	if method == "" {
		method = http.MethodGet
	}
	req, err := http.NewRequestWithContext(ctx, method, r.URL, r.Body)
	if err != nil {
		return nil, err
	}
	for k, v := range r.Headers {
		req.Header.Set(k, v)
	}
	contentType := r.ContentType
	if contentType == "" {
		contentType = "application/json"
	}
	req.Header.Set("Content-Type", contentType)
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := ReadUpstream(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%w", err)
	}
	return &OutboundAnswer{StatusCode: resp.StatusCode, ContentType: resp.Header.Get("Content-Type"), Body: body}, nil
}

func (a *OutboundAnswer) OK() bool { return a.StatusCode >= 200 && a.StatusCode < 300 }

func (a *OutboundAnswer) RateLimited() bool { return a.StatusCode == http.StatusTooManyRequests }

// RefusedForCredentials reports a refusal outright or a lack of authority.
func (a *OutboundAnswer) RefusedForCredentials() bool {
	return a.StatusCode == http.StatusForbidden || a.StatusCode == http.StatusUnauthorized
}

func (a *OutboundAnswer) IsJSON() bool { return IsJSONContentType(a.ContentType) }

// IsJSONContentType is the one reading of an upstream content type, so two readers cannot disagree.
func IsJSONContentType(raw string) bool {
	media, _, err := mime.ParseMediaType(raw)
	if err != nil {
		return false
	}
	return media == "application/json" || strings.HasSuffix(media, "+json")
}
