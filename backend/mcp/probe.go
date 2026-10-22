package mcp

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"mike/backend/types"
	"mike/pkg/logging"
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
	server, withheld, err := r.probeTarget(body)
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
		if withheld {
			// A server that was never offered the credential needs a different fix from one that
			// rejected it, and the status code cannot tell the two apart.
			answer.Message = withheldTokenNotice + " " + answer.Message
		}
		return answer, nil
	}
	answer.OK = true
	if withheld {
		answer.Message = withheldTokenNotice
	}
	return answer, nil
}

// withheldTokenNotice tells the person the probe ran without the credential they expected.
const withheldTokenNotice = "A saved token was not sent, because this address names a different host " +
	"than the saved server; re-enter the token to test this address with it."

// probeTarget is the form's row as a registered server. A masked header comes from the registry, never
// the request, and is spent only on the host that row names. withheld says a header was held back.
func (r *Registry) probeTarget(body TestRequest) (types.MCPServer, bool, error) {
	if strings.TrimSpace(body.URL) == "" {
		return types.MCPServer{}, false, errors.New("url is required")
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
		return types.MCPServer{}, false, err
	}

	// An unregistered id has no row, so its URL is empty, so no stored value belongs anywhere.
	row := r.storedRow(server.ID)
	storedHost := types.URLHost(row.URL)
	typedHost := types.URLHost(server.URL)

	withheld := false
	for _, h := range body.Headers {
		name := strings.TrimSpace(h.Name)
		if name == "" {
			continue
		}
		value := h.Value
		fromVault := value == types.MaskedSecret
		if fromVault {
			value = storedHeader(row, name)
			if value != "" && !types.SameHost(typedHost, storedHost) {
				logging.For(logging.MCP).Infof("mcp server %s: stored %s header withheld from %s, which is not the saved host %s",
					row.Name, name, typedHost, storedHost)
				withheld = true
				continue
			}
			if value != "" {
				// The log names the server and the host, never the secret.
				logging.For(logging.MCP).Infof("mcp server %s: stored %s header sent to %s", row.Name, name, typedHost)
			}
		}
		if value == "" {
			continue
		}
		keep := value
		server.Headers = append(server.Headers, types.MCPHeader{Name: name, Value: &keep})
	}
	return server, withheld, nil
}

// storedRow is one registered server by id, with its header values opened. An unregistered id has none,
// which reads as the zero row.
func (r *Registry) storedRow(id string) types.MCPServer {
	if id == "" {
		return types.MCPServer{}
	}
	servers, err := r.registered()
	if err != nil {
		return types.MCPServer{}
	}
	for _, s := range servers {
		if s.ID == id {
			return s
		}
	}
	return types.MCPServer{}
}

func storedHeader(row types.MCPServer, name string) string {
	for _, h := range row.Headers {
		if strings.EqualFold(h.Name, name) && h.Value != nil {
			return *h.Value
		}
	}
	return ""
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
