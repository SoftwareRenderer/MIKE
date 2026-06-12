package store

import (
	"kanban/backend/types"
	"math"
	"testing"
)

func TestGetProjectStatsEmpty(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	stats, err := s.GetProjectStats("proj-1")
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}
	if stats.TotalTasks != 0 {
		t.Fatalf("expected 0 tasks, got %d", stats.TotalTasks)
	}
	if len(stats.ByModel) != 0 {
		t.Fatalf("expected 0 models, got %d", len(stats.ByModel))
	}
}

func TestGetProjectStatsBasic(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := newTask("task-1", "proj-1")
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Create assistant messages with token stats
	msg1 := newMessage("m1", "task-1", "assistant", "response 1")
	msg1.ModelName = "model-a"
	msg1.ProviderName = "provider-x"
	msg1.PromptTokens = 100
	msg1.PredictedTokens = 50
	msg1.CacheTokens = 20
	msg1.PromptMs = 500
	msg1.PredictedMs = 2000

	msg2 := newMessage("m2", "task-1", "assistant", "response 2")
	msg2.ModelName = "model-a"
	msg2.ProviderName = "provider-x"
	msg2.PromptTokens = 200
	msg2.PredictedTokens = 100
	msg2.CacheTokens = 50
	msg2.PromptMs = 1000
	msg2.PredictedMs = 4000

	for _, m := range []*types.TaskMessage{msg1, msg2} {
		if err := s.CreateTaskMessage(m); err != nil {
			t.Fatalf("create message: %v", err)
		}
	}

	stats, err := s.GetProjectStats("proj-1")
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}

	if stats.TotalTasks != 1 {
		t.Fatalf("expected 1 task, got %d", stats.TotalTasks)
	}
	if stats.TotalPromptTokens != 300 {
		t.Fatalf("expected 300 prompt tokens, got %d", stats.TotalPromptTokens)
	}
	if stats.TotalGeneratedTokens != 150 {
		t.Fatalf("expected 150 generated tokens, got %d", stats.TotalGeneratedTokens)
	}
	if stats.TotalCacheTokens != 70 {
		t.Fatalf("expected 70 cache tokens, got %d", stats.TotalCacheTokens)
	}
	if stats.TotalProcessedTokens != 230 {
		t.Fatalf("expected 230 processed tokens (300-70), got %d", stats.TotalProcessedTokens)
	}
	if len(stats.ByModel) != 1 {
		t.Fatalf("expected 1 model group, got %d", len(stats.ByModel))
	}
}

func TestGetProjectStatsMultipleModels(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := newTask("task-1", "proj-1")
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Messages from different models
	msg1 := newMessage("m1", "task-1", "assistant", "model-a response")
	msg1.ModelName = "model-a"
	msg1.PromptTokens = 100
	msg1.PredictedTokens = 50

	msg2 := newMessage("m2", "task-1", "assistant", "model-b response")
	msg2.ModelName = "model-b"
	msg2.PromptTokens = 200
	msg2.PredictedTokens = 100

	for _, m := range []*types.TaskMessage{msg1, msg2} {
		if err := s.CreateTaskMessage(m); err != nil {
			t.Fatalf("create message: %v", err)
		}
	}

	stats, err := s.GetProjectStats("proj-1")
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}

	if len(stats.ByModel) != 2 {
		t.Fatalf("expected 2 model groups, got %d", len(stats.ByModel))
	}

	// Find each model
	var modelA, modelB *types.ModelStats
	for i := range stats.ByModel {
		if stats.ByModel[i].ModelName == "model-a" {
			modelA = &stats.ByModel[i]
		} else if stats.ByModel[i].ModelName == "model-b" {
			modelB = &stats.ByModel[i]
		}
	}
	if modelA == nil || modelB == nil {
		t.Fatal("missing model group")
	}
	if modelA.PromptTokens != 100 || modelA.GeneratedTokens != 50 {
		t.Fatalf("model-a stats wrong: %+v", modelA)
	}
	if modelB.PromptTokens != 200 || modelB.GeneratedTokens != 100 {
		t.Fatalf("model-b stats wrong: %+v", modelB)
	}
}

