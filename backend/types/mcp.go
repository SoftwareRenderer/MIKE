package types

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// MaskedSecret stands in for a stored secret in a response, and reads as "unchanged" on the way back.
// The config view writes it and the probe resolves it back to what is stored.
const MaskedSecret = "\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022" // 10 bullets

// A server's tools are never stored here: they are read from the server itself.

// MaxMCPServers bounds the registry: every enabled server's tools ride on every provider request.
const MaxMCPServers = 16

// MaxToolsPerServer bounds how many of one server's tools are offered; the rest are reported as dropped.
const MaxToolsPerServer = 32

// MaxMCPHeaders bounds one server's request headers.
const MaxMCPHeaders = 8

// MCPServerIDBytes is the entropy behind a server's ID.
const MCPServerIDBytes = 8

// MCPServerSlugLen caps the server half of an offered tool name. The save uses the same cap to refuse
// two servers whose names would name their tools alike.
const MCPServerSlugLen = 24

// MCPServer is one registered server. Headers holds the sealed value, or the opened value only where
// the box has opened it, so a config read never hands out a secret.
type MCPServer struct {
	// ID survives a rename on purpose: a project's enable list holds these, and an ID derived from
	// the name would silently disable the server for every project that enabled it.
	ID      string      `json:"id"`
	Name    string      `json:"name"`
	URL     string      `json:"url"`
	Enabled bool        `json:"enabled"`
	Headers []MCPHeader `json:"headers,omitempty"`
	// TimeoutSeconds is this server's deadline for one session, with no ceiling. Zero means no deadline:
	// the wait ends when the caller's context does, which is what a long-running remote tool needs.
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
}

// MCPHeader is one header sent to the server on every request. Value follows Provider.APIKey's
// tri-state across a save: nil or the masked placeholder keeps what is stored, "" clears it, and
// anything else is a new value to seal.
type MCPHeader struct {
	Name  string  `json:"name"`
	Value *string `json:"value,omitempty"`
}

// Timeout is this server's deadline for one session. Zero is no deadline — the wait is bounded by the
// caller's context, which ends when a task is stopped or this process shuts down.
func (m *MCPServer) Timeout() time.Duration {
	if m.TimeoutSeconds <= 0 {
		return 0
	}
	return time.Duration(m.TimeoutSeconds) * time.Second
}

// NormalizeMCPServerIDs cleans a project's opt-in list: trimmed, de-duplicated, empty entries gone.
// An ID with no registered server behind it is kept.
func NormalizeMCPServerIDs(ids []string) ([]string, error) {
	out := make([]string, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		if len(out) == MaxMCPServers {
			return nil, fmt.Errorf("too many MCP servers enabled: the most is %d", MaxMCPServers)
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, nil
}

// ValidateMCPServer is the row fence for one server: its address, its deadline, its header field
// names. A save and the Test action both run it, so they refuse the same shapes; the set-level rules
// (a name, a name not already taken, a slug not already taken) are the save's.
func ValidateMCPServer(name, rawURL string, timeoutSeconds int, headers []MCPHeader) error {
	if err := validateMCPServerURL(name, rawURL); err != nil {
		return err
	}
	if timeoutSeconds < 0 {
		return fmt.Errorf("MCP server %q has a timeout of %d seconds; it must be 0 (no deadline) or more", name, timeoutSeconds)
	}
	if len(headers) > MaxMCPHeaders {
		return fmt.Errorf("MCP server %q declares %d headers; the most is %d", name, len(headers), MaxMCPHeaders)
	}
	names := make(map[string]bool, len(headers))
	for _, h := range headers {
		hname := strings.TrimSpace(h.Name)
		if hname == "" {
			return fmt.Errorf("MCP server %q has a header with no name", name)
		}
		if !isHeaderFieldName(hname) {
			return fmt.Errorf("MCP server %q has the header name %q, which is not a header field name", name, hname)
		}
		lower := strings.ToLower(hname)
		if names[lower] {
			return fmt.Errorf("MCP server %q declares the header %q twice", name, hname)
		}
		names[lower] = true
	}
	return nil
}

// isHeaderFieldName reports whether s is a valid HTTP field name (RFC 9110 tchar); a name carrying a
// space, a colon or a line break would put more than one header on the wire.
func isHeaderFieldName(s string) bool {
	const tchars = "!#$%&'*+-.^_`|~0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	for i := 0; i < len(s); i++ {
		if !strings.ContainsRune(tchars, rune(s[i])) {
			return false
		}
	}
	return len(s) > 0
}

// validateMCPServerURL is the address fence for the value this process dials: an http or https origin
// and nothing else. Credentials belong in a header.
func validateMCPServerURL(name, raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("MCP server %q has no URL", name)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("MCP server %q has an unreadable URL: %s", name, shortenForReport(raw))
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("MCP server %q must be reached over http or https, not %q", name, u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("MCP server %q has a URL with no host", name)
	}
	if u.User != nil {
		return fmt.Errorf("MCP server %q puts credentials in the URL; send them as a header instead", name)
	}
	if u.Fragment != "" {
		return fmt.Errorf("MCP server %q has a URL fragment, which is not part of an address", name)
	}
	return nil
}

// shortenForReport keeps a parse failure reportable without echoing a URL that may hold a credential.
func shortenForReport(raw string) string {
	if len(raw) > 60 {
		return raw[:60] + "…"
	}
	return raw
}

// NewMCPServerID returns the ID a newly registered server gets. It is random rather than a slug of
// the name so a rename keeps every project's enable list pointing at the server it named.
func NewMCPServerID() (string, error) {
	var b [MCPServerIDBytes]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
