package exec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kanban/backend/config"
	"kanban/backend/store"
	"kanban/backend/types"
)

// newReadTestExecutor creates a TaskExecutor with a test store and worktree for testing read_file.
func newReadTestExecutor(t *testing.T, worktreePath string) *TaskExecutor {
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

func TestReadFile_NoTruncation_SmallFile(t *testing.T) {
	dir := t.TempDir()
	te := newReadTestExecutor(t, dir)
	task := makeTestTask(t, te.store, "proj-read-1", "task-read-1")

	// Write a small file
	content := "line 1\nline 2\nline 3\n"
	filePath := "test.txt"
	if err := os.WriteFile(filepath.Join(dir, filePath), []byte(content), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	result, ok := te.executeTool(context.Background(), "read_file", map[string]any{
		"path": filePath,
	}, dir, task, &executionState{}, nil)

	if !ok {
		t.Fatalf("expected success, got error: %s", result)
	}
	if result != content {
		t.Errorf("expected full content, got:\n%s", result)
	}
	if strings.Contains(result, "truncated") {
		t.Error("small file should not be truncated")
	}
}

func TestReadFile_Truncation_LargeFile(t *testing.T) {
	dir := t.TempDir()
	te := newReadTestExecutor(t, dir)
	task := makeTestTask(t, te.store, "proj-read-2", "task-read-2")

	// Write a file larger than 50KB
	lines := make([]string, 6000)
	for i := 0; i < 6000; i++ {
		lines[i] = strings.Repeat("x", 10) // ~10 chars per line = ~60KB total
	}
	content := strings.Join(lines, "\n")
	filePath := "large.txt"
	if err := os.WriteFile(filepath.Join(dir, filePath), []byte(content), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	result, ok := te.executeTool(context.Background(), "read_file", map[string]any{
		"path": filePath,
	}, dir, task, &executionState{}, nil)

	if !ok {
		t.Fatalf("expected success, got error: %s", result)
	}
	if !strings.Contains(result, "truncated") {
		t.Error("large file should be truncated with notice")
	}
	// Result should be at most ~50KB + truncation notice
	if len(result) > 52*1024 {
		t.Errorf("expected result <= ~52KB, got %d bytes", len(result))
	}
}

func TestReadFile_Offset_AppliedBeforeTruncation(t *testing.T) {
	dir := t.TempDir()
	te := newReadTestExecutor(t, dir)
	task := makeTestTask(t, te.store, "proj-read-3", "task-read-3")

	// Write a file larger than 50KB with identifiable line content
	totalLines := 8000
	lines := make([]string, totalLines)
	for i := 0; i < totalLines; i++ {
		lines[i] = "line_number_" + string(rune('0'+i%10)) + "_" + string(rune('0'+(i/10)%10))
	}
	content := strings.Join(lines, "\n")
	filePath := "large-offset.txt"
	if err := os.WriteFile(filepath.Join(dir, filePath), []byte(content), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	// Read starting from line 6001 (past the 50KB truncation point)
	// With the fix, offset is applied BEFORE truncation, so this should work.
	// Without the fix, the file would be truncated at 50KB first, and offset=6001 would return empty.
	result, ok := te.executeTool(context.Background(), "read_file", map[string]any{
		"path":   filePath,
		"offset": float64(6001),
	}, dir, task, &executionState{}, nil)

	if !ok {
		t.Fatalf("expected success, got error: %s", result)
	}
	// Should contain content from line 6001 onward, not be empty
	if strings.Contains(result, "empty") || strings.Contains(result, "offset past end") {
		t.Errorf("expected content from line 6001, but got empty/offset-past-end. offset was likely applied after truncation. Result: %s", result[:200])
	}
	// First line should be line_number_1_0 (line 6001, 0-indexed: 6000)
	// 6000 % 10 = 0, 6000 / 10 % 10 = 0 -> "line_number_0_0"
	// Actually: lines[6000] = "line_number_" + string(rune('0'+6000%10)) + "_" + string(rune('0'+(6000/10)%10))
	// = "line_number_0_0"
	if !strings.HasPrefix(result, "line_number_0_0") {
		t.Errorf("expected first line to be 'line_number_0_0', got: %s", result[:50])
	}
}

func TestReadFile_OffsetAndLimit(t *testing.T) {
	dir := t.TempDir()
	te := newReadTestExecutor(t, dir)
	task := makeTestTask(t, te.store, "proj-read-4", "task-read-4")

	// Write a file with 100 lines
	lines := make([]string, 100)
	for i := 0; i < 100; i++ {
		lines[i] = "line_" + string(rune('0'+i%10))
	}
	content := strings.Join(lines, "\n")
	filePath := "test-offset-limit.txt"
	if err := os.WriteFile(filepath.Join(dir, filePath), []byte(content), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	// Read lines 50-55 (1-indexed)
	result, ok := te.executeTool(context.Background(), "read_file", map[string]any{
		"path":   filePath,
		"offset": float64(50),
		"limit":  float64(6),
	}, dir, task, &executionState{}, nil)

	if !ok {
		t.Fatalf("expected success, got error: %s", result)
	}
	resultLines := strings.Split(result, "\n")
	if len(resultLines) != 6 {
		t.Errorf("expected 6 lines, got %d: %v", len(resultLines), resultLines)
	}
	// Line 50 (1-indexed) is index 49 in 0-indexed
	// lines[49] = "line_" + string(rune('0'+49%10)) = "line_9"
	if resultLines[0] != "line_9" {
		t.Errorf("expected first line 'line_9', got %q", resultLines[0])
	}
}

func TestReadFile_LimitOnly(t *testing.T) {
	dir := t.TempDir()
	te := newReadTestExecutor(t, dir)
	task := makeTestTask(t, te.store, "proj-read-5", "task-read-5")

	// Write a file with 50 lines
	lines := make([]string, 50)
	for i := 0; i < 50; i++ {
		lines[i] = "line_" + string(rune('0'+i%10))
	}
	content := strings.Join(lines, "\n")
	filePath := "test-limit.txt"
	if err := os.WriteFile(filepath.Join(dir, filePath), []byte(content), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	// Read first 10 lines
	result, ok := te.executeTool(context.Background(), "read_file", map[string]any{
		"path":  filePath,
		"limit": float64(10),
	}, dir, task, &executionState{}, nil)

	if !ok {
		t.Fatalf("expected success, got error: %s", result)
	}
	resultLines := strings.Split(result, "\n")
	if len(resultLines) != 10 {
		t.Errorf("expected 10 lines, got %d: %v", len(resultLines), resultLines)
	}
	if resultLines[0] != "line_0" {
		t.Errorf("expected first line 'line_0', got %q", resultLines[0])
	}
}

func TestReadFile_OffsetPastEnd(t *testing.T) {
	dir := t.TempDir()
	te := newReadTestExecutor(t, dir)
	task := makeTestTask(t, te.store, "proj-read-6", "task-read-6")

	// Write a file with 10 lines
	content := "line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8\nline9\nline10\n"
	filePath := "test-past-end.txt"
	if err := os.WriteFile(filepath.Join(dir, filePath), []byte(content), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	// Read with offset past end: the LLM must be made aware of the invalid offset
	// so it can correct its request. This is reported as a failed tool call with a
	// descriptive message (not a silent empty result), and must not panic.
	result, ok := te.executeTool(context.Background(), "read_file", map[string]any{
		"path":   filePath,
		"offset": float64(100),
	}, dir, task, &executionState{}, nil)

	if ok {
		t.Fatalf("expected failure for offset past end, got success: %s", result)
	}
	if !strings.Contains(result, "Error") || !strings.Contains(result, "offset") {
		t.Errorf("expected error message informing about the invalid offset, got: %s", result)
	}
	if !strings.Contains(result, "10") {
		t.Errorf("expected error message to include the file line count, got: %s", result)
	}
}

// TestReadFile_OffsetPastEnd_NoPanic is a regression test for the production panic
// `slice bounds out of range [217:194]` (read_file with offset beyond the file's
// line count must surface an error, never panic).
func TestReadFile_OffsetPastEnd_NoPanic(t *testing.T) {
	dir := t.TempDir()
	te := newReadTestExecutor(t, dir)
	task := makeTestTask(t, te.store, "proj-read-8", "task-read-8")

	// 194 lines exactly (matches the panic: len(lines) == 194).
	lines := make([]string, 194)
	for i := range lines {
		lines[i] = "line_" + string(rune('0'+i%10))
	}
	content := strings.Join(lines, "\n")
	filePath := "repro-panic.txt"
	if err := os.WriteFile(filepath.Join(dir, filePath), []byte(content), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	// offset=218 -> offsetIdx=217 > len(lines)=194. Must NOT panic.
	_, ok := te.executeTool(context.Background(), "read_file", map[string]any{
		"path":   filePath,
		"offset": float64(218),
	}, dir, task, &executionState{}, nil)
	if ok {
		t.Fatal("expected failure for offset past end")
	}
}

// TestReadFile_OffsetValid_LimitClamped verifies that a valid offset with an
// oversized limit is silently clamped to the remaining lines (no error).
func TestReadFile_OffsetValid_LimitClamped(t *testing.T) {
	dir := t.TempDir()
	te := newReadTestExecutor(t, dir)
	task := makeTestTask(t, te.store, "proj-read-9", "task-read-9")

	lines := make([]string, 100)
	for i := range lines {
		lines[i] = "line_" + string(rune('0'+i%10))
	}
	content := strings.Join(lines, "\n")
	filePath := "clamp.txt"
	if err := os.WriteFile(filepath.Join(dir, filePath), []byte(content), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	// offset=90, limit=500 (limit exceeds the 11 remaining lines) -> clamped to 11 lines.
	result, ok := te.executeTool(context.Background(), "read_file", map[string]any{
		"path":   filePath,
		"offset": float64(90),
		"limit":  float64(500),
	}, dir, task, &executionState{}, nil)
	if !ok {
		t.Fatalf("expected success with clamped limit, got error: %s", result)
	}
	resultLines := strings.Split(result, "\n")
	if len(resultLines) != 11 {
		t.Errorf("expected 11 remaining lines (offset 90..100), got %d: %v", len(resultLines), resultLines)
	}
	if resultLines[0] != "line_9" {
		t.Errorf("expected first line 'line_9', got %q", resultLines[0])
	}
}

// TestReadFile_LimitOnly_Clamped verifies an oversized limit without an offset is
// silently clamped to the total line count.
func TestReadFile_LimitOnly_Clamped(t *testing.T) {
	dir := t.TempDir()
	te := newReadTestExecutor(t, dir)
	task := makeTestTask(t, te.store, "proj-read-10", "task-read-10")

	lines := make([]string, 50)
	for i := range lines {
		lines[i] = "line_" + string(rune('0'+i%10))
	}
	content := strings.Join(lines, "\n")
	filePath := "clamp-limit.txt"
	if err := os.WriteFile(filepath.Join(dir, filePath), []byte(content), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	result, ok := te.executeTool(context.Background(), "read_file", map[string]any{
		"path":  filePath,
		"limit": float64(1000),
	}, dir, task, &executionState{}, nil)
	if !ok {
		t.Fatalf("expected success with clamped limit, got error: %s", result)
	}
	resultLines := strings.Split(result, "\n")
	if len(resultLines) != 50 {
		t.Errorf("expected all 50 lines (limit clamped), got %d", len(resultLines))
	}
}

func TestReadFile_OffsetAndLimit_AfterTruncationBoundary(t *testing.T) {
	dir := t.TempDir()
	te := newReadTestExecutor(t, dir)
	task := makeTestTask(t, te.store, "proj-read-7", "task-read-7")

	// Write a file larger than 50KB
	totalLines := 10000
	lines := make([]string, totalLines)
	for i := 0; i < totalLines; i++ {
		lines[i] = "line_" + string(rune('0'+i%10)) + "_content_" + string(rune('A'+i%26))
	}
	content := strings.Join(lines, "\n")
	filePath := "large-with-limit.txt"
	if err := os.WriteFile(filepath.Join(dir, filePath), []byte(content), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	// Read lines 7001-7010 (past the 50KB truncation point)
	// With the fix: offset applied first, then limit, then truncation (result is small, no truncation)
	// Without the fix: truncation first, then offset=7001 returns empty
	result, ok := te.executeTool(context.Background(), "read_file", map[string]any{
		"path":   filePath,
		"offset": float64(7001),
		"limit":  float64(10),
	}, dir, task, &executionState{}, nil)

	if !ok {
		t.Fatalf("expected success, got error: %s", result)
	}
	if strings.Contains(result, "empty") || strings.Contains(result, "offset past end") {
		t.Errorf("expected 10 lines from offset 7001, but got empty. offset was applied after truncation. Result: %s", result)
	}
	if strings.Contains(result, "truncated") {
		// With limit=10, result should be small enough to not trigger truncation
		t.Errorf("expected no truncation with limit=10, but got truncation notice")
	}
	resultLines := strings.Split(result, "\n")
	if len(resultLines) != 10 {
		t.Errorf("expected 10 lines, got %d: %v", len(resultLines), resultLines)
	}
}
