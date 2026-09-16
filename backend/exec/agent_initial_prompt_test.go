package exec

import (
	"strings"
	"testing"

	"mike/backend/types"
)

// The rule this holds is the one an agent cannot tell you it got wrong: what its first message is. A
// resumed session already contains the conversation, so sending the transcript again pays for it twice;
// a fresh subprocess with no session has nothing, so the transcript is the only memory it gets.
func TestAgentInitialPromptSendsEachThingOnce(t *testing.T) {
	const prompt = "the task prompt"
	const userMsg = "the message the user just sent"

	for _, tc := range []struct {
		name    string
		msgs    []types.TaskMessage
		initial string
		resumed bool
		want    string
		// forbidden, if set, must not appear in the prompt at all
		not string
		// once, if set, must appear exactly once in the prompt
		once string
	}{
		{
			name: "fresh task", msgs: nil, initial: "", resumed: false, want: prompt,
		},
		{
			name:    "lazy start on a resumed session sends only the new message",
			msgs:    []types.TaskMessage{{Role: "user", Content: prompt}, {Role: "user", Content: userMsg}},
			initial: userMsg, resumed: true, want: userMsg,
			not: "conversation_history",
		},
		{
			name:    "lazy start without a session replays the older rows and the new message",
			msgs:    []types.TaskMessage{{Role: "user", Content: prompt}, {Role: "assistant", Content: "a reply"}, {Role: "user", Content: userMsg}},
			initial: userMsg, resumed: false, want: userMsg,
			// The lazy start stored the user's message before the agent was started, so the
			// row and the new message are the same words: the replay must stop before them.
			once: userMsg,
		},
		{
			name:    "restart on a resumed session sends the task prompt",
			msgs:    []types.TaskMessage{{Role: "user", Content: prompt}, {Role: "assistant", Content: "a reply"}},
			initial: "", resumed: true, want: prompt,
			not: "conversation_history",
		},
		{
			name:    "restart without a session replays the stored conversation",
			msgs:    []types.TaskMessage{{Role: "user", Content: prompt}, {Role: "assistant", Content: "a reply"}},
			initial: "", resumed: false, want: "<conversation_history>",
		},
		{
			name:    "a task with one stored message starts from its prompt",
			msgs:    []types.TaskMessage{{Role: "user", Content: prompt}},
			initial: "", resumed: false, want: prompt,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task := &types.Task{Prompt: prompt}
			got := agentInitialPrompt(task, tc.msgs, tc.initial, tc.resumed)

			if !strings.Contains(got, tc.want) {
				t.Errorf("prompt = %q, want it to contain %q", got, tc.want)
			}
			if tc.not != "" && strings.Contains(got, tc.not) {
				t.Errorf("prompt must not contain %q: %q", tc.not, got)
			}
			if n := strings.Count(got, tc.once); tc.once != "" && n != 1 {
				t.Errorf("the new message appears %d times in the prompt, want exactly 1:\n%s", n, got)
			}
			if got == "" {
				t.Error("an empty initial prompt starts an agent with nothing to do")
			}
		})
	}
}
