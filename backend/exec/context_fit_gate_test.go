package exec

import (
	"strings"
	"testing"

	"mike/backend/types"
	"mike/pkg/logging"
)

func TestClassifyOverflow(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		request int
		history int
		budget  int
		want    overflowCause
	}{
		{name: "inside the budget", request: 1000, history: 900, budget: 2000, want: causeFits},
		{name: "no window reported", request: 100000, history: 900, budget: 0, want: causeFits},
		{name: "the batch is what does not fit", request: 3000, history: 1500, budget: 2000, want: causeToolOutput},
		{name: "the conversation alone is over", request: 5000, history: 2500, budget: 2000, want: causeHistory},
	}
	for _, tc := range cases {
		if got := classifyOverflow(tc.request, tc.history, tc.budget); got != tc.want {
			t.Errorf("%s: cause = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestToolBatchTokens(t *testing.T) {
	t.Parallel()
	conv := convWith(
		trimRow{id: "c1", name: types.ToolReadFile, output: filler(1000, "aa")},
		trimRow{id: "c2", name: types.ToolReadFile, output: filler(1000, "bb")},
	)
	if got, want := toolBatchTokens(conv), estimatePromptTokens(conv[3:]); got != want {
		t.Errorf("batch = %d tokens, want the two results at %d", got, want)
	}
	plain := []types.ChatMessage{{Role: "system", Content: "prompt"}, {Role: "user", Content: "hello"}}
	if got := toolBatchTokens(plain); got != 0 {
		t.Errorf("a conversation with no tool results has a batch of %d", got)
	}
	// Both gates measure the conversation the same way: the request without its newest batch.
	if got, want := conversationTokens(conv), estimatePromptTokens(conv[:3]); got != want {
		t.Errorf("conversation = %d tokens, want the request without its batch at %d", got, want)
	}
}

// gateSetup is summarizeSetup plus what a must-fit test needs: a context window, and a usage far under it,
// so any summary these tests see came from the gate and not from the usage trigger.
func gateSetup(t *testing.T, window int, summaries ...string) (*TaskExecutor, *taskExecSetup, *loopContext) {
	t.Helper()
	te, setup, lc, _ := summarizeSetup(t, summaries...)
	cw := window
	lc.contextWindow = &cw
	lc.usage = &types.ChatUsage{PromptTokens: 10}
	return te, setup, lc
}

func summarizationRows(t *testing.T, te *TaskExecutor, taskID, threadID string) int {
	n := 0
	for _, role := range rolesInOrder(t, te, taskID, threadID) {
		if role == "summarization" {
			n++
		}
	}
	return n
}

func TestFitRequestSendsAFittingConversationUnchanged(t *testing.T) {
	t.Parallel()
	te, setup, lc := gateSetup(t, 8192)
	lc.messages = []types.ChatMessage{
		{Role: "system", Content: "the prompt"},
		{Role: "user", Content: "hello"},
	}

	sent := te.fitRequestToWindow(setup, lc, "test-model")

	if len(sent) != 2 || sent[1].Content != "hello" {
		t.Errorf("a request inside its budget was rewritten: %+v", sent)
	}
}

func TestFitRequestWithoutAWindowJudgesNothing(t *testing.T) {
	t.Parallel()
	te, setup, lc := gateSetup(t, 8192)
	lc.contextWindow = nil
	lc.messages = convWith(trimRow{id: "c1", name: types.ToolReadFile, output: filler(8000, "aa")})

	sent := te.fitRequestToWindow(setup, lc, "test-model")

	if sent[3].Content != lc.messages[3].Content {
		t.Error("an unreported window must send what the conversation holds")
	}
}

// The batch is the cause here, so summarizing would drop history to clear room for output nobody read.
func TestFitRequestTrimsTheBatchAndKeepsTheConversation(t *testing.T) {
	t.Parallel()
	te, setup, lc := gateSetup(t, 4000)
	conv := convWith(trimRow{id: "c1", name: types.ToolReadFile, output: filler(8000, "aa")})
	lc.messages = conv

	sent := te.fitRequestToWindow(setup, lc, "test-model")

	budget := promptBudget(4000, defaultSummarizeThreshold)
	if over := estimatePromptTokens(sent) - budget; over > 0 {
		t.Errorf("the request sent is %d tokens over budget", over)
	}
	if !strings.Contains(sent[3].Content, "trimmed to") {
		t.Error("the oversized result was sent whole")
	}
	if lc.messages[3].Content != conv[3].Content {
		t.Error("the trim was written back into the conversation; the stored row keeps every byte")
	}
	if n := len(rolesInOrder(t, te, setup.task.ID, "thread-1")); n != 0 {
		t.Errorf("%d rows were written for a run that only trimmed", n)
	}
}

// A provider that reports nothing leaves the estimate as the only measure, and the batch is still cut.
func TestFitRequestWithoutReportedUsageStillTrimsTheBatch(t *testing.T) {
	t.Parallel()
	te, setup, lc := gateSetup(t, 4000)
	lc.usage = nil
	lc.messages = convWith(trimRow{id: "c1", name: types.ToolReadFile, output: filler(8000, "aa")})

	sent := te.fitRequestToWindow(setup, lc, "test-model")

	if !strings.Contains(sent[3].Content, "trimmed to") {
		t.Error("an oversized result was sent whole because nothing reported its size")
	}
}

// A resumed run rebuilds its conversation from stored rows and has made no call yet, so the count stored on
// the newest assistant row is the only real measure of what that conversation costs.
func TestFitRequestPricesAResumedConversationFromTheStoredCount(t *testing.T) {
	t.Parallel()
	te, setup, lc := gateSetup(t, 4000)
	lc.usage = nil
	lc.reportedPrompt = promptBudget(4000, defaultSummarizeThreshold) / 2
	lc.messages = convWith(trimRow{id: "c1", name: types.ToolReadFile, output: filler(3000, "aa")})

	sent := te.fitRequestToWindow(setup, lc, "test-model")

	if !strings.Contains(sent[3].Content, "trimmed to") {
		t.Error("the resumed run priced its conversation by estimate alone and sent the oversized result whole")
	}
	if n := summarizationRows(t, te, setup.task.ID, "thread-1"); n != 0 {
		t.Errorf("%d summarization rows, want 0", n)
	}
}

// The shape of the failure this path exists to stop: 150,299 tokens of conversation already counted against
// a 262,144 window, and one tool result the word estimate cannot price correctly.
func TestFitRequestTrimsTheOversizedResultOfAWindowFullConversation(t *testing.T) {
	t.Parallel()
	const window = 262144
	te, setup, lc := gateSetup(t, window)
	lc.usage = &types.ChatUsage{PromptTokens: 150299}
	lc.messages = convWith(trimRow{id: "c1", name: types.ToolBash, output: filler(102177, "aa")})

	sent := te.fitRequestToWindow(setup, lc, "test-model")

	if !strings.Contains(sent[3].Content, "trimmed to") {
		t.Fatal("the run was handed the whole result, which the provider refuses")
	}
	budget := promptBudget(window, defaultSummarizeThreshold)
	extra := reportedConversation(lc, lc.messages) - conversationTokens(buildLLMContext(lc.messages))
	if over := conversationTokens(sent) + extra + toolBatchTokens(sent) - budget; over > 0 {
		t.Errorf("the request is %d tokens over the budget the provider priced", over)
	}
}

func TestFitRequestSummarizesWhenTheConversationIsTheCause(t *testing.T) {
	chdirRepoRoot(t)
	te, setup, lc := gateSetup(t, 1000, "SUMMARY-GATE")
	lc.messages = append([]types.ChatMessage{
		{Role: "system", Content: "the prompt"},
		{Role: "user", Content: filler(3000, "aa")},
	}, convWith(trimRow{id: "c1", name: types.ToolBash, output: "listed"})[2:]...)

	sent := te.fitRequestToWindow(setup, lc, "test-model")

	if !strings.Contains(sent[0].Content, "SUMMARY-GATE") {
		t.Error("a conversation over its budget was sent without a summary")
	}
	if got := summarizationRows(t, te, setup.task.ID, "thread-1"); got != 1 {
		t.Errorf("%d summarization rows, want 1", got)
	}
	if over := estimatePromptTokens(sent) - promptBudget(1000, defaultSummarizeThreshold); over > 0 {
		t.Errorf("the request sent is %d tokens over budget", over)
	}
}

// The provider counted this conversation larger than a word count can.
func TestFitRequestTrimsWhatTheProviderPricedOverBudget(t *testing.T) {
	t.Parallel()
	te, setup, lc := gateSetup(t, 4000)
	lc.messages = convWith(trimRow{id: "c1", name: types.ToolReadFile, output: filler(3000, "aa")})
	budget := promptBudget(4000, defaultSummarizeThreshold)
	lc.usage = &types.ChatUsage{PromptTokens: budget / 2}

	sent := te.fitRequestToWindow(setup, lc, "test-model")

	if !strings.Contains(sent[3].Content, "trimmed to") {
		t.Error("the estimate alone said the request fitted, so the oversized result was sent whole")
	}
	if n := summarizationRows(t, te, setup.task.ID, "thread-1"); n != 0 {
		t.Errorf("%d summarization rows, want 0: the conversation is inside what the provider priced", n)
	}
	extra := reportedConversation(lc, lc.messages) - conversationTokens(buildLLMContext(lc.messages))
	if over := conversationTokens(sent) + extra + toolBatchTokens(sent) - budget; over > 0 {
		t.Errorf("the request sent is %d tokens over what the provider priced as the budget", over)
	}
}

// A summarization replaced the conversation that the reported count describes.
func TestFitRequestIgnoresAReportedCountOlderThanTheSummary(t *testing.T) {
	t.Parallel()
	te, setup, lc := gateSetup(t, 1000)
	lc.messages = []types.ChatMessage{
		{Role: "system", Content: "the prompt"},
		{Role: "assistant", Content: "the answer it priced"},
		{Role: "summarization", Content: "what replaced all of it"},
		{Role: "user", Content: "hello"},
	}
	lc.usage = &types.ChatUsage{PromptTokens: 50000}

	if got := reportedConversation(lc, lc.messages); got != 0 {
		t.Errorf("reported conversation = %d tokens, want 0: that number prices rows that are gone", got)
	}
	sent := te.fitRequestToWindow(setup, lc, "test-model")
	if sent[len(sent)-1].Content != "hello" {
		t.Error("a request inside its budget was rewritten")
	}
}

// The usage trigger runs just before this gate, and one conversation state is answered once.
func TestFitRequestDoesNotSummarizeWhatWasJustSummarized(t *testing.T) {
	chdirRepoRoot(t)
	te, setup, lc := gateSetup(t, 1000, "SUMMARY-GATE")
	lc.messages = []types.ChatMessage{
		{Role: "system", Content: "the prompt"},
		{Role: "user", Content: filler(3000, "aa")},
	}
	lc.summarizedAtLen = len(lc.messages)

	te.fitRequestToWindow(setup, lc, "test-model")

	if got := summarizationRows(t, te, setup.task.ID, "thread-1"); got != 0 {
		t.Errorf("%d summarization rows, want 0: the conversation has not grown since the last one", got)
	}
}

// This is the log line the operator sees, not the notice the model is sent. Not t.Parallel: TestSink
// redirects the whole process's log.
func TestGateReportsTheCutToTheLog(t *testing.T) {
	buf, restore := logging.TestSink()
	defer restore()

	te, setup, lc := gateSetup(t, 4000)
	lc.messages = convWith(trimRow{id: "c1", name: types.ToolReadFile, output: filler(8000, "aa")})
	te.fitRequestToWindow(setup, lc, "test-model")

	got := buf.String()
	if !strings.Contains(got, "component=exec") || !strings.Contains(got, "task_id="+setup.task.ID) {
		t.Errorf("the report must name the component and the task, got %q", got)
	}
	if !strings.Contains(got, "read_file") || !strings.Contains(got, "dropping ") {
		t.Errorf("the report must name the tool and what it dropped, got %q", got)
	}
}

func TestGateStaysSilentWhenTheRequestFits(t *testing.T) {
	buf, restore := logging.TestSink()
	defer restore()

	te, setup, lc := gateSetup(t, 32768)
	lc.messages = convWith(trimRow{id: "c1", name: types.ToolReadFile, output: "one\ntwo\n"})
	te.fitRequestToWindow(setup, lc, "test-model")

	if strings.Contains(buf.String(), setup.task.ID) {
		t.Errorf("a request that fitted was reported anyway: %q", buf.String())
	}
}

// A conversation that has not grown since a summarization is not worth a second one, however far over the
// threshold its usage was.
func TestUsageTriggerSummarizesOncePerConversationState(t *testing.T) {
	chdirRepoRoot(t)
	te, setup, lc := gateSetup(t, 1000, "SUMMARY-ONE", "SUMMARY-TWO")
	lc.usage = &types.ChatUsage{PromptTokens: 950} // over the 900 budget, and stays over
	lc.messages = []types.ChatMessage{
		{Role: "system", Content: "the prompt"},
		{Role: "user", Content: filler(3000, "aa")},
	}

	te.summarize.maybeAutoSummarize(setup, lc, "test-model", true)
	te.summarize.maybeAutoSummarize(setup, lc, "test-model", true)

	if got := summarizationRows(t, te, setup.task.ID, "thread-1"); got != 1 {
		t.Errorf("%d summarization rows, want 1", got)
	}
}
