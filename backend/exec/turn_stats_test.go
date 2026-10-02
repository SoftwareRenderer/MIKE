package exec

import (
	"encoding/json"
	"slices"
	"testing"
)

// TestTurnStatsFrameIsTheFlatCompletePayload pins the keys the panel reads off the complete frame.
// TestGoldenWire covers HTTP bodies only, so no other gate would see a renamed or dropped frame key.
func TestTurnStatsFrameIsTheFlatCompletePayload(t *testing.T) {
	frame := turnStatsFrame("review", "msg-1", 11, 22, 33, 44, 55, "llama-3", 666, nil)

	want := []string{
		"assistant_message_id", "cache_tokens", "duration", "model_name", "predicted_ms",
		"predicted_tokens", "prompt_ms", "prompt_tokens", "state",
	}
	got := make([]string, 0, len(frame))
	for k := range frame {
		got = append(got, k)
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("frame keys = %v, want %v", got, want)
	}
	for k, want := range map[string]any{
		"state": "review", "assistant_message_id": "msg-1",
		"prompt_tokens": 11, "predicted_tokens": 22, "cache_tokens": 33,
		"prompt_ms": 44, "predicted_ms": 55, "model_name": "llama-3", "duration": int64(666),
	} {
		if frame[k] != want {
			t.Errorf("frame[%q] = %#v, want %#v", k, frame[k], want)
		}
	}

	withSummary := turnStatsFrame("idle", "msg-2", 1, 2, 3, 4, 5, "m", 6, map[string]any{"summary": "Agent finished responding"})
	if withSummary["summary"] != "Agent finished responding" || len(withSummary) != len(want)+1 {
		t.Errorf("extra not merged into a 9-key frame: %#v", withSummary)
	}

	// A nested value would reach the panel as an object.
	b, err := json.Marshal(turnStatsFrame("idle", "msg-3", 0, 0, 0, 0, 0, "m", 0, nil))
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatal(err)
	}
	for k, v := range wire {
		if _, nested := v.(map[string]any); nested {
			t.Errorf("frame[%q] is a nested object: %s", k, b)
		}
	}
}
