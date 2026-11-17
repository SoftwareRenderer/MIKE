package exec

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mike/backend/config"
	"mike/backend/store"
	"mike/backend/types"
)

// newToolTestExecutor creates a TaskExecutor with no LSP manager — the state a host-side tool call
// runs against when no language extension is installed.
func newToolTestExecutor(t *testing.T, worktreePath string) *TaskExecutor {
	t.Helper()
	s := testStore(t)
	appStreamMgr := newTestStreamMgr(t, s, nil)
	return newTestExecutorWith(t, Deps{
		Store:        s,
		AppStreamMgr: appStreamMgr,
		ConfigStore:  &config.ConfigStore{},
	})
}

func makeTestTask(t *testing.T, s *store.Store, projectID, taskID string) *types.Task {
	t.Helper()
	createProject(t, s, projectID, projectID)
	now := time.Now().UTC()
	task := &types.Task{
		ID:             taskID,
		ProjectID:      projectID,
		Title:          "Test Task",
		Prompt:         "Test prompt",
		Status:         types.StatusInProgress,
		ExecutionState: types.ExecStateRunning,
		LlamaServerURL: "http://test:8080",
		ModelName:      "test-model",
		OperatingMode:  "act",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	return task
}

// windowedLines is the file the window rows read: 100 identifiable lines with no trailing newline,
// so strings.Split yields exactly one element per line.
func windowedLines() string {
	lines := make([]string, 100)
	for i := range lines {
		lines[i] = fmt.Sprintf("line_%03d", i+1)
	}
	return strings.Join(lines, "\n")
}

// window returns lines from the windowed file, 1-indexed and inclusive.
func window(from, to int) string {
	return strings.Join(strings.Split(windowedLines(), "\n")[from-1:to], "\n")
}

// windowLines is one cut over the file the model asked for. An offset past the end is answered
// rather than answered with nothing — a silent empty read sends the model to reread a file that is
// already in front of it — and an oversized limit is clamped rather than refused.
func TestReadFileWindowsTheFile(t *testing.T) {
	t.Parallel()
	content := windowedLines()
	cases := []struct {
		name    string
		args    map[string]any
		want    string
		wantErr string
	}{
		{name: "no offset or limit", args: map[string]any{"path": "f.txt"}, want: content},
		{name: "offset 1 is the whole file", args: map[string]any{"path": "f.txt", "offset": float64(1)}, want: content},
		{name: "offset starts at that line", args: map[string]any{"path": "f.txt", "offset": float64(96)}, want: window(96, 100)},
		{name: "offset and limit", args: map[string]any{"path": "f.txt", "offset": float64(50), "limit": float64(6)}, want: window(50, 55)},
		{name: "limit alone takes the first lines", args: map[string]any{"path": "f.txt", "limit": float64(10)}, want: window(1, 10)},
		{name: "a limit larger than the file is clamped", args: map[string]any{"path": "f.txt", "limit": float64(1000)}, want: content},
		{name: "a limit past the offset is clamped to what is left",
			args: map[string]any{"path": "f.txt", "offset": float64(90), "limit": float64(500)}, want: window(90, 100)},
		{name: "an offset past the end says so, with the line count",
			args:    map[string]any{"path": "f.txt", "offset": float64(101)},
			wantErr: "offset 101 is past the end of the file (file has 100 line(s))"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			te := newToolTestExecutor(t, dir)
			task := makeTestTask(t, te.store, "proj-win-"+strings.ReplaceAll(tc.name, " ", "-"), "task-win")
			if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte(content), 0o644); err != nil {
				t.Fatalf("seed file: %v", err)
			}

			result, ok := te.executeTool(context.Background(), "read_file", tc.args, dir, task, &executionState{}, nil)
			if tc.wantErr != "" {
				if ok {
					t.Fatalf("expected failure, got success: %s", result)
				}
				if !strings.Contains(result, tc.wantErr) {
					t.Errorf("result %q does not say %q", result, tc.wantErr)
				}
				return
			}
			if !ok {
				t.Fatalf("expected success, got failure: %s", result)
			}
			if result != tc.want {
				t.Errorf("got %q, want %q", result, tc.want)
			}
			if strings.Contains(result, "truncated") {
				t.Errorf("a window that fits must not carry the cut notice: %q", result[:min(120, len(result))])
			}
		})
	}
}

