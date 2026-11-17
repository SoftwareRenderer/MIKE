package exec

import (
	"context"
	"errors"
	"slices"
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

// TestUtilityTarget is where a utility call (the commit-message route in backend/api, an
// automated answer here) gets its provider: the configured utility pair when one is named, the
// task's own endpoint when nobody is, and the agent registry when the name belongs to an extension.
func TestUtilityTarget(t *testing.T) {
	t.Parallel()
	const taskURL, taskModel = "http://task-url:8010/v1", "task-model"
	tests := []struct {
		name   string
		config *types.Config
		agents []types.Provider
		url    string
		model  string
		wantOK bool
	}{
		{
			name: "the configured utility pair answers",
			config: &types.Config{
				Providers: []types.Provider{
					{Name: "worker", URL: "http://worker:8010/v1", Models: []types.ModelConfig{{Name: "chat-model"}}},
					{Name: "utility", URL: "http://utility:8010/v1", Models: []types.ModelConfig{{Name: "cheap-model"}}},
				},
				WorkerProvider:  "worker",
				WorkerModel:     "chat-model",
				UtilityProvider: "utility",
				UtilityModel:    "cheap-model",
			},
			url:    "http://utility:8010/v1",
			model:  "cheap-model",
			wantOK: true,
		},
		{
			name: "with no utility named the task's own endpoint answers",
			config: &types.Config{
				Providers: []types.Provider{{Name: "worker", URL: "http://worker:8010/v1"}},
			},
			url:   taskURL,
			model: taskModel,
		},
		{
			name: "an agent provider comes from the registry, not the provider list",
			config: &types.Config{
				UtilityProvider: "pi",
				UtilityModel:    "pi",
			},
			agents: []types.Provider{{Name: "pi", Kind: "agent", URL: "agent://pi"}},
			url:    "agent://pi",
			model:  "pi",
			wantOK: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestExecutor(t)
			if tc.agents != nil {
				e.configStore.SetAgentRegistry(stubAgentLister{providers: tc.agents})
			}
			if err := e.configStore.UpdateConfig(tc.config); err != nil {
				t.Fatalf("update config: %v", err)
			}

			url, model, ok := e.utilityTarget(&types.Task{ID: "task-1", LlamaServerURL: taskURL, ModelName: taskModel})
			if ok != tc.wantOK {
				t.Errorf("ok = %v, want %v", ok, tc.wantOK)
			}
			if url != tc.url {
				t.Errorf("url = %q, want %q", url, tc.url)
			}
			if model != tc.model {
				t.Errorf("model = %q, want %q", model, tc.model)
			}
		})
	}
}

// ResolveUtilityTarget is utilityTarget with the `ok` dropped, so the cases above are its behaviour.
// Its callers are the commit-message route in backend/api and automatedAnswers here.

// --- collectUniqueModels Tests ---

// TestCollectUniqueModels names the models a thread ran with: one line each, in first-seen order,
// and a row that reported no model contributes nothing.
func TestCollectUniqueModels(t *testing.T) {
	t.Parallel()
	assistant := func(models ...string) []types.TaskMessage {
		var msgs []types.TaskMessage
		for _, m := range models {
			msgs = append(msgs, types.TaskMessage{Role: "assistant", ModelName: m})
		}
		return msgs
	}
	tests := []struct {
		name string
		msgs []types.TaskMessage
		want []string
	}{
		{"no rows", nil, nil},
		{"duplicates collapse to their first appearance", assistant("gpt-4o", "gpt-4o", "claude-3.5", "gpt-4o", "llama-3"), []string{"gpt-4o", "claude-3.5", "llama-3"}},
		{"a row that reported no model is skipped", assistant("", "gpt-4o", "", "claude-3.5"), []string{"gpt-4o", "claude-3.5"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := collectUniqueModels(tc.msgs)
			if !slices.Equal(got, tc.want) {
				t.Errorf("models = %v, want %v", got, tc.want)
			}
		})
	}
}

// --- buildAssistedBy Tests ---

