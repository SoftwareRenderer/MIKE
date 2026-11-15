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
