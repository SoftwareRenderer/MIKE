package extensions

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// The probe sends a tool's own declared request to an address a person typed but has not saved.

// Shorter than the per-tool bound: a dialog is waiting on this answer.
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

// The refusal carries the declaration, because the declaration is the only thing the probe may send: it
// may dial only an address one of this extension's own tools would send to.
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

// Every rejection is an answer in the body, not a status code: the dialog shows a refused probe and a
// failed probe in the same slot.
func runProbe(ctx context.Context, ext *Extension, tool *ToolConfig, req testURLRequest) testURLResponse {
	key := strings.TrimSpace(req.Key)
	target := strings.TrimSpace(req.URL)
	host, _, err := ParseTargetURL(target)
	if err != nil {
		return rejectedProbe(fmt.Sprintf("address %s: %v", key, err))
	}
	probing := ext.WithOverlayConfig(req.Config, map[string]any{key: target})
	request, err := tool.FillRequest(probeArgs(tool), probing.Config)
	if err != nil {
		return rejectedProbe(err.Error())
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
		return probeFailure(host, err)
	}
	return classifyProbe(tool, result)
}

func rejectedProbe(message string) testURLResponse {
	return testURLResponse{Class: "unusable", Message: message}
}

// WithOverlayConfig is the stored row with the form's current values on top. The row is untouched.
func (e *Extension) WithOverlayConfig(overlay map[string]any, origin map[string]any) *Extension {
	if e == nil {
		return nil
	}
	copied := *e
	copied.Config = map[string]any{}
	for k, v := range e.Config {
		copied.Config[k] = v
	}
	for k, v := range overlay {
		if e.Manifest.ConfigSchema.GetProperty(k) == nil {
			continue
		}
		copied.Config[k] = v
	}
	for k, v := range origin {
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
		// An enum's values belong to the extension, so nothing generic can pick one.
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

// No route, nothing listening and a TLS failure are one class: the fix is the same.
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
	// Renders the body first: an answer that cannot render cannot be used.
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

// The class of an answer that cannot be used: what the upstream's status and content type say, or
// `fallback`. Answering in HTML needs a settings change whatever the status said, so that class wins.
func answerProblemClass(result *EgressResult, fallback string) string {
	if class := failureClass(result); class != "" {
		return class
	}
	return fallback
}
