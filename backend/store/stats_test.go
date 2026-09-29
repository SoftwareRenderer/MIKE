package store

import (
	"math"
	"mike/backend/types"
	"testing"
)

func TestGetProjectStatsEmpty(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	msg.PromptMs = 1000    // 1s
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

func TestGetProjectStatsThroughputExcludesNoTimingData(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := newTask("task-1", "proj-1")
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Provider with timing data: 500 gen tokens in 2000ms → 250 tok/s
	msg1 := newMessage("m1", "task-1", "assistant", "provider response")
	msg1.ModelName = "gpt-4"
	msg1.PromptTokens = 1000
	msg1.PredictedTokens = 500
	msg1.PromptMs = 1000
	msg1.PredictedMs = 2000

	// Agent provider with NO timing data: 2000 gen tokens, 0ms
	// This should NOT inflate the overall throughput
	msg2 := newMessage("m2", "task-1", "assistant", "agent response")
	msg2.ModelName = "pi"
	msg2.PromptTokens = 500
	msg2.PredictedTokens = 2000
	msg2.PromptMs = 0
	msg2.PredictedMs = 0

	for _, m := range []*types.TaskMessage{msg1, msg2} {
		if err := s.CreateTaskMessage(m); err != nil {
			t.Fatalf("create message: %v", err)
		}
	}

	stats, err := s.GetProjectStats("proj-1")
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}

	// Total tokens include both providers
	if stats.TotalGeneratedTokens != 2500 {
		t.Fatalf("expected 2500 total generated tokens, got %d", stats.TotalGeneratedTokens)
	}

	// Overall gen TPS counts only the 500 tokens the provider with timing data reported, over its 2s.
	// Including the timing-less provider would answer (500+2000)/2s = 1250.
	if !floatEquals(stats.AvgGenerationTps, 250.0, 1.0) {
		t.Fatalf("expected ~250 generation tok/s, got %.2f", stats.AvgGenerationTps)
	}

	// Per-model stats should still show correct individual values
	if len(stats.ByModel) != 2 {
		t.Fatalf("expected 2 model groups, got %d", len(stats.ByModel))
	}
	var gpt4, pi *types.ModelStats
	for i := range stats.ByModel {
		if stats.ByModel[i].ModelName == "gpt-4" {
			gpt4 = &stats.ByModel[i]
		} else if stats.ByModel[i].ModelName == "pi" {
			pi = &stats.ByModel[i]
		}
	}
	if gpt4 == nil || pi == nil {
		t.Fatal("missing model group")
	}
	// gpt-4 has timing data, so its TPS should be correct
	if !floatEquals(gpt4.AvgGenerationTps, 250.0, 1.0) {
		t.Fatalf("expected gpt-4 ~250 gen tok/s, got %.2f", gpt4.AvgGenerationTps)
	}
	// pi has no timing data, so its TPS should be 0
	if pi.AvgGenerationTps != 0 {
		t.Fatalf("expected pi gen tok/s = 0, got %.2f", pi.AvgGenerationTps)
	}
}

func TestGetProjectStatsMultipleTasks(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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

	// Delete (reset) stats for model-a
	if err := s.DeleteProjectStats("proj-1", "model-a", ""); err != nil {
		t.Fatalf("delete stats: %v", err)
	}

	stats, err := s.GetProjectStats("proj-1")
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}

	// model-a should have zeroed stats, model-b should remain
	var modelA, modelB *types.ModelStats
	for i := range stats.ByModel {
		if stats.ByModel[i].ModelName == "model-a" {
			modelA = &stats.ByModel[i]
		} else if stats.ByModel[i].ModelName == "model-b" {
			modelB = &stats.ByModel[i]
		}
	}
	if modelA == nil {
		t.Fatal("expected model-a in stats (zeroed, not removed)")
	}
	if modelA.PromptTokens != 0 || modelA.GeneratedTokens != 0 {
		t.Fatalf("expected model-a stats zeroed, got %+v", modelA)
	}
	if modelB == nil || modelB.PromptTokens != 200 {
		t.Fatalf("expected model-b unchanged, got %+v", modelB)
	}
	// All messages should still exist (Reset no longer deletes messages)
	msgs, _ := s.GetTaskMessages("task-1", "")
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages (Reset should not delete messages), got %d", len(msgs))
	}
}

