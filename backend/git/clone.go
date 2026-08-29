package git

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// cloneProgressRe matches the "Receiving objects: 42% (123/456)..." progress
// lines emitted by `git clone --progress`. This is the only reliable,
// monotonically-increasing progress signal git provides for a clone — the
// "Counting objects" and "Resolving deltas" phases report no percent.
var cloneProgressRe = regexp.MustCompile(`Receiving objects:\s+(\d{1,3})%`)

// CloneAllowedProtocols is the transport allowlist enforced on every clone: the
// URL schemes git may use. Helper transports (ext:, fd:, python:, …) are absent on
// purpose — the clone target is client-supplied, so the product cannot rely on the
// host git defaulting to refusing them.
var CloneAllowedProtocols = []string{"http", "https", "git", "ssh", "file"}

// CloneArgs builds the argv for a progress-reporting clone. The transport
// restrictions come before the `clone` subcommand, so they reach the process that
// parses the client-supplied source URL:
//
//	git -c protocol.ext.allow=never clone --progress <src> <dst>
//
// src must already be validated (see managers.validateGitCloneURL); in particular
// it must not start with '-', which git would read as a flag.
func CloneArgs(src, dst string) []string {
	return []string{"-c", "protocol.ext.allow=never", "clone", "--progress", src, dst}
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

// Clone runs `git clone --progress src dst` (restricted to
// CloneAllowedProtocols — see CloneArgs/CloneEnv) and invokes
// onProgress with the "Receiving objects" percentage (0-100) every time it
// strictly increases. onProgress may be nil. The --progress flag forces
// progress output even though stderr is not a terminal.
//
// It returns the git process' stderr output (progress + diagnostics, useful
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
