package mcp

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"mike/backend/types"
)

// The Test action answers over the cache and writes nothing to storage: the row it dials is the form's
// current values, so an unsaved URL and an unsaved token are both exercised. Its transport is
// `POST /api/mcp/test`, so this package names no net/http type.

// TestRequest is what the Test action is asked about. A header whose value is types.MaskedSecret asks
// for the stored one.
type TestRequest struct {
	// ID is the registered row this draft came from, empty for a server never saved; a masked header
	// resolves against it.
	ID             string       `json:"id"`
	Name           string       `json:"name"`
	URL            string       `json:"url"`
	TimeoutSeconds int          `json:"timeout_seconds"`
	Headers        []TestHeader `json:"headers"`
}

// TestHeader is one request header as the form holds it.
type TestHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// TestAnswer is what the action reports: OK to branch on, Kind for which fix applies, Message for the
// person reading it, and the tool counts the card's verdict line is written from.
type TestAnswer struct {
	OK      bool   `json:"ok"`
	Kind    string `json:"kind,omitempty"`
	Message string `json:"message"`
	Tools   int    `json:"tools"`
	Dropped int    `json:"dropped,omitempty"`
}

// ProbeForm dials the server the form describes and lists it. A server that could not be reached is a
// verdict with a kind and a sentence, not an error; only a body there is no point dialling is an error,
// and it names the field the same way a save of that row would.
func (r *Registry) ProbeForm(ctx context.Context, body TestRequest) (TestAnswer, error) {
	server, err := r.probeTarget(body)
	if err != nil {
		return TestAnswer{}, err
	}

	tools, dropped, probeErr := r.Probe(ctx, server)
	if ctx.Err() != nil {
		// The caller gave up; anything the dial answered afterwards reaches nobody.
		return TestAnswer{}, ctx.Err()
	}
	answer := TestAnswer{Tools: len(tools), Dropped: dropped}
	if probeErr != nil {
		answer.Kind = string(failureKind(probeErr))
		answer.Message = probeErr.Error()
		return answer, nil
	}
	answer.OK = true
	return answer, nil
}

// probeTarget is the form's row as a registered server. A masked header is resolved from the registry
// rather than the request, so a saved token is exercised without the browser holding it.
func (r *Registry) probeTarget(body TestRequest) (types.MCPServer, error) {
	if strings.TrimSpace(body.URL) == "" {
		return types.MCPServer{}, errors.New("url is required")
	}
	server := types.MCPServer{
		ID:             strings.TrimSpace(body.ID),
		Name:           strings.TrimSpace(body.Name),
		URL:            strings.TrimRight(strings.TrimSpace(body.URL), "/"),
		Enabled:        true,
		TimeoutSeconds: body.TimeoutSeconds,
	}
	if server.Name == "" {
		server.Name = reportName(server.URL)
	}
	headers := make([]types.MCPHeader, 0, len(body.Headers))
	for _, h := range body.Headers {
		headers = append(headers, types.MCPHeader{Name: strings.TrimSpace(h.Name)})
	}
	if err := types.ValidateMCPServer(server.Name, server.URL, body.TimeoutSeconds, headers); err != nil {
		return types.MCPServer{}, err
	}

	stored := r.storedHeaders(server.ID)
	for _, h := range body.Headers {
		name := strings.TrimSpace(h.Name)
		if name == "" {
			continue
		}
		value := h.Value
		if value == types.MaskedSecret {
			value = stored[strings.ToLower(name)]
		}
		if value == "" {
			continue
		}
		keep := value
		server.Headers = append(server.Headers, types.MCPHeader{Name: name, Value: &keep})
	}
	return server, nil
}

// storedHeaders is one registered server's header values, opened, by lower-cased name. An unregistered
// id has none, so a masked value there sends nothing.
func (r *Registry) storedHeaders(id string) map[string]string {
	if id == "" {
		return nil
	}
	servers, err := r.registered()
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for _, s := range servers {
		if s.ID != id {
			continue
		}
		for _, h := range s.Headers {
			if h.Value != nil {
				out[strings.ToLower(h.Name)] = *h.Value
			}
		}
	}
	return out
}

// reportName attributes a failure to the address the person typed when the form has no name yet.
func reportName(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return "the server"
}

// failureKind is the classification a caller answers with. A failure that is not one of ours is
// reported as the server not answering, which is what the person will find.
func failureKind(err error) Failure {
	var failure *Error
	if errors.As(err, &failure) && failure != nil {
		return failure.Kind
	}
	return FailureUnreachable
}
