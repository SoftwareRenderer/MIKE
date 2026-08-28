# TODO

## Goal

Make token/timing stats work against vLLM providers (and keep llama.cpp correct) by fixing six client-protocol bugs found while probing `saturn:8100`, `saturn:8101` (vLLM) and `neptune:8010` (llama.cpp).

## Scope

**In:** #1 `stream_options`, #2 `delta.reasoning`, #3 vLLM `metrics` timings, #4 empty-content hard error, #5 context window via llama-swap, #6 stale live stats.
**Out (per user):** #7 `reasoning_effort: high` → 400 on vLLM.
**Out (config-resolved):** #8 `prompt_tokens_details: null` — vLLM now emits `cached_tokens` (verified: 4000/4060 on warm prefix cache), so our existing `prompt_tokens - cache_tokens` math is already correct. No code change.

## Tasks

### 1. Reasoning field normalization (#2)

- [x] Add `Reasoning *string` (json tag `reasoning,omitempty`) to `ChatChoiceDelta` (`backend/types/llm.go:65`).
- [x] Add `Reasoning *string` (json tag `reasoning,omitempty`) to `ChatChoiceMessage` (`backend/types/llm.go:72`).
- [x] Add pure helper `normalizeReasoning(*ChatChoice)` in `backend/llama/`: when `Delta.ReasoningContent` is nil-or-empty and `Delta.Reasoning` is non-empty, copy `Reasoning` into `ReasoningContent`. **Prefer, never concatenate** — when both are present, `reasoning_content` wins (some proxies send both; concatenating would double the text).
- [x] Call the helper in `ChatStream`'s per-frame loop before `hasContent(choice)`/`cb(choice)` (`backend/llama/client.go:317-322`) so `firstTokenTime` and all downstream consumers see the populated field.
- [x] Call the helper for the non-streaming decoded `result.Choices[0]` (`backend/llama/client.go:410-419`).
- [x] Verify no `backend/exec` change is needed: `hasContent` (`client.go:465`) and `tokenCb` (`loop.go:741`) read `Delta.ReasoningContent` and are fixed by the parse-boundary normalization. *(grep for `Delta.Reasoning`/`Message.Reasoning` outside tests: no hits.)*
- [x] Tests in `backend/llama/client_test.go`: vLLM-shaped SSE (`delta.reasoning` only) → reasoning surfaced via callback AND `PromptMs`/`PredictedMs` populated by the wall-clock fallback (proves `firstTokenTime` was set); both-fields-present → no duplication.
  - Also extended `ChatChoiceMessage`, which previously discarded non-streaming reasoning entirely (llama.cpp sends `message.reasoning_content`, vLLM `message.reasoning`).
  - **Regression-proof:** with the fix reverted, `TestChatStream_ReasoningFieldVariants/vLLM_reasoning` fails on both assertions (`reasoning = ""` and `prompt_ms = 0`); the llama.cpp variant passes either way, confirming the test targets the real bug.

### 2. Request `stream_options` (#1)

- [x] Add `"stream_options": {"include_usage": true}` to the `ChatStream` request body (`backend/llama/client.go:211-216`). Unconditional, no 400-retry safety net (decision 1B).
- [x] Test: assert the key is present in the outbound body, mirroring `TestChatStream_ReasoningParams` (`client_test.go:603`).
  - Extra characterization test added: `TestChatStream_UsageFrameWithEmptyChoices` — vLLM's usage frame shape (`"choices":[]` + `usage` + `metrics`) is consumed without invoking the token callback; this is also the shape constraint behind Task 6's callback design.

### 3. vLLM `metrics` → timings (#3)

- [x] Add `Metrics *VllmMetrics` (json tag `metrics,omitempty`) to `ChatResponse` (`backend/types/llm.go:116`) with `TimeToFirstTokenMs`, `GenerationTimeMs`, `QueueTimeMs`, `MeanItlMs`, `TokensPerSecond` (float64).
- [x] In `ChatStream`'s final-usage resolution: when `Timings` is absent and `Metrics` present, set `usage.PromptMs = ttft_ms` and `usage.PredictedMs = generation_time_ms`. Provider `timings` keep priority; wall-clock fallback stays last.
- [x] Same for the non-streaming path (`backend/llama/client.go:426-456`), before the token-weight split heuristic.
- [x] Test: vLLM-shaped final frame with `metrics` → exact `PromptMs`/`PredictedMs`; llama.cpp frame with both `timings` and `metrics` → `timings` wins.
  - Implemented via `== 0` guards rather than an explicit "Timings absent" branch, so a server sending both keeps `timings` priority and wall-clock stays last. Pre-existing `TestChatStream_TimingsFromProvider` and both `WallClockTimingFallback` tests still pass, confirming no regression in the precedence chain.

### 4. Empty content must not hard-error (#4, decision 3A)

