# Auto-Answer

Use this guide before modifying `automatedAnswers` (`backend/exec/executor.go`), `ask_question` handling, the text-only stop in `finalizeTurn` (`backend/exec/loop.go`), or `autoAnswerTextStop` (`backend/exec/loop_helpers.go`).

Subtasks run on behalf of their parent. When a subtask calls `ask_question`, the system uses the parent's context to generate an answer and continue the run. Only two specific stop conditions are auto-answered.

## Eligibility: The `Autostarted` Flag

`Autostarted` (column `tasks.autostarted`) tracks a task's origin for the current run. It is reset by `StartTask` on every start. It distinguishes whether a task was started by a direct user action or automatically by the system:

| The run began when… | `Autostarted` |
|---|---|
| Start, retry or drag on **this** task (the three routes that start one, all through `startUnderSchedule`; a message forwarded by `ForwardUserMessage`) | false |
| Start or drag on its **parent**, or on a task whose subtasks run instead — every handler and route that starts a branch reaches `StartReadySubtasks`, which starts what `subtasksToStart` picks | true |
| Sibling continuation after another task finished, or the parent's own start once all children are done (`AutoStartNextSibling`) | true |
| The memory supervisor resumed it after a memory pause (`resumeProvenance`) | whatever the paused run recorded |

`subtasksToStart` is that pick: every startable descendant under the **Run subtasks in parallel** setting, and
the single deepest one without it.

The memory supervisor picks tasks to resume based on memory cost, not origin. Therefore, `resumeProvenance` preserves the `Autostarted` status from the paused run. This prevents a task started by a user from being auto-answered after a memory pause.

Starting a parent automatically enables auto-answering for its entire subtree. Tasks started directly by a user are never auto-answered. Note: if a user sends a message to a subtask that was already started automatically, the `Autostarted` flag is not cleared. The task continues to be auto-answered.

Auto-answering requires a parent task and is disabled in `plan` and `explore` modes, where the output is the final deliverable.

## The Answering Process

The system makes a non-streaming LLM call without tools. It uses `prompts/answer_question.tmpl` (parent's title and prompt) and `answer_question_user.tmpl` (the questions). The call uses the parent's provider and model, falling back to the subtask's, then to the Utility pair (`utilityTarget`).

The model must answer with numbered options (e.g., `1) … 2) …`). `splitNumberedAnswers` validates these markers; decimals are ignored. Any numbering error, missing segments, or an empty response is treated as a failure, and the question is forwarded to the user.

## Shape 1: `ask_question`

The answer replaces the original `ask_question` tool result: the `toolResult` output is overwritten, the database row is updated via `UpdateTaskMessageToolResult`, and a new `tool_result` frame is broadcast. `ToolCall.svelte` identifies these as automated answers. If auto-answering is declined, the task moves to review.

## Shape 2: a turn that ends with only a message

`finalizeTurn` first settles the turn (marking it as review, releasing the slot, and sending a `complete` frame), runs auto-summarization, and processes any queued user messages. `autoAnswerTextStop` runs last, ensuring that a real user message always takes precedence over an automated answer. On success, it:

- writes the answer as an **advisory** row (`kind: "auto_answer"`, `flushAdvisories`). This prevents the summarizer from mistaking the automated answer for a user message. `toAPIMessage` treats advisories as user messages for the model's context, and the row is persisted. The UI uses `advisoryLabel` for the card title.
- reclaims the running-task slot (`syncTaskSlot`), re-establishes the run claim (`persistTaskClaiming`), broadcasts `status/running`, and triggers the next LLM call (`turnNextLLMCall`).

It returns `turnWaitIdle` to maintain the settled turn, or `turnStop` if the slot wait was cancelled. This happens before `AutoStartNextSibling`, as a continuing run has not yet finished its branch.

## Limitations

The answering model only sees the parent's title and prompt; it has no access to the subtask's history, flags, or tools. Merge decisions are handled separately: `auto_commit` and `auto_merge` are only used during `request_review`, which auto-answer does not trigger. `ArmCommitAndDone` remains the only way to authorize a merge. For example, a subtask with `auto_merge=false` will be auto-answered, commit its own branch, and wait in review without affecting the parent's branch.

Nothing caps the number of answers per run. The bounds are the loop detector (`maxLoopHints` fails the task) and cancellation; a failed merge or an unresolved conflict still waits for a person.

## Tests

`backend/exec/auto_answer_test.go` — the answering call, the answer grammar, and which `ask_question` calls count as questions. `backend/exec/auto_answer_text_stop_test.go` — the text-only stop: continued and answered, unanswered when user-started or in plan mode, and answered without reaching the merge. `chat-entries.unit.ts` titles the card. `memory_supervisor_test.go` pins that a resume keeps the provenance the paused run recorded. Both executor specs drive a real run, so `make check-race` covers them.