func TestDeleteProjectStatsWithProvider(t *testing.T) {
	t.Parallel()
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

	// Reset only provider-a's stats for model-a
	if err := s.DeleteProjectStats("proj-1", "model-a", "provider-a"); err != nil {
		t.Fatalf("delete stats: %v", err)
	}

	stats, err := s.GetProjectStats("proj-1")
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}

	// Both providers should still appear, but provider-a should be zeroed
	if len(stats.ByModel) != 2 {
		t.Fatalf("expected 2 model groups, got %d", len(stats.ByModel))
	}
	var providerA, providerB *types.ModelStats
	for i := range stats.ByModel {
		if stats.ByModel[i].ProviderName == "provider-a" {
			providerA = &stats.ByModel[i]
		} else if stats.ByModel[i].ProviderName == "provider-b" {
			providerB = &stats.ByModel[i]
		}
	}
	if providerA == nil || providerA.PromptTokens != 0 {
		t.Fatalf("expected provider-a zeroed, got %+v", providerA)
	}
	if providerB == nil || providerB.PromptTokens != 200 {
		t.Fatalf("expected provider-b unchanged, got %+v", providerB)
	}
}

func TestGetProjectStatsTitleMessages(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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

func TestStatsSurviveTaskDeletion(t *testing.T) {
	t.Parallel()
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
	msg.ModelName = "model-a"
	msg.PromptTokens = 100
	msg.PredictedTokens = 50
	msg.CacheTokens = 10
	if err := s.CreateTaskMessage(msg); err != nil {
		t.Fatalf("create message: %v", err)
	}

	// Verify stats before deletion
	stats, err := s.GetProjectStats("proj-1")
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}
	if stats.TotalPromptTokens != 100 {
		t.Fatalf("expected 100 prompt tokens before delete, got %d", stats.TotalPromptTokens)
	}
	if stats.TotalGeneratedTokens != 50 {
		t.Fatalf("expected 50 generated tokens before delete, got %d", stats.TotalGeneratedTokens)
	}
	if len(stats.ByModel) != 1 || stats.ByModel[0].TaskCount != 1 {
		t.Fatalf("expected task_count=1 before delete, got %+v", stats.ByModel)
	}

	// Delete the task (cascade deletes messages from task_messages)
	if err := s.DeleteTask("proj-1", "task-1"); err != nil {
		t.Fatalf("delete task: %v", err)
	}

	// Stats should be unchanged (cumulative, survives task deletion)
	stats, err = s.GetProjectStats("proj-1")
	if err != nil {
		t.Fatalf("get stats after delete: %v", err)
	}
	if stats.TotalPromptTokens != 100 {
		t.Fatalf("expected 100 prompt tokens after delete (cumulative), got %d", stats.TotalPromptTokens)
	}
	if stats.TotalGeneratedTokens != 50 {
		t.Fatalf("expected 50 generated tokens after delete (cumulative), got %d", stats.TotalGeneratedTokens)
	}
	if stats.TotalCacheTokens != 10 {
		t.Fatalf("expected 10 cache tokens after delete (cumulative), got %d", stats.TotalCacheTokens)
	}
	// task_count should also be cumulative (survives task deletion)
	if len(stats.ByModel) != 1 || stats.ByModel[0].TaskCount != 1 {
		t.Fatalf("expected task_count=1 after delete (cumulative), got %+v", stats.ByModel)
	}
	// TotalTasks is cumulative (matches per-model task_count), survives task deletion
	if stats.TotalTasks != 1 {
		t.Fatalf("expected 1 task (cumulative), got %d", stats.TotalTasks)
	}
}

