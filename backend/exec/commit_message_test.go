package exec

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"mike/backend/types"
)

// stubAgentLister implements config.AgentProviderLister with a fixed set of
// registry-discovered agent providers.
type stubAgentLister struct{ providers []types.Provider }

func (s stubAgentLister) ToProviders() []types.Provider { return s.providers }

// --- utilityTarget Tests ---

func TestUtilityTarget_UsesConfiguredUtilityProvider(t *testing.T) {
	t.Parallel()
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
		ID:             "task-1",
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
	t.Parallel()
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
	t.Parallel()
	e := newTestExecutor(t)

	// Agent providers are registry-derived (enabled agent extensions), not
	// persisted config rows — wire the registry, not the provider list.
	e.configStore.SetAgentRegistry(stubAgentLister{providers: []types.Provider{
		{Name: "pi", Kind: "agent", URL: "agent://pi"},
	}})
	if err := e.configStore.UpdateConfig(&types.Config{
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

// --- ResolveUtilityTarget Tests ---

func TestResolveUtilityTarget_UsesConfiguredUtilityProvider(t *testing.T) {
	t.Parallel()
	e := newTestExecutor(t)

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
		ID:             "task-1",
		LlamaServerURL: "http://worker:8010/v1",
		ModelName:      "chat-model",
	}

	url, model := e.ResolveUtilityTarget(task)
	if url != "http://utility:8010/v1" {
		t.Errorf("url = %q, want utility provider URL", url)
	}
	if model != "cheap-model" {
		t.Errorf("model = %q, want utility model", model)
	}
}

func TestResolveUtilityTarget_FallsBackToTaskWhenUnset(t *testing.T) {
	t.Parallel()
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

	url, model := e.ResolveUtilityTarget(task)
	if url != "http://task-url:8010/v1" {
		t.Errorf("url = %q, want task URL fallback", url)
	}
	if model != "task-model" {
		t.Errorf("model = %q, want task model fallback", model)
	}
}

// --- collectUniqueModels Tests ---

func TestCollectUniqueModels_Empty(t *testing.T) {
	t.Parallel()
	models := collectUniqueModels(nil)
	if len(models) != 0 {
		t.Errorf("expected 0 models, got %d: %v", len(models), models)
	}
}

func TestCollectUniqueModels_SingleModel(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

// --- buildAssistedBy Tests ---

func TestBuildAssistedBy_Empty(t *testing.T) {
	t.Parallel()
	result := buildAssistedBy(nil)
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}

func TestBuildAssistedBy_SingleModel(t *testing.T) {
	t.Parallel()
	result := buildAssistedBy([]string{"gpt-4o"})
	expected := "\n\nAssisted-by: MIKE:gpt-4o"
	if result != expected {
		t.Errorf("got %q, want %q", result, expected)
	}
}

func TestBuildAssistedBy_StripsOrgPrefix(t *testing.T) {
	t.Parallel()
	result := buildAssistedBy([]string{"org/model"})
	expected := "\n\nAssisted-by: MIKE:model"
	if result != expected {
		t.Errorf("buildAssistedBy(%q) = %q, want %q", "org/model", result, expected)
	}

	mixed := buildAssistedBy([]string{"org/model", "model"})
	if !strings.Contains(mixed, "Assisted-by: MIKE:model") {
		t.Errorf("expected org prefix stripped + MIKE prefix, got %q", mixed)
	}
	if !strings.Contains(mixed, "Assisted-by: MIKE:model") {
		t.Errorf("expected non-namespaced model with MIKE prefix, got %q", mixed)
	}
	if strings.Contains(mixed, "org/") {
		t.Errorf("expected no 'org/' in trailer, got %q", mixed)
	}
}

func TestBuildAssistedBy_MultipleModels(t *testing.T) {
	t.Parallel()
	result := buildAssistedBy([]string{"gpt-4o", "claude-3.5", "llama-3"})
	lines := strings.Split(result, "\n")
	// "\n\n" splits into ["", "", "Assisted-by: gpt-4o", "Assisted-by: claude-3.5", "Assisted-by: llama-3"]
	if len(lines) != 5 {
		t.Fatalf("expected 5 lines (2 empty prefix + 3 co-authored), got %d: %q", len(lines), result)
	}
	if lines[0] != "" || lines[1] != "" {
		t.Errorf("first two lines should be empty (from \n\n prefix)")
	}
	if lines[2] != "Assisted-by: MIKE:gpt-4o" {
		t.Errorf("line 2 = %q", lines[2])
	}
	if lines[3] != "Assisted-by: MIKE:claude-3.5" {
		t.Errorf("line 3 = %q", lines[3])
	}
	if lines[4] != "Assisted-by: MIKE:llama-3" {
		t.Errorf("line 4 = %q", lines[4])
	}
}

// --- buildCommitMessageWithAssistedBy Tests ---

func TestBuildCommitMessageWithAssistedBy_SingleModel(t *testing.T) {
	t.Parallel()
	result := buildCommitMessageWithAssistedBy("Fix bug", []string{"gpt-4o"})
	expected := "Fix bug\n\nAssisted-by: MIKE:gpt-4o"
	if result != expected {
		t.Errorf("got %q, want %q", result, expected)
	}
}

func TestBuildCommitMessageWithAssistedBy_MultipleModels(t *testing.T) {
	t.Parallel()
	result := buildCommitMessageWithAssistedBy("Add feature", []string{"gpt-4o", "claude-3.5"})
	if !strings.HasPrefix(result, "Add feature\n\n") {
		t.Errorf("should start with message body, got %q", result)
	}
	if !strings.Contains(result, "Assisted-by: MIKE:gpt-4o") {
		t.Error("should contain MIKE:gpt-4o co-author")
	}
	if !strings.Contains(result, "Assisted-by: MIKE:claude-3.5") {
		t.Error("should contain MIKE:claude-3.5 co-author")
	}
	// Count Assisted-by lines
	count := strings.Count(result, "Assisted-by:")
	if count != 2 {
		t.Errorf("expected 2 Assisted-by lines, got %d", count)
	}
}

func TestBuildCommitMessageWithAssistedBy_NoModels(t *testing.T) {
	t.Parallel()
	result := buildCommitMessageWithAssistedBy("Fix bug", nil)
	if result != "Fix bug" {
		t.Errorf("expected just the message, got %q", result)
	}
}

func TestBuildCommitMessageWithAssistedBy_EmptyModelList(t *testing.T) {
	t.Parallel()
	result := buildCommitMessageWithAssistedBy("Fix bug", []string{})
	if result != "Fix bug" {
		t.Errorf("expected just the message, got %q", result)
	}
}

func TestBuildCommitMessageWithAssistedBy_PreservesMessageBody(t *testing.T) {
	t.Parallel()
	// Message body with newlines should be preserved
	body := "Fix bug\n\nDetailed explanation of the fix"
	result := buildCommitMessageWithAssistedBy(body, []string{"gpt-4o"})
	if !strings.HasPrefix(result, body) {
		t.Errorf("message body should be preserved, got %q", result)
	}
	if !strings.Contains(result, "Assisted-by: MIKE:gpt-4o") {
		t.Error("should contain co-author")
	}
}

// --- The project's answer to the Assisted-by line ---

// commitTrailerExecutor builds an executor whose utility model answers with the given label (or fails),
// over a project with the given answer to the Assisted-by line.
func commitTrailerExecutor(t *testing.T, commitAssistedBy *bool, label string, callErr error) (*TaskExecutor, *types.Task) {
	t.Helper()
	te, s := newTestExecutorWithStore(t)
	now := time.Now().UTC()
	project := &types.Project{ID: "proj-trailer", Name: "trailer", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now, CommitAssistedBy: commitAssistedBy}
	if err := s.CreateProject(project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &types.Task{ID: "task-trailer", ProjectID: project.ID, Title: "Fix the parser", ModelName: "worker-model", Status: types.StatusInProgress}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	te.merge.utilityTarget = func(*types.Task) (string, string, bool) { return "http://utility.test/v1", "utility-model", true }
	te.merge.chatCompletion = func(context.Context, string, string, []types.ChatMessage, *ChatCompletionOpts) (*types.ChatUsage, string, error) {
		return nil, label, callErr
	}
	return te, task
}

// TestGenerateCommitMessage_AssistedByFollowsTheProject: a row that never answered keeps the line, an explicit off drops it.
func TestGenerateCommitMessage_AssistedByFollowsTheProject(t *testing.T) {
	on, off := true, false
	for _, tc := range []struct {
		name        string
		assistedBy  *bool
		wantTrailer bool
	}{
		{"unset keeps the line", nil, true},
		{"on keeps the line", &on, true},
		{"off drops the line", &off, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			te, task := commitTrailerExecutor(t, tc.assistedBy, "Fix the parser flush", nil)
			got := te.merge.GenerateCommitMessage(context.Background(), task)
			if strings.Contains(got, "Assisted-by") != tc.wantTrailer {
				t.Fatalf("commit message %q, want trailer: %t", got, tc.wantTrailer)
			}
			if !strings.HasPrefix(got, "Fix the parser flush") {
				t.Fatalf("the generated label was replaced: %q", got)
			}
		})
	}
}

// TestGenerateCommitMessage_FallbackFollowsTheProject: the title-only fallback carries the trailer only
// where the project wants it.
func TestGenerateCommitMessage_FallbackFollowsTheProject(t *testing.T) {
	off := false
	te, task := commitTrailerExecutor(t, &off, "", errors.New("provider down"))
	if got := te.merge.GenerateCommitMessage(context.Background(), task); got != "Fix the parser" {
		t.Fatalf("a project that declined the trailer got %q", got)
	}

	te, task = commitTrailerExecutor(t, nil, "", errors.New("provider down"))
	got := te.merge.GenerateCommitMessage(context.Background(), task)
	if !strings.Contains(got, "Assisted-by: MIKE:utility-model") {
		t.Fatalf("unset project fallback = %q, want the utility model named", got)
	}
}
