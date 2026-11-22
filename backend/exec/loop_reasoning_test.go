package exec

import (
	"strings"
	"testing"
	"time"

	pkgagent "mike/pkg/agent"

	"mike/backend/types"
)

// collapseReasoning sets the preference a collapsed run reads once, when it starts.
func collapseReasoning(t *testing.T, te *TaskExecutor, on bool) {
	t.Helper()
	cfg, err := te.configStore.GetConfig()
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	cfg.CollapseReasoning = &on
	if err := te.configStore.UpdateConfig(cfg); err != nil {
		t.Fatalf("set collapse_reasoning: %v", err)
	}
}

func captureFlusher(collapsed bool) (*streamFlusher, *[]types.StreamEvent) {
	var events []types.StreamEvent
	return newStreamFlusher("proj", "task", func(_, _ string, e types.StreamEvent) {
		events = append(events, e)
	}, newLoopDetector(), collapsed), &events
}

// TestStreamFlusher_CollapsedReasoningAnnouncesCountNotText: the reasoning a call produces is counted and
// never written, and a flush with no growth owes no frame.
func TestStreamFlusher_CollapsedReasoningAnnouncesCountNotText(t *testing.T) {
	t.Parallel()
	flusher, events := captureFlusher(true)

	flusher.appendReasoning("Thinking ")
	flusher.appendReasoning("about it")
	if len(*events) != 0 {
		t.Fatalf("expected nothing on the wire before a flush, got %d frames", len(*events))
	}

	flusher.flush()
	if len(*events) != 1 {
		t.Fatalf("expected one frame per flush, got %d", len(*events))
	}
	frame := (*events)[0]
	if frame.Type != types.WSTypeReasoningProgress {
		t.Fatalf("frame type = %v, want %v", frame.Type, types.WSTypeReasoningProgress)
	}
	data, ok := frame.Data.(map[string]any)
	if !ok {
		t.Fatalf("frame data is %T, want a flat map", frame.Data)
	}
	if data["chars"] != 17 {
		t.Errorf("chars = %v, want the 17 characters this call has reasoned", data["chars"])
	}

	flusher.flush()
	if len(*events) != 1 {
		t.Errorf("a flush with no new reasoning sent %d more frames, want none", len(*events)-1)
	}
}

// TestStreamFlusher_CollapsedReasoningStillStreamsTokens: only the reasoning stream is collapsed; the
// answer keeps batching into a token frame.
func TestStreamFlusher_CollapsedReasoningStillStreamsTokens(t *testing.T) {
	t.Parallel()
	flusher, events := captureFlusher(true)

	flusher.appendReasoning("hmm")
	flusher.appendToken("Hello")
	flusher.flush()

	if len(*events) != 2 {
		t.Fatalf("expected a token frame and a count frame, got %d frames", len(*events))
	}
	if (*events)[0].Type != types.WSTypeToken {
		t.Errorf("first frame = %v, want the token frame", (*events)[0].Type)
	}
	if (*events)[1].Type != types.WSTypeReasoningProgress {
		t.Errorf("second frame = %v, want the count frame", (*events)[1].Type)
	}
}

// TestStreamFlusher_CollapsedReasoningCountRestartsOnRetry: the retried call reasons from nothing, so the
// count the client follows restarts.
func TestStreamFlusher_CollapsedReasoningCountRestartsOnRetry(t *testing.T) {
	t.Parallel()
	flusher, events := captureFlusher(true)

	flusher.appendReasoning(strings.Repeat("x", 40))
	flusher.flush()
	flusher.reset()
	flusher.appendReasoning("fresh")
	flusher.flush()

	if len(*events) != 2 {
		t.Fatalf("expected one count frame per flush, got %d", len(*events))
	}
	got := (*events)[1].Data.(map[string]any)["chars"]
	if got != 5 {
		t.Errorf("chars after reset = %v, want 5 — the retry reasoned over again", got)
	}
}

