package stream

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// streamInitialFrames dials a real HandleStream server, subscribes to the
// project (with optional known_stats), and collects frames until the trailing
// project_branch frame arrives (the last frame sendInitialProjectData emits).
func streamInitialFrames(t *testing.T, asm *AppStreamManager, projectID string, knownStats map[string]string) []map[string]any {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(asm.HandleStream))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	sub := map[string]any{"project_id": projectID}
	if len(knownStats) > 0 {
		sub["known_stats"] = knownStats
	}
	data, _ := json.Marshal(sub)
	payload := []byte(`{"type":"subscribe_project","data":` + string(data) + `}`)
	if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	var frames []map[string]any
	for {
		_, raw, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		var frame map[string]any
		if err := json.Unmarshal(raw, &frame); err != nil {
			continue
		}
		frames = append(frames, frame)
		if frame["type"] == "project_branch" {
			return frames
		}
	}
}

func diffStatsFrames(frames []map[string]any) []map[string]any {
	var out []map[string]any
	for _, f := range frames {
		if f["type"] == "task_diff_stats" {
			out = append(out, f)
		}
	}
	return out
}

func TestDoneTaskInitialStats_SentWhenUnknown(t *testing.T) {
	repoDir, s, _, wm, task := setupDoneSnapshotTest(t)
	base := revParse(t, repoDir, "HEAD")
	tip := commitInWorktree(t, wm, "task-1", "a.txt", "alpha\n")
	markDone(t, s, task, base, tip)

	asm := NewAppStreamManager(s, wm)
	t.Cleanup(asm.Close)

	frames := streamInitialFrames(t, asm, "proj-1", nil)
	stats := diffStatsFrames(frames)
	if len(stats) != 1 {
		t.Fatalf("expected 1 task_diff_stats frame, got %d", len(stats))
	}
	if stats[0]["task_id"] != "task-1" {
		t.Fatalf("expected task_id=task-1, got %v", stats[0]["task_id"])
	}
	if stats[0]["files_n"] != float64(1) {
		t.Fatalf("expected files_n=1, got %v", stats[0]["files_n"])
	}
	if stats[0]["has_committed_changes"] != true {
		t.Fatalf("expected has_committed_changes=true, got %v", stats[0]["has_committed_changes"])
	}
	// already_merged must be present (computed fresh, not snapshotted).
	if _, ok := stats[0]["already_merged"]; !ok {
		t.Fatal("expected already_merged field in frame")
	}
}

func TestDoneTaskInitialStats_SkippedWhenKnown(t *testing.T) {
	repoDir, s, _, wm, task := setupDoneSnapshotTest(t)
	base := revParse(t, repoDir, "HEAD")
	tip := commitInWorktree(t, wm, "task-1", "a.txt", "alpha\n")
	markDone(t, s, task, base, tip)
	fresh, err := s.GetTaskByTaskID("task-1")
	if err != nil {
		t.Fatalf("reload task: %v", err)
	}

	asm := NewAppStreamManager(s, wm)
	t.Cleanup(asm.Close)

	// The client declares it already has stats for this exact task state.
	known := map[string]string{"task-1": fresh.UpdatedAt.Format(time.RFC3339)}
	frames := streamInitialFrames(t, asm, "proj-1", known)
	if stats := diffStatsFrames(frames); len(stats) != 0 {
		t.Fatalf("expected no task_diff_stats frame (known to client), got %d", len(stats))
	}
}

func TestDoneTaskInitialStats_StaleKnownStillSends(t *testing.T) {
	repoDir, s, _, wm, task := setupDoneSnapshotTest(t)
	base := revParse(t, repoDir, "HEAD")
	tip := commitInWorktree(t, wm, "task-1", "a.txt", "alpha\n")
	markDone(t, s, task, base, tip)

	asm := NewAppStreamManager(s, wm)
	t.Cleanup(asm.Close)

	// Stale updated_at (task changed since the client cached) → must send.
	known := map[string]string{"task-1": "2000-01-01T00:00:00Z"}
	frames := streamInitialFrames(t, asm, "proj-1", known)
	if stats := diffStatsFrames(frames); len(stats) != 1 {
		t.Fatalf("expected 1 task_diff_stats frame for stale known state, got %d", len(stats))
	}
}

func TestDoneTaskInitialStats_ServedFromSnapshotWithoutGit(t *testing.T) {
	repoDir, s, _, wm, task := setupDoneSnapshotTest(t)
	base := revParse(t, repoDir, "HEAD")
	tip := commitInWorktree(t, wm, "task-1", "a.txt", "alpha\n")
	markDone(t, s, task, base, tip)

	// Materialize the snapshot first (this is what an earlier connection did).
	if _, err := MaterializeDoneTaskDiff(s, wm, "proj-1", task); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	// Break git entirely: the frame must come from the snapshot, not git.
	if err := os.Rename(filepath.Join(repoDir, ".git"), filepath.Join(repoDir, ".git.bak")); err != nil {
		t.Fatalf("rename .git: %v", err)
	}
	t.Cleanup(func() { _ = os.Rename(filepath.Join(repoDir, ".git.bak"), filepath.Join(repoDir, ".git")) })

	asm := NewAppStreamManager(s, wm)
	t.Cleanup(asm.Close)

	frames := streamInitialFrames(t, asm, "proj-1", nil)
	stats := diffStatsFrames(frames)
	if len(stats) != 1 {
		t.Fatalf("expected 1 task_diff_stats frame from snapshot, got %d", len(stats))
	}
	if stats[0]["files_n"] != float64(1) {
		t.Fatalf("expected files_n=1, got %v", stats[0]["files_n"])
	}
}

// TestNilProjectWatcherNoPanic covers the pre-existing latent panic: when
// fsnotify.NewWatcher fails (e.g. inotify limit exhausted under heavy
// parallel load), NewAppStreamManager leaves projectWatcher nil and the
// unguarded Watch/Unwatch calls nil-panicked, dropping the WS connection.
func TestNilProjectWatcherNoPanic(t *testing.T) {
	_, s, _, wm, _ := setupDoneSnapshotTest(t)

	asm := NewAppStreamManager(s, wm)
	t.Cleanup(asm.Close)
	// Simulate an fsnotify init failure (which really does happen under heavy
	// parallel test load — inotify limit). If the watcher was created, tear it
	// down first so it can't keep firing events during the test.
	if asm.projectWatcher != nil {
		asm.projectWatcher.Close()
		asm.projectWatcher = nil
	}

	// Subscribe + initial data must complete with the connection intact.
	frames := streamInitialFrames(t, asm, "proj-1", nil)
	if len(frames) == 0 {
		t.Fatal("expected initial data frames")
	}
}
