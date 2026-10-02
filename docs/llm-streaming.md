# Talking to the model: what is sent, what is waited for

One rule owns everything here: **a long wait is not a fault.** A completion is finished by its last token and not
by the clock, so nothing in the LLM path ends a call because the provider took a while. What the path does instead
is measure the wait, say what it measures, and let a person set a ceiling if they want one.

This is not caution about slow networks. A self-hosted vLLM composing a grammar-constrained tool call was captured
holding **16,267 generated tokens behind 76-89 seconds of silence** — HTTP/1.1 response open, TCP keepalive ACKed,
`ESTABLISHED`, zero bytes — then finishing with `finish_reason: tool_calls` and `[DONE]`. The buffer is inside the
server's constrained decoder, and it exists for every such turn: a 74k-prompt tool call reproduced it 3 times out
of 3. Removing `tools` from the same body streamed 1,500 frames with a 9.9 s worst gap. Silence during a tool call
is therefore the normal case, and a timeout set below it fails the turn every time.

## What measures what

| Thing | Where | What it answers | Ends a call? |
|---|---|---|---|
| One silence clock | `backend/llama/client.go` (ticker beside the scanner, `lastByte` stamped per line) | how long since the last line, and which phase | only under a ceiling |
| Notice floors | `streamNoticeFirstToken` (15 s), `streamNoticeMidResponse` (30 s) | when a watching person is told | no |
| `stream_idle_seconds` | `types.Provider`, resolved by `ConfigStore.GetProviderStreamIdle` | the operator's ceiling for one quiet response | yes, default `0` = none |
| TCP keepalive | `httputil.streamTransport` (`Idle 30s, Interval 15s, Count 3`) | has the host vanished | yes, in ~75 s |
| `llmRetryWindow`, `llmRetryAttempts` | `backend/exec/loop.go` | how long a run keeps retrying at all | stops retrying |

Before this there were two clocks and neither was honest: a `time.AfterFunc` armed only *after* the first line, set
to 30 s, whose expiry returned a synthesised `context.DeadlineExceeded` — which reached the user as
`LLM connection failed: context deadline exceeded` about a connection that had answered and was still answering.
Now one clock measures both phases, the client kills nothing unless a ceiling was set, and the error for reaching a
ceiling is a different error from a broken transport.

## Phases, because the two silences mean different things

`StreamPhaseFirstToken` is silence before any token: usually prompt processing (a ~60k-token prefill measured 9.5-9.9 s
to the first byte on a warm cache, and cold is longer). `StreamPhaseMidResponse` is silence after a token: usually
output the provider is holding back, as above. The phase travels on the wait frame (`docs/reconnection.md`) because
the sentences differ, and it decides which notice floor applies. The provider's `return_progress` flag (llama.cpp-only,
ignored by vLLM) cannot be relied on to reset either clock — the client's own read is the only signal.

## The ceiling, and where policy lives

The transport **measures**; `backend/exec` **decides**. `SetSilencePolicy(ceiling, onWait)` is called by
`newLlamaClient` with `GetProviderStreamIdle(serverURL)` — per provider, because a 200k-context self-hosted model and
a hosted API do not have the same notion of "too quiet", and the value is 0 unless somebody typed one in Settings.
`onWait` is nil for the utility calls (commit messages, summaries) that have nobody watching them, which changes what
is reported and never what is tolerated; the ceiling still applies to them, because it is the same endpoint.

Reaching a ceiling returns `llama.ErrStreamSilent{Silent, Phase}`, which `classifyLLMFailure` renders as
`llm_stream_silent` naming the seconds and pointing at the setting. `llm_connection_failed` is reserved for a
transport fault. Nothing on that path is allowed to say "deadline": it was the truest possible sentence about the
wrong thing, and it sent people to check a network that was fine.

## Retrying a quiet provider

`backoffFor` waits **at least the silence it observed** for `ErrStreamSilent` — a provider that said nothing for 40 s
does not want the same 254 KB request again in 2 s — while a transport fault keeps the short ladder, because there the
next attempt is a different connection. `llmRetryWindow` bounds the whole retry episode: the old shape was four
identical requests inside two and a half minutes, each re-uploading a 74k-token prompt into the same stall, which
looks to the provider like a client that will not stop. Failures still land in Review through `failTask` (a person
decides what to do with a half-done turn), so the sentence has to carry the guidance.

