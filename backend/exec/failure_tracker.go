package exec

// turnResult describes the outcome of a single tool call within a turn.
type turnResult struct {
	signature string // the identity the count runs on: name + ":" + args
	name      string // the tool called
	rawArgs   string // the raw arguments it was given
	success   bool
}

// callResult records one tool call's outcome. The signature is built here so it cannot disagree with the
// pair the loop breaker matches rows on.
func callResult(name, rawArgs string, success bool) turnResult {
	return turnResult{signature: name + ":" + rawArgs, name: name, rawArgs: rawArgs, success: success}
}

// failureTracker counts consecutive turns where identical tool calls fail.
// "Identical" means the same tool name with the same arguments.
// Multiple failures of the same call in one turn count as a single failure.
type failureTracker struct {
	// lastFailure is the failed call the count runs on: its signature says whether the next turn is the
	// same failure, its name and arguments say which call it was.
	lastFailure turnResult
	failCount   int // consecutive identical failures
}

// failingCall returns the tool and arguments of the failure being counted. Empty when nothing is counted.
func (t *failureTracker) failingCall() (name, rawArgs string) {
	return t.lastFailure.name, t.lastFailure.rawArgs
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
		// Different tool calls failed — reset counter, start fresh with the last one
		t.lastFailure = results[len(results)-1]
		t.failCount = 1
		return 1
	}

	// All failed with the same signature
	repeat := results[0].signature == t.lastFailure.signature
	t.lastFailure = results[0]
	if repeat {
		t.failCount++
	} else {
		t.failCount = 1
	}
	return t.failCount
}

func (t *failureTracker) reset() {
	t.lastFailure = turnResult{}
	t.failCount = 0
}
