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
	foreign := "binary-ish output \xff\xfe end"

	repaired := toolResultText(foreign)
	if _, err := json.Marshal([]types.ChatMessage{{Role: "tool", Content: repaired}}); err != nil {
		t.Errorf("a repaired tool result still cannot be marshaled into a request: %v", err)
	}
	// The control: the test measures the repair, not a marshaller that never fails.
	if _, err := json.Marshal([]types.ChatMessage{{Role: "tool", Content: foreign}}); err == nil {
		t.Error("marshaling the unrepaired result succeeded, so nothing here proves the repair holds")
	}
	if !strings.Contains(repaired, "binary-ish output") || !strings.Contains(repaired, "end") {
		t.Errorf("the repair dropped the text around the stray bytes: %q", repaired)
	}
}

func TestToolResultTextRepairsARuneSplitByTheByteCap(t *testing.T) {
	// The per-result cap cuts on a byte index, so a cut inside a multi-byte rune is the everyday
	// source of the same invalid string a stray byte produces.
	cut := "héllo wörld"[:2]
	repaired := toolResultText(cut)
	if _, err := json.Marshal(map[string]string{"output": repaired}); err != nil {
		t.Errorf("a rune split by the cap cannot be marshaled: %v", err)
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
