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
	var ft failureTracker
	sig := "read_file:{\"path\":\"foo.txt\"}"

	ft.recordTurn(failResults(sig))
	ft.recordTurn(failResults(sig))
	if ft.count() != 2 {
		t.Errorf("before success: count = %d, want 2", ft.count())
	}

	ft.recordTurn(okResults(sig))
	if ft.count() != 0 {
		t.Errorf("after success: count = %d, want 0", ft.count())
	}

	// Fail again — should restart from 1
	if got := ft.recordTurn(failResults(sig)); got != 1 {
		t.Errorf("after success then fail: count = %d, want 1", got)
	}
}

func TestFailureTracker_DifferentCallResets(t *testing.T) {
	var ft failureTracker
	sigA := "read_file:{\"path\":\"foo.txt\"}"
	sigB := "read_file:{\"path\":\"bar.txt\"}"

	ft.recordTurn(failResults(sigA))
	ft.recordTurn(failResults(sigA))
	if ft.count() != 2 {
		t.Errorf("before different: count = %d, want 2", ft.count())
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
	var ft failureTracker
	sig := "read_file:{\"path\":\"foo.txt\"}"

	ft.recordTurn(failResults(sig))
	ft.recordTurn(failResults(sig))
	if ft.count() != 2 {
		t.Errorf("before text: count = %d, want 2", ft.count())
	}

	// Text-only response (empty results) resets counter
	ft.recordTurn(nil)
	if ft.count() != 0 {
		t.Errorf("after text-only: count = %d, want 0", ft.count())
	}
}

func TestFailureTracker_MultipleIdenticalInOneTurnCountsAsOne(t *testing.T) {
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
	var ft failureTracker
	sigA := "read_file:{\"path\":\"foo.txt\"}"
	sigB := "write_file:{\"path\":\"bar.txt\"}"

	ft.recordTurn(failResults(sigA))
	if ft.count() != 1 {
		t.Errorf("before mixed: count = %d, want 1", ft.count())
	}

	// Mixed: one success + one failure — resets because a success occurred
	ft.recordTurn(mixedResults([]string{sigA, sigB}))
	if ft.count() != 0 {
		t.Errorf("after mixed (has success): count = %d, want 0", ft.count())
	}
}

func TestFailureTracker_ReachesMax(t *testing.T) {
	var ft failureTracker
	sig := "read_file:{\"path\":\"foo.txt\"}"
	maxFailures := 3

	for i := 1; i <= maxFailures; i++ {
		count := ft.recordTurn(failResults(sig))
		if count != i {
			t.Errorf("turn %d: count = %d, want %d", i, count, i)
		}
	}

	if ft.count() != maxFailures {
		t.Errorf("final count = %d, want %d", ft.count(), maxFailures)
	}
}

func TestFailureTracker_DifferentFailuresInOneTurn(t *testing.T) {
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
	var ft failureTracker
	ft.lastFailureSig = "some_sig"
	ft.failCount = 5

	ft.reset()
	if ft.count() != 0 {
		t.Errorf("after reset: count = %d, want 0", ft.count())
	}
	if ft.lastFailureSig != "" {
		t.Errorf("after reset: lastFailureSig = %q, want empty", ft.lastFailureSig)
	}
}
