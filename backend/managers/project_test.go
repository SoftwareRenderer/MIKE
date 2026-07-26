package managers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kanban/backend/config"
	"kanban/backend/store"
	"kanban/backend/types"
)

// floatApprox checks if two floats are approximately equal within a tolerance.
func floatApprox(t *testing.T, got, want, tol float64) {
	t.Helper()
	if got < want-tol || got > want+tol {
		t.Fatalf("float not approximately equal: got %.6f, want %.6f (tol %.6f)", got, want, tol)
	}
}

// testProjectHandler creates a ProjectHandler with a temporary database for testing.
func testProjectHandler(t *testing.T) (*ProjectHandler, string) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	worktreesDir := filepath.Join(dir, "worktrees")
	os.MkdirAll(worktreesDir, 0o755)

	s, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	cfgStore := config.NewConfigStore(s, dbPath, "test")

	h := &ProjectHandler{
		store:       s,
		configStore: cfgStore,
	}

	// Create a test project
	proj := &types.Project{
		ID:   "test-proj",
		Name: "test",
		Path: dir,
	}
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	return h, "test-proj"
}

// createTestTaskWithMessages creates a task with assistant messages for stats testing.
func createTestTaskWithMessages(t *testing.T, s *store.Store, projID, taskID, modelName, providerName string,
	promptTokens, predictedTokens, cacheTokens, promptMs, predictedMs int,
) {
	t.Helper()
	task := &types.Task{
		ID:        taskID,
		ProjectID: projID,
		Title:     "test task",
		Status:    "backlog",
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	msg := &types.TaskMessage{
		ID:            taskID + "-msg-1",
		TaskID:        taskID,
		Role:          "assistant",
		Content:       "test response",
		ModelName:     modelName,
		ProviderName:  providerName,
		PromptTokens:  promptTokens,
		PredictedTokens: predictedTokens,
		CacheTokens:   cacheTokens,
		PromptMs:      promptMs,
		PredictedMs:   predictedMs,
		CreatedAt:     time.Now(),
	}
	if err := s.CreateTaskMessage(msg); err != nil {
		t.Fatalf("create message: %v", err)
	}
}

func TestApplyPricingBasic(t *testing.T) {
	h, projID := testProjectHandler(t)

	// Create a task with messages
	createTestTaskWithMessages(t, h.store, projID, "task-1", "model-a", "prov-x",
		1000, 500, 100, 1000, 5000)

	// Set up provider with pricing (no power settings)
	cfg, _ := h.configStore.GetConfig()
	cfg.Providers = []types.Provider{
		{
			Name:          "prov-x",
			DisplayName:   "Provider X",
			URL:           "http://localhost:8080/v1",
			InputPrice:    3.0,   // $3 per 1M input tokens
			OutputPrice:   5.0,   // $5 per 1M output tokens
			CacheReadPrice: 1.0, // $1 per 1M cache-read tokens
		},
	}
	h.configStore.UpdateConfig(cfg)

	// Get stats via the handler
	stats, err := h.store.GetProjectStats(projID)
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}
	h.applyPricing(stats)

	// API cost: (900 * 3 + 100 * 1 + 500 * 5) / 1M = (2700 + 100 + 2500) / 1M = 0.0053
	if len(stats.ByModel) != 1 {
		t.Fatalf("expected 1 model, got %d", len(stats.ByModel))
	}
	m := stats.ByModel[0]
	floatApprox(t, m.Cost, 0.0053, 0.0001)
	floatApprox(t, stats.TotalCost, 0.0053, 0.0001)
	// No power settings, so power should be 0
	floatApprox(t, m.PowerKwh, 0, 0.0001)
}

