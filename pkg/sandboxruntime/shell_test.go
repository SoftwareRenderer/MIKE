package sandboxruntime

import (
	"os/exec"
	"testing"
)

// quotingCommand runs a script through the same `/bin/sh -c` a container's exec receives.
func quotingCommand(t *testing.T, script string) []byte {
	t.Helper()
	out, err := exec.Command("sh", "-c", script).Output()
	if err != nil {
		t.Fatalf("sh -c %q: %v", script, err)
	}
	return out
}

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
			expected: `'o'\''brien/path'`,
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
			result := ShellQuote(tt.input)
			if result != tt.expected {
				t.Errorf("ShellQuote(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

const shellQuoteCanary = "/tmp/shellquote_pwned_canary"

// TestShellQuoteSafety runs the command through a real shell; a payload that escapes quoting creates the
// canary file.
func TestShellQuoteSafety(t *testing.T) {
	payloads := []string{
		"sandbox; touch " + shellQuoteCanary,
		"sandbox && touch " + shellQuoteCanary,
		"sandbox | touch " + shellQuoteCanary,
		"$(touch " + shellQuoteCanary + ")",
		"sandbox`touch " + shellQuoteCanary + "`",
		"sandbox\ntouch " + shellQuoteCanary,
		"sandbox > " + shellQuoteCanary,
	}

	for _, payload := range payloads {
		t.Run("payload: "+payload, func(t *testing.T) {
			_ = exec.Command("rm", "-f", shellQuoteCanary).Run()
			t.Cleanup(func() { _ = exec.Command("rm", "-f", shellQuoteCanary).Run() })

			// `test -d` on the argument is harmless whether the path exists.
			quoted := ShellQuote(payload)
			quotingCommand(t, "test -d "+quoted+" || true")

			if err := exec.Command("test", "-f", shellQuoteCanary).Run(); err == nil {
				t.Errorf("payload %q broke out of quoting and created the canary; quoted result was %q", payload, quoted)
			}
		})
	}
}

// TestShellQuoteRoundTrip requires the shell's reading of a quoted argument to be the input exactly.
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
			out := quotingCommand(t, "printf '%s' "+ShellQuote(payload))
			if string(out) != payload {
				t.Errorf("round trip lost bytes: input %q, got %q", payload, string(out))
			}
		})
	}
}
