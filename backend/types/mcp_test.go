package types

import (
	"strings"
	"testing"
	"time"
)

// A timeout of 0 is the value that means "no deadline", not an unset one: a long-running remote tool is
// the reason it exists. There is no ceiling above it — a long-but-finite deadline is a choice, not a
// mistake to correct.
func TestMCPTimeoutZeroIsNoDeadline(t *testing.T) {
	for _, tc := range []struct {
		seconds int
		want    time.Duration
	}{
		{seconds: 0, want: 0},
		{seconds: -1, want: 0},
		{seconds: 5, want: 5 * time.Second},
		{seconds: 30 * 60, want: 30 * 60 * time.Second},
	} {
		s := MCPServer{TimeoutSeconds: tc.seconds}
		if got := s.Timeout(); got != tc.want {
			t.Errorf("Timeout() for %d seconds = %v, want %v", tc.seconds, got, tc.want)
		}
	}
}

// The refusal is read by the person who has to fix the field: a negative is all it refuses, and it says
// what 0 means rather than pointing at a default or a ceiling.
func TestMCPTimeoutRefusalNamesTheNoDeadlineValue(t *testing.T) {
	err := ValidateMCPServer("negative", "https://a.example.com", -1, nil)
	if err == nil {
		t.Fatal("a negative timeout was accepted")
	}
	if !strings.Contains(err.Error(), "0") {
		t.Errorf("refusal %q does not say what 0 means", err)
	}
	for _, seconds := range []int{0, 1, 30 * 60} {
		if err := ValidateMCPServer("row", "https://a.example.com", seconds, nil); err != nil {
			t.Errorf("%d seconds was refused: %v", seconds, err)
		}
	}
}
