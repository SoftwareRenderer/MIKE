# Auto-Answer

Read this before touching `automatedAnswers` (`backend/exec/executor.go`), the `ask_question` handling or
the text-only stop in `finalizeTurn` (`backend/exec/loop.go`), or `autoAnswerTextStop`
(`backend/exec/loop_helpers.go`).

A subtask runs on its parent's behalf, so `ask_question` has nobody to answer it. Auto-answer asks the
parent's context instead and lets the run continue. Two stop shapes are answered, and nothing else is.

## Eligibility: what `Autostarted` means

`Autostarted` (`types.Task`, column `tasks.autostarted`, `json:"-"` so the browser never sees it) is
**per-run provenance**, rewritten by `StartTask` on every start. It answers "did a click name this
task?", not "did a human want this":

| The run began when… | `Autostarted` |
|---|---|
| Start, retry or drag on **this** task (`HandleStart`, `HandleStartSubtask`, the `HandleMove` branch that starts one, a message forwarded by `ForwardUserMessage`) | false |
| Start or drag on its **parent**, or on a task whose subtasks run instead — the same three handlers reach `StartReadySubtasks`, which starts what `subtasksToStart` picks | true |
| Sibling continuation after another task finished, or the parent's own start once all children are done (`AutoStartNextSibling`) | true |
| The memory supervisor resumed it after a memory pause (`resumeProvenance`) | whatever the paused run recorded |

`subtasksToStart` is that pick: every startable descendant under the **Run subtasks in parallel** setting, and
the single deepest one without it.

The supervisor row is deliberate: it picks its victim by memory cost, which says nothing about who is
waiting on the task, so `resumeProvenance` hands the resume the answer the paused run already gave.
Without that, a subtask its user started by name would come back from a pause answering for them.

So starting a parent opts its whole subtree into answering itself, and a task the user starts by name
never gets answered. One gap in that last clause: a message arriving mid-run does not clear the flag,
and neither does the lazy start a message triggers re-declare it, so a chain-started subtask a person
has since been typing into keeps answering itself.

Both paths additionally require a parent to answer from (no parent ⇒ decline) and are off in plan and
explore mode, where a text answer is the deliverable.

## The answering call

One non-streaming completion, no tools: `prompts/answer_question.tmpl` carries the parent's title and
prompt, `answer_question_user.tmpl` the questions, in order. The endpoint is the **parent's** provider
and model, falling back to the subtask's and then to the Utility pair (`utilityTarget`).

The model answers `"1) … 2) …"` on one line and `splitNumberedAnswers` accepts only markers numbered in
the expected position, skipping decimals (`Go 1.21`). Out-of-order numbering, too few segments, or an
empty response is an error, not a guess — and an error means the question goes to the user.

## Shape 1: `ask_question`

The question is already a tool result, so the answer replaces it: the `toolResult` output is
overwritten, the row is rewritten (`UpdateTaskMessageToolResult`), and a second `tool_result` frame
re-broadcasts it so the card fills in. `ToolCall.svelte` recognises an answered question as *result ≠
question* and titles the block "Automated answer (parent context)". A decline leaves the question as
the result and the task moves to review — which is also what clears a pending commit-and-done click.

## Shape 2: a turn that ends with only a message

`finalizeTurn`'s text-only branch settles the turn first (review, slot released, `complete` frame),
runs auto-summarization, and takes any queued user message. Only then does `autoAnswerTextStop` run, so
**a real message always beats an automated answer**, and the board shows review for the length of the
answering call. On success it:

- writes the answer as an **advisory** row (`kind: "auto_answer"`, `flushAdvisories`) — never a user
  row, which would fake a user bubble and make the summarizer believe the user spoke.
  `toAPIMessage` maps advisory→user, so the model still reads it as a reply, and the row survives a
  restart. The card is titled by `advisoryLabel`.
- reclaims the running-task slot (`syncTaskSlot`), claims the run again (`persistTaskClaiming`),
  broadcasts `status/running`, and returns `turnNextLLMCall`.

It returns `turnWaitIdle` (the zero value) to leave the settled turn alone and `turnStop` only when the
slot wait was cancelled. The call site sits **before** `AutoStartNextSibling`: a run that keeps working
has not finished its branch.

## What it does not do

The answering model sees the parent's title and prompt and nothing else — not the subtask's history,
not its flags — and has no tool. Every merge decision stays where it is read: `auto_commit` and
`auto_merge` are consulted only inside the `request_review` handling, which auto-answer never reaches,
and `ArmCommitAndDone` is still the only thing that authorizes a merge. A subtask with `auto_merge=false`
therefore gets answered, runs on to `request_review`, commits its own branch and waits in review with
the parent's branch untouched.

Nothing caps the number of answers per run. The bounds are the loop detector (`maxLoopHints` fails the
task) and cancellation; a failed merge or an unresolved conflict still waits for a person.

## Tests

`backend/exec/auto_answer_test.go` — the answering call, the answer grammar, and which `ask_question`
calls count as questions. `backend/exec/auto_answer_text_stop_test.go` — the text-only stop: continued
and answered, unanswered when user-started or in plan mode, and answered without reaching the merge.
`chat-entries.unit.ts` titles the card. `memory_supervisor_test.go` pins that a resume keeps the
provenance the paused run recorded. Both executor specs drive a real run, so `make check-race`
covers them.
