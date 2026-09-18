package exec

// The Done gate reads one error value that can arrive carrying two different failures — the merge, and
// the bracket's restore of the checkout's own parked work. These pin which shape answers "delivered",
// because each answer is wrong in the opposite direction: too strict leaves a merged task in Review with
// no explanation, too loose marks a task done whose work never reached the target branch.

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

func TestMergedDeliveredReadsTheMergeNotTheRestore(t *testing.T) {
	// mergeInto adds the sentinel only when the merge landed, which is the whole classification.
	landed := fmt.Errorf("%w: %w", errMergedWithConflictedRestore, conflictedRestore)
	// The bracket's own answer when the merge failed and the restore conflicted too: the restore conflict
	// is still reachable inside a failed merge, which is why mergeInto classifies on the merge outcome.
	bothFailed := errors.Join(
		errors.New("checkout target branch main: exit status 1"),
		fmt.Errorf("restore the stashed work: %w", conflictedRestore),
	)

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nothing failed",
			err:  nil,
			want: true,
		},
		{
			name: "the branch landed, the parked work conflicted",
			err:  landed,
			want: true,
		},
		{
			name: "the merge failed and the restore conflicted",
			err:  bothFailed,
			want: false,
		},
		{
			// The sync-merge bracket can conflict before the final merge has run. A call site that
			// classified on the git error directly would mark that task done on an unmerged branch.
			name: "a sync restore conflicted with no merge yet",
			err:  conflictedRestore,
			want: false,
		},
		{
			name: "an ordinary merge failure",
			err:  errors.New("merge failed (not a conflict): nothing to merge"),
			want: false,
		},
	}

	for _, tc := range tests {
		if got := MergedDelivered(tc.err); got != tc.want {
			t.Errorf("%s: MergedDelivered = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestConflictResolutionFinishesOnALandedMerge: the resolving turn's Done decision reads the same
// predicate, or a click that survived a conflict is stopped by the restore that followed its merge.
func TestConflictResolutionFinishesOnALandedMerge(t *testing.T) {
	resolution := func(mergeErr error) conflictResolution {
		return conflictResolution{
			task:       &types.Task{ID: "resolving-task", AutoMerge: true},
			committed:  true,
			mergeErr:   mergeErr,
			commitDone: true,
		}
	}

	if !resolution(nil).finishesTask() {
		t.Fatal("fixture: a clean resolution finishes the task")
	}
	if !resolution(fmt.Errorf("%w: %w", errMergedWithConflictedRestore, conflictedRestore)).finishesTask() {
		t.Error("a resolution whose merge landed does not finish the task")
	}
	if resolution(errors.New("merge failed (not a conflict): nothing to merge")).finishesTask() {
		t.Error("a resolution whose merge failed finishes the task")
	}
}
