package exec

import (
	"testing"
)

func failResults(sigs ...string) []turnResult {
	results := make([]turnResult, len(sigs))
	for i, sig := range sigs {
		results[i] = turnResult{signature: sig, success: false}
	}
	return results
}

func okResults(sigs ...string) []turnResult {
	results := make([]turnResult, len(sigs))
	for i, sig := range sigs {
		results[i] = turnResult{signature: sig, success: true}
	}
	return results
}

func mixedResults(sigs []string) []turnResult {
	results := make([]turnResult, len(sigs))
	for i, sig := range sigs {
		results[i] = turnResult{signature: sig, success: i == 0}
	}
	return results
}

// trackerStep is one turn fed to the tracker, the consecutive-failure count it must leave behind and,
// when the row cares, the call the tracker holds as the failing one.
type trackerStep struct {
	results  []turnResult
	want     int
	wantLast string
}

const (
	trackerSigA  = "read_file:{\"path\":\"foo.txt\"}"
	trackerSigB  = "read_file:{\"path\":\"bar.txt\"}"
	trackerSigW  = "write_file:{\"path\":\"bar.txt\"}"
	trackerSigLS = "bash:{\"command\":\"ls\"}"
)

// The counter prices one decision: how many turns in a row asked the same thing and got the same
// failure. Anything that breaks that identity — a success anywhere in the turn, a different call, a
// text-only answer — restarts it at 1, and a turn that repeats one call counts once, not once per call.
func TestFailureTracker_TurnSequences(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		steps []trackerStep
	}{
		{
			name: "the same call failing increments, up to the limit",
			steps: []trackerStep{
				{failResults(trackerSigA), 1, ""},
				{failResults(trackerSigA), 2, ""},
				{failResults(trackerSigA), 3, ""},
			},
		},
		{
			name: "a success resets and the count restarts",
			steps: []trackerStep{
				{failResults(trackerSigA), 1, ""},
				{failResults(trackerSigA), 2, ""},
				{okResults(trackerSigA), 0, ""},
				{failResults(trackerSigA), 1, ""},
			},
		},
		{
			name: "a different call resets",
			steps: []trackerStep{
				{failResults(trackerSigA), 1, ""},
				{failResults(trackerSigA), 2, ""},
				{failResults(trackerSigB), 1, ""},
				{failResults(trackerSigA), 1, ""},
			},
		},
		{
			name: "a text-only turn resets",
			steps: []trackerStep{
				{failResults(trackerSigA), 1, ""},
				{failResults(trackerSigA), 2, ""},
				{nil, 0, ""},
			},
		},
		{
			name: "identical calls in one turn count once",
			steps: []trackerStep{
				{failResults(trackerSigA, trackerSigA, trackerSigA), 1, ""},
				{failResults(trackerSigA, trackerSigA, trackerSigA), 2, ""},
			},
		},
		{
			name: "one success anywhere in the turn resets",
			steps: []trackerStep{
				{failResults(trackerSigA), 1, ""},
				{mixedResults([]string{trackerSigA, trackerSigW}), 0, ""},
			},
		},
		{
			name: "the last failing call of the turn is the one tracked",
			steps: []trackerStep{
				{failResults(trackerSigA, trackerSigLS), 1, trackerSigLS},
				{failResults(trackerSigLS), 2, ""},
				{failResults(trackerSigA), 1, trackerSigA},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ft failureTracker
			for i, step := range tc.steps {
				if got := ft.recordTurn(step.results); got != step.want {
					t.Errorf("turn %d: count = %d, want %d", i+1, got, step.want)
				}
				if step.wantLast != "" && ft.lastFailure.signature != step.wantLast {
					t.Errorf("turn %d: tracked failure = %q, want %q", i+1, ft.lastFailure.signature, step.wantLast)
				}
			}
		})
	}
}

func TestFailureTracker_ResetClearsState(t *testing.T) {
	t.Parallel()
	var ft failureTracker
	ft.lastFailure = turnResult{signature: "some_sig"}
	ft.failCount = 5

	ft.reset()
	if ft.failCount != 0 {
		t.Errorf("after reset: failCount = %d, want 0", ft.failCount)
	}
	if ft.lastFailure.signature != "" {
		t.Errorf("after reset: tracked failure = %q, want empty", ft.lastFailure.signature)
	}
	if name, args := ft.failingCall(); name != "" || args != "" {
		t.Errorf("after reset: failingCall = %q %q, want nothing named", name, args)
	}
}

// The loop breaker matches a row by the call itself, which is what the row stores.
func TestFailureTracker_FailingCallNamesTheCall(t *testing.T) {
	t.Parallel()
	const args = `{"command":"make test"}`
	var ft failureTracker
	ft.recordTurn([]turnResult{{
		signature: "bash:" + args, name: "bash", rawArgs: args, success: false,
	}})

	if name, got := ft.failingCall(); name != "bash" || got != args {
		t.Errorf("failingCall = %q %q, want bash %q", name, got, args)
	}
}
