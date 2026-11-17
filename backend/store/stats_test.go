package store

import (
	"math"
	"mike/backend/types"
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

// One project's totals, and the model/provider groups the dialog breaks them into: a group is one row of
// the cumulative table, so two models are two rows and never one sum.
func TestGetProjectStatsBasic(t *testing.T) {
	s := testStore(t)
	if err := s.CreateProject(newProject("proj-1", "test", "/tmp/test")); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask("task-1", "proj-1")); err != nil {
		t.Fatalf("create task: %v", err)
	}

	seed := []struct {
		id, model, provider                       string
		prompt, predicted, cache, promptMs, genMs int
	}{
		{"m1", "model-a", "provider-x", 100, 50, 20, 500, 2000},
		{"m2", "model-a", "provider-x", 200, 100, 50, 1000, 4000},
		{"m3", "model-b", "provider-y", 200, 100, 0, 0, 0},
	}
	for _, m := range seed {
		msg := newMessage(m.id, "task-1", "assistant", "response "+m.id)
		msg.ModelName, msg.ProviderName = m.model, m.provider
		msg.PromptTokens, msg.PredictedTokens, msg.CacheTokens = m.prompt, m.predicted, m.cache
		msg.PromptMs, msg.PredictedMs = m.promptMs, m.genMs
		if err := s.CreateTaskMessage(msg); err != nil {
			t.Fatalf("create %s: %v", m.id, err)
		}
	}

	stats, err := s.GetProjectStats("proj-1")
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}
	if stats.TotalTasks != 1 {
		t.Errorf("TotalTasks = %d, want 1", stats.TotalTasks)
	}
	if stats.TotalPromptTokens != 500 || stats.TotalGeneratedTokens != 250 || stats.TotalCacheTokens != 70 {
		t.Errorf("totals = prompt %d, generated %d, cache %d; want 500, 250, 70",
			stats.TotalPromptTokens, stats.TotalGeneratedTokens, stats.TotalCacheTokens)
	}
	if stats.TotalProcessedTokens != 430 {
		t.Errorf("TotalProcessedTokens = %d, want 430 (prompt minus cache)", stats.TotalProcessedTokens)
	}

	if len(stats.ByModel) != 2 {
		t.Fatalf("%d model groups, want 2: %+v", len(stats.ByModel), stats.ByModel)
	}
	for _, want := range []struct {
		model, provider              string
		prompt, generated, taskCount int
	}{
		{"model-a", "provider-x", 300, 150, 1},
		{"model-b", "provider-y", 200, 100, 1},
	} {
		var found *types.ModelStats
		for i := range stats.ByModel {
			if stats.ByModel[i].ModelName == want.model && stats.ByModel[i].ProviderName == want.provider {
				found = &stats.ByModel[i]
			}
		}
		if found == nil {
			t.Fatalf("no group for %s/%s: %+v", want.model, want.provider, stats.ByModel)
		}
		if found.PromptTokens != want.prompt || found.GeneratedTokens != want.generated {
			t.Errorf("%s/%s = prompt %d, generated %d; want %d, %d",
				want.model, want.provider, found.PromptTokens, found.GeneratedTokens, want.prompt, want.generated)
		}
		if found.TaskCount != want.taskCount {
			t.Errorf("%s/%s task_count = %d, want %d", want.model, want.provider, found.TaskCount, want.taskCount)
		}
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

func TestGetProjectStatsThroughputExcludesNoTimingData(t *testing.T) {
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

	// Prompt throughput: the 1000 tokens over the 1s the one provider with timing data reported.
	if !floatEquals(stats.AvgPromptTps, 1000.0, 1.0) {
		t.Fatalf("expected ~1000 prompt tok/s, got %.2f", stats.AvgPromptTps)
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
	if !floatEquals(gpt4.AvgPromptTps, 1000.0, 1.0) {
		t.Fatalf("expected gpt-4 ~1000 prompt tok/s, got %.2f", gpt4.AvgPromptTps)
	}
	if !floatEquals(gpt4.AvgGenerationTps, 250.0, 1.0) {
		t.Fatalf("expected gpt-4 ~250 gen tok/s, got %.2f", gpt4.AvgGenerationTps)
	}
	// pi has no timing data, so its TPS should be 0
	if pi.AvgGenerationTps != 0 {
		t.Fatalf("expected pi gen tok/s = 0, got %.2f", pi.AvgGenerationTps)
	}
}

// Resetting a project's usage zeroes exactly the group the dialog named: with a provider named, only that
// provider's row; with none, the whole model. The zeroing takes the counters, the cost and the task rows
// behind them together, and never the messages that made them.
func TestDeleteProjectStats(t *testing.T) {
	seed := func(t *testing.T) *Store {
		s := testStore(t)
		if err := s.CreateProject(newProject("proj-1", "test", "/tmp/test")); err != nil {
			t.Fatalf("create project: %v", err)
		}
		if err := s.CreateTask(newTask("task-1", "proj-1")); err != nil {
			t.Fatalf("create task: %v", err)
		}
		for _, m := range []struct {
			id, role, model, provider string
			prompt, predicted         int
		}{
			{"m1", "assistant", "model-a", "provider-a", 100, 50},
			{"m2", "assistant", "model-a", "provider-b", 200, 100},
			{"m3", "assistant", "model-b", "provider-x", 300, 70},
			{"m4", "user", "", "", 50, 0},
		} {
			msg := newMessage(m.id, "task-1", m.role, m.id)
			msg.ModelName, msg.ProviderName = m.model, m.provider
			msg.PromptTokens, msg.PredictedTokens = m.prompt, m.predicted
			if err := s.CreateTaskMessage(msg); err != nil {
				t.Fatalf("create %s: %v", m.id, err)
			}
		}
		s.AccumulateProjectStats("proj-1", "model-a", "provider-a", 1.50)
		s.AccumulateProjectStats("proj-1", "model-a", "provider-b", 0.75)
		return s
	}

	for _, tc := range []struct {
		name     string
		model    string
		provider string
		zeroed   [][2]string
		kept     []struct {
			model, provider string
			cost            float64
		}
	}{
		{
			name:     "one provider of a model",
			model:    "model-a",
			provider: "provider-a",
			zeroed:   [][2]string{{"model-a", "provider-a"}},
			kept: []struct {
				model, provider string
				cost            float64
			}{
				{"model-a", "provider-b", 0.75},
				{"model-b", "provider-x", 0},
			},
		},
		{
			name:     "a model under every provider",
			model:    "model-a",
			provider: "",
			zeroed:   [][2]string{{"model-a", "provider-a"}, {"model-a", "provider-b"}},
			kept: []struct {
				model, provider string
				cost            float64
			}{
				{"model-b", "provider-x", 0},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := seed(t)
			if err := s.DeleteProjectStats("proj-1", tc.model, tc.provider); err != nil {
				t.Fatalf("delete stats: %v", err)
			}

			stats, err := s.GetProjectStats("proj-1")
			if err != nil {
				t.Fatalf("get stats: %v", err)
			}
			for _, group := range tc.zeroed {
				ms := statsGroup(t, stats, group[0], group[1])
				if ms.PromptTokens != 0 || ms.GeneratedTokens != 0 || ms.Cost != 0 || ms.TaskCount != 0 {
					t.Errorf("%s/%s still reads prompt %d, generated %d, cost %v, task_count %d",
						group[0], group[1], ms.PromptTokens, ms.GeneratedTokens, ms.Cost, ms.TaskCount)
				}
			}
			for _, group := range tc.kept {
				ms := statsGroup(t, stats, group.model, group.provider)
				if ms.PromptTokens == 0 || ms.TaskCount != 1 || ms.Cost != group.cost {
					t.Errorf("%s/%s was caught in the reset: prompt %d, task_count %d, cost %v",
						group.model, group.provider, ms.PromptTokens, ms.TaskCount, ms.Cost)
				}
			}

			msgs, err := s.GetTaskMessages("task-1", "")
			if err != nil {
				t.Fatalf("get messages: %v", err)
			}
			if len(msgs) != 4 {
				t.Errorf("%d messages left, want all 4 — a usage reset is not a conversation reset", len(msgs))
			}
		})
	}
}

func statsGroup(t *testing.T, stats *types.ProjectStats, model, provider string) types.ModelStats {
	t.Helper()
	for _, ms := range stats.ByModel {
		if ms.ModelName == model && ms.ProviderName == provider {
			return ms
		}
	}
	t.Fatalf("no %s/%s group in %+v", model, provider, stats.ByModel)
	return types.ModelStats{}
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

// The counters are cumulative: they sum the tasks that made them and outlive the rows that made them, so
// deleting a task does not silently rewrite what the project has already spent. task_count travels with
// them for the same reason — it counts the tasks that ran, not the rows still standing.
func TestStatsCumulativeAcrossTasks(t *testing.T) {
	s := testStore(t)
	if err := s.CreateProject(newProject("proj-1", "test", "/tmp/test")); err != nil {
		t.Fatalf("create project: %v", err)
	}

	for _, taskID := range []string{"task-1", "task-2"} {
		if err := s.CreateTask(newTask(taskID, "proj-1")); err != nil {
			t.Fatalf("create %s: %v", taskID, err)
		}
		msg := newMessage("m-"+taskID, taskID, "assistant", "response")
		msg.ModelName = "model-a"
		msg.PromptTokens = 100
		msg.PredictedTokens = 50
		msg.CacheTokens = 10
		if err := s.CreateTaskMessage(msg); err != nil {
			t.Fatalf("create message for %s: %v", taskID, err)
		}
	}

	want := func(t *testing.T, stats *types.ProjectStats, stage string) {
		t.Helper()
		if stats.TotalPromptTokens != 200 || stats.TotalGeneratedTokens != 100 || stats.TotalCacheTokens != 20 {
			t.Errorf("%s: totals = prompt %d, generated %d, cache %d; want 200, 100, 20",
				stage, stats.TotalPromptTokens, stats.TotalGeneratedTokens, stats.TotalCacheTokens)
		}
		if len(stats.ByModel) != 1 || stats.ByModel[0].TaskCount != 2 {
			t.Errorf("%s: task_count = %+v, want one group counting 2 tasks", stage, stats.ByModel)
		}
		if stats.TotalTasks != 2 {
			t.Errorf("%s: TotalTasks = %d, want 2", stage, stats.TotalTasks)
		}
	}

	stats, err := s.GetProjectStats("proj-1")
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}
	want(t, stats, "before the delete")

	if err := s.DeleteTask("proj-1", "task-1"); err != nil {
		t.Fatalf("delete the task whose messages made the totals: %v", err)
	}
	stats, err = s.GetProjectStats("proj-1")
	if err != nil {
		t.Fatalf("get stats after the delete: %v", err)
	}
	want(t, stats, "after the delete")
}

func TestGetProjectStatsTotalTasksExcludesLLMFreeTasks(t *testing.T) {
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
