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

func TestFailureTracker_SameCallFailingIncrements(t *testing.T) {
	t.Parallel()
	var ft failureTracker
	sig := "read_file:{\"path\":\"foo.txt\"}"

	if got := ft.recordTurn(failResults(sig)); got != 1 {
		t.Errorf("first failure: count = %d, want 1", got)
	}
	if got := ft.recordTurn(failResults(sig)); got != 2 {
		t.Errorf("second identical failure: count = %d, want 2", got)
	}
	if got := ft.recordTurn(failResults(sig)); got != 3 {
		t.Errorf("third identical failure: count = %d, want 3", got)
	}
}

func TestFailureTracker_SuccessResets(t *testing.T) {
	t.Parallel()
	var ft failureTracker
	sig := "read_file:{\"path\":\"foo.txt\"}"

	if got := ft.recordTurn(failResults(sig)); got != 1 {
		t.Errorf("first failure: count = %d, want 1", got)
	}
	if got := ft.recordTurn(failResults(sig)); got != 2 {
		t.Errorf("second failure: count = %d, want 2", got)
	}

	if got := ft.recordTurn(okResults(sig)); got != 0 {
		t.Errorf("after success: count = %d, want 0", got)
	}

	// Fail again — should restart from 1
	if got := ft.recordTurn(failResults(sig)); got != 1 {
		t.Errorf("after success then fail: count = %d, want 1", got)
	}
}

func TestFailureTracker_DifferentCallResets(t *testing.T) {
	t.Parallel()
	var ft failureTracker
	sigA := "read_file:{\"path\":\"foo.txt\"}"
	sigB := "read_file:{\"path\":\"bar.txt\"}"

	if got := ft.recordTurn(failResults(sigA)); got != 1 {
		t.Errorf("first failure: count = %d, want 1", got)
	}
	if got := ft.recordTurn(failResults(sigA)); got != 2 {
		t.Errorf("second failure: count = %d, want 2", got)
	}

	// Different tool call fails — resets to 1
	if got := ft.recordTurn(failResults(sigB)); got != 1 {
		t.Errorf("different failure: count = %d, want 1", got)
	}

	// Back to original — resets to 1 again
	if got := ft.recordTurn(failResults(sigA)); got != 1 {
		t.Errorf("back to original: count = %d, want 1", got)
	}
}

func TestFailureTracker_TextOnlyResets(t *testing.T) {
	t.Parallel()
	var ft failureTracker
	sig := "read_file:{\"path\":\"foo.txt\"}"

	if got := ft.recordTurn(failResults(sig)); got != 1 {
		t.Errorf("first failure: count = %d, want 1", got)
	}
	if got := ft.recordTurn(failResults(sig)); got != 2 {
		t.Errorf("second failure: count = %d, want 2", got)
	}

	// Text-only response (empty results) resets counter
	if got := ft.recordTurn(nil); got != 0 {
		t.Errorf("after text-only: count = %d, want 0", got)
	}
}

func TestFailureTracker_MultipleIdenticalInOneTurnCountsAsOne(t *testing.T) {
	t.Parallel()
	var ft failureTracker
	sig := "read_file:{\"path\":\"foo.txt\"}"

	// Three identical failures in one turn = count 1 (not 3)
	if got := ft.recordTurn(failResults(sig, sig, sig)); got != 1 {
		t.Errorf("multiple identical in one turn: count = %d, want 1", got)
	}

	// Same call fails again in next turn = count 2
	if got := ft.recordTurn(failResults(sig, sig, sig)); got != 2 {
		t.Errorf("next turn same call: count = %d, want 2", got)
	}
}

func TestFailureTracker_MixedSuccessFailureResets(t *testing.T) {
	t.Parallel()
	var ft failureTracker
	sigA := "read_file:{\"path\":\"foo.txt\"}"
	sigB := "write_file:{\"path\":\"bar.txt\"}"

	if got := ft.recordTurn(failResults(sigA)); got != 1 {
		t.Errorf("before mixed: count = %d, want 1", got)
	}

	// Mixed: one success + one failure — resets because a success occurred
	if got := ft.recordTurn(mixedResults([]string{sigA, sigB})); got != 0 {
		t.Errorf("after mixed (has success): count = %d, want 0", got)
	}
}

func TestFailureTracker_ReachesMax(t *testing.T) {
	t.Parallel()
	var ft failureTracker
	sig := "read_file:{\"path\":\"foo.txt\"}"
	maxFailures := 3

	var last int
	for i := 1; i <= maxFailures; i++ {
		last = ft.recordTurn(failResults(sig))
		if last != i {
			t.Errorf("turn %d: count = %d, want %d", i, last, i)
		}
	}

	if last != maxFailures {
		t.Errorf("final count = %d, want %d", last, maxFailures)
	}
}

func TestFailureTracker_DifferentFailuresInOneTurn(t *testing.T) {
	t.Parallel()
	var ft failureTracker
	sigA := "read_file:{\"path\":\"foo.txt\"}"
	sigB := "bash:{\"command\":\"ls\"}"

	// Two different failures in one turn — count = 1 (last signature wins)
	if got := ft.recordTurn(failResults(sigA, sigB)); got != 1 {
		t.Errorf("different failures in one turn: count = %d, want 1", got)
	}
	// The tracked signature should be the last one (sigB)
	if ft.lastFailureSig != sigB {
		t.Errorf("lastFailureSig = %q, want %q", ft.lastFailureSig, sigB)
	}

	// Repeating sigB increments
	if got := ft.recordTurn(failResults(sigB)); got != 2 {
		t.Errorf("repeat last sig: count = %d, want 2", got)
	}

	// sigA is different from sigB, resets to 1
	if got := ft.recordTurn(failResults(sigA)); got != 1 {
		t.Errorf("different sig: count = %d, want 1", got)
	}
}

func TestFailureTracker_ResetClearsState(t *testing.T) {
	t.Parallel()
	var ft failureTracker
	ft.lastFailureSig = "some_sig"
	ft.failCount = 5

	ft.reset()
	if ft.failCount != 0 {
		t.Errorf("after reset: failCount = %d, want 0", ft.failCount)
	}
	if ft.lastFailureSig != "" {
		t.Errorf("after reset: lastFailureSig = %q, want empty", ft.lastFailureSig)
	}
}
