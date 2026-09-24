package exec

import (
	"context"
	"strings"
	"testing"
	"time"

	"mike/backend/llama"
	"mike/backend/types"
)

// TestEveryDeclaredToolIsDispatched checks that every tool offered to a provider is also dispatched.
// The three sides of a tool call agree only on a string, so an und dispatched name is not a compile
// error: the model calls the tool, nothing answers, and the call renders as "Running" forever.
//
// Behavioural on purpose - grepping tools.go for `case "…"` would pass while the dispatcher was
// broken and fail when someone reformatted it.
func TestEveryDeclaredToolIsDispatched(t *testing.T) {
	t.Parallel()
	te := newTestExecutor(t)
	worktree := t.TempDir()
	task := &types.Task{ID: "dispatch-probe", ProjectID: "dispatch-proj", Title: "probe"}
	state := &executionState{ctx: t.Context()}

	for _, name := range llama.AllToolNames() {
		t.Run(name, func(t *testing.T) {
			// No arguments: every tool must reject that on its own. The point is not what the
			// tool answers but that it answers at all, so a bounded context keeps a tool that
			// somehow starts real work from hanging the suite.
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()

			result, ok := te.executeTool(ctx, name, map[string]any{}, worktree, task, state, nil)
			if strings.HasPrefix(result, "Unknown tool:") {
				t.Errorf("%q is offered to providers but the dispatch table has no entry for it: %s", name, result)
			}
			// A failed call has to explain itself; an empty failure is indistinguishable from a
			// call still running. (An empty result with ok is fine - bash with no command is a no-op.)
			if !ok && result == "" {
				t.Errorf("%q failed with an empty message; a tool call with no answer renders as Running forever", name)
			}
		})
	}
}
