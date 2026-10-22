package extensions

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"mike/backend/types"
	"mike/pkg/logging"
)

// The probe sends a tool's own declared request to an address a person typed but has not saved.

// Shorter than per-tool bounds because a dialog waits on this answer.
const probeTimeout = 15 * time.Second

// The probe's query text does not matter; the round trip, the address and the answer shape do.
const probeArgument = "hello world"

// The form's current values, so the probe tests what is typed rather than only what is stored.
type testURLRequest struct {
	Key string `json:"key"`
	// The typed address. It grants this one call, saved or not.
	URL    string         `json:"url"`
	Config map[string]any `json:"config"`
}

// A class the dialog colours, and one sentence for a person to act on.
type testURLResponse struct {
	OK      bool   `json:"success"`
	Class   string `json:"class"`
	Message string `json:"message"`
	Status  int    `json:"status,omitempty"`
	Results int    `json:"results,omitempty"`
}

// The probe may only dial addresses that the extension's own tools would send to.
func probeTarget(ext *Extension, key string) (*ToolConfig, string) {
	if key == "" {
		return nil, "key is required"
	}
	prop := ext.Manifest.ConfigSchema.GetProperty(key)
	if prop == nil {
		return nil, fmt.Sprintf("config key %q is not a property of this extension", key)
	}
	if prop.Type != SchemaTypeURL {
		return nil, fmt.Sprintf("config key %q is a %q property, not an address", key, prop.Type)
	}
	tool, ok := toolForOriginProperty(ext, key)
	if !ok {
		return nil, fmt.Sprintf("no tool this extension contributes sends a request to the %s address", key)
	}
	return tool, ""
}

// Rejections are returned in the response body so the dialog can display them in a consistent slot.
func runProbe(ctx context.Context, ext *Extension, tool *ToolConfig, req testURLRequest) testURLResponse {
	key := strings.TrimSpace(req.Key)
	target := strings.TrimSpace(req.URL)
	host, _, err := ParseTargetURL(target)
	if err != nil {
		return rejectedProbe(fmt.Sprintf("address %s: %v", key, err))
	}
	probing, withheld := probeConfig(ext, key, host, target, req.Config)
	request, err := tool.FillRequest(probeArgs(tool), probing.Config)
	if err != nil {
		return noteWithheld(rejectedProbe(err.Error()), withheld)
	}
	// The grant is the address being tested, never a stored one.
	egress := EgressRequest{
		URL:          request.URL,
		Method:       request.Method,
		GrantedHosts: []string{host},
		Timeout:      probeTimeout,
		Source:       "probe:" + tool.Name,
	}
	if request.Method == "POST" {
		egress.Form = request.Query
	}
	result, err := Egress(ctx, probing, egress)
	if err != nil {
		return noteWithheld(probeFailure(host, err), withheld)
	}
	return noteWithheld(classifyProbe(tool, result), withheld)
}

// withheldSecretNotice tells the person the verdict was computed without the credential they expected.
const withheldSecretNotice = "A saved credential was not sent, because this address names a different " +
	"host than the saved address does; re-enter the credential to test this address with it."

func noteWithheld(answer testURLResponse, withheld bool) testURLResponse {
	if !withheld {
		return answer
	}
	answer.Message = withheldSecretNotice + " " + answer.Message
	return answer
}

// probeConfig is the stored row as the probe sees it: the form's values on top, and the row's
// credentials only for the address they were saved with.
func probeConfig(ext *Extension, key, typedHost, target string, form map[string]any) (*Extension, bool) {
	values := make(map[string]any, len(form)+1)
	for name, value := range form {
		if text, isStr := value.(string); isStr && text == types.MaskedSecret && ext.SensitiveProperty(name) {
			// The mask is the browser's placeholder for the stored value, not a credential.
			continue
		}
		values[name] = value
	}
	// The address under test is never withheld, sensitive or not: there is no probe to send without it.
	values[key] = target
	probing := ext.withOverlayConfig(values)

	// Credentials belong to the saved address, and with none saved they belong nowhere.
	storedHost := ""
	if raw, isStr := ext.Config[key].(string); isStr {
		storedHost = types.URLHost(raw)
	}
	send := types.SameHost(storedHost, typedHost)

	withheld := false
	for name, value := range ext.Config {
		text, isStr := value.(string)
		// Everything in the overlay came from the person, who may send their own values wherever they chose.
		if _, fromForm := values[name]; fromForm || !ext.SensitiveProperty(name) || !isStr || strings.TrimSpace(text) == "" {
			continue
		}
		if send {
			// The log records that a stored credential left the process, but never the value.
			logging.For(logging.Extensions).Infof("extension=%s stored config %q sent to %s", ext.ID, name, typedHost)
			continue
		}
		delete(probing.Config, name)
		logging.For(logging.Extensions).Infof("extension=%s stored config %q withheld from %s, which is not the saved host %s",
			ext.ID, name, typedHost, storedHost)
		withheld = true
	}
	return probing, withheld
}

