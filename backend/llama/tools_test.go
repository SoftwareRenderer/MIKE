package llama

import (
	"testing"
)

func TestGetPlanModeToolDefinitions(t *testing.T) {
	tools := GetPlanModeToolDefinitions()

	// Should have exactly 4 tools: read_file, bash, create_task, task_complete
	if len(tools) != 4 {
		t.Fatalf("expected 4 tools in plan mode, got %d", len(tools))
	}

	toolNames := make(map[string]bool)
	for _, tool := range tools {
		toolNames[tool.Function.Name] = true
	}

	// Should have read_file, bash, create_task, task_complete
	expectedTools := []string{"read_file", "bash", "create_task", "task_complete"}
	for _, name := range expectedTools {
		if !toolNames[name] {
			t.Errorf("expected tool %q in plan mode, not found", name)
		}
	}

	// Should NOT have write_file, edit_file
	forbiddenTools := []string{"write_file", "edit_file"}
	for _, name := range forbiddenTools {
		if toolNames[name] {
			t.Errorf("tool %q should NOT be available in plan mode", name)
		}
	}
}

func TestGetToolDefinitions_Standard(t *testing.T) {
	tools := GetToolDefinitions()
	
	toolNames := make(map[string]bool)
	for _, tool := range tools {
		toolNames[tool.Function.Name] = true
	}
	
	// Standard mode should have all tools
	expectedTools := []string{"read_file", "write_file", "edit_file", "bash", "task_complete", "create_task"}
	for _, name := range expectedTools {
		if !toolNames[name] {
			t.Errorf("expected tool %q in standard mode, not found", name)
		}
	}
}
