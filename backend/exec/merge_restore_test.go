package exec

// The Done gate reads one error value that can arrive carrying two different failures — the merge, and
// the bracket's restore of the checkout's own parked work. Each wrong answer breaks the opposite way:
// too strict leaves a merged task in Review with no explanation, too loose marks Done a task whose work
// never reached the target branch.

import (
	"errors"
	"fmt"
	"testing"

	"mike/backend/git"
	"mike/backend/types"
)

// conflictedRestore is what the git layer reports when the parked work came back unmerged.
var conflictedRestore = &git.RestoreConflictError{
	Hash:       "0f1e2d3c4b5a",
	Paths:      []string{"notes.md"},
	GitMessage: "CONFLICT (content): Merge conflict in notes.md",
}

// TestDoneGateNeedsADeliveredMerge is the classification both the merge caller and the resolving
// turn's Done decision read: MergedDelivered answers "did the branch reach its target" and
// conflictResolution.finishesTask adds what a click still requires — a commit, a wanted merge, and a
// conflict that is over. A click that survived a conflict must not be stopped by the restore that
// followed its merge.
func TestDoneGateNeedsADeliveredMerge(t *testing.T) {
	t.Parallel()

	// mergeInto adds the sentinel only when the merge landed, which is the whole classification.
	landed := fmt.Errorf("%w: %w", errMergedWithConflictedRestore, conflictedRestore)
	// The bracket's own answer when the merge failed and the restore conflicted too: the restore conflict
	// is still reachable inside a failed merge, which is why mergeInto classifies on the merge outcome.
	bothFailed := errors.Join(
		errors.New("checkout target branch main: exit status 1"),
		fmt.Errorf("restore the stashed work: %w", conflictedRestore),
	)
	mergeFailed := errors.New("merge failed (not a conflict): nothing to merge")

	tests := []struct {
		name          string
		mergeErr      error
		committed     bool
		clicked       bool
		autoMerge     bool
		stillConflict bool
		wantDelivered bool
		wantFinishes  bool
	}{
		{name: "nothing failed", committed: true, clicked: true, autoMerge: true, wantDelivered: true, wantFinishes: true},
		{
			name:     "the branch landed, the parked work conflicted",
			mergeErr: landed, committed: true, clicked: true, autoMerge: true,
			wantDelivered: true, wantFinishes: true,
		},
		{
			name:     "the merge failed and the restore conflicted",
			mergeErr: bothFailed, committed: true, clicked: true, autoMerge: true,
		},
		{
			// The sync-merge bracket can conflict before the final merge has run. A call site that
			// classified on the git error directly would mark that task done on an unmerged branch.
			name:     "a sync restore conflicted with no merge yet",
			mergeErr: conflictedRestore, committed: true, clicked: true, autoMerge: true,
		},
		{
			name:     "an ordinary merge failure",
			mergeErr: mergeFailed, committed: true, clicked: true, autoMerge: true,
		},
		{
			name:      "nothing was committed",
			committed: false, clicked: true, autoMerge: true,
			wantDelivered: true,
		},
		{
			// A resolution the person never asked to merge, with auto_merge off: nothing landed,
			// so nothing is Done.
			name:      "no merge was wanted",
			committed: true, wantDelivered: true,
		},
		{
			name:      "the task is still conflicted",
			committed: true, clicked: true, autoMerge: true, stillConflict: true,
			wantDelivered: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := MergedDelivered(tc.mergeErr); got != tc.wantDelivered {
				t.Errorf("MergedDelivered(%v) = %v, want %v", tc.mergeErr, got, tc.wantDelivered)
			}
			resolution := conflictResolution{
				task:       &types.Task{ID: "resolving-task", AutoMerge: tc.autoMerge, HasMergeConflict: tc.stillConflict},
				committed:  tc.committed,
				mergeErr:   tc.mergeErr,
				commitDone: tc.clicked,
			}
			if got := resolution.finishesTask(); got != tc.wantFinishes {
				t.Errorf("finishesTask = %v, want %v", got, tc.wantFinishes)
			}
		})
	}
}
