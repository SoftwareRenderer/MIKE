package extensions

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// Templates and answer shapes are processed together to ensure consistent manifest interpretation.

type FilledRequest struct {
	Method string
	// The configured origin plus the extension's path; for a GET the parameters are already in it.
	URL   string
	Query url.Values
}

// configuredOrigin ensures consistent validation when offering and running a tool.
func (t *ToolConfig) configuredOrigin(config map[string]any) (address, problem string) {
	key := t.OriginProperty()
	if key == "" {
		return "", "request.url reads no address from the extension's configuration"
	}
	value, err := ConfiguredURLOf(key, config)
	if err != nil {
		return "", err.Error()
	}
	if value == "" {
		return "", fmt.Sprintf("no address is configured — set %s under Settings → Extensions", key)
	}
	return value, ""
}

// FillRequest resolves the tool's origin and parameters; empty parameters are dropped.
func (t *ToolConfig) FillRequest(args map[string]any, config map[string]any) (*FilledRequest, error) {
	base, problem := t.configuredOrigin(config)
	if problem != "" {
		return nil, fmt.Errorf("%s: %s", t.Name, problem)
	}
	_, origin, err := ParseTargetURL(base)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", t.Name, err)
	}

	values, err := t.fillParameters(t.Request.Query, args, config)
	if err != nil {
		return nil, err
	}
	suffix, err := fillTemplate(t.RequestSuffix(), args, config)
	if err != nil {
		return nil, fmt.Errorf("%s: request.url: %w", t.Name, err)
	}

	method := strings.ToUpper(t.Request.Method)
	if method == "" {
		method = "GET"
	}
	target := origin + suffix
	if method != "POST" && len(values) > 0 {
		target += "?" + values.Encode()
	}

	// Verifies the final URL matches the configured origin.
	u, err := url.Parse(target)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", t.Name, err)
	}
	if composeOrigin(u) != origin {
		return nil, fmt.Errorf("%s: the request would leave for %s, not the configured %s", t.Name, composeOrigin(u), origin)
	}
	return &FilledRequest{Method: method, URL: target, Query: values}, nil
}

// Both spellings carry the port explicitly, so the comparison is exact.
func composeOrigin(u *url.URL) string {
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)
}

// fillParameters resolves the declared query entries, dropping the ones nothing fills.
func (t *ToolConfig) fillParameters(declared map[string]string, args, config map[string]any) (url.Values, error) {
	if len(declared) == 0 {
		return nil, nil
	}
	values := url.Values{}
	for _, key := range sortedToolKeys(declared) {
		filled, err := fillTemplate(declared[key], args, config)
		if err != nil {
			return nil, fmt.Errorf("%s: request.query[%q]: %w", t.Name, key, err)
		}
		if filled == "" {
			continue
		}
		values.Set(key, filled)
	}
	return values, nil
}

// An unresolved placeholder indicates a validation bug.
func fillTemplate(tmpl string, args, config map[string]any) (string, error) {
	if tmpl == "" {
		return "", nil
	}
	out := placeholderPattern.ReplaceAllStringFunc(tmpl, func(match string) string {
		body := match[2 : len(match)-2]
		name, hasDefault := splitDefault(body)
		name = strings.TrimSpace(name)
		value, found := templateValue(name, args, config)
		if !found || strings.TrimSpace(value) == "" {
			if hasDefault {
				_, def, _ := strings.Cut(body, "|")
				return strings.TrimSpace(def)
			}
			return ""
		}
		return value
	})
	if strings.Contains(out, "{{") {
		return "", fmt.Errorf("left an unresolved placeholder in %q", tmpl)
	}
	return out, nil
}

func templateValue(name string, args, config map[string]any) (string, bool) {
	if key, ok := strings.CutPrefix(name, "config."); ok {
		return configString(config[key])
	}
	if key, ok := strings.CutPrefix(name, "args."); ok {
		return configString(args[key])
	}
	return "", false
}

// A float64 is printed as an integer when it is one, and never in exponent form.
func configString(v any) (string, bool) {
	switch typed := v.(type) {
	case nil:
		return "", false
	case string:
		return typed, true
	case bool:
		return strconv.FormatBool(typed), true
	case float64:
		if typed == float64(int64(typed)) {
			return strconv.FormatInt(int64(typed), 10), true
		}
		return strconv.FormatFloat(typed, 'f', -1, 64), true
	default:
		return fmt.Sprintf("%v", v), true
	}
}

