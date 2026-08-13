# TODO

## Goal
Automated answer system: when an **auto-started** task (started from a parent) calls `ask_question`, answer it automatically using the **parent's prompt context** instead of stopping and waiting for a human. The subtask then continues autonomously, and the Q&A is surfaced intuitively in the chat.

## Decisions (from user)
1. LLM endpoint: the **parent's** `LlamaServerURL`/`ModelName` (fallback → subtask's → configured Utility pair).
2. Subtask **continues autonomously** after the answer is injected.
3. Context: **only the parent's prompt** (plus parent title + subtask title for readability).
4. **No limit** on auto-answered questions per run.
5. Visibility: yes — surface the automated Q&A in the chat UI.

## Key design points
- `setup.task.Autostarted` (already persisted) gates the branch at `backend/exec/loop.go:1255`.
- A single turn may contain **multiple `ask_question` calls**; they are **consolidated** into ONE automated-answer request so all questions are answered at once (one LLM call). Answers are aligned back to their tool calls by order.
- Each answer is injected into the matching `toolResults[i].output` so the loop `continue`s normally with the answers as the ask_question tool results; each persisted tool_call message and a re-broadcast `WSTypeToolResult` are updated so the UI shows them live and after refresh.
- If auto-answering fails (no parent / LLM error / unparseable consolidated response), fall back to the existing stop-and-wait behavior.
- **Critical fix:** `RenderSystemPrompt`/`ValidateTemplates` glob all `*.tmpl` and `tmpl.Execute` renders the alphabetically-first file as root. Adding `answer_question.tmpl` (sorts before `base.tmpl`) would hijack the system prompt. Fix: render the root template explicitly via `ExecuteTemplate(&buf, "base.tmpl", data)`. (Verified: ParseFiles names each file by basename, so `"base.tmpl"` is the stable name.)

## Tasks

### 1. Prompt template + renderer (`prompts/`, `backend/prompts/prompts.go`)
- [x] Create `prompts/answer_question.tmpl`: system prompt for the automated answerer — act as the parent task, answer the subtask's question(s) using the parent's prompt context. Fields: `ParentTitle`, `ParentPrompt`, `SubtaskTitle`, `Questions` (all questions in this turn). Instructs the model to answer **each question in order in a single line**, in the simple format `1) answer to first question 2) answer to second...`.
- [x] Fix `RenderSystemPrompt` and `ValidateTemplates` to render the root template via `tmpl.ExecuteTemplate(&buf, "base.tmpl", data)` instead of `tmpl.Execute` (prevents the new file from hijacking the system prompt).
- [x] Add `AnswerQuestionData` struct (with `Questions []string`) and `RenderAnswerQuestionPrompt(data)` in `backend/prompts/prompts.go` (mirrors `RenderSummarizePrompt`).

### 2. Executor automated-answer helper (`backend/exec/executor.go`)
- [x] Add `automatedAnswers(ctx, subtask *types.Task, questions []string) ([]string, error)`:
  - Load parent via `store.GetTaskByTaskID(subtask.ParentTaskID)`; return error if no parent / not found.
  - Render `answer_question.tmpl` with parent title+prompt, subtask title, and all `questions`.
  - Resolve endpoint: parent's `LlamaServerURL`/`ModelName` → subtask's → `ResolveUtilityTarget(subtask)`.
  - Call `te.ChatCompletion(ctx, serverURL, model, [system], nil)` (the questions are embedded in the rendered prompt).
  - Split the single response on the numbered pattern (`N)` or `N.`) to extract answers aligned to `questions` by index. Return an error if the response is empty or cannot be parsed into the expected number of answers — caller falls back to stop-and-wait.

### 3. Loop wiring (`backend/exec/loop.go`)
- [x] Extend the local `toolResult` struct with `messageID`, `args`, `rawArgs`, `success`, `duration`; populate it at append time (using `toolCallMsgID`, parsed args, `raw_args`, `success`, `toolDuration`).
- [x] In the `ask_question` block: if `setup.task.Autostarted`, **gather ALL ask_question calls in this turn** into a `[]string` of questions, call `te.automatedAnswers` ONCE to get all answers, then for each ask_question call set the matching `toolResults[i].output = answer`, persist it (`UpdateTaskMessageToolResult(messageID, {output: answer, success: true, duration})`), and re-broadcast `WSTypeToolResult` (same tool_id) with the answer; then **do not stop** — fall through so the loop appends the tool results and `continue`s.
- [x] Keep the existing stop-and-wait path for non-autostarted tasks and for autostarted tasks whose auto-answer fails (log + `return`).

### 4. Frontend visibility (`frontend/src/lib/components/Chat/ToolCall.svelte`)
- [x] For `ask_question` with an auto-answer (`tc.result` present and `!= tc.args.question`), render an "Automated answer (parent context)" block below the question (markdown), plus scoped CSS for the label/box.

### 5. Tests
- [x] `backend/prompts/prompts_test.go`: `TestRenderAnswerQuestionPrompt` (output contains parent prompt/title) and a regression test that `RenderSystemPrompt` still renders the base system prompt now that `answer_question.tmpl` exists.
- [x] `backend/exec/`: `TestAutomatedAnswers` with an httptest mock LLM (like `mockSummarizeLLM`) — a single question returns one answer, and **multiple questions return answers aligned by index**; a no-parent case returns an error.

### 6. Build & verify
- [x] `gofmt -w` on edited Go files.
- [x] `go build ./...` succeeds.
- [x] `go test ./backend/... -count=1 -parallel 8` passes (excluding the two pre-existing `backend/git` failures).
- [x] `cd frontend && npm run check` and `npm run build` succeed.

### 7. Docs
- [x] Add a brief note in `AGENTS.md` describing the automated answer prompt (`answer_question.tmpl`) and that auto-started subtasks route `ask_question` to it.

## Notes
- No changes to the `Autostarted` flag or its semantics from the previous feature.
- Answers are fed to the model as the ask_question tool results, so the model sees both each question (in the tool call args) and its answer, and continues working.
- Multiple ask_question calls in one turn are consolidated into a single LLM call; the model returns answers in the simple `1) ... 2) ...` format, split back to tool calls by index.
- Edge case: a root task started directly when all children are done can have `Autostarted=true` but no parent — auto-answer falls back to stop-and-wait there.
