package exec

import (
	"testing"

	"mike/backend/types"
)

func TestTruncateForRegenerate(t *testing.T) {
	tests := []struct {
		name     string
		input    []types.ChatMessage
		expected int    // expected length after truncation
		lastRole string // role of last message after truncation
	}{
		{
			name: "removes last user message and assistant response",
			input: []types.ChatMessage{
				{Role: "system", Content: "system prompt"},
				{Role: "user", Content: "hello"},
				{Role: "assistant", Content: "hi there"},
			},
			expected: 1, // [system]
			lastRole: "system",
		},
		{
			name: "removes last user, assistant with tool calls, and tool results",
			input: []types.ChatMessage{
				{Role: "system", Content: "system prompt"},
				{Role: "user", Content: "read file"},
				{Role: "assistant", Content: "", ToolCalls: []types.ToolCallMsg{}},
				{Role: "tool", Content: "file contents"},
				{Role: "user", Content: "now summarize"},
				{Role: "assistant", Content: "here is the summary"},
			},
			expected: 4, // [system, user, assistant, tool]
			lastRole: "tool",
		},
		{
			name: "no user message - returns unchanged",
			input: []types.ChatMessage{
				{Role: "system", Content: "system prompt"},
			},
			expected: 1,
			lastRole: "system",
		},
		{
			name: "single user message - removes it",
			input: []types.ChatMessage{
				{Role: "system", Content: "system prompt"},
				{Role: "user", Content: "hey"},
			},
			expected: 1,
			lastRole: "system",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := truncateForRegenerate(tt.input)
			if len(result) != tt.expected {
				t.Errorf("expected length %d, got %d", tt.expected, len(result))
			}
			if len(result) > 0 && result[len(result)-1].Role != tt.lastRole {
				t.Errorf("expected last role %q, got %q", tt.lastRole, result[len(result)-1].Role)
			}
		})
	}
}

// TestRemoveDuplicateUserMessage covers the de-duplication that stops an echoed user
// message from being replayed to the LLM. It is deliberately conservative: only an exact
// repeat is dropped, and the NEWEST copy is the one kept (that is the turn the user just
// sent). The content-parts rows are the reason this is not a plain string compare —
// identical text with a different image, a different part count, or a plain message next to
// a ContentParts message are different user turns and must survive.
func TestRemoveDuplicateUserMessage(t *testing.T) {
	img := &types.ImageSource{Type: "base64", MediaType: "image/png", Data: "abc123"}
	parts := func(text string, withImage bool) []types.ContentPart {
		p := []types.ContentPart{{Type: "text", Text: text}}
		if withImage {
			p = append(p, types.ContentPart{Type: "image", ImageSource: img})
		}
		return p
	}

	tests := []struct {
		name     string
		input    []types.ChatMessage
		expected int
		// keptText, when set, asserts the surviving copy at keptIndex is that user message.
		keptIndex int
		keptText  string
	}{
		{
			name: "removes first of two consecutive duplicate user messages",
			input: []types.ChatMessage{
				{Role: "system", Content: "system prompt"},
				{Role: "user", Content: "hey"},
				{Role: "user", Content: "hey"},
			},
			expected: 2, keptIndex: 1, keptText: "hey",
		},
		{
			name: "keeps messages when content differs",
			input: []types.ChatMessage{
				{Role: "system", Content: "system prompt"},
				{Role: "user", Content: "hey"},
				{Role: "user", Content: "hello"},
			},
			expected: 3,
		},
		{
			name: "no duplicate - returns unchanged",
			input: []types.ChatMessage{
				{Role: "system", Content: "system prompt"},
				{Role: "user", Content: "hey"},
				{Role: "assistant", Content: "hi"},
			},
			expected: 3,
		},
		{
			name: "identical content parts are dropped",
			input: []types.ChatMessage{
				{Role: "system", Content: "system prompt"},
				{Role: "user", ContentParts: parts("check this", true)},
				{Role: "user", ContentParts: parts("check this", true)},
			},
			expected: 2,
		},
		{
			name: "same image, different text is kept",
			input: []types.ChatMessage{
				{Role: "system", Content: "system prompt"},
				{Role: "user", ContentParts: parts("check this", true)},
				{Role: "user", ContentParts: parts("check that", true)},
			},
			expected: 3,
		},
		{
			name: "one message carries an image the other does not",
			input: []types.ChatMessage{
				{Role: "system", Content: "system prompt"},
				{Role: "user", ContentParts: parts("check this", false)},
				{Role: "user", ContentParts: parts("check this", true)},
			},
			expected: 3,
		},
		{
			name: "plain text next to content parts is kept",
			input: []types.ChatMessage{
				{Role: "system", Content: "system prompt"},
				{Role: "user", Content: "hello"},
				{Role: "user", ContentParts: parts("hello", true)},
			},
			expected: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := removeDuplicateUserMessage(tt.input)
			if len(result) != tt.expected {
				t.Fatalf("kept %d of %d messages, want %d", len(result), len(tt.input), tt.expected)
			}
			if tt.keptText == "" {
				return
			}
			if msg := result[tt.keptIndex]; msg.Role != "user" || msg.Content != tt.keptText {
				t.Errorf("result[%d] = role=%q content=%q, want user %q",
					tt.keptIndex, msg.Role, msg.Content, tt.keptText)
			}
		})
	}
}

// TestRegenerateFullFlow verifies that truncateForRegenerate + append
// produces the correct context (no duplicate user messages).
func TestRegenerateFullFlow(t *testing.T) {
	// Simulate the hot path: lc.messages has the full conversation,
	// a regenerate user message arrives, we truncate then append.
	lcMessages := []types.ChatMessage{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "hey"},
		{Role: "assistant", Content: "hello! how can I help?"},
	}

	// Regenerate arrives - truncate before the last user message
	lcMessages = truncateForRegenerate(lcMessages)

	// Append the re-sent user message
	newUserMsg := types.ChatMessage{Role: "user", Content: "hey"}
	lcMessages = append(lcMessages, newUserMsg)

	// Verify: should be [system, user] - no duplicate
	if len(lcMessages) != 2 {
		t.Errorf("expected 2 messages, got %d", len(lcMessages))
	}
	if lcMessages[0].Role != "system" {
		t.Errorf("expected first message to be system, got %s", lcMessages[0].Role)
	}
	if lcMessages[1].Role != "user" || lcMessages[1].Content != "hey" {
		t.Errorf("expected second message to be user 'hey', got %+v", lcMessages[1])
	}
}
