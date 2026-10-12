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
		t.reset()
		return 0
	}

	for _, r := range results {
		if r.success {
			t.reset()
			return 0
		}
	}

	sigs := make(map[string]struct{})
	for _, r := range results {
		sigs[r.signature] = struct{}{}
	}

	if len(sigs) > 1 {
		t.lastFailureSig = results[len(results)-1].signature
		t.failCount = 1
		return 1
	}

	sig := results[0].signature
	if sig == t.lastFailureSig {
		t.failCount++
	} else {
		t.lastFailureSig = sig
		t.failCount = 1
	}
	return t.failCount
}

func (t *failureTracker) reset() {
	t.lastFailureSig = ""
	t.failCount = 0
}
