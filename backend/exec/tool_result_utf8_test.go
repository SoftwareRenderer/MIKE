package exec

import (
	"encoding/json/v2"
	"strings"
	"testing"

	"mike/backend/types"
)

// A tool result carrying bytes that are not valid UTF-8 reaches three encoders: the next provider
// request, the WebSocket frame that shows the result, and the stored row on replay. v2 makes each of
// them an error where v1 wrote U+FFFD.

func TestToolResultTextKeepsTheConversationMarshalable(t *testing.T) {
	// The everyday sources of a tool result that is not valid UTF-8: a command that printed binary, and
	// the per-result cap cutting a multi-byte rune in half.
	cases := []struct {
		name  string
		in    string
		wants []string // what must survive the repair
	}{
		{name: "binary-ish output", in: "binary-ish output \xff\xfe end", wants: []string{"binary-ish output", "end"}},
		{name: "a rune split by the byte cap", in: "héllo wörld"[:2], wants: []string{"h"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repaired := toolResultText(tc.in)
			if _, err := json.Marshal([]types.ChatMessage{{Role: "tool", Content: repaired}}); err != nil {
				t.Errorf("a repaired tool result still cannot be marshaled into a request: %v", err)
			}
			if _, err := json.Marshal(map[string]string{"output": repaired}); err != nil {
				t.Errorf("a repaired tool result still cannot be marshaled into the stored row: %v", err)
			}
			// The control: the test measures the repair, not a marshaller that never fails.
			if _, err := json.Marshal([]types.ChatMessage{{Role: "tool", Content: tc.in}}); err == nil {
				t.Error("marshaling the unrepaired result succeeded, so nothing here proves the repair holds")
			}
			for _, want := range tc.wants {
				if !strings.Contains(repaired, want) {
					t.Errorf("the repair dropped %q: %q", want, repaired)
				}
			}
		})
	}
}

func TestToolResultMessageKeepsARowHoldingForeignBytes(t *testing.T) {
	// A row whose JSON holds a stray byte fails v2's decode. Silently returning false here would
	// leave the assistant's tool_call with no tool row beside it.
	m := types.TaskMessage{ToolResult: "{\"output\":\"seen \xff before the cap\"}"}

	msg, ok := toolResultMessage(m)
	if !ok {
		t.Fatalf("a stored tool row holding foreign bytes was dropped instead of rebuilt")
	}
	if !strings.Contains(msg.Content, "seen ") || !strings.Contains(msg.Content, " before the cap") {
		t.Errorf("rebuilt content lost the text around the stray byte: %q", msg.Content)
	}
}