func TestStatsCumulativeAcrossTasks(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create two tasks with messages from the same model
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

	// Stats should sum both tasks
	if stats.TotalPromptTokens != 200 {
		t.Fatalf("expected 200 prompt tokens (100+100), got %d", stats.TotalPromptTokens)
	}
	if stats.TotalGeneratedTokens != 100 {
		t.Fatalf("expected 100 generated tokens (50+50), got %d", stats.TotalGeneratedTokens)
	}
	if len(stats.ByModel) != 1 || stats.ByModel[0].TaskCount != 2 {
		t.Fatalf("expected task_count=2, got %+v", stats.ByModel)
	}

	// Delete one task — stats and task_count should remain cumulative
	if err := s.DeleteTask("proj-1", "task-1"); err != nil {
		t.Fatalf("delete task: %v", err)
	}
	stats, err = s.GetProjectStats("proj-1")
	if err != nil {
		t.Fatalf("get stats after delete: %v", err)
	}
	if stats.TotalPromptTokens != 200 {
		t.Fatalf("expected 200 prompt tokens (cumulative after delete), got %d", stats.TotalPromptTokens)
	}
	if len(stats.ByModel) != 1 || stats.ByModel[0].TaskCount != 2 {
		t.Fatalf("expected task_count=2 (cumulative after delete), got %+v", stats.ByModel)
	}
	if stats.TotalTasks != 2 {
		t.Fatalf("expected 2 tasks (cumulative after delete), got %d", stats.TotalTasks)
	}
}

func TestResetProjectStatsZerosCumulativeTable(t *testing.T) {
	t.Parallel()
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
	msg.ModelName = "model-a"
	msg.PromptTokens = 100
	msg.PredictedTokens = 50
	if err := s.CreateTaskMessage(msg); err != nil {
		t.Fatalf("create message: %v", err)
	}

	// Reset stats for model-a
	if err := s.DeleteProjectStats("proj-1", "model-a", ""); err != nil {
		t.Fatalf("reset stats: %v", err)
	}

	// Cumulative stats should be zeroed
	stats, err := s.GetProjectStats("proj-1")
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}
	if stats.TotalPromptTokens != 0 {
		t.Fatalf("expected 0 prompt tokens after reset, got %d", stats.TotalPromptTokens)
	}
	if stats.TotalGeneratedTokens != 0 {
		t.Fatalf("expected 0 generated tokens after reset, got %d", stats.TotalGeneratedTokens)
	}

	// Original message should still exist in task_messages
	msgs, err := s.GetTaskMessages("task-1", "")
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message (Reset should not delete messages), got %d", len(msgs))
	}
}

func TestStatsBackfill(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := newTask("task-1", "proj-1")
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Insert messages directly into task_messages (simulating pre-migration data)
	_, err := s.db.Exec(
		`INSERT INTO task_messages (id, task_id, thread_id, role, content, model_name, provider_name, prompt_tokens, predicted_tokens, cache_tokens, prompt_ms, predicted_ms, duration, created_at) VALUES (?, ?, NULL, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?)`,
		"m1", "task-1", "assistant", "old response", "model-a", "provider-x", 100, 50, 10, 500, 2000, "2024-01-01T00:00:00Z",
	)
	if err != nil {
		t.Fatalf("insert old message: %v", err)
	}

	// Simulate the backfill that would run on migration
	_, _ = s.db.Exec(`
		INSERT INTO project_token_stats (project_id, model_name, provider_name, prompt_tokens, cache_tokens, predicted_tokens, prompt_ms, predicted_ms)
		SELECT
			t.project_id,
			COALESCE(tm.model_name, 'unknown'),
			COALESCE(tm.provider_name, ''),
			SUM(tm.prompt_tokens),
			SUM(COALESCE(tm.cache_tokens, 0)),
			SUM(tm.predicted_tokens),
			SUM(tm.prompt_ms),
			SUM(tm.predicted_ms)
		FROM task_messages tm
		JOIN tasks t ON t.id = tm.task_id
		WHERE tm.role IN ('assistant', 'title')
		GROUP BY t.project_id, tm.model_name, tm.provider_name
	`)
	_, _ = s.db.Exec(`
		INSERT INTO project_token_tasks (project_id, model_name, provider_name, task_id)
		SELECT DISTINCT
			t.project_id,
			COALESCE(tm.model_name, 'unknown'),
			COALESCE(tm.provider_name, ''),
			tm.task_id
		FROM task_messages tm
		JOIN tasks t ON t.id = tm.task_id
		WHERE tm.role IN ('assistant', 'title')
	`)

	// Stats should reflect the backfilled data
	stats, err := s.GetProjectStats("proj-1")
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}
	if stats.TotalPromptTokens != 100 {
		t.Fatalf("expected 100 prompt tokens from backfill, got %d", stats.TotalPromptTokens)
	}
	if stats.TotalGeneratedTokens != 50 {
		t.Fatalf("expected 50 generated tokens from backfill, got %d", stats.TotalGeneratedTokens)
	}
	if stats.TotalCacheTokens != 10 {
		t.Fatalf("expected 10 cache tokens from backfill, got %d", stats.TotalCacheTokens)
	}
	if len(stats.ByModel) != 1 || stats.ByModel[0].ModelName != "model-a" {
		t.Fatalf("expected model-a in by_model, got %+v", stats.ByModel)
	}
	if stats.ByModel[0].TaskCount != 1 {
		t.Fatalf("expected task_count=1 from backfill, got %d", stats.ByModel[0].TaskCount)
	}
}

