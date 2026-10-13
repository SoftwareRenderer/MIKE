package llama

import (
	"strings"
	"testing"

	"mike/backend/types"
)

func delta(index int, id, name, args string) types.ChatToolCall {
	var c types.ChatToolCall
	c.Index = index
	c.ID = id
	c.Type = "function"
	c.Function.Name = name
	c.Function.Arguments = args
	return c
}

// A provider splits the argument JSON wherever it likes; the completed call must read the same
// however the stream was cut.
func TestToolCallAccumulatorJoinsDeltasOfAnySize(t *testing.T) {
	full := `{"path":"src/a.go","content":"` + strings.Repeat("x", 5000) + `"}`
	for _, piece := range []int{1, 7, 64, 4096, len(full)} {
		var acc ToolCallAccumulator
		for i := 0; i < len(full); i += piece {
			end := min(i+piece, len(full))
			// A provider announces the name once and then sends only arguments.
			name := ""
			if i == 0 {
				name = "write_file"
			}
			acc.Add([]types.ChatToolCall{delta(0, "call_1", name, full[i:end])})
		}
		got := acc.Calls()
		if len(got) != 1 {
			t.Fatalf("piece=%d: %d calls, want 1", piece, len(got))
		}
		if got[0].ID != "call_1" || got[0].Function.Name != "write_file" {
			t.Errorf("piece=%d: id/name = %q/%q, want call_1/write_file", piece, got[0].ID, got[0].Function.Name)
		}
		if got[0].Function.Arguments != full {
			t.Errorf("piece=%d: arguments are %d bytes, want the %d sent", piece, len(got[0].Function.Arguments), len(full))
		}
	}
}

// Parallel calls arrive interleaved on their own indexes; the loop reads them back in the order
// they opened.
func TestToolCallAccumulatorKeepsParallelCallsApart(t *testing.T) {
	var acc ToolCallAccumulator
	acc.Add([]types.ChatToolCall{delta(0, "a", "read_file", `{"path":`)})
	acc.Add([]types.ChatToolCall{delta(1, "b", "list_dir", `{"path":"b"}`)})
	acc.Add([]types.ChatToolCall{delta(0, "", "", `"a"}`)})

	got := acc.Calls()
	if len(got) != 2 {
		t.Fatalf("%d calls, want 2", len(got))
	}
	if got[0].ID != "a" || got[1].ID != "b" {
		t.Errorf("order = %s,%s, want a,b", got[0].ID, got[1].ID)
	}
	if got[0].Function.Arguments != `{"path":"a"}` {
		t.Errorf("first call's arguments = %q", got[0].Function.Arguments)
	}
	if got[0].Index != 0 || got[1].Index != 1 {
		t.Errorf("indexes = %d,%d, want 0,1", got[0].Index, got[1].Index)
	}
}

// A retried or cancelled call must not leave its partial arguments behind.
func TestToolCallAccumulatorResetStartsTheNextCallClean(t *testing.T) {
	var acc ToolCallAccumulator
	acc.Add([]types.ChatToolCall{delta(0, "old", "write_file", `{"path":"half`)})
	acc.Reset()
	if got := acc.Calls(); got != nil {
		t.Fatalf("after Reset: %d calls, want none", len(got))
	}
	acc.Add([]types.ChatToolCall{delta(0, "new", "read_file", `{"path":"x"}`)})
	got := acc.Calls()
	if len(got) != 1 || got[0].ID != "new" || got[0].Function.Arguments != `{"path":"x"}` {
		t.Fatalf("after Reset: %+v, want one clean call", got)
	}
}

// The loop takes a snapshot every token to show the call growing, so a snapshot must keep the
// arguments it was taken with.
func TestToolCallAccumulatorSnapshotKeepsItsOwnArguments(t *testing.T) {
	var acc ToolCallAccumulator
	acc.Add([]types.ChatToolCall{delta(0, "a", "write_file", `{"path":"x","content":"123`)})
	first := acc.Calls()[0].Function.Arguments

	acc.Add([]types.ChatToolCall{delta(0, "", "", `456"}`)})

	if first != `{"path":"x","content":"123` {
		t.Errorf("snapshot changed under the caller: %q", first)
	}
	if got := acc.Calls()[0].Function.Arguments; got != `{"path":"x","content":"123456"}` {
		t.Errorf("later snapshot = %q", got)
	}
}
