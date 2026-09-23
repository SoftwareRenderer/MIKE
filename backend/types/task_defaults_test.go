package types

import "testing"

// TestSubtaskStatusFor pins the column a new subtask lands in. The task API, the agent's
// create_task tool and frontend/src/lib/utils/subtask-status.ts all read this rule.
func TestSubtaskStatusFor(t *testing.T) {
	for _, tc := range []struct {
		parent Status
		want   Status
	}{
		{StatusBacklog, StatusBacklog},
		{StatusInProgress, StatusInProgress},
		{StatusReview, StatusBacklog},
		{StatusDone, StatusBacklog},
		// A status that is not a column must still yield one, never the input back.
		{Status(""), StatusBacklog},
		{Status("archived"), StatusBacklog},
	} {
		got := SubtaskStatusFor(tc.parent)
		if got != tc.want {
			t.Errorf("SubtaskStatusFor(%q) = %q, want %q", tc.parent, got, tc.want)
		}
		// The answer is always a column the board renders.
		if _, ok := ParseStatus(string(got)); !ok {
			t.Errorf("SubtaskStatusFor(%q) = %q, which is not a column", tc.parent, got)
		}
	}
}

// TestClearSubtaskBaseBranch pins where a base branch may live.
func TestClearSubtaskBaseBranch(t *testing.T) {
	pinned := "0123456789abcdef0123456789abcdef01234567"

	root := &Task{BaseRef: "test", BaseCommit: pinned}
	ClearSubtaskBaseBranch(root)
	if root.BaseRef != "test" || root.BaseCommit != pinned {
		t.Errorf("a root task kept %q/%q, want its own base branch and pinned commit", root.BaseRef, root.BaseCommit)
	}

	child := &Task{ParentTaskID: "parent-1", BaseRef: "test", BaseCommit: pinned}
	ClearSubtaskBaseBranch(child)
	if child.BaseRef != "" || child.BaseCommit != "" {
		t.Errorf("a nested task kept %q/%q, want both cleared", child.BaseRef, child.BaseCommit)
	}
}