- [x] Remove the `"empty content in response"` error (`backend/llama/client.go:419-422`); return `("", usage, nil)` when the model spent its whole budget on reasoning.
- [x] Guard `summarizeContext` (`backend/exec/context.go:96`): an empty summary must return an error, because `loop_helpers.go:403+` installs the summary unconditionally and `buildLLMContext` then drops all history before it — a silently empty summary would destroy the conversation context.
- [x] Audit remaining `ChatCompletion` callers; each must already have a fallback (verified by reading, confirm by grep): title-gen `title == ""` (`backend/managers/task.go:1160`), checkpoint labels (`context.go:497`, `:523` → `fallback`), thread name (`handler_thread.go:438` `err == nil && TrimSpace(name) != ""`), auto-answer (`executor.go:293`), manual/HTTP summarize (`handler_http.go:321`).
  - **All confirmed safe.** `content` is now a `string` (not `*string`) since the nil check moved inside; this also removed the `strings.TrimSpace(*content)` dereference.
- [x] Tests: non-streaming `{"message":{"content":null,"reasoning":"..."}}` → no error, empty content, usage returned; `summarizeContext` with empty summary → error and history untouched.
  - **Discovered off-plan:** `summarizeContext` dereferenced `usage.PromptTokens` unconditionally — with `content == nil` no longer erroring, a provider sending no `usage` block returns `usage == nil` and would panic. Added a nil guard on usage there. Verified by `TestSummarizeContext_NonEmptySummarySucceeds` (usage still read when present).
  - **Regression-proof:** with the guard removed, `TestSummarizeContext_EmptySummaryErrors` fails on both variants; `TestBuildLLMContext_EmptySummaryErasesHistory` documents the exact data-loss mechanism (`startIdx = lastSummaryIdx + 1`).

### 5. Context window through llama-swap + vLLM (#5)

- [x] In `DiscoverContextWindow`'s upstream response struct (`backend/config/llm.go:258-264`), also parse `max_model_len`; return `meta.n_ctx` when present, else `max_model_len`. (llama-swap strips `max_model_len` at the proxy and never injects `meta`; the upstream vLLM does publish it.)
- [x] Update the function doc comment to state it covers vLLM and llama.cpp upstreams.
- [x] Test in `backend/config/`: httptest llama-swap proxy (`owned_by: "llama-swap"`, no `max_model_len`) + upstream exposing `max_model_len: 262144` → 262144; llama.cpp upstream with `meta.n_ctx` → unchanged behavior; neither → nil.
  - Added `backend/config/context_window_test.go`: 5 upstream shapes + non-llama-swap skip (asserts the upstream probe is never called) + unreachable server → nil.
  - **Regression-proof:** removing the `max_model_len` branch fails `TestDiscoverContextWindow/vLLM_upstream:_max_model_len`; llama.cpp case passes either way.

### 6. Live stats from in-flight usage (#6, decision 2A)

