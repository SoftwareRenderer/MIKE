package httputil

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"strings"
	"time"
)

// A request to an external URL. This defines how the request is constructed:
// one attempt, using caller-provided headers and reading the response under the upstream size limit.

type OutboundRequest struct {
	Method string
	URL    string
	// Headers to include. Conditionally needed headers should be omitted rather than sent empty.
	Headers map[string]string
	// Request body. If ContentType is empty, a nil body is treated as JSON.
	Body        io.Reader
	ContentType string
	// If zero, only the context bounds the call.
	Timeout time.Duration
	// Hosts explicitly allowed by the operator. Loopback addresses are only dialed if explicitly granted.
	GrantedHosts []string
}

type OutboundAnswer struct {
	StatusCode  int
	ContentType string
	Body        []byte
}

// DoOutbound makes one attempt to the specified URL. Redirects are not followed to
// prevent credentials from being sent to an unvalidated host.
func DoOutbound(ctx context.Context, r OutboundRequest) (*OutboundAnswer, error) {
	return DoOutboundWith(ctx, NewExtensionEgressClientGranting(r.Timeout, r.GrantedHosts), r)
}

// DoOutboundWith makes the call using a provided http.Client.
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

func (a *OutboundAnswer) OK() bool          { return a.StatusCode >= 200 && a.StatusCode < 300 }
func (a *OutboundAnswer) RateLimited() bool { return a.StatusCode == http.StatusTooManyRequests }

// ErrDownloadTooLarge indicates the response body exceeded the size limit.
var ErrDownloadTooLarge = errors.New("download exceeds its limit")

// DownloadToFileGranting streams a GET request to dest using a client that allows
// the specified hosts, returning the byte count and SHA256 digest.
func DownloadToFileGranting(ctx context.Context, rawURL, dest string, limit int64, grantedHosts []string) (int64, string, error) {
	client := NewExtensionEgressClientGranting(0, grantedHosts)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, "", fmt.Errorf("%s answered %s", rawURL, resp.Status)
	}

	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()

	hasher := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, hasher), io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return 0, "", err
	}
	if n > limit {
		return 0, "", fmt.Errorf("%w: %s sent more than %d bytes", ErrDownloadTooLarge, rawURL, limit)
	}
	return n, hex.EncodeToString(hasher.Sum(nil)), nil
}

// RefusedForCredentials returns true if the response indicates a lack of authority.
func (a *OutboundAnswer) RefusedForCredentials() bool {
	return a.StatusCode == http.StatusForbidden || a.StatusCode == http.StatusUnauthorized
}

func (a *OutboundAnswer) IsJSON() bool { return IsJSONContentType(a.ContentType) }

// IsJSONContentType provides a single point of truth for identifying JSON content types.
func IsJSONContentType(raw string) bool {
	media, _, err := mime.ParseMediaType(raw)
	if err != nil {
		return false
	}
	return media == "application/json" || strings.HasSuffix(media, "+json")
}
