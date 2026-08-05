package exec

import (
	"strings"
	"testing"

	"kanban/backend/types"
)

// --- utilityTarget Tests ---

func TestUtilityTarget_UsesConfiguredUtilityProvider(t *testing.T) {
	e := newTestExecutor(t)

	// Configure two providers and set utility to the second one.
	providers := []types.Provider{
		{Name: "worker", URL: "http://worker:8010/v1", Models: []types.ModelConfig{{Name: "chat-model"}}},
		{Name: "utility", URL: "http://utility:8010/v1", Models: []types.ModelConfig{{Name: "cheap-model"}}},
	}
	if err := e.configStore.UpdateConfig(&types.Config{
		Providers:       providers,
		WorkerProvider:  "worker",
		WorkerModel:     "chat-model",
		UtilityProvider: "utility",
		UtilityModel:    "cheap-model",
	}); err != nil {
		t.Fatalf("update config: %v", err)
	}

	task := &types.Task{
		ID:            "task-1",
		LlamaServerURL: "http://worker:8010/v1",
		ModelName:      "chat-model",
	}

	url, model, ok := e.utilityTarget(task)
	if !ok {
		t.Errorf("ok = false, want true (utility configured)")
	}
	if url != "http://utility:8010/v1" {
		t.Errorf("url = %q, want utility provider URL", url)
	}
	if model != "cheap-model" {
		t.Errorf("model = %q, want utility model", model)
	}
}

func TestUtilityTarget_FallsBackToTaskWhenUnset(t *testing.T) {
	e := newTestExecutor(t)

	if err := e.configStore.UpdateConfig(&types.Config{
		Providers: []types.Provider{{Name: "worker", URL: "http://worker:8010/v1"}},
	}); err != nil {
		t.Fatalf("update config: %v", err)
	}

	task := &types.Task{
		LlamaServerURL: "http://task-url:8010/v1",
		ModelName:      "task-model",
	}

	url, model, ok := e.utilityTarget(task)
	if ok {
		t.Errorf("ok = true, want false (utility unset)")
	}
	if url != "http://task-url:8010/v1" {
		t.Errorf("url = %q, want task URL fallback", url)
	}
	if model != "task-model" {
		t.Errorf("model = %q, want task model fallback", model)
	}
}

func TestUtilityTarget_AgentProvider(t *testing.T) {
	e := newTestExecutor(t)

	providers := []types.Provider{
		{Name: "pi", Kind: "agent", URL: "agent://pi"},
	}
	if err := e.configStore.UpdateConfig(&types.Config{
		Providers:       providers,
		UtilityProvider: "pi",
		UtilityModel:    "pi",
	}); err != nil {
		t.Fatalf("update config: %v", err)
	}

	task := &types.Task{ID: "task-1"}
	url, model, ok := e.utilityTarget(task)
	if !ok {
		t.Errorf("ok = false, want true (agent utility configured)")
	}
	if url != "agent://pi" {
		t.Errorf("url = %q, want agent URL", url)
	}
	if model != "pi" {
		t.Errorf("model = %q, want agent model", model)
	}
}

// --- collectUniqueModels Tests ---

func TestCollectUniqueModels_Empty(t *testing.T) {
	models := collectUniqueModels(nil)
	if len(models) != 0 {
		t.Errorf("expected 0 models, got %d: %v", len(models), models)
	}
}

func TestCollectUniqueModels_SingleModel(t *testing.T) {
	msgs := []types.TaskMessage{
		{Role: "assistant", ModelName: "gpt-4o"},
		{Role: "assistant", ModelName: "gpt-4o"},
		{Role: "assistant", ModelName: "gpt-4o"},
	}
	models := collectUniqueModels(msgs)
	if len(models) != 1 || models[0] != "gpt-4o" {
		t.Errorf("expected [gpt-4o], got %v", models)
	}
}

func TestCollectUniqueModels_MultipleModels(t *testing.T) {
	msgs := []types.TaskMessage{
		{Role: "assistant", ModelName: "gpt-4o"},
		{Role: "assistant", ModelName: "gpt-4o"},
		{Role: "assistant", ModelName: "claude-3.5"},
		{Role: "assistant", ModelName: "gpt-4o"}, // duplicate, should not appear again
		{Role: "assistant", ModelName: "llama-3"},
	}
	models := collectUniqueModels(msgs)
	expected := []string{"gpt-4o", "claude-3.5", "llama-3"}
	if len(models) != len(expected) {
		t.Fatalf("expected %d models, got %d: %v", len(expected), len(models), models)
	}
	for i, m := range expected {
		if models[i] != m {
			t.Errorf("models[%d] = %q, want %q", i, models[i], m)
		}
	}
}

func TestCollectUniqueModels_PreservesOrder(t *testing.T) {
	msgs := []types.TaskMessage{
		{Role: "assistant", ModelName: "second"},
		{Role: "assistant", ModelName: "first"},
		{Role: "assistant", ModelName: "third"},
	}
	models := collectUniqueModels(msgs)
	// Order should be first appearance: second, first, third
	expected := []string{"second", "first", "third"}
	if len(models) != len(expected) {
		t.Fatalf("expected %d models, got %d", len(expected), len(models))
	}
	for i, m := range expected {
		if models[i] != m {
			t.Errorf("models[%d] = %q, want %q", i, models[i], m)
		}
	}
}