// RenderAnswer extracts manifest-defined fields from the response; rendering failures are errors to distinguish them from empty results.
func (r ToolResponse) RenderAnswer(body []byte, isJSON bool) (string, error) {
	if !isJSON {
		return "", fmt.Errorf("the upstream answered with %s rather than JSON — this instance does not serve the format the extension asks for", contentTypeHint(body))
	}
	var doc any
	if err := json.Unmarshal(body, &doc); err != nil {
		return "", fmt.Errorf("the upstream's JSON does not parse: %v", err)
	}
	found := lookupPath(doc, r.Results)
	if found == nil {
		return "", fmt.Errorf("the upstream's JSON has no %q field — this is not the answer shape the extension declares", r.Results)
	}
	items, ok := found.([]any)
	if !ok {
		return "", fmt.Errorf("the upstream's %q field is %T, not a list of results", r.Results, found)
	}
	limit := r.ResultLimit()
	shown := items
	if len(shown) > limit {
		shown = shown[:limit]
	}

	var out strings.Builder
	out.WriteString(resultHeader(len(shown), len(items), lookupPath(doc, r.Total)))
	for i, item := range shown {
		out.WriteString("\n" + strconv.Itoa(i+1) + ".")
		for n, field := range r.Fields {
			value := flattenText(lookupPath(item, field.Key))
			if value == "" {
				continue
			}
			if n == 0 {
				out.WriteString(" ")
			} else {
				out.WriteString("\n   ")
			}
			out.WriteString(field.Label)
			out.WriteString(": ")
			out.WriteString(clampRunes(value, r.SnippetLimit()))
		}
	}
	if len(shown) < len(items) {
		out.WriteString(fmt.Sprintf("\n… %d more results the extension's max_results did not render", len(items)-len(shown)))
	}
	return out.String(), nil
}

// CountResults counts the results at the path; an absent or non-list path has none.
func (r ToolResponse) CountResults(body []byte) int {
	var doc any
	if err := json.Unmarshal(body, &doc); err != nil {
		return 0
	}
	items, ok := lookupPath(doc, r.Results).([]any)
	if !ok {
		return 0
	}
	return len(items)
}

func resultHeader(shown, total int, reported any) string {
	if shown == 0 {
		return "No results."
	}
	head := fmt.Sprintf("%d result", shown)
	if shown != 1 {
		head += "s"
	}
	if total > shown {
		head += fmt.Sprintf(" of %d", total)
	}
	if n := reportedCount(reported); n > total {
		head += fmt.Sprintf(" (%d the upstream says match)", n)
	}
	return head + ":"
}

func reportedCount(v any) int {
	switch typed := v.(type) {
	case float64:
		return int(typed)
	case string:
		n, _ := strconv.Atoi(typed)
		return n
	default:
		return 0
	}
}

// A numeric segment indexes an array.
func lookupPath(doc any, path string) any {
	if path == "" {
		return nil
	}
	current := doc
	for _, seg := range strings.Split(path, ".") {
		switch node := current.(type) {
		case map[string]any:
			current = node[seg]
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(node) {
				return nil
			}
			current = node[i]
		default:
			return nil
		}
		if current == nil {
			return nil
		}
	}
	return current
}

// A nested value is flattened to text, because a tool result is text.
func flattenText(v any) string {
	switch typed := v.(type) {
	case nil:
		return ""
	case string:
		return oneLine(typed)
	case float64:
		text, _ := configString(typed)
		return text
	case bool:
		return strconv.FormatBool(typed)
	case []any:
		parts := make([]string, 0, len(typed))
		for _, entry := range typed {
			if text := flattenText(entry); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, " · ")
	case map[string]any:
		keys := slices.Sorted(maps.Keys(typed))
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			if text := flattenText(typed[k]); text != "" {
				parts = append(parts, k+": "+text)
			}
		}
		return strings.Join(parts, " · ")
	default:
		return oneLine(fmt.Sprintf("%v", v))
	}
}

func oneLine(text string) string {
	return strings.Join(strings.Fields(strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(text)), " ")
}

// Counts characters, not bytes, so a multibyte snippet is cut at a readable length.
func clampRunes(text string, max int) string {
	if max <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return strings.TrimRight(string(runes[:max]), " ,;") + "…"
}

func contentTypeHint(body []byte) string {
	trimmed := strings.TrimLeft(string(body), " \t\r\n")
	if strings.HasPrefix(trimmed, "<") {
		return "an HTML page"
	}
	return "a response that is not JSON"
}
