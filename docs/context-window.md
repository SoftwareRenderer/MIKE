# The context window

A turn's request is assembled in `backend/exec/context_fit.go` and sent from the exec loop. This owns the
judgement of whether it fits: the budget, what is measured to compare against it, and what happens when the
answer is no. What a provider does with a request that arrives too large is not covered here — MIKE never
finds out, because the request was already made to fit.

## What must stay true

- **The request being sent is the thing measured.** That is what the provider counted for the conversation at
  its last call, plus every message appended since — a read appended after the last answer is exactly the
  bytes that overflow.
- **The provider's own count wins over a guess about it.** A word count prices message text and tool call
  arguments; the system prompt, the tool definitions and the provider's tokenizer are in no word count. It
  decides only where a provider reports no usage.
- **One budget, read the same way by both triggers.** The prompt ceiling is `promptBudget`: a percentage of
  the model's window, the rest left for the answer. The same Settings value drives both.
- **A crossing is answered by its cause.** A conversation already over budget without its newest tool
  results is summarized; results that do not fit are trimmed. A summary drops everything older than the
  marker, so answering a batch overrun with one would delete the read the model just asked for.
- **A tool message survives.** Every assistant `tool_call` keeps the tool message that answers it, because a
  missing one is a malformed request, which is the failure this path exists to prevent.
- **Trimming never reaches storage.** The stored tool row, the reviewer's card and the conversation in
  `loopContext` keep every byte. Only the copy a call is built from is cut, and it is re-derived per call: a
  conversation rebuilt from the database hands the full result back, and must be trimmed again.
- **A trimmed result is cut once,** so its notice reports what the tool returned rather than what an earlier
  cut left.
- **Nothing is invented about a window nobody reported.** No window means no judgement: the gate sends what
  the conversation holds.

## How it is held

`fitRequestToWindow` is the one place a request's size is judged, and the loop builds every call through it.

`promptBudget` answers the largest prompt this window accepts, and `shouldSummarize` compares against it, so
the threshold and the ceiling cannot drift apart. `estimatePromptTokens` measures the assembled request; it
is a word count, not a tokenizer, and the gate is deliberately pessimistic at the margin — cutting too much
costs the model one re-read, sending too much costs a refused request.

`classifyOverflow` splits the request with `conversationTokens`: what it would cost without the newest run of
tool results. Over budget with the batch gone means the conversation is the cause, and `summarizeNow`
compacts it. Otherwise `trimToFit` shortens results, oldest first, each to the room left, never below
`minKeptTokens`, and each keeps a notice naming the route back: `offset` and `limit` for a read, a narrowed
re-run for a command. A cut is reported to the log with its `task_id` and the count dropped.

`reportedConversation` is where the provider's count enters. It adds the answer and the results appended
since that call to what the provider counted. The room `trimToFit` is given shrinks by whatever the estimate
could not see, because trimming against the estimate alone stops too late and reaches the provider over its
limit anyway. A summarization newer than that answer prices nothing: it replaced the conversation the number
described.

A resumed run has made no call, so `conversationFromDB` carries over the count stored on the newest assistant
row. Without it the first call of every resumed run is priced by estimate alone, which is how a task that
already failed on window size fails again the moment it is retried.

Two triggers can ask for a summary in one iteration — the usage trigger `maybeAutoSummarize` and the cause
rule here — and both reach the provider through `summarizeNow`, which holds the one rule that keeps that
from costing two summaries: `loopContext.summarizedAtLen` is the conversation length a summarization left
behind, and nothing summarizes again until it grows.

## The window nobody reported

A provider that reports no window, and answers no discovery request, leaves `contextWindow` nil. Then neither
trigger fires and a run that outgrows the model is refused by the provider and lands in Review as
`llm_request_rejected`. That is the accepted cost of inventing nothing: a guessed window would trim output
the model needed, on a number nothing told us.

## Checking this

`go test ./backend/exec/ -run 'TestPromptBudget|TestTrimToFit|TestFitRequest|TestGate|TestRunTrims'` —
`TestRunTrimsAnOversizedReadBeforeTheProviderSeesIt` runs a task end to end and judges the bytes the provider
was handed. `TestRunTrimsWhatTheReportedUsagePutOverBudget` runs one against a provider that reported a usage
leaving no room at all. `TestFitRequestPricesAResumedConversationFromTheStoredCount` covers the resumed run,
which has no live usage to price by. A gate that stopped being wired, or stopped believing the provider, fails
there rather than quietly.