func TestApplyPricingWithPower(t *testing.T) {
	h, projID := testProjectHandler(t)

	// Create a task with messages (5000ms predicted time)
	createTestTaskWithMessages(t, h.store, projID, "task-1", "model-a", "prov-x",
		1000, 500, 100, 1000, 5000)

	// Set up provider with pricing AND power settings
	cfg, _ := h.configStore.GetConfig()
	cfg.Providers = []types.Provider{
		{
			Name:                 "prov-x",
			DisplayName:          "Provider X",
			URL:                  "http://localhost:8080/v1",
			InputPrice:           3.0,
			OutputPrice:          5.0,
			CacheReadPrice:       1.0,
			PowerConsumptionW:    500.0, // 500W GPU
			ElectricityCostPerKwh: 0.12, // $0.12 per kWh
		},
	}
	h.configStore.UpdateConfig(cfg)

	stats, err := h.store.GetProjectStats(projID)
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}
	h.applyPricing(stats)

	if len(stats.ByModel) != 1 {
		t.Fatalf("expected 1 model, got %d", len(stats.ByModel))
	}
	m := stats.ByModel[0]

	// Power: 500W * 5000ms / 3,600,000,000 = 0.00069444 kWh
	expectedKwh := 500.0 * 5000.0 / 3_600_000_000
	floatApprox(t, m.PowerKwh, expectedKwh, 0.000001)
	floatApprox(t, stats.TotalPowerKwh, expectedKwh, 0.000001)

	// Electricity cost: 0.00069444 * 0.12 = 0.00008333
	expectedElecCost := expectedKwh * 0.12
	// Total cost = API cost + electricity cost
	expectedTotalCost := 0.0053 + expectedElecCost
	floatApprox(t, m.Cost, expectedTotalCost, 0.0001)
	floatApprox(t, stats.TotalCost, expectedTotalCost, 0.0001)
}

func TestApplyPricingPowerOnlyNoElectricity(t *testing.T) {
	h, projID := testProjectHandler(t)

	createTestTaskWithMessages(t, h.store, projID, "task-1", "model-a", "prov-x",
		1000, 500, 100, 1000, 5000)

	// Set up provider with power but NO electricity cost
	cfg, _ := h.configStore.GetConfig()
	cfg.Providers = []types.Provider{
		{
			Name:              "prov-x",
			DisplayName:       "Provider X",
			URL:               "http://localhost:8080/v1",
			InputPrice:        3.0,
			OutputPrice:       5.0,
			CacheReadPrice:    1.0,
			PowerConsumptionW: 500.0,
			// ElectricityCostPerKwh not set (0)
		},
	}
	h.configStore.UpdateConfig(cfg)

	stats, err := h.store.GetProjectStats(projID)
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}
	h.applyPricing(stats)

	if len(stats.ByModel) != 1 {
		t.Fatalf("expected 1 model, got %d", len(stats.ByModel))
	}
	m := stats.ByModel[0]

	// Power should be computed
	expectedKwh := 500.0 * 5000.0 / 3_600_000_000
	floatApprox(t, m.PowerKwh, expectedKwh, 0.000001)
	floatApprox(t, stats.TotalPowerKwh, expectedKwh, 0.000001)

	// Cost should be API only (no electricity added)
	floatApprox(t, m.Cost, 0.0053, 0.0001)
	floatApprox(t, stats.TotalCost, 0.0053, 0.0001)
}

func TestApplyPricingAgentProviderSkipped(t *testing.T) {
	h, projID := testProjectHandler(t)

	createTestTaskWithMessages(t, h.store, projID, "task-1", "agent-model", "my-agent",
		1000, 500, 100, 1000, 5000)

	// Set up agent provider with power settings (should be ignored)
	cfg, _ := h.configStore.GetConfig()
	cfg.Providers = []types.Provider{
		{
			Name:                 "my-agent",
			DisplayName:          "My Agent",
			Kind:                 "agent",
			URL:                  "agent://my-agent",
			InputPrice:           0,
			OutputPrice:          0,
			PowerConsumptionW:    500.0,
			ElectricityCostPerKwh: 0.12,
		},
	}
	h.configStore.UpdateConfig(cfg)

	stats, err := h.store.GetProjectStats(projID)
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}
	h.applyPricing(stats)

	if len(stats.ByModel) != 1 {
		t.Fatalf("expected 1 model, got %d", len(stats.ByModel))
	}
	m := stats.ByModel[0]

	// Agent provider: power/electricity should NOT be computed
	floatApprox(t, m.PowerKwh, 0, 0.000001)
	floatApprox(t, stats.TotalPowerKwh, 0, 0.000001)
	// Cost should be 0 (no pricing set for agent)
	floatApprox(t, m.Cost, 0, 0.0001)
}