func TestGetProjectStatsTotalTasksExcludesLLMFreeTasks(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create 3 tasks: 2 with LLM usage, 1 without
	for _, taskID := range []string{"task-1", "task-2", "task-3"} {
		task := newTask(taskID, "proj-1")
		if err := s.CreateTask(task); err != nil {
			t.Fatalf("create task %s: %v", taskID, err)
		}
	}

	// Only task-1 and task-2 have LLM usage
	for _, taskID := range []string{"task-1", "task-2"} {
		msg := newMessage("m-"+taskID, taskID, "assistant", "response")
		msg.PromptTokens = 100
		msg.PredictedTokens = 50
		if err := s.CreateTaskMessage(msg); err != nil {
			t.Fatalf("create message for %s: %v", taskID, err)
		}
	}

	stats, err := s.GetProjectStats("proj-1")
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}

	// TotalTasks should only count tasks with LLM usage (2), not all tasks (3)
	if stats.TotalTasks != 2 {
		t.Fatalf("expected 2 tasks with LLM usage, got %d", stats.TotalTasks)
	}
	// Sum of per-model task_counts should also be 2
	var sumTaskCount int
	for _, m := range stats.ByModel {
		sumTaskCount += m.TaskCount
	}
	if sumTaskCount != 2 {
		t.Fatalf("expected sum of task_counts to be 2, got %d", sumTaskCount)
	}
}

func TestAccumulateProjectStats(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := newTask("task-1", "proj-1")
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Create an assistant message to seed the project_token_stats row
	msg := newMessage("m1", "task-1", "assistant", "response")
	msg.ModelName = "model-a"
	msg.ProviderName = "provider-x"
	msg.PromptTokens = 100
	msg.PredictedTokens = 50
	if err := s.CreateTaskMessage(msg); err != nil {
		t.Fatalf("create message: %v", err)
	}

	// Accumulate cost via AccumulateProjectStats
	s.AccumulateProjectStats("proj-1", "model-a", "provider-x", 0.45)
	s.AccumulateProjectStats("proj-1", "model-a", "provider-x", 0.55)

	// Verify the accumulated cost is reflected in GetProjectStats
	stats, err := s.GetProjectStats("proj-1")
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}
	if len(stats.ByModel) != 1 {
		t.Fatalf("expected 1 model, got %d", len(stats.ByModel))
	}
	expected := 0.45 + 0.55
	if stats.ByModel[0].Cost != expected {
		t.Errorf("expected cost %.2f, got %f", expected, stats.ByModel[0].Cost)
	}
	if stats.TotalCost != expected {
		t.Errorf("expected total cost %.2f, got %f", expected, stats.TotalCost)
	}
}

func TestDeleteProjectStatsResetsCost(t *testing.T) {
	t.Parallel()
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
	msg.ModelName = "model-a"
	msg.ProviderName = "provider-x"
	if err := s.CreateTaskMessage(msg); err != nil {
		t.Fatalf("create message: %v", err)
	}
	s.AccumulateProjectStats("proj-1", "model-a", "provider-x", 1.50)

	// Verify cost is set
	stats, _ := s.GetProjectStats("proj-1")
	if stats.ByModel[0].Cost != 1.50 {
		t.Fatalf("expected cost 1.50, got %f", stats.ByModel[0].Cost)
	}

	// Delete stats should zero out cost
	if err := s.DeleteProjectStats("proj-1", "model-a", "provider-x"); err != nil {
		t.Fatalf("delete stats: %v", err)
	}
	stats, _ = s.GetProjectStats("proj-1")
	if stats.ByModel[0].Cost != 0 {
		t.Errorf("expected cost 0 after delete, got %f", stats.ByModel[0].Cost)
	}
}