- [x] Add `onUsage func(*types.ChatUsage)` as a new (nil-able) param to `ChatStream`, after `onProgress`. Do **not** extend `types.StreamCallback`: vLLM's usage frame has `"choices":[]`, and `cb(choice)` fires per choice only, so an empty-choices frame can never deliver it.
- [x] Invoke `onUsage` with a fresh copy of the accumulated usage whenever `usage`/`timings` changes, independent of whether the frame carried choices (covers llama.cpp per-frame `timings` and vLLM's empty-choices final usage frame).
- [x] Update call sites: `loop.go:818` and `loop.go:841` (real callback), `executor.go:419` `StreamCompletion` (pass through, `nil` acceptable), and the 9 test fakes in `client_test.go` / `client_adapter_test.go` (add `nil`).
- [x] In `loop.go`: move the stats block out of `tokenCb` (currently `:788-800`, reads `lc.usage`) into the new `onUsage` closure reading the snapshot. Fixes both failure modes: nil on turn 1, and stale previous-turn values on turn 2+ (`lc.usage` is assigned once at `:439` and never reset, while `lastPredictedN` resets per turn at `:706`).
- [x] Correct the now-inaccurate comment at `loop.go:787-788`.
- [x] Test: fake server streams tokens, then a usage frame → the callback observes monotonically increasing token counts, and the value never decreases across a second `ChatStream` call on the same loop state.
  - Introduced a named `types.UsageCallback` (matches the existing `StreamCallback`/`PromptProgressCallback` style) rather than an inline func type.
  - `TestChatStream_OnUsageCallback` covers: llama.cpp per-frame monotonic snapshots, vLLM empty-choices frame, **snapshot copy-safety** (handing out the live pointer fails this — the timings path mutates one struct in place), and nil-onUsage.
  - 13 existing `ChatStream` test fakes updated with the trailing `nil`.

### 7. Verification

- [x] `gofmt -w` touched files; `go build -trimpath -o kanban .` — clean. `go vet` clean on `llama`, `exec`, `types`, `config`.
- [x] `go test ./backend/... ./pkg/... -count=1 -parallel 8` — every package passes **except four pre-existing/environmental failures, none caused by this work** (verified below).
- [ ] Restart dev server (`make stop && make run`) and live-check vs `neptune` (llama.cpp): `[llama] final usage` non-zero `prompt_ms`/`predicted_ms`, `[stats] ... t/s` plausible (~35-70). **Deferred — needs a running server + real task turn; not done in this session.**
- [ ] Live check vs `saturn` (vLLM): usage now arrives, reasoning renders, Stats modal shows non-`-` Prompt/Gen speed. **Deferred — raw curl probes already confirmed the wire behavior these tests encode.**
- [ ] Confirm `cached_tokens` subtracts on a warm prefix cache. **Deferred — raw probe already showed vLLM reports `cached_tokens: 4000/4060` and the subtraction path is unit-covered.**

### Pre-existing failures (NOT from this work — verified by reverting my changes and re-running)

- `backend/exec` `TestExtractCommitContext_*` (6): fail identically with my `context.go` production changes fully reverted. `ExtractCommitContext` (context.go:799) is a pure function unrelated to `summarizeContext`.
- `backend/managers` `TestHandlePull_Success`: `git commit: Committer identity unknown` — no global git identity in this sandbox (an AGENTS.md-documented test-env prerequisite).
- `backend/sandbox` (~20) and `backend/server` `TestTaskOperatingModeRemount`: podman `'overlay' is not supported over overlayfs` — nested-container storage limit; AGENTS.md notes sandbox tests need a real `runsc`/podman host. Not code-related.

## Discovered Tasks

- [x] **D1** (found in Task 4): `summarizeContext` dereferenced `usage.PromptTokens` with no nil check. Task 4 removed the error path that previously masked this, so a provider returning no `usage` block alongside empty content would panic. Added a `usage != nil` guard around the four usage fields. Required for Task 4's correctness; no behavior change when usage is present.

## Follow-up Work

- [x] Comment cleanup pass: shortened every comment added by this change to plain language across `llama/client.go`, `llama/client_test.go`, `types/llm.go`, `config/llm.go`, `config/context_window_test.go`, `exec/context.go`, `exec/loop.go`, `exec/executor.go`, `exec/summarize_empty_test.go`. Fixed along the way: a duplicated `ChatStream` doc line left by an interrupted edit, a doc reference to a non-existent exported `NormalizeReasoning`, a "wall-clock clock" typo, an inaccurate "three upstream shapes" count (now five cases), an orphaned provider-registration comment in a test that registers nothing, and two test doc comments that broke Go's "comment starts with the func name" convention.
  - Also corrected an inaccurate claim in the `StreamCompletion` comment: the sole caller (`context.go:730`) uses `_, err :=` and discards usage — it does not "use the returned usage".
  - Re-verified after the pass: gofmt clean, build clean, vet clean, 41 new test cases pass, no change to the pre-existing failure set.

## Notes

**Decisions.** 1B — `stream_options` unconditional, *no* auto-strip-on-400 net. 2A — new `onUsage` param. 3A — empty content returns no error; callers keep their existing fallbacks.

**Accepted risk (1B).** `return_progress` had to be explicitly removed for 4 providers in `MIKE-extensions/llm/api-compatibility/mappings.json`, so unknown request keys do break providers. `stream_options` is verified accepted by llama.cpp b10603 and vLLM (`vllm-0.1.dev20073`), but a stricter provider would 400 → the retry loop burns ~14 s then `failTask`. Escalate to the strip-and-retry net if that appears in the field.

**Evidence (measured this session).**

| | Saturn :8101 (vLLM direct) | Saturn :8100/upstream (vLLM) | Neptune :8010/upstream (llama.cpp) |
|---|---|---|---|
| `usage` w/o `stream_options` | none | none | none |
| `usage` w/ `stream_options` | yes | yes | yes (harmless, additive) |
| `timings` | never | never | per-frame + final |
| `prompt_progress` | ignored | ignored | yes |
| reasoning field | `delta.reasoning` | `delta.reasoning` | `delta.reasoning_content` |
| `content` on budget exhaustion | `null` | `null` | `""` |
| ctx window via our code | ok (`max_model_len`) | **nil** | ok (`meta.n_ctx`) |

**Cross-checked arithmetic:** our formula reproduces llama.cpp's own numbers — `prompt_n + cache_n` (4+8) equals the `usage.prompt_tokens` (12) it sent in the same frame, and `122×1000/1768.5ms = 69 tok/s` vs its reported `predicted_per_second: 68.4`.

**No frontend change** — WS event shapes are unchanged, so the build is Go-only (no `npm run build`, no svelte-check).

**Unchanged by design:** the `timings`-overwrites-`usage` overlap at `client.go:302-308` is correct; `prompt_tokens_details: null` parses safely to 0 (verified) and #8's config fix makes it moot.