func TestApplyPricingMultipleProviders(t *testing.T) {
	h, projID := testProjectHandler(t)

	// Task 1: model-a from prov-x (with power)
	createTestTaskWithMessages(t, h.store, projID, "task-1", "model-a", "prov-x",
		1000, 500, 100, 1000, 5000)

	// Task 2: model-b from prov-y (no power)
	createTestTaskWithMessages(t, h.store, projID, "task-2", "model-b", "prov-y",
		2000, 300, 200, 2000, 3000)

	cfg, _ := h.configStore.GetConfig()
	cfg.Providers = []types.Provider{
		{
			Name:              "prov-x",
			DisplayName:       "Provider X",
			URL:               "http://localhost:8080/v1",
			InputPrice:        3.0,
			OutputPrice:       5.0,
			CacheReadPrice:    1.0,
			PowerConsumptionW: 500.0,
		},
		{
			Name:        "prov-y",
			DisplayName: "Provider Y",
			URL:         "http://localhost:9090/v1",
			InputPrice:  1.0,
			OutputPrice: 2.0,
			// No power settings
		},
	}
	h.configStore.UpdateConfig(cfg)

	stats, err := h.store.GetProjectStats(projID)
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}
	h.applyPricing(stats)

	if len(stats.ByModel) != 2 {
		t.Fatalf("expected 2 models, got %d", len(stats.ByModel))
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
		t.Fatal("missing model")
	}

	// model-a: has power, no electricity cost
	expectedKwhA := 500.0 * 5000.0 / 3_600_000_000
	floatApprox(t, modelA.PowerKwh, expectedKwhA, 0.000001)
	// API cost for model-a: (900*3 + 100*1 + 500*5) / 1M = 0.0053
	floatApprox(t, modelA.Cost, 0.0053, 0.0001)

	// model-b: no power
	floatApprox(t, modelB.PowerKwh, 0, 0.000001)
	// API cost for model-b: (1800*1 + 200*0 + 300*2) / 1M = (1800 + 600) / 1M = 0.0024
	// Note: prov-y has no cache_read_price set, so cache cost is 0
	floatApprox(t, modelB.Cost, 0.0024, 0.0001)

	// Totals
	floatApprox(t, stats.TotalPowerKwh, expectedKwhA, 0.000001)
	floatApprox(t, stats.TotalCost, 0.0053+0.0024, 0.0001)
}

// Test JSON serialization of new fields
func TestApplyPricingJSONSerialization(t *testing.T) {
	h, projID := testProjectHandler(t)

	createTestTaskWithMessages(t, h.store, projID, "task-1", "model-a", "prov-x",
		1000, 500, 100, 1000, 5000)

	cfg, _ := h.configStore.GetConfig()
	cfg.Providers = []types.Provider{
		{
			Name:                 "prov-x",
			DisplayName:          "Provider X",
			URL:                  "http://localhost:8080/v1",
			InputPrice:           3.0,
			OutputPrice:          5.0,
			CacheReadPrice:       1.0,
			PowerConsumptionW:    500.0,
			ElectricityCostPerKwh: 0.12,
		},
	}
	h.configStore.UpdateConfig(cfg)

	stats, err := h.store.GetProjectStats(projID)
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}
	h.applyPricing(stats)

	// Marshal to JSON and verify fields are present
	data, err := json.Marshal(stats)
	if err != nil {
		t.Fatalf("marshal stats: %v", err)
	}
	jsonStr := string(data)

	// Verify power_kwh field is in JSON
	if !strings.Contains(jsonStr, `"power_kwh"`) {
		t.Fatalf("expected power_kwh in JSON, got: %s", jsonStr)
	}
	if !strings.Contains(jsonStr, `"total_power_kwh"`) {
		t.Fatalf("expected total_power_kwh in JSON, got: %s", jsonStr)
	}

	// Unmarshal and verify values
	var unmarshaled types.ProjectStats
	if err := json.Unmarshal(data, &unmarshaled); err != nil {
		t.Fatalf("unmarshal stats: %v", err)
	}
	if len(unmarshaled.ByModel) != 1 {
		t.Fatalf("expected 1 model in JSON, got %d", len(unmarshaled.ByModel))
	}
	m := unmarshaled.ByModel[0]

	if m.PowerKwh <= 0 {
		t.Fatalf("expected power_kwh > 0, got %f", m.PowerKwh)
	}
	if unmarshaled.TotalPowerKwh <= 0 {
		t.Fatalf("expected total_power_kwh > 0, got %f", unmarshaled.TotalPowerKwh)
	}
	expectedKwh := 500.0 * 5000.0 / 3_600_000_000
	expectedTotalCost := 0.0053 + expectedKwh*0.12
	floatApprox(t, m.Cost, expectedTotalCost, 0.0001)
}
