package api

// A card nobody chose a model for has to arrive with the pair its start can run. A card that named one
// half is left as sent, because the worker's model may belong to another provider.

import (
	"encoding/json/v2"
	"net/http"
	"testing"

	"mike/backend/types"
)

const (
	workerURL   = "https://api.example.invalid/v1"
	workerModel = "worker-model"
)

// workerConfig is one LLM provider as the worker, plus a stated answer to every default the ladder reads.
// The worker model is listed second, so the pair is read from the config row and not from the first model.
func workerConfig(t *testing.T, h *TaskHandler) {
	t.Helper()
	mode := types.ModePlan
	turns := 5
	yes := true
	if err := h.configStore.UpdateConfig(&types.Config{
		Providers: []types.Provider{{ID: 7, Name: "worker", Kind: types.ProviderKindLLM,
			URL: workerURL, Models: []types.ModelConfig{{Name: "sibling-model"}, {Name: workerModel}}}},
		WorkerProvider:           "7",
		WorkerModel:              workerModel,
		DefaultOperatingMode:     &mode,
		DefaultMaxFailedTurns:    &turns,
		DefaultAutoCommit:        &yes,
		DefaultSubtasksAutoMerge: &yes,
	}); err != nil {
		t.Fatalf("configure the worker pair: %v", err)
	}
}

func TestNewTaskTakesTheConfiguredWorkerPair(t *testing.T) {
	h, s, _ := newTestTaskHandler(t)
	if err := s.CreateProject(&types.Project{ID: "pair-proj", Name: "pair-proj", Path: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	workerConfig(t, h)
	mux := newTaskMux(h)

	cases := []struct {
		name                     string
		body                     map[string]any
		wantURL, wantModel       string
		wantMode                 types.OperatingMode
		wantTurns                int
		wantCommit, wantSubMerge bool
	}{
		{
			// Every config default lands: this is the row the extension create writes.
			name:    "neither half named",
			body:    map[string]any{"prompt": "Do the thing"},
			wantURL: workerURL, wantModel: workerModel,
			wantMode: types.ModePlan, wantTurns: 5, wantCommit: true, wantSubMerge: true,
		},
		{
			// An empty string names nothing, as the offline queue sends it.
			name:    "both halves sent empty",
			body:    map[string]any{"prompt": "Do the thing", "llama_server_url": "", "model_name": ""},
			wantURL: workerURL, wantModel: workerModel,
			wantMode: types.ModePlan, wantTurns: 5, wantCommit: true, wantSubMerge: true,
		},
		{
			// The worker's model is never paired with another provider.
			name:    "only the provider named",
			body:    map[string]any{"prompt": "Do the thing", "llama_server_url": "https://theirs.invalid/v1"},
			wantURL: "https://theirs.invalid/v1", wantModel: "",
			wantMode: types.ModePlan, wantTurns: 5, wantCommit: true, wantSubMerge: true,
		},
		{
			name:    "only the model named",
			body:    map[string]any{"prompt": "Do the thing", "model_name": "theirs"},
			wantURL: "", wantModel: "theirs",
			wantMode: types.ModePlan, wantTurns: 5, wantCommit: true, wantSubMerge: true,
		},
		{
			// A key the body carried is left alone.
			name: "every field named",
			body: map[string]any{"prompt": "Do the thing", "llama_server_url": "https://theirs.invalid/v1",
				"model_name": "theirs", "operating_mode": "explore", "max_consecutive_failed_turns": 1,
				"auto_commit": false, "subtasks_auto_merge": false},
			wantURL: "https://theirs.invalid/v1", wantModel: "theirs",
			wantMode: types.ModeExplore, wantTurns: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := doTaskRequest(t, mux, http.MethodPost, "/api/projects/pair-proj/tasks", tc.body)
			if rr.Code != http.StatusCreated {
				t.Fatalf("create = %d: %s", rr.Code, rr.Body.String())
			}
			var created types.Task
			if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
				t.Fatal(err)
			}
			// The row, not the answer: the start path reads the stored task.
			stored, err := s.GetTask("pair-proj", created.ID)
			if err != nil {
				t.Fatalf("read back the created task: %v", err)
			}
			if stored.LlamaServerURL != tc.wantURL || stored.ModelName != tc.wantModel {
				t.Errorf("pair = (%q, %q), want (%q, %q)", stored.LlamaServerURL, stored.ModelName, tc.wantURL, tc.wantModel)
			}
			if stored.OperatingMode != tc.wantMode {
				t.Errorf("operating_mode = %q, want %q", stored.OperatingMode, tc.wantMode)
			}
			if stored.MaxConsecutiveFailedTurns != tc.wantTurns {
				t.Errorf("max_consecutive_failed_turns = %d, want %d", stored.MaxConsecutiveFailedTurns, tc.wantTurns)
			}
			if stored.AutoCommit != tc.wantCommit || stored.SubtasksAutoMerge != tc.wantSubMerge {
				t.Errorf("auto_commit=%v subtasks_auto_merge=%v, want %v/%v",
					stored.AutoCommit, stored.SubtasksAutoMerge, tc.wantCommit, tc.wantSubMerge)
			}
		})
	}
}

