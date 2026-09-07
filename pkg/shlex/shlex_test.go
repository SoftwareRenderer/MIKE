package shlex

// Quote/Join guard a command line built on the host and run by a shell INSIDE a container: the task-sandbox
// probe runner and the language-server environment check both render argv from an extension manifest. If a
// word can escape its own quoting, a manifest is arbitrary shell execution.
//
// The table tests assert the shape; the round-trip test asserts the property that matters, by asking a real
// /bin/sh to reproduce the words back.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestQuote(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "make", "'make'"},
		{"space", "two words", "'two words'"},
		{"empty", "", "''"},
		{"single quote", "it's", `'it'\''s'`},
		{"only a quote", "'", `''\'''`},
		{"newline", "a\nb", "'a\nb'"},
		{"tab", "a\tb", "'a\tb'"},
		// Everything below is shell syntax that MUST stay data.
		{"command substitution", "$(id)", "'$(id)'"},
		{"backtick substitution", "`id`", "'`id`'"},
		{"semicolon", "a;rm -rf /", "'a;rm -rf /'"},
		{"pipe", "a|b", "'a|b'"},
		{"redirect", "a>/etc/x", "'a>/etc/x'"},
		{"glob", "*.go", "'*.go'"},
		{"variable", "$HOME", "'$HOME'"},
		{"double quote", `say "hi"`, `'say "hi"'`},
		{"backslash", `a\b`, `'a\b'`},
		{"dash lead", "-rf", "'-rf'"},
		{"comment char", "a#b", "'a#b'"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Quote(tc.in); got != tc.want {
				t.Errorf("Quote(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestJoin(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want string
	}{
		{"empty", nil, ""},
		{"one", []string{"ls"}, "'ls'"},
		{"two", []string{"ls", "-la"}, "'ls' '-la'"},
		{"word with space", []string{"sh", "-c", "echo hi"}, "'sh' '-c' 'echo hi'"},
		// An empty argument must survive as an argument, not vanish into a gap between quotes.
		{"empty argument", []string{"echo", "", "x"}, "'echo' '' 'x'"},
		{"injection attempt", []string{"go", "list", "$(curl evil)"}, "'go' 'list' '$(curl evil)'"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Join(tc.in); got != tc.want {
				t.Errorf("Join(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// hostileWords are the payloads that would run, read, or delete something if quoting ever regressed.
// Each is paired with itself: the round-trip asserts the shell hands back EXACTLY these bytes.
var hostileWords = []string{
	"plain",
	"two words",
	"it's",
	`say "hi"`,
	"a\nb",
	"a\tb",
	"$(touch /tmp/shlex_pwned)",
	"`touch /tmp/shlex_pwned`",
	"a; touch /tmp/shlex_pwned",
	"a | wc -l",
	"a > /tmp/shlex_pwned",
	"*.go",
	"$HOME",
	"",
	"-rf",
	"a\\b",
	"\\",
	"'" + "'" + "'",
	"$(IFS_;touch /tmp/shlex_pwned)",
}

// recordSep is the record separator the probe prints between arguments: a byte that appears in none
// of the test words, so a split on it is unambiguous.
const recordSep = "\x1e"

// argPrinterScript writes a probe that prints its OWN arguments separated by $SHLEX_RS, and returns
// its path. Production renders `argv` into `podman exec … sh -c "<line>"`, so the test does the same:
// the rendered line is text the shell must parse, and the probe receives whatever words come out.
//
// The separator arrives by environment variable rather than by being quoted into the script body —
// a probe about quoting must not need its own quoting trick, and a literal newline would not survive
// being written into a printf argument.
func argPrinterScript(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "printargs.sh")
	body := "#!/bin/sh\nfirst=1\nfor a in \"$@\"; do\n  if [ \"$first\" = 1 ]; then first=0; else printf '%s' \"$SHLEX_RS\"; fi\n  printf '%s' \"$a\"\ndone\nprintf '\\n'\n"
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatalf("write probe: %v", err)
	}
	return p
}

// runProbe renders argv with Join and hands the line to /bin/sh exactly as the sandbox does, then
// returns the words the probe actually received.
func runProbe(t *testing.T, probe string, argv []string) []string {
	t.Helper()
	line := Join(append([]string{probe}, argv...))
	cmd := exec.Command("/bin/sh", "-c", line)
	// A one-name allowlist rather than os.Environ(): the command line is hostile by construction, and
	// a test that models the invariant it guards beats one that only asserts it.
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "SHLEX_RS=" + recordSep}
	cmd.WaitDelay = 10 * time.Second

	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("shell run failed: %v\nline: %s", err, line)
	}
	return strings.Split(strings.TrimSuffix(string(out), "\n"), recordSep)
}

// TestJoinRoundTripsThroughARealShell is the property test: /bin/sh must reproduce every word
// byte-for-byte, including the empty one. A quoting bug shows up as a wrong word count, changed bytes,
// or — if a payload were ever live — a side effect the test also checks for.
//
// The environment is a one-name allowlist rather than os.Environ(): the command line is hostile by
// construction, and a test that models the invariant it guards is better than one that only asserts it.
func TestJoinRoundTripsThroughARealShell(t *testing.T) {
	// Cleared first, not just checked after: a previous run (or a previous, broken Quote) leaving the
	// file behind would otherwise make this fail for a reason that is not the code under test.
	t.Cleanup(func() { _ = os.Remove("/tmp/shlex_pwned") })
	_ = os.Remove("/tmp/shlex_pwned")

	got := runProbe(t, argPrinterScript(t), hostileWords)

	if len(got) != len(hostileWords) {
		t.Fatalf("shell produced %d words from %d: %q", len(got), len(hostileWords), got)
	}
	for i, want := range hostileWords {
		if got[i] != want {
			t.Errorf("word %d came back changed: got %q, want %q", i, got[i], want)
		}
	}

	// Belt and braces: none of the payloads may have run.
	if _, err := os.Stat("/tmp/shlex_pwned"); err == nil {
		t.Error("a payload executed — the quoting was live, not literal")
		_ = os.Remove("/tmp/shlex_pwned")
	}
}

// TestJoinPreservesArgumentPositions pins the other half of the contract: not only is every word
// literal, the WORD COUNT survives. A quoting scheme that merged two words or split one would keep
// every byte and still hand the sandbox a different argv than the caller built.
func TestJoinPreservesArgumentPositions(t *testing.T) {
	tests := []struct {
		name string
		argv []string
	}{
		{"three", []string{"go", "list", "./..."}},
		{"embedded spaces", []string{"sh", "-c", "go list -deps ./...", "extra"}},
		{"empty middle word", []string{"echo", "", "x"}},
		{"payloads", []string{"echo", "$(id)", "a;b", "c|d", "*"}},
		{"single", []string{"ls"}},
	}
	probe := argPrinterScript(t)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := runProbe(t, probe, tc.argv)
			// An empty word prints as an empty record, so the record count is the word count.
			if len(got) != len(tc.argv) {
				t.Errorf("%d arguments in, %d out: %q", len(tc.argv), len(got), got)
			}
		})
	}
}