func TestGetProjectStatsExcludesUserMessages(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := newTask("task-1", "proj-1")
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// User messages should not count toward stats
	userMsg := newMessage("m1", "task-1", "user", "hello")
	userMsg.PromptTokens = 999
	userMsg.PredictedTokens = 999

	assistantMsg := newMessage("m2", "task-1", "assistant", "hi")
	assistantMsg.PromptTokens = 100
	assistantMsg.PredictedTokens = 50

	for _, m := range []*types.TaskMessage{userMsg, assistantMsg} {
		if err := s.CreateTaskMessage(m); err != nil {
			t.Fatalf("create message: %v", err)
		}
	}

	stats, err := s.GetProjectStats("proj-1")
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}

	// Should only count assistant message
	if stats.TotalPromptTokens != 100 {
		t.Fatalf("expected 100 prompt tokens (assistant only), got %d", stats.TotalPromptTokens)
	}
	if stats.TotalGeneratedTokens != 50 {
		t.Fatalf("expected 50 generated tokens (assistant only), got %d", stats.TotalGeneratedTokens)
	}
}

func TestGetProjectStatsThroughput(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := newTask("task-1", "proj-1")
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	msg := newMessage("m1", "task-1", "assistant", "response")
	msg.PromptTokens = 1000
	msg.PredictedTokens = 500
	msg.CacheTokens = 0
	msg.PromptMs = 1000 // 1s
	msg.PredictedMs = 2000 // 2s

	if err := s.CreateTaskMessage(msg); err != nil {
		t.Fatalf("create message: %v", err)
	}

	stats, err := s.GetProjectStats("proj-1")
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}

	// Prompt throughput: 1000 tokens / 1s = 1000 tok/s
	if !floatEquals(stats.AvgPromptTps, 1000.0, 1.0) {
		t.Fatalf("expected ~1000 prompt tok/s, got %.2f", stats.AvgPromptTps)
	}
	// Generation throughput: 500 tokens / 2s = 250 tok/s
	if !floatEquals(stats.AvgGenerationTps, 250.0, 1.0) {
		t.Fatalf("expected ~250 generation tok/s, got %.2f", stats.AvgGenerationTps)
	}
}

func TestGetProjectStatsMultipleTasks(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create 2 tasks with messages
	for _, taskID := range []string{"task-1", "task-2"} {
		task := newTask(taskID, "proj-1")
		if err := s.CreateTask(task); err != nil {
			t.Fatalf("create task: %v", err)
		}

		msg := newMessage("m-"+taskID, taskID, "assistant", "response")
		msg.ModelName = "model-a"
		msg.PromptTokens = 100
		msg.PredictedTokens = 50
		if err := s.CreateTaskMessage(msg); err != nil {
			t.Fatalf("create message: %v", err)
		}
	}

	stats, err := s.GetProjectStats("proj-1")
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}

	if stats.TotalTasks != 2 {
		t.Fatalf("expected 2 tasks, got %d", stats.TotalTasks)
	}
	if stats.TotalPromptTokens != 200 {
		t.Fatalf("expected 200 prompt tokens, got %d", stats.TotalPromptTokens)
	}
	if len(stats.ByModel) != 1 {
		t.Fatalf("expected 1 model group, got %d", len(stats.ByModel))
	}
	if stats.ByModel[0].TaskCount != 2 {
		t.Fatalf("expected task_count=2, got %d", stats.ByModel[0].TaskCount)
	}
}

func TestDeleteProjectStats(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := newTask("task-1", "proj-1")
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	msg1 := newMessage("m1", "task-1", "assistant", "model-a")
	msg1.ModelName = "model-a"
	msg1.PromptTokens = 100
	msg1.PredictedTokens = 50

	msg2 := newMessage("m2", "task-1", "assistant", "model-b")
	msg2.ModelName = "model-b"
	msg2.PromptTokens = 200
	msg2.PredictedTokens = 100

	msg3 := newMessage("m3", "task-1", "user", "user message")
	msg3.PromptTokens = 50

	for _, m := range []*types.TaskMessage{msg1, msg2, msg3} {
		if err := s.CreateTaskMessage(m); err != nil {
			t.Fatalf("create message: %v", err)
		}
	}

	// Delete stats for model-a
	if err := s.DeleteProjectStats("proj-1", "model-a", ""); err != nil {
		t.Fatalf("delete stats: %v", err)
	}

	stats, err := s.GetProjectStats("proj-1")
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}

	// model-a should be gone, model-b should remain
	if len(stats.ByModel) != 1 || stats.ByModel[0].ModelName != "model-b" {
		t.Fatalf("expected only model-b, got %+v", stats.ByModel)
	}
	// User messages should not have been deleted
	msgs, _ := s.GetTaskMessages("task-1", "")
	if len(msgs) < 1 {
		t.Fatalf("user messages should not be deleted")
	}
}

