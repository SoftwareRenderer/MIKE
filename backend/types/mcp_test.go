package types

import (
	"strings"
	"testing"
	"time"
)

// A timeout of 0 is the value that means "no deadline", not an unset one: a long-running remote tool is
// the reason it exists. The ceiling still holds whatever else is set, because the save refused a row past
// it and a hand-written one must not buy an unbounded clock by overrunning the scale.
func TestMCPTimeoutZeroIsNoDeadline(t *testing.T) {
	for _, tc := range []struct {
		seconds int
		want    time.Duration
	}{
		{seconds: 0, want: 0},
		{seconds: -1, want: 0},
		{seconds: 5, want: 5 * time.Second},
		{seconds: MaxMCPTimeoutSeconds, want: MaxMCPTimeoutSeconds * time.Second},
		{seconds: MaxMCPTimeoutSeconds + 1, want: MaxMCPTimeoutSeconds * time.Second},
	} {
		s := MCPServer{TimeoutSeconds: tc.seconds}
		if got := s.Timeout(); got != tc.want {
			t.Errorf("Timeout() for %d seconds = %v, want %v", tc.seconds, got, tc.want)
		}
	}
}

// The refusal is read by the person who has to fix the field, so it names the value that means no
// deadline rather than pointing at a default that no longer exists.
func TestMCPTimeoutRefusalNamesTheNoDeadlineValue(t *testing.T) {
	err := ValidateMCPServer("slow", "https://a.example.com", MaxMCPTimeoutSeconds+1, nil)
	if err == nil {
		t.Fatal("a timeout past the ceiling was accepted")
	}
	if !strings.Contains(err.Error(), "no deadline") {
		t.Errorf("refusal %q does not say what 0 means", err)
	}
	if err := ValidateMCPServer("forever", "https://a.example.com", 0, nil); err != nil {
		t.Errorf("0 is a valid timeout (no deadline), refused with: %v", err)
	}
	if err := ValidateMCPServer("negative", "https://a.example.com", -1, nil); err == nil {
		t.Error("a negative timeout was accepted")
	}
}