// The window is cut to the per-result cap only after the offset and limit have been applied, so the
// model can reach any region of a huge file; the cut always says it happened, because a truncated
// read that reads as the whole one sends the model off to conclude what isn't there.
func TestReadFileCutsTheWindowNotTheFile(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		lines     []string
		args      map[string]any
		want      string // the exact result, when the whole file fits
		prefix    string // the first line the result must start with
		wantSub   string // a substring the result must carry
		wantErr   string // a substring the refusal must carry
		wantLines int    // the exact number of lines the result must hold
		maxLen    int    // bound on the result, 0 for none
	}{
		{
			name:  "a file under the cap comes back whole",
			lines: []string{"line 1", "line 2", "line 3", ""},
			args:  map[string]any{"path": "f.txt"},
			want:  "line 1\nline 2\nline 3\n",
		},
		{
			name:    "a file past the cap is cut and says so",
			lines:   paddedLines(6000),
			args:    map[string]any{"path": "f.txt"},
			prefix:  "xxxxxxxxxx",
			wantSub: "truncated",
			maxLen:  52 * 1024,
		},
		{
			name:   "an offset past the cut reads the region it named",
			lines:  numberedLines(8000),
			args:   map[string]any{"path": "f.txt", "offset": float64(6001)},
			prefix: "line_number_0_0",
		},
		{
			name:      "an offset and limit past the cut are not cut themselves",
			lines:     mixedLines(10000),
			args:      map[string]any{"path": "f.txt", "offset": float64(7001), "limit": float64(10)},
			wantLines: 10,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			te := newToolTestExecutor(t, dir)
			task := makeTestTask(t, te.store, "proj-cap-"+strings.ReplaceAll(tc.name, " ", "-"), "task-cap")
			content := strings.Join(tc.lines, "\n")
			if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte(content), 0o644); err != nil {
				t.Fatalf("seed file: %v", err)
			}

			result, ok := te.executeTool(context.Background(), "read_file", tc.args, dir, task, &executionState{}, nil)
			if tc.wantErr != "" {
				if ok {
					t.Fatalf("expected failure, got success: %s", result)
				}
				if !strings.Contains(result, tc.wantErr) {
					t.Errorf("result %q does not say %q", result, tc.wantErr)
				}
				return
			}
			if !ok {
				t.Fatalf("expected success, got failure: %s", result)
			}
			if tc.want != "" && result != tc.want {
				t.Errorf("got %q, want %q", result, tc.want)
			}
			if tc.prefix != "" && !strings.HasPrefix(result, tc.prefix) {
				t.Errorf("result starts %q, want %q", firstChars(result, 40), tc.prefix)
			}
			if tc.wantSub != "" && !strings.Contains(result, tc.wantSub) {
				t.Errorf("the cut is silent: %q", firstChars(result, 80))
			}
			if tc.wantSub == "" && strings.Contains(result, "truncated") {
				t.Errorf("a window that fits carries the cut notice: %q", firstChars(result, 80))
			}
			if tc.wantLines != 0 {
				if got := strings.Split(result, "\n"); len(got) != tc.wantLines {
					t.Errorf("result holds %d lines, want %d: %q", len(got), tc.wantLines, firstChars(result, 80))
				}
			}
			if tc.maxLen != 0 && len(result) > tc.maxLen {
				t.Errorf("result is %d bytes, want at most %d", len(result), tc.maxLen)
			}
		})
	}
}

func firstChars(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func paddedLines(n int) []string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = strings.Repeat("x", 10)
	}
	return lines
}

// numberedLines makes line i identifiable by its own digits, so a test can tell which region came back.
func numberedLines(n int) []string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = "line_number_" + string(rune('0'+i%10)) + "_" + string(rune('0'+(i/10)%10))
	}
	return lines
}

func mixedLines(n int) []string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = "line_" + string(rune('0'+i%10)) + "_content_" + string(rune('A'+i%26))
	}
	return lines
}
