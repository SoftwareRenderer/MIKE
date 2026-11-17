package llama

import (
	"sort"
	"testing"

	"mike/backend/types"
)

// toolSets is the one listing of which mode offers which tool, by name. Plan mode's absence of
// write_file and edit_file is the point: a planning turn is held by the read-only worktree mount and
// this list, not by an executor allowlist, so a name added here is a grant.
var toolSets = []struct {
	mode  string
	offer func() []types.ChatTool
	want  []string
}{
	{
		mode:  "act",
		offer: GetToolDefinitions,
		want: []string{types.ToolReadFile, types.ToolWriteFile, types.ToolEditFile, types.ToolBash,
			types.ToolAskQuestion, types.ToolRequestReview},
	},
	{
		mode:  "plan",
		offer: GetPlanModeToolDefinitions,
		want: []string{types.ToolReadFile, types.ToolBash, types.ToolAskQuestion, types.ToolRequestReview,
			types.ToolCreateTask, types.ToolViewTasks, types.ToolEditTask},
	},
	{
		mode:  "explore",
		offer: GetExploreModeToolDefinitions,
		want: []string{types.ToolReadFile, types.ToolWriteFile, types.ToolEditFile, types.ToolBash,
			types.ToolAskQuestion, types.ToolRequestReview, types.ToolCreateTask, types.ToolViewTasks,
			types.ToolEditTask},
	},
	{
		mode:  "lsp",
		offer: GetLSPToolDefinitions,
		want:  []string{types.ToolLSPReferences, types.ToolLSPDefinition, types.ToolLSPHover, types.ToolLSPRename},
	},
}

// TestToolSetPerMode: a mode offers exactly its own names — one too many is a tool a planning or
// exploring turn was never given, one too few is a tool its prompt still tells the model to call.
func TestToolSetPerMode(t *testing.T) {
	for _, set := range toolSets {
		t.Run(set.mode, func(t *testing.T) {
			defs := set.offer()
			if len(defs) == 0 {
				t.Fatal("this mode offers no tools at all")
			}
			got := append([]string(nil), toolNames(defs)...)
			sort.Strings(got)
			want := append([]string(nil), set.want...)
			sort.Strings(want)
			if len(got) != len(want) {
				t.Fatalf("offered %v, want %d tools %v", got, len(want), want)
			}
			for i := range got {
				if got[i] != want[i] {
					t.Fatalf("offered %v, want %v", got, want)
				}
			}
		})
	}
}

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

	offered := make(map[string]bool)
	for _, set := range toolSets {
		for _, n := range toolNames(set.offer()) {
			if !declared[n] {
				t.Errorf("%s mode offers %q, which is not in types.CoreToolNames()", set.mode, n)
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
	var all []types.ChatTool
	for _, set := range toolSets {
		all = append(all, set.offer()...)
	}
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
