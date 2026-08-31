package git

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// cloneProgressRe matches the "Receiving objects: 42% (123/456)..." lines that
// `git clone --progress` writes to stderr. It is the only progress signal a clone
// gives that increases monotonically — other phases report no percent.
var cloneProgressRe = regexp.MustCompile(`Receiving objects:\s+(\d{1,3})%`)

// CloneAllowedProtocols is the transport allowlist for every clone: the URL
// schemes git may use. Helper transports (ext:, fd:, python:) are absent on purpose.
var CloneAllowedProtocols = []string{"http", "https", "git", "ssh", "file"}

// CloneProtocolArgs fences git's transport choice. It must come BEFORE the `clone`
// subcommand, so the settings reach the process that parses the client-supplied URL,
// not a child it spawns. Git runs a URL whose scheme names an executable helper
// (`ext::sh -c 'id'` is a shell), and a user's protocol.ext.allow=always re-enables
// that, so the refusal is set here rather than left to git's default.
// so it is set explicitly here and in CloneEnv.
func CloneProtocolArgs() []string {
	return []string{"-c", "protocol.ext.allow=never"}
}

// CloneArgs builds the argv for a progress-reporting clone:
//
//	git -c protocol.ext.allow=never clone --progress <src> <dst>
//
// src must already be validated (see ValidateCloneURL); in particular it must
// not start with '-', which git would read as a flag.
func CloneArgs(src, dst string) []string {
	return append(CloneProtocolArgs(), "clone", "--progress", src, dst)
}

// ValidateCloneURL validates a clone source before it reaches `git clone`. Every
// clone of a caller-supplied source — project creation, an extension checkout — goes
// through here, so there is one answer to "may this reach git?".
//
// It rejects a leading '-' (git would read it as a flag) and helper transports,
// which run a host binary with this process's environment. Accepted: a URL with an
// allowed scheme, scp-like SSH syntax (git@host:org/repo.git), or a bare filesystem
// path — local cloning is a supported workflow.
// is a supported workflow.
func ValidateCloneURL(cloneURL string) error {
	s := strings.TrimSpace(cloneURL)
	if s == "" {
		return fmt.Errorf("clone source is required")
	}
	if strings.HasPrefix(s, "-") {
		return fmt.Errorf("clone source must not start with a dash")
	}
	if strings.ContainsAny(s, "\x00\n\r\v\f") {
		return fmt.Errorf("clone source must not contain control characters")
	}
	// Remote-helper syntax (e.g. "ext::sh -c 'id'") never appears in a real URL and has
	// no "://", so it is caught before the scheme rules. Only the text before a "://" is
	// searched, so an IPv6 host (http://[::1]:8080/…) still passes.
	head := s
	if i := strings.Index(s, "://"); i >= 0 {
		head = s[:i]
	}
	if strings.Contains(head, "::") {
		return fmt.Errorf("clone source must not use the git remote-helper transport syntax (::)")
	}
	// Scheme must be in CloneAllowedProtocols, compared verbatim: git's own check is
	// case-sensitive, so "HTTPS://…" is refused as transport 'HTTPS'.
	if scheme, ok := cloneURLScheme(s); ok {
		if !slices.Contains(CloneAllowedProtocols, scheme) {
			return fmt.Errorf("clone source scheme %q is not allowed (allowed: %s; schemes are case-sensitive)",
				scheme, strings.Join(CloneAllowedProtocols, ", "))
		}
		return nil
	}
	// No scheme: git picks an scp-style SSH remote or a local path by looking at
	// the first ':' — text before it that holds no '/' is a host.
	if i := strings.Index(s, ":"); i >= 0 {
		if slash := strings.Index(s, "/"); slash < 0 || i < slash {
			return validateScpLikeCloneURL(s, i)
		}
	}
	// Anything else is a local filesystem path (absolute or relative).
	return nil
}

// cloneURLScheme returns the scheme of a "scheme://rest" URL, exactly as written
// (git matches schemes case-sensitively, so the validator must not normalise it).
func cloneURLScheme(s string) (string, bool) {
	i := strings.Index(s, "://")
	if i <= 0 {
		return "", false
	}
	scheme := s[:i]
	for j := 0; j < len(scheme); j++ {
		c := scheme[j]
		alnum := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		if !alnum && (j == 0 || (c != '+' && c != '-' && c != '.')) {
			return "", false
		}
	}
	return scheme, true
}

// validateScpLikeCloneURL validates the scp-style SSH form `[user@]host:path`
// (e.g. `git@github.com:org/repo.git`), the only scheme-less form git treats as a
// remote. Looser shapes — whitespace, a second colon or slash in the host, an
// empty path — are rejected.
func validateScpLikeCloneURL(s string, colon int) error {
	host, path := s[:colon], s[colon+1:]
	if host == "" || path == "" {
		return fmt.Errorf("clone source is not a valid repository URL or path")
	}
	for i := 0; i < len(host); i++ {
		c := host[i]
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '.' || c == '-' || c == '_' || c == '@'
		if !ok {
			return fmt.Errorf("clone source host %q is not valid", host)
		}
	}
	if strings.HasPrefix(path, ":") || strings.ContainsAny(path, " \t") {
		return fmt.Errorf("clone source must not contain whitespace")
	}
	return nil
}

// CloneEnv builds the environment for a clone: never prompt for credentials, and
// restrict the transports git may speak (GIT_ALLOW_PROTOCOL is enforced by git
// itself, whatever the user's git config says).
func CloneEnv() []string {
	return append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ALLOW_PROTOCOL="+strings.Join(CloneAllowedProtocols, ":"),
	)
}

// Clone runs `git clone --progress src dst`, restricted to CloneAllowedProtocols
// (see CloneArgs/CloneEnv), and calls onProgress with the "Receiving objects"
// percentage (0-100) every time it strictly increases. onProgress may be nil.
// --progress forces progress output even though stderr is not a terminal.
//
// It returns git's stderr (progress + diagnostics, useful in error messages) and
// the process error, if any.
// for error messages) and the process error, if any.
func Clone(src, dst string, onProgress func(percent int)) (string, error) {
	// `git clone` is silent on stdout — stderr carries both progress and
	// errors, so discarding stdout is safe.
	cmd := exec.Command("git", CloneArgs(src, dst)...)
	cmd.Env = CloneEnv()
	cmd.Stdout = io.Discard

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("clone stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start git clone: %w", err)
	}

	var out bytes.Buffer
	last := -1
	scanner := bufio.NewScanner(stderr)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		_, _ = out.WriteString(line + "\n")
		if onProgress == nil {
			continue
		}
		// Git rewrites the progress line in place with carriage returns, so a
		// single scanned "line" (newline-terminated) can contain many updates.
		for _, m := range cloneProgressRe.FindAllStringSubmatch(line, -1) {
			pct, convErr := strconv.Atoi(m[1])
			if convErr != nil || pct <= last {
				continue
			}
			last = pct
			onProgress(pct)
		}
	}

	waitErr := cmd.Wait()
	if scanErr := scanner.Err(); scanErr != nil && waitErr == nil {
		waitErr = scanErr
	}
	if waitErr != nil {
		return out.String(), waitErr
	}
	return out.String(), nil
}
