package exec

// turnResult describes the outcome of a single tool call within a turn.
type turnResult struct {
	signature string // "tool_name:raw_args" identifying the call
	success   bool
}

// failureTracker counts consecutive turns where identical tool calls fail.
// "Identical" means the same tool name with the same arguments.
// Multiple failures of the same call in one turn count as a single failure.
type failureTracker struct {
	lastFailureSig string // signature of the last failed call
	failCount      int    // consecutive identical failures
}

// recordTurn processes the results of all tool calls in a single turn.
// Pass an empty slice for a text-only (no tool calls) response.
// Returns the current consecutive failure count after processing.
func (t *failureTracker) recordTurn(results []turnResult) int {
	if len(results) == 0 {
		// Text-only response resets counter
		t.reset()
		return 0
	}

	// If any tool succeeded, reset
	for _, r := range results {
		if r.success {
			t.reset()
			return 0
		}
	}

	// All failed — check if they all share the same signature
	sigs := make(map[string]struct{})
	for _, r := range results {
		sigs[r.signature] = struct{}{}
	}

	if len(sigs) > 1 {
		// Different tool calls failed — reset counter, start fresh with the last one
		t.lastFailureSig = results[len(results)-1].signature
		t.failCount = 1
		return 1
	}

	// All failed with the same signature
	sig := results[0].signature
	if sig == t.lastFailureSig {
		t.failCount++
	} else {
		t.lastFailureSig = sig
		t.failCount = 1
	}
	return t.failCount
}

// count returns the current consecutive failure count.
func (t *failureTracker) count() int {
	return t.failCount
}

// reset clears all tracked state.
func (t *failureTracker) reset() {
	t.lastFailureSig = ""
	t.failCount = 0
}