// TestBuildAssistedBy writes one trailer per contributing model. A namespaced model id is one
// name: the prefix tells apart two models the provider would otherwise report identically.
func TestBuildAssistedBy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		models []string
		want   string
	}{
		{
			name:   "a namespaced model id keeps its prefix",
			models: []string{"org/model"},
			want:   "\n\nAssisted-by: MIKE:org/model",
		},
		{
			name:   "a prefixed and a bare name are two models",
			models: []string{"org/model", "model"},
			want:   "\n\nAssisted-by: MIKE:org/model\nAssisted-by: MIKE:model",
		},
		{
			name:   "every contributing model gets its own line",
			models: []string{"gpt-4o", "claude-3.5", "llama-3"},
			want:   "\n\nAssisted-by: MIKE:gpt-4o\nAssisted-by: MIKE:claude-3.5\nAssisted-by: MIKE:llama-3",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildAssistedBy(tc.models); got != tc.want {
				t.Errorf("buildAssistedBy(%q) = %q, want %q", tc.models, got, tc.want)
			}
		})
	}
}

// --- buildCommitMessageWithAssistedBy Tests ---

// TestBuildCommitMessageWithAssistedBy keeps the generated body byte-for-byte and appends one
// trailer per model; with no model to name the message is returned untouched.
func TestBuildCommitMessageWithAssistedBy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		body   string
		models []string
		want   string
	}{
		{"one model", "Fix bug", []string{"gpt-4o"}, "Fix bug\n\nAssisted-by: MIKE:gpt-4o"},
		{"two models", "Add feature", []string{"gpt-4o", "claude-3.5"},
			"Add feature\n\nAssisted-by: MIKE:gpt-4o\nAssisted-by: MIKE:claude-3.5"},
		{"no model leaves the message alone", "Fix bug", nil, "Fix bug"},
		{"a multi-paragraph body is preserved", "Fix bug\n\nDetailed explanation of the fix",
			[]string{"gpt-4o"}, "Fix bug\n\nDetailed explanation of the fix\n\nAssisted-by: MIKE:gpt-4o"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildCommitMessageWithAssistedBy(tc.body, tc.models); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
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

// TestGenerateCommitMessage_AssistedByFollowsTheProject: the line appears only where the project said
// yes — an unset row and an explicit off both drop it — and it rides the title-only fallback the same
// way the generated label does.
func TestGenerateCommitMessage_AssistedByFollowsTheProject(t *testing.T) {
	on, off := true, false
	for _, tc := range []struct {
		name        string
		assistedBy  *bool
		label       string
		callErr     error
		wantTrailer bool
		wantPrefix  string
	}{
		{"generated, unset drops the line", nil, "Fix the parser flush", nil, false, "Fix the parser flush"},
		{"generated, on keeps the line", &on, "Fix the parser flush", nil, true, "Fix the parser flush"},
		{"generated, off drops the line", &off, "Fix the parser flush", nil, false, "Fix the parser flush"},
		{"fallback, unset is the bare title", nil, "", errors.New("provider down"), false, "Fix the parser"},
		{"fallback, off is the bare title", &off, "", errors.New("provider down"), false, "Fix the parser"},
		{"fallback, on names the utility model", &on, "", errors.New("provider down"), true, "Fix the parser"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			te, task := commitTrailerExecutor(t, tc.assistedBy, tc.label, tc.callErr)
			got := te.merge.GenerateCommitMessage(context.Background(), task)
			if !strings.HasPrefix(got, tc.wantPrefix) {
				t.Fatalf("commit message %q, want it to start with %q", got, tc.wantPrefix)
			}
			if strings.Contains(got, "Assisted-by") != tc.wantTrailer {
				t.Fatalf("commit message %q, want trailer: %t", got, tc.wantTrailer)
			}
			if tc.wantTrailer && tc.callErr != nil && !strings.Contains(got, "Assisted-by: MIKE:utility-model") {
				t.Errorf("commit message %q, want the utility model named", got)
			}
		})
	}
}