// TestStreamFlusher_HeldReasoningKeepsTheVisibleShapes: reasoning that IS the call's answer arrives once at
// the end; reasoning that only explains an answer on screen never arrives until it is asked for.
func TestStreamFlusher_HeldReasoningKeepsTheVisibleShapes(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("thought", 100)
	cases := []struct {
		name        string
		reasoning   string
		content     string
		hasToolCall bool
		wantSent    bool
	}{
		{name: "short reasoning that is the whole answer", reasoning: "A brief thought", wantSent: true},
		{name: "reasoning that carried a tool call", reasoning: long, hasToolCall: true, wantSent: true},
		{name: "reasoning behind an answer", reasoning: long, content: "The answer."},
		{name: "short reasoning behind an answer", reasoning: "A brief thought", content: "The answer."},
		{name: "long reasoning with nothing else to show", reasoning: long},
		{name: "a call that reasoned about nothing", content: "The answer."},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			flusher, events := captureFlusher(true)
			flusher.sendHeldReasoning(tc.reasoning, tc.content, tc.hasToolCall)

			var sent string
			for _, e := range *events {
				if e.Type == types.WSTypeReasoning {
					sent = e.Data.(map[string]string)["text"]
				}
			}
			if !tc.wantSent {
				if len(*events) != 0 {
					t.Fatalf("expected no frame, got %d: %v", len(*events), *events)
				}
				return
			}
			if sent != tc.reasoning {
				t.Errorf("held reasoning = %q, want the whole text", sent)
			}
		})
	}
}

// TestStreamFlusher_HeldReasoningIsSilentWhenReasoningStreams: an uncollapsed call sent its reasoning as it
// was written; a second copy at the end would be the same words twice.
func TestStreamFlusher_HeldReasoningIsSilentWhenReasoningStreams(t *testing.T) {
	t.Parallel()
	flusher, events := captureFlusher(false)

	flusher.appendReasoning("A brief thought")
	flusher.flush()
	flusher.sendHeldReasoning("A brief thought", "", false)

	if len(*events) != 1 || (*events)[0].Type != types.WSTypeReasoning {
		t.Fatalf("frames = %v, want the one streamed reasoning frame and no held copy", *events)
	}
}

func captureFeed(collapsed bool) (*agentReasoningFeed, *[]types.StreamEvent) {
	var events []types.StreamEvent
	feed := newAgentReasoningFeed(func(e types.StreamEvent) { events = append(events, e) }, collapsed)
	feed.interval = time.Hour // so only the test decides when the next announcement is due
	return feed, &events
}

// TestAgentReasoningFeed_ForwardsWhatItDoesNotCollapse: tool cards, tokens and errors still go out, an
// uncollapsed feed still forwards reasoning, and a collapsed one counts it once per interval.
func TestAgentReasoningFeed_ForwardsWhatItDoesNotCollapse(t *testing.T) {
	t.Parallel()

	collapsed, got := captureFeed(true)
	collapsed.forward(types.StreamEvent{Type: types.WSTypeToken, Data: map[string]string{"text": "hi"}}, "")
	collapsed.forward(types.StreamEvent{Type: types.WSTypeReasoning, Data: map[string]string{"text": "Thinking"}}, "Thinking")
	if len(*got) != 2 {
		t.Fatalf("frames = %v, want the token and one count frame", *got)
	}
	if (*got)[0].Type != types.WSTypeToken {
		t.Errorf("first frame = %v, want the token frame", (*got)[0].Type)
	}
	if (*got)[1].Type != types.WSTypeReasoningProgress || (*got)[1].Data.(map[string]any)["chars"] != 8 {
		t.Errorf("second frame = %v, want one count frame announcing the 8 characters so far", (*got)[1])
	}
	// The next thinking waits for its interval instead of putting a frame on the socket per word.
	collapsed.forward(types.StreamEvent{Type: types.WSTypeReasoning, Data: map[string]string{"text": " more"}}, " more")
	if len(*got) != 2 {
		t.Errorf("frames = %v, want no second count frame inside the interval", *got)
	}

	open, plain := captureFeed(false)
	open.forward(types.StreamEvent{Type: types.WSTypeReasoning, Data: map[string]string{"text": "Thinking"}}, "Thinking")
	if len(*plain) != 1 || (*plain)[0].Type != types.WSTypeReasoning {
		t.Fatalf("frames = %v, want the reasoning frame an uncollapsed run always sent", *plain)
	}
}

