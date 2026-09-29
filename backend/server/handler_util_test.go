package server

import (
	"os/exec"
	"testing"
)

func TestShellQuote(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple path",
			input:    "/home/sandbox/myproj/subdir",
			expected: "'/home/sandbox/myproj/subdir'",
		},
		{
			name:     "path with spaces",
			input:    "/home/sandbox/my proj/sub dir",
			expected: "'/home/sandbox/my proj/sub dir'",
		},
		{
			name:     "command injection via semicolon",
			input:    "sandbox; rm -rf /",
			expected: "'sandbox; rm -rf /'",
		},
		{
			name:     "command injection via backtick",
			input:    "sandbox`whoami`",
			expected: "'sandbox`whoami`'",
		},
		{
			name:     "command injection via dollar substitution",
			input:    "sandbox$(whoami)",
			expected: "'sandbox$(whoami)'",
		},
		{
			name:     "path with single quote",
			input:    "o'brien/path",
			expected: "'o'\"'\"'brien/path'",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "''",
		},
		{
			name:     "path with newline",
			input:    "sandbox/\nrm -rf /",
			expected: "'sandbox/\nrm -rf /'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := shellQuote(tt.input)
			if result != tt.expected {
				t.Errorf("shellQuote(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// TestShellQuoteSafety verifies that the quoted output cannot be used for
// command injection by actually executing the constructed command through a
// POSIX shell and confirming the payload does not break out of quoting.
func TestShellQuoteSafety(t *testing.T) {
	// This test uses a canary file. If the payload breaks out of quoting,
	// the canary file will be created, indicating a vulnerability.
	payloads := []string{
		"sandbox; touch /tmp/shellquote_pwned_canary",
		"sandbox && touch /tmp/shellquote_pwned_canary",
		"sandbox | touch /tmp/shellquote_pwned_canary",
		"$(touch /tmp/shellquote_pwned_canary)",
		"sandbox`touch /tmp/shellquote_pwned_canary`",
		"sandbox\ntouch /tmp/shellquote_pwned_canary",
		"sandbox > /tmp/shellquote_pwned_canary",
	}

	for _, payload := range payloads {
		t.Run("payload: "+payload, func(t *testing.T) {
			// Clean up canary before test
			_ = exec.Command("rm", "-f", "/tmp/shellquote_pwned_canary").Run()

			quoted := shellQuote(payload)

			// Construct a command that uses the quoted payload as an argument
			// to `test -d`, which is harmless whether the path exists or not.
			// If quoting fails, the injection will create the canary file.
			shellCmd := "test -d " + quoted + " || true"

			cmd := exec.Command("sh", "-c", shellCmd)
			err := cmd.Run()
			if err != nil {
				t.Fatalf("sh -c failed unexpectedly: %v", err)
			}

			// Verify the canary file was NOT created (no injection)
			if _, err := exec.LookPath("test"); err == nil {
				checkCmd := exec.Command("test", "-f", "/tmp/shellquote_pwned_canary")
				if checkCmd.Run() == nil {
					t.Errorf("Command injection detected! Payload %q created canary file. Quoted result was: %q", payload, quoted)
				}
			}

			_ = exec.Command("rm", "-f", "/tmp/shellquote_pwned_canary").Run()
		})
	}
}

// TestShellQuoteRoundTrip verifies that shellQuote produces output that, when
// parsed by a POSIX shell, yields exactly the original input as a single
// argument.
func TestShellQuoteRoundTrip(t *testing.T) {
	payloads := []string{
		"simple_path",
		"path with spaces",
		"path/with/special;chars",
		"o'brien",
		"nested'quote",
		"",
		"trailing_space ",
		" leading_space",
	}

	for _, payload := range payloads {
		t.Run("payload: "+payload, func(t *testing.T) {
			quoted := shellQuote(payload)

			// Use `printf '%s'` to print the single argument, then capture it.
			// The shell will parse the quoted string and pass it as one argument.
			shellCmd := "printf '%s' " + quoted
			cmd := exec.Command("sh", "-c", shellCmd)
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("sh -c failed: %v", err)
			}

			if string(out) != payload {
				t.Errorf("Round trip failed: input %q, quoted %q, got %q", payload, quoted, string(out))
			}
		})
	}
}