The retry loop reads the ladder through `Deps.LLMBackoff`, whose default is `backoffFor`. A test whose subject is
what the loop *does* — retried, then failed into Review — injects a short one (`fastRetryLadder`); these numbers are
held by `TestBackoffFor_*` and `TestQuietStreamBackoffWaitsAtLeastTheSilenceItSaw`, by no integration test.

## Keeping the socket honest

`httputil.NewStreamClient()` is the LLM client's transport and no one else's, because its keepalive config is
unusual: `KeepAliveConfig{Enable: true, Idle: 30s, Interval: 15s, Count: 3}` notices a host that stopped existing in
~75 s instead of the ~12 minutes the OS defaults take. Two things it is **not**:

- **Not a stream liveness test.** A wedged application over a live socket keeps `ESTABLISHED`, keeps answering
  keepalives, and blocks the reader forever. That is precisely the healthy-but-silent case above.
- **Not an HTTP/2 `ReadIdleTimeout`.** The provider link here is cleartext HTTP/1.1 (measured through the client's
  own `Transport`), so h2 pings never fire. An earlier version of this file proposed gating a kill on the provider's
  `/metrics`; that was wrong twice — the metrics on that deployment are host CPU/load only, and silence is not death.

`KeepAliveConfig` is a `Dialer` field with no `Transport` twin, so a `Transport.Clone()` keeps a clone of the *old*
`DialContext` — which is why this transport is built, not cloned-and-tweaked, and why `sharedTransport` (extension
egress, update checks) keeps its own defaults.

## Sampler overrides: a blank field sends nothing

**A model row's `sampling` object (`types.ModelSampling` in `backend/types/domain.go`) is the only source
of a sampler field in a request body, and a field that is nil is absent from it.**

Settings → Providers → a model row holds `temperature`, `top_p`, `top_k`, `min_p`, `frequency_penalty`,
`presence_penalty`, `repetition_penalty` and `seed`. `applySamplingParams` in `backend/llama/client.go`
writes the fields that are set and touches nothing else, so an empty field leaves the provider's own
sampling in charge. A `0` is a typed value and is sent: greedy decoding is a decision, not a blank.

The row is read once, in `newLlamaClient` through `GetModelSampling` in `backend/config/sampling.go`,
beside the adapter and the silence ceiling. A utility call — a title, a summary, a commit label —
samples the way the model it asks for was configured, and no call site carries the lookup. The
overrides go in before `applyAdapter`, so an `llm` extension mapping can still rename or drop one.

The range is judged where the row is saved: `validateModelSampling` refuses a value outside it before
any config row is written. The status and sentence that answer such a refusal are
`docs/api-contract.md → Partial updates decode into pointer fields`. Those bounds and the field hints
in `frontend/src/lib/i18n/en.ts` (`settings.sampling.*Hint`) are the same numbers, and a change to one
is a change to the other.

## Tests

- `backend/llama/client_test.go` — ceiling ends the stream; silence before the first token is waited out; a stream
  that resumes completes, with its tokens intact.
- `backend/exec/quiet_stream_test.go` — the frame pair for the wait; silence-then-resume finishes the turn with no
  error row; a configured ceiling reports `llm_stream_silent` and not `llm_connection_failed`.
- `backend/exec/llm_crash_test.go`, `llm_request_rejected_test.go` — a dead transport is still `llm_connection_failed`,
  a refusal is still one call.
- `backend/config/config_test.go` — an unset or negative `stream_idle_seconds` means no ceiling.
- `backend/config/sampling_test.go` — a sampler bound at its edge and just outside it; a refused save
  writes nothing.
- `backend/llama/client_sampling_test.go` — the eight keys arrive, a blank sends none, and a mapping
  still sees them.
- `backend/exec/sampling_override_test.go` — a call that is not a task turn carries them too.
- `backend/httputil/client_test.go` — the streaming client has no timeout and does not share `sharedTransport`.
## Checking this

`go test ./backend/stream/ ./backend/llama/` covers the frame grammar and the token accounting.
`make check-race` runs the stream package, where the one-writer-per-socket rule lives.
