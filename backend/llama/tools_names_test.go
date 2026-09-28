package llama

import (
	"testing"

	"mike/backend/types"
)

// TestToolSetsUseOnlyDeclaredNames: offered names and declared names must match in both directions,
// or the provider, the dispatcher and the frontend disagree about a tool.
func TestToolSetsUseOnlyDeclaredNames(t *testing.T) {
	declared := make(map[string]bool, len(types.CoreToolNames()))
	for _, n := range types.CoreToolNames() {
		if declared[n] {
			t.Errorf("%q is declared twice", n)
		}
		declared[n] = true
	}

	sets := map[string][]types.ChatTool{
		"act":     GetToolDefinitions(),
		"plan":    GetPlanModeToolDefinitions(),
		"explore": GetExploreModeToolDefinitions(),
		"lsp":     GetLSPToolDefinitions(),
	}

	offered := make(map[string]bool)
	for mode, defs := range sets {
		if len(defs) == 0 {
			t.Fatalf("%s mode offers no tools", mode)
		}
		for _, n := range toolNames(defs) {
			if !declared[n] {
				t.Errorf("%s mode offers %q, which is not in types.CoreToolNames()", mode, n)
			}
			offered[n] = true
		}
	}

	// A declared name in no set is usually a definition dropped from a mode by accident.
	for _, n := range types.CoreToolNames() {
		if !offered[n] {
			t.Errorf("%q is declared but offered by no mode", n)
		}
	}
}

// TestDefinitionsCarryTheirName: a definition with an empty Name turns into a tool call with an
// empty Function.Name, which exec skips when pairing calls with results - so the tool never runs and
// the card stays on "Running".
func TestDefinitionsCarryTheirName(t *testing.T) {
	all := append(append(append(GetToolDefinitions(), GetPlanModeToolDefinitions()...),
		GetExploreModeToolDefinitions()...), GetLSPToolDefinitions()...)
	if len(all) == 0 {
		t.Fatal("no tool definitions at all")
	}
	seen := map[string]int{}
	for i := range all {
		def := all[i].Function
		if def == nil {
			t.Fatalf("tool definition %d has no Function", i)
		}
		if def.Name == "" {
			t.Fatalf("tool definition %d (%s) has an empty Name", i, def.DisplayName)
		}
		seen[def.Name]++
	}
	// bash and request_review legitimately carry two definitions each - the read-only bash
	// variant plan mode offers, and the plan-shaped review card - both under one name so the
	// model is never shown two near-identical tools. The LSP tools appear in the lsp set only.
	// Anything repeating more often than that is a definition pasted into a mode by mistake.
	for name, count := range seen {
		if count > 3 {
			t.Errorf("%q appears in %d definitions, more than the modes that legitimately carry a variant", name, count)
		}
	}
}

// TestToolNamesMatchTheirDefinitions closes the loop the constants exist for: types.CoreToolNames and
// the definitions must be the same set, so renaming a constant without the definition (or the
// other way round) fails here rather than at the first tool call nobody answers.
func TestToolNamesMatchTheirDefinitions(t *testing.T) {
	declared := map[string]bool{}
	for _, n := range types.CoreToolNames() {
		declared[n] = true
	}
	all := append(append(append(GetToolDefinitions(), GetPlanModeToolDefinitions()...),
		GetExploreModeToolDefinitions()...), GetLSPToolDefinitions()...)
	for _, n := range toolNames(all) {
		if !declared[n] {
			t.Errorf("a tool is defined as %q but types.CoreToolNames does not declare it", n)
		}
	}
}

// toolNames extracts the names of a definition list, tolerating a missing Function so a broken
// definition surfaces in the test that owns it rather than as a nil dereference here.
func toolNames(defs []types.ChatTool) []string {
	out := make([]string, 0, len(defs))
	for i := range defs {
		if defs[i].Function == nil {
			out = append(out, "")
			continue
		}
		out = append(out, defs[i].Function.Name)
	}
	return out
}