// TestAgentReasoningFeed_HeldKeepsTheAgentsOwnAnswers: an adapter's assistant row is read by the same rule
// the provider stream is.
func TestAgentReasoningFeed_HeldKeepsTheAgentsOwnAnswers(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("thought", 100)
	toolCall := pkgagent.PersistEntry{Role: "tool_call", ToolCallsJSON: `{"id":"c1","name":"bash"}`}

	cases := []struct {
		name     string
		entries  []pkgagent.PersistEntry
		wantText string
	}{
		{
			name:     "an assistant row whose only words are its reasoning",
			entries:  []pkgagent.PersistEntry{{Role: "assistant", ReasoningContent: "Planning the work"}},
			wantText: "Planning the work",
		},
		{
			name: "an assistant row that answers as well as reasons",
			entries: []pkgagent.PersistEntry{{
				Role: "assistant", Content: "Done.", ReasoningContent: long,
			}},
		},
		{
			name: "reasoning a tool call carried",
			entries: []pkgagent.PersistEntry{
				{Role: "assistant", ReasoningContent: long}, toolCall,
			},
			wantText: long,
		},
		{
			name:    "a row that reasoned about nothing",
			entries: []pkgagent.PersistEntry{{Role: "assistant", Content: "Done."}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			feed, events := captureFeed(true)
			feed.held(tc.entries)

			var sent string
			for _, e := range *events {
				if e.Type == types.WSTypeReasoning {
					sent = e.Data.(map[string]string)["text"]
				}
			}
			if sent != tc.wantText {
				t.Errorf("held reasoning = %q, want %q", sent, tc.wantText)
			}
		})
	}
}

// TestAgentReasoningFeed_HeldSaysNothingWhileReasoningStreams: an uncollapsed run already showed every
// word.
func TestAgentReasoningFeed_HeldSaysNothingWhileReasoningStreams(t *testing.T) {
	t.Parallel()
	feed, events := captureFeed(false)

	feed.held([]pkgagent.PersistEntry{{Role: "assistant", ReasoningContent: "Planning the work"}})

	if len(*events) != 0 {
		t.Fatalf("frames = %v, want none — the text already streamed", *events)
	}
}

// TestHeldReasoningFrameClosesEveryCollapsedCount: the replay buffer keeps a collapsed call's newest count
// frame for whoever subscribes next, and that frame is the only thing saying the count is over — including for
// the call whose reasoning the reader was shown, which is the case that once owed no frame at all.
func TestHeldReasoningFrameClosesEveryCollapsedCount(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("x", 300)
	cases := []struct {
		name      string
		reasoning string
		collapsed bool
		want      bool
	}{
		{name: "long reasoning", reasoning: long, collapsed: true, want: true},
		{name: "brief reasoning", reasoning: "A brief thought", collapsed: true, want: true},
		{name: "an uncollapsed run", reasoning: long, collapsed: false},
		{name: "a call that reasoned about nothing", reasoning: "", collapsed: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			frame, ok := closingReasoningFrame("row-1", tc.reasoning, tc.collapsed)
			if ok != tc.want {
				t.Fatalf("ok = %v, want %v", ok, tc.want)
			}
			if !ok {
				return
			}
			if frame.Type != types.WSTypeReasoningProgress {
				t.Errorf("frame type = %v, want %v", frame.Type, types.WSTypeReasoningProgress)
			}
			data, isMap := frame.Data.(map[string]any)
			if !isMap || data["message_id"] != "row-1" || data["chars"] != len(tc.reasoning) {
				t.Errorf("frame data = %v, want the row id and the %d characters it holds", frame.Data, len(tc.reasoning))
			}
		})
	}
}
