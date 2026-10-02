package stream

import (
	"encoding/json"
	"testing"
)

// Three producers write a task_diff_stats frame: the stored done-task snapshot, the worktree watcher and
// the executor. One diff must answer one hash and one has_committed_changes whichever of them spoke.
func TestDoneTaskStatsMatchAcrossProducers(t *testing.T) {
	repoDir, s, _, wm, task := setupDoneSnapshotTest(t)
	base := revParse(t, repoDir, "HEAD")
	tip := commitInWorktree(t, wm, "task-1", "a.txt", "alpha\n")
	markDone(t, s, task, base, tip)

	done, err := MaterializeDoneTaskDiff(s, wm, "proj-1", task)
	if err != nil || done == nil || !done.HasChanges {
		t.Fatalf("materialize done diff: %v %+v", err, done)
	}
	var stored map[string]any
	if err := json.Unmarshal([]byte(done.StatsJSON), &stored); err != nil {
		t.Fatalf("stored stats: %v", err)
	}

	var frames []DiffStatsEvent
	ww, err := NewWorktreeWatcher(wm, func(ev DiffStatsEvent) { frames = append(frames, ev) }, nil, nil)
	if err != nil {
		t.Skipf("skip done-task stats test (inotify limit): %v", err)
	}
	defer ww.Close()
	if err := ww.Watch("task-1", "proj-1"); err != nil {
		t.Fatalf("watch task: %v", err)
	}
	ww.computeAndBroadcast("task-1", "proj-1")
	if len(frames) != 1 {
		t.Fatalf("broadcast %d frames, want 1", len(frames))
	}

	if stored["hash"] != frames[0].DiffHash {
		t.Errorf("the snapshot and the watcher hash one done diff differently: snapshot %q, watcher %q",
			stored["hash"], frames[0].DiffHash)
	}
	if stored["has_committed_changes"] != true || !frames[0].HasCommittedChanges {
		t.Errorf("has_committed_changes: snapshot %v, watcher %v; committed unmerged work must read true to both",
			stored["has_committed_changes"], frames[0].HasCommittedChanges)
	}
}