// A child takes its auto-commit and its auto-merge from the parent rather than the config, and its pair
// from the worker rather than from the row above it.
func TestNewSubtaskTakesItsAutoMergeFromItsParent(t *testing.T) {
	h, s, _ := newTestTaskHandler(t)
	if err := s.CreateProject(&types.Project{ID: "child-proj", Name: "child-proj", Path: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	yes := true
	if err := h.configStore.UpdateConfig(&types.Config{
		Providers: []types.Provider{{ID: 7, Name: "worker", Kind: types.ProviderKindLLM,
			URL: workerURL, Models: []types.ModelConfig{{Name: workerModel}}}},
		WorkerProvider:           "7",
		WorkerModel:              workerModel,
		DefaultAutoCommit:        &yes,
		DefaultAutoMerge:         &yes,
		DefaultSubtasksAutoMerge: &yes,
	}); err != nil {
		t.Fatalf("configure the worker pair: %v", err)
	}
	parent := &types.Task{
		ID: "parent-row", ProjectID: "child-proj", Title: "the parent", Status: types.StatusBacklog,
		Branch: "main", AutoCommit: false, AutoMerge: true, SubtasksAutoMerge: false,
	}
	if err := s.CreateTask(parent); err != nil {
		t.Fatal(err)
	}
	mux := newTaskMux(h)

	rr := doTaskRequest(t, mux, http.MethodPost, "/api/projects/child-proj/tasks",
		map[string]any{"prompt": "Do the child thing", "parent_task_id": parent.ID})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rr.Code, rr.Body.String())
	}
	var created types.Task
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	child, err := s.GetTask("child-proj", created.ID)
	if err != nil {
		t.Fatalf("read back the created task: %v", err)
	}
	if child.AutoCommit {
		t.Error("the child took the config's auto-commit; the parent's own answer decides")
	}
	if child.AutoMerge {
		t.Error("the child took the config's auto-merge; the parent's subtasks_auto_merge decides")
	}
	if !child.SubtasksAutoMerge {
		t.Error("the child's own answer for its children came from the parent, not the config")
	}
	if child.LlamaServerURL != workerURL || child.ModelName != workerModel {
		t.Errorf("pair = (%q, %q), want the worker pair (%q, %q) — a child never inherits the row above it",
			child.LlamaServerURL, child.ModelName, workerURL, workerModel)
	}
	if child.Status != types.StatusBacklog {
		t.Errorf("status = %q, want the column SubtaskStatusFor gives a backlog parent", child.Status)
	}

	// A parent nobody can find is refused, not quietly created as a root task.
	missing := doTaskRequest(t, mux, http.MethodPost, "/api/projects/child-proj/tasks",
		map[string]any{"prompt": "Orphan", "parent_task_id": "no-such-parent"})
	if missing.Code != http.StatusBadRequest {
		t.Errorf("create with a missing parent = %d, want 400: %s", missing.Code, missing.Body.String())
	}
}

// With no provider configured there is no pair to take, and the card is still created for the person to
// start once one exists.
func TestNewTaskWithoutAnyProviderIsStillCreated(t *testing.T) {
	h, s, _ := newTestTaskHandler(t)
	if err := s.CreateProject(&types.Project{ID: "noprov-proj", Name: "noprov-proj", Path: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	mux := newTaskMux(h)

	rr := doTaskRequest(t, mux, http.MethodPost, "/api/projects/noprov-proj/tasks", map[string]any{"prompt": "Do the thing"})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rr.Code, rr.Body.String())
	}
	var created types.Task
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	stored, err := s.GetTask("noprov-proj", created.ID)
	if err != nil {
		t.Fatalf("read back the created task: %v", err)
	}
	if stored.LlamaServerURL != "" || stored.ModelName != "" {
		t.Errorf("pair = (%q, %q), want both empty — nothing is configured to answer for a model", stored.LlamaServerURL, stored.ModelName)
	}
	if stored.OperatingMode != types.ModeAct {
		t.Errorf("operating_mode = %q, want act", stored.OperatingMode)
	}
}
