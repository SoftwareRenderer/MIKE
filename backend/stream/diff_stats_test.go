package stream

// ComputeDiffStats is the one place the diff panel's numbers are computed: every producer of a
// task_diff_stats frame (the worktree watcher, the executor, the done-task snapshot) reads them from
// here, so a field must mean the same thing whichever of them sent it.

import (
	"testing"

	"mike/backend/git"
)

func TestComputeDiffStatsCountsVisibleFilesOnly(t *testing.T) {
	tests := []struct {
		name                 string
		tracked              map[string]*git.TrackedDiffFile
		untracked            []git.UntrackedFile
		wantTracked          int
		wantUntracked        int
		wantVisibleTracked   int
		wantVisibleUntracked int
		wantUntrackedLines   int
	}{
		{
			name: "empty",
		},
		{
			name: "single tracked file",
			tracked: map[string]*git.TrackedDiffFile{
				"a.txt": {Path: "a.txt", Added: 5, Removed: 3},
			},
			wantTracked:        5,
			wantUntracked:      3,
			wantVisibleTracked: 1,
		},
		{
			name: "multiple tracked files",
			tracked: map[string]*git.TrackedDiffFile{
				"a.txt": {Path: "a.txt", Added: 5, Removed: 3},
				"b.txt": {Path: "b.txt", Added: 2, Removed: 1},
			},
			wantTracked:        7,
			wantUntracked:      4,
			wantVisibleTracked: 2,
		},
		{
			name: "truncated tracked file uses truncated counts",
			tracked: map[string]*git.TrackedDiffFile{
				// After truncation, Added/Removed reflect the truncated lines and
				// OrigAdded/OrigRemoved the full file. The panel shows the truncated number.
				"large.txt": {Path: "large.txt", Added: 6, Removed: 0, Truncated: true, OrigAdded: 90, OrigRemoved: 0},
			},
			wantTracked:        6, // truncated count, NOT 90
			wantVisibleTracked: 1,
		},
		{
			name: "excludes internal files from the tracked side",
			tracked: map[string]*git.TrackedDiffFile{
				"a.txt":        {Path: "a.txt", Added: 5, Removed: 3},
				".git/config":  {Path: ".git/config", Added: 100, Removed: 200},
				"kanban.trace": {Path: "kanban.trace", Added: 7, Removed: 7},
			},
			wantTracked:        5, // only a.txt
			wantUntracked:      3,
			wantVisibleTracked: 1,
		},
		{
			name: "excludes internal files from the untracked side",
			tracked: map[string]*git.TrackedDiffFile{
				"a.txt": {Path: "a.txt", Added: 1, Removed: 1},
			},
			untracked: []git.UntrackedFile{
				{Path: "new.txt", Lines: []string{"line1"}},
				{Path: ".git/new", Lines: []string{"spam1", "spam2", "spam3", "spam4"}},
			},
			wantTracked:          1,
			wantUntracked:        1,
			wantVisibleTracked:   1,
			wantVisibleUntracked: 1,
			wantUntrackedLines:   1, // new.txt, NOT .git/new
		},
		{
			name: "a truncated untracked file counts its original lines",
			untracked: []git.UntrackedFile{
				{Path: "new.txt", Lines: []string{"line1"}, OrigAdded: 100, Truncated: true},
			},
			wantVisibleUntracked: 1,
			wantUntrackedLines:   100,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ComputeDiffStats(&git.TaskDiff{Tracked: tt.tracked, Untracked: tt.untracked})
			if got.TrackedAdd != tt.wantTracked || got.TrackedDel != tt.wantUntracked {
				t.Errorf("tracked add/del = %d/%d, want %d/%d",
					got.TrackedAdd, got.TrackedDel, tt.wantTracked, tt.wantUntracked)
			}
			if got.VisibleTracked != tt.wantVisibleTracked || got.VisibleUntracked != tt.wantVisibleUntracked {
				t.Errorf("visible tracked/untracked = %d/%d, want %d/%d",
					got.VisibleTracked, got.VisibleUntracked, tt.wantVisibleTracked, tt.wantVisibleUntracked)
			}
			if got.UntrackedLines != tt.wantUntrackedLines {
				t.Errorf("UntrackedLines = %d, want %d", got.UntrackedLines, tt.wantUntrackedLines)
			}
		})
	}
}

// The client adds UntrackedLines to TrackedAdd to get the card's "+N", so folding untracked lines into
// TrackedAdd here — which one of this function's two predecessors did — prints every untracked line
// twice.
func TestComputeDiffStatsKeepsUntrackedLinesOutOfTrackedAdd(t *testing.T) {
	got := ComputeDiffStats(&git.TaskDiff{
		Tracked:   map[string]*git.TrackedDiffFile{"a.txt": {Path: "a.txt", Added: 3, Removed: 2}},
		Untracked: []git.UntrackedFile{{Path: "new.txt", Lines: []string{"l1", "l2", "l3"}}},
	})
	if got.TrackedAdd != 3 {
		t.Errorf("TrackedAdd = %d, want 3 — untracked lines belong to UntrackedLines", got.TrackedAdd)
	}
	if got.UntrackedLines != 3 {
		t.Errorf("UntrackedLines = %d, want 3", got.UntrackedLines)
	}
}