func rejectedProbe(message string) testURLResponse {
	return testURLResponse{Class: "unusable", Message: message}
}

// withOverlayConfig is the stored row with the form's values on top, and the row itself untouched. A key
// the manifest does not declare is dropped, so a form cannot invent a configuration property.
func (e *Extension) withOverlayConfig(values map[string]any) *Extension {
	if e == nil {
		return nil
	}
	copied := *e
	copied.Config = map[string]any{}
	for k, v := range e.Config {
		copied.Config[k] = v
	}
	for k, v := range values {
		if e.Manifest.ConfigSchema.GetProperty(k) == nil {
			continue
		}
		copied.Config[k] = v
	}
	return &copied
}

// Uses the extension's own tool declaration rather than a made-up request.
func toolForOriginProperty(ext *Extension, key string) (*ToolConfig, bool) {
	tools, _ := ext.UsableContributedTools()
	for i := range tools {
		if tools[i].OriginProperty() == key {
			return &tools[i], true
		}
	}
	return nil, false
}

// Only the required parameters are filled; the optional ones stay out.
func probeArgs(tool *ToolConfig) map[string]any {
	args := map[string]any{}
	props, _ := tool.Parameters["properties"].(map[string]any)
	required, _ := tool.Parameters["required"].([]any)
	for _, entry := range required {
		name, _ := entry.(string)
		def, ok := props[name].(map[string]any)
		if !ok {
			continue
		}
		// Enum values belong to the extension and cannot be guessed.
		if _, hasEnum := def["enum"]; hasEnum {
			continue
		}
		switch def["type"] {
		case "string":
			args[name] = probeArgument
		case "integer", "number":
			args[name] = 1
		}
	}
	return args
}

// Network and TLS failures are grouped as they require the same fix.
func probeFailure(host string, err error) testURLResponse {
	switch {
	case errors.Is(err, ErrEgressForbidden):
		return testURLResponse{Class: "forbidden", Message: err.Error()}
	case errors.Is(err, ErrEgressUnusable):
		return testURLResponse{Class: "unusable", Message: strings.TrimPrefix(err.Error(), ErrEgressUnusable.Error()+": ")}
	}
	cause := strings.TrimPrefix(err.Error(), ErrEgressUpstream.Error()+": ")
	return testURLResponse{
		Class:   "unreachable",
		Message: fmt.Sprintf("Nothing answered at %s — %s. Check the address, and that the service listens on an interface this server can reach.", host, cause),
	}
}

// "reachable" means the probe got what a real call would have got.
func classifyProbe(tool *ToolConfig, result *EgressResult) testURLResponse {
	if !result.OK() {
		return testURLResponse{
			Class:   answerProblemClass(result, "status"),
			Message: fmt.Sprintf("Reached it, and it answered HTTP %d%s.", result.StatusCode, FailureHint(result)),
			Status:  result.StatusCode,
		}
	}
	// An answer that cannot be rendered is unusable.
	if _, err := tool.Response.RenderAnswer(result.Body, result.IsJSON()); err != nil {
		return testURLResponse{
			Class:   answerProblemClass(result, "unexpected_shape"),
			Message: err.Error(),
			Status:  result.StatusCode,
		}
	}
	count := tool.Response.CountResults(result.Body)
	if count == 0 {
		return testURLResponse{
			Class: "empty", Status: result.StatusCode,
			Message: "Reached it and it answered in the shape the extension declares, with no results. On a search instance that is usually its engines failing, not an empty index.",
		}
	}
	plural := "s"
	if count == 1 {
		plural = ""
	}
	return testURLResponse{
		OK: true, Class: "reachable", Status: result.StatusCode, Results: count,
		Message: fmt.Sprintf("Reached it — %d result%s for the probe query.", count, plural),
	}
}

// The problem class is determined by the upstream's status and content type, with HTML answers taking priority.
func answerProblemClass(result *EgressResult, fallback string) string {
	if class := failureClass(result); class != "" {
		return class
	}
	return fallback
}