func TestDeleteProjectStatsWithProvider(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := newTask("task-1", "proj-1")
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Same model, different providers
	msg1 := newMessage("m1", "task-1", "assistant", "provider-a")
	msg1.ModelName = "model-a"
	msg1.ProviderName = "provider-a"
	msg1.PromptTokens = 100

	msg2 := newMessage("m2", "task-1", "assistant", "provider-b")
	msg2.ModelName = "model-a"
	msg2.ProviderName = "provider-b"
	msg2.PromptTokens = 200

	for _, m := range []*types.TaskMessage{msg1, msg2} {
		if err := s.CreateTaskMessage(m); err != nil {
			t.Fatalf("create message: %v", err)
		}
	}

	// Delete only provider-a's stats for model-a
	if err := s.DeleteProjectStats("proj-1", "model-a", "provider-a"); err != nil {
		t.Fatalf("delete stats: %v", err)
	}

	stats, err := s.GetProjectStats("proj-1")
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}

	if len(stats.ByModel) != 1 {
		t.Fatalf("expected 1 model group, got %d", len(stats.ByModel))
	}
	if stats.ByModel[0].ProviderName != "provider-b" {
		t.Fatalf("expected provider-b, got %s", stats.ByModel[0].ProviderName)
	}
}

func TestGetProjectStatsTitleMessages(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := newTask("task-1", "proj-1")
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Title messages should be counted (same as assistant)
	titleMsg := newMessage("m1", "task-1", "title", "generated title")
	titleMsg.PromptTokens = 50
	titleMsg.PredictedTokens = 10

	if err := s.CreateTaskMessage(titleMsg); err != nil {
		t.Fatalf("create message: %v", err)
	}

	stats, err := s.GetProjectStats("proj-1")
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}

	if stats.TotalPromptTokens != 50 {
		t.Fatalf("expected 50 prompt tokens from title message, got %d", stats.TotalPromptTokens)
	}
}

func TestGetProjectStatsIsolatedByProject(t *testing.T) {
	s := testStore(t)
	// Create two projects
	for _, id := range []string{"proj-1", "proj-2"} {
		proj := newProject(id, id, "/tmp/"+id)
		if err := s.CreateProject(proj); err != nil {
			t.Fatalf("create project: %v", err)
		}
		task := newTask("task-"+id, id)
		if err := s.CreateTask(task); err != nil {
			t.Fatalf("create task: %v", err)
		}
		msg := newMessage("m-"+id, "task-"+id, "assistant", "response")
		msg.PromptTokens = 100
		msg.PredictedTokens = 50
		if err := s.CreateTaskMessage(msg); err != nil {
			t.Fatalf("create message: %v", err)
		}
	}

	stats1, err := s.GetProjectStats("proj-1")
	if err != nil {
		t.Fatalf("get stats proj-1: %v", err)
	}
	stats2, err := s.GetProjectStats("proj-2")
	if err != nil {
		t.Fatalf("get stats proj-2: %v", err)
	}

	// Each project should see only its own stats
	if stats1.TotalTasks != 1 || stats2.TotalTasks != 1 {
		t.Fatalf("each project should have 1 task: proj-1=%d, proj-2=%d", stats1.TotalTasks, stats2.TotalTasks)
	}
	if stats1.TotalPromptTokens != 100 || stats2.TotalPromptTokens != 100 {
		t.Fatalf("each project should have 100 prompt tokens: proj-1=%d, proj-2=%d", stats1.TotalPromptTokens, stats2.TotalPromptTokens)
	}
}

// floatEquals checks if two floats are approximately equal within a tolerance.
func floatEquals(a, b, tol float64) bool {
	return math.Abs(a-b) < tol
}
