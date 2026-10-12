package extensions

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"mike/backend/httputil"
	"mike/pkg/logging"
)

// The one path an extension's outbound request takes, from the browser-callable proxy route or from a
// tool call. Two permissions apply: the manifest's `allowed_domains`, and the stored `url` properties,
// which are wider and are the only way a non-public address is reached (see
// httputil.NewExtensionEgressClientGranting).

type (
	EgressRequest struct {
		URL    string
		Method string
		Body   string
		// Sent application/x-www-form-urlencoded, which is how a POST tool sends its parameters.
		Form url.Values
		// Allowed beside the manifest's allowlist, and the only hosts a loopback address may be reached at.
		GrantedHosts []string
		Timeout      time.Duration
		Source       string
	}

	EgressResult = httputil.OutboundAnswer
)

var (
	// ErrEgressUnusable is a malformed request this package will not attempt.
	ErrEgressUnusable = errors.New("extension egress refused")
	// ErrEgressForbidden is a well-formed request to a host this extension was never allowed to reach.
	ErrEgressForbidden = errors.New("extension egress not permitted")
	// ErrEgressUpstream is the upstream's fault: no route, no answer, or an answer past the cap.
	ErrEgressUpstream = errors.New("extension egress upstream failed")

	// A variable because the address rule refuses the loopback address an httptest server listens on.
	egressCall = httputil.DoOutbound
)

const defaultEgressTimeout = 30 * time.Second

// Egress verifies if an extension may reach the requested host.
func Egress(ctx context.Context, ext *Extension, req EgressRequest) (*EgressResult, error) {
	if ext == nil {
		return nil, fmt.Errorf("%w: no extension", ErrEgressUnusable)
	}
	network := ext.Manifest.Capabilities.GetNetwork()
	if network == nil {
		return nil, fmt.Errorf("%w: the extension declares no network capability", ErrEgressUnusable)
	}
	method := strings.ToUpper(strings.TrimSpace(req.Method))
	if method == "" {
		method = "GET"
	}

	u, err := url.Parse(strings.TrimSpace(req.URL))
	if err != nil {
		return nil, fmt.Errorf("%w: invalid url: %v", ErrEgressUnusable, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("%w: only http/https URLs are allowed", ErrEgressUnusable)
	}
	if err := allowHost(ext, network, u.Hostname(), req.GrantedHosts); err != nil {
		return nil, err
	}

	body, contentType, err := egressBody(req)
	if err != nil {
		return nil, err
	}
	headers := resolvedEgressHeaders(network.Headers, ext.Config)

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = defaultEgressTimeout
	}
	source := req.Source
	if source == "" {
		source = "proxy"
	}
	logging.For(logging.Extensions).Infof("extension=%s source=%s method=%s url=%s status=pending", ext.Name, source, method, u.String())

	answer, err := egressCall(ctx, httputil.OutboundRequest{
		Method:       method,
		URL:          u.String(),
		Headers:      headers,
		Body:         body,
		ContentType:  contentType,
		Timeout:      timeout,
		GrantedHosts: req.GrantedHosts,
	})
	if err != nil {
		logging.For(logging.Extensions).Warnf("extension=%s source=%s url=%s %v", ext.Name, source, u.String(), err)
		if errors.Is(err, httputil.ErrUpstreamTooLarge) {
			return nil, fmt.Errorf("%w: response too large", ErrEgressUpstream)
		}
		return nil, fmt.Errorf("%w: %v", ErrEgressUpstream, err)
	}
	logging.For(logging.Extensions).Infof("extension=%s source=%s method=%s url=%s status=%d bytes=%d",
		ext.Name, source, method, u.String(), answer.StatusCode, len(answer.Body))
	return answer, nil
}

// failureClass reads an answer's status and content type to ensure
// tool calls and probes report the same failure class.
func failureClass(result *EgressResult) string {
	switch {
	case result == nil:
		return ""
	case !result.IsJSON():
		return "not_json"
	case result.RateLimited():
		return "rate_limited"
	case result.RefusedForCredentials():
		return "refused"
	}
	return ""
}

// One sentence per class, naming what a person can change.
var failureHints = map[string]string{
	"not_json":     " — and it answered in HTML, not JSON, so this instance does not serve the format the extension asks for",
	"rate_limited": " — the instance refused for sending too many requests; its rate limiter is the usual cause",
	"refused":      " — the instance refused the request; check whether it expects an auth token",
}

// FailureHint is the prose form of failureClass, appended to a status code.
func FailureHint(result *EgressResult) string {
	return failureHints[failureClass(result)]
}

// Matches the manifest's allowlist or a configured address. An empty allowlist means the manifest
// declares no third-party API.
func allowHost(ext *Extension, network *NetworkConfig, host string, granted []string) error {
	for _, d := range network.AllowedDomains {
		if d == host {
			return nil
		}
	}
	for _, g := range granted {
		if g == host {
			return nil
		}
	}
	if len(granted) == 0 {
		return fmt.Errorf("%w: domain not in allowed_domains: %s", ErrEgressForbidden, host)
	}
	return fmt.Errorf("%w: %s is neither in allowed_domains nor an address configured for %s", ErrEgressForbidden, host, ext.Name)
}

func egressBody(req EgressRequest) (body *strings.Reader, contentType string, err error) {
	if len(req.Form) > 0 && req.Body != "" {
		return nil, "", fmt.Errorf("%w: a request carries either a form or a body, not both", ErrEgressUnusable)
	}
	if len(req.Form) > 0 {
		return strings.NewReader(req.Form.Encode()), "application/x-www-form-urlencoded", nil
	}
	return strings.NewReader(req.Body), "", nil
}

// A template that resolves to nothing, or to itself because no property backs it, is left off rather than
// sent as a literal "{{token}}".
func resolvedEgressHeaders(templates map[string]string, config map[string]any) map[string]string {
	if len(templates) == 0 {
		return nil
	}
	headers := make(map[string]string, len(templates))
	for k, template := range templates {
		resolved := resolveProxyTemplate(template, config)
		if resolved == "" || resolved == template {
			continue
		}
		headers[k] = resolved
	}
	return headers
}