func TestCollectUniqueModels_SkipsEmptyModelName(t *testing.T) {
	msgs := []types.TaskMessage{
		{Role: "assistant", ModelName: ""},
		{Role: "assistant", ModelName: "gpt-4o"},
		{Role: "assistant", ModelName: ""},
		{Role: "assistant", ModelName: "claude-3.5"},
	}
	models := collectUniqueModels(msgs)
	expected := []string{"gpt-4o", "claude-3.5"}
	if len(models) != len(expected) {
		t.Fatalf("expected %d models, got %d: %v", len(expected), len(models), models)
	}
	for i, m := range expected {
		if models[i] != m {
			t.Errorf("models[%d] = %q, want %q", i, models[i], m)
		}
	}
}

func TestCollectUniqueModels_AllEmpty(t *testing.T) {
	msgs := []types.TaskMessage{
		{Role: "assistant", ModelName: ""},
		{Role: "assistant", ModelName: ""},
	}
	models := collectUniqueModels(msgs)
	if len(models) != 0 {
		t.Errorf("expected 0 models, got %d: %v", len(models), models)
	}
}

func TestCollectUniqueModels_UserMessagesIgnored(t *testing.T) {
	// Only assistant messages have model names; user/tool/summarization messages are skipped
	msgs := []types.TaskMessage{
		{Role: "user", ModelName: ""},
		{Role: "assistant", ModelName: "gpt-4o"},
		{Role: "tool", ModelName: ""},
	}
	models := collectUniqueModels(msgs)
	if len(models) != 1 || models[0] != "gpt-4o" {
		t.Errorf("expected [gpt-4o], got %v", models)
	}
}

// --- buildCoAuthoredBy Tests ---

func TestBuildCoAuthoredBy_Empty(t *testing.T) {
	result := buildCoAuthoredBy(nil)
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}

func TestBuildCoAuthoredBy_SingleModel(t *testing.T) {
	result := buildCoAuthoredBy([]string{"gpt-4o"})
	expected := "\n\nCo-authored-by: gpt-4o"
	if result != expected {
		t.Errorf("got %q, want %q", result, expected)
	}
}

func TestBuildCoAuthoredBy_MultipleModels(t *testing.T) {
	result := buildCoAuthoredBy([]string{"gpt-4o", "claude-3.5", "llama-3"})
	lines := strings.Split(result, "\n")
	// "\n\n" splits into ["", "", "Co-authored-by: gpt-4o", "Co-authored-by: claude-3.5", "Co-authored-by: llama-3"]
	if len(lines) != 5 {
		t.Fatalf("expected 5 lines (2 empty prefix + 3 co-authored), got %d: %q", len(lines), result)
	}
	if lines[0] != "" || lines[1] != "" {
		t.Errorf("first two lines should be empty (from \n\n prefix)")
	}
	if lines[2] != "Co-authored-by: gpt-4o" {
		t.Errorf("line 2 = %q", lines[2])
	}
	if lines[3] != "Co-authored-by: claude-3.5" {
		t.Errorf("line 3 = %q", lines[3])
	}
	if lines[4] != "Co-authored-by: llama-3" {
		t.Errorf("line 4 = %q", lines[4])
	}
}

// --- buildCommitMessageWithCoAuthors Tests ---

func TestBuildCommitMessageWithCoAuthors_SingleModel(t *testing.T) {
	result := buildCommitMessageWithCoAuthors("Fix bug", []string{"gpt-4o"})
	expected := "Fix bug\n\nCo-authored-by: gpt-4o"
	if result != expected {
		t.Errorf("got %q, want %q", result, expected)
	}
}

func TestBuildCommitMessageWithCoAuthors_MultipleModels(t *testing.T) {
	result := buildCommitMessageWithCoAuthors("Add feature", []string{"gpt-4o", "claude-3.5"})
	if !strings.HasPrefix(result, "Add feature\n\n") {
		t.Errorf("should start with message body, got %q", result)
	}
	if !strings.Contains(result, "Co-authored-by: gpt-4o") {
		t.Error("should contain gpt-4o co-author")
	}
	if !strings.Contains(result, "Co-authored-by: claude-3.5") {
		t.Error("should contain claude-3.5 co-author")
	}
	// Count Co-authored-by lines
	count := strings.Count(result, "Co-authored-by:")
	if count != 2 {
		t.Errorf("expected 2 Co-authored-by lines, got %d", count)
	}
}

func TestBuildCommitMessageWithCoAuthors_NoModels(t *testing.T) {
	result := buildCommitMessageWithCoAuthors("Fix bug", nil)
	if result != "Fix bug" {
		t.Errorf("expected just the message, got %q", result)
	}
}

func TestBuildCommitMessageWithCoAuthors_EmptyModelList(t *testing.T) {
	result := buildCommitMessageWithCoAuthors("Fix bug", []string{})
	if result != "Fix bug" {
		t.Errorf("expected just the message, got %q", result)
	}
}

func TestBuildCommitMessageWithCoAuthors_PreservesMessageBody(t *testing.T) {
	// Message body with newlines should be preserved
	body := "Fix bug\n\nDetailed explanation of the fix"
	result := buildCommitMessageWithCoAuthors(body, []string{"gpt-4o"})
	if !strings.HasPrefix(result, body) {
		t.Errorf("message body should be preserved, got %q", result)
	}
	if !strings.Contains(result, "Co-authored-by: gpt-4o") {
		t.Error("should contain co-author")
	}
}
