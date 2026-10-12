# LLM Streaming: Wait Times and Sampler Overrides

The core principle for LLM streaming is that a long wait is not necessarily a fault. A completion is finished by its last token, not by a clock. The system measures wait times and provides notices, but only ends a call if the operator has set a specific ceiling.

This is critical for self-hosted models (e.g., vLLM) where grammar-constrained tool calls can result in long periods of silence (up to 90 seconds) while the server buffers tokens, even while the TCP connection remains established.

## Wait Time Measurement

| Component | Location | Purpose | Ends Call? |
|---|---|---|---|
| Silence Clock | `backend/llama/client.go` | Measures time since the last received line. | Only if ceiling is reached. |
| Notice Floors | `streamNoticeFirstToken` (15s), `streamNoticeMidResponse` (30s) | Determines when to notify the user of a wait. | No. |
| `stream_idle_seconds` | `types.Provider` | The operator's defined ceiling for a quiet response. | Yes (default `0` = none). |
| TCP Keepalive | `httputil.streamTransport` | Detects if the host has vanished. | Yes (approx. 75s). |
| Retry Window | `backend/exec/loop.go` | The total time allowed for all retry attempts. | Stops retrying. |

## Streaming Phases

Silence is categorized into two phases, which determine the applicable notice floor:

1. **`StreamPhaseFirstToken`**: Silence before the first token arrives. This is typically prompt processing (prefill).
2. **`StreamPhaseMidResponse`**: Silence after the first token has been received. This is typically output buffering by the provider.

The client's own read operation is the only reliable signal to reset these clocks.

## The Silence Ceiling

The transport measures silence, but `backend/exec` decides the policy via `SetSilencePolicy(ceiling, onWait)`. 

- **Policy**: The ceiling is fetched per provider (`GetProviderStreamIdle`). This allows different limits for a fast hosted API versus a slower self-hosted model.
- **Reporting**: If a ceiling is reached, the system returns `llama.ErrStreamSilent`, which is rendered as `llm_stream_silent`. This is distinct from `llm_connection_failed` (a transport fault) to prevent users from troubleshooting their network when the issue is provider latency.

## Retrying Quiet Providers

When `ErrStreamSilent` occurs, the retry backoff is at least as long as the observed silence. This prevents hammering a stalled provider with the same large request every few seconds. 

In contrast, transport faults use a shorter backoff ladder. All retries are bounded by `llmRetryWindow`. If a turn fails after retries, it is sent to Review for human intervention.

## Socket Health (Keepalives)

`httputil.NewStreamClient()` uses a custom `KeepAliveConfig` (`Idle 30s, Interval 15s, Count 3`) to detect dead hosts in ~75s, significantly faster than the OS default (~12 minutes).

**Important Distinctions**:
- **Not a liveness test**: A wedged application over a live socket will still answer keepalives.
- **HTTP/1.1**: The connection is cleartext HTTP/1.1, so HTTP/2 pings are not used.

## Sampler Overrides

Sampling parameters (e.g., `temperature`, `top_p`, `seed`) are sourced exclusively from the model row in Settings (`types.ModelSampling`).

- **Nil Fields**: If a field is nil, it is omitted from the request, allowing the provider to use its own defaults.
- **Zero Values**: A value of `0` is treated as a deliberate decision (e.g., greedy decoding) and is sent to the provider.
- **Application**: Overrides are applied in `newLlamaClient` and are used for all calls associated with that model, including utility calls (summaries, commit labels).
- **Validation**: `validateModelSampling` ensures values are within bounds before they are saved to the configuration.

## Tests

- `backend/llama/client_test.go`: Ceiling enforcement and silence handling.
- `backend/exec/quiet_stream_test.go`: Wait frames and `llm_stream_silent` error reporting.
- `backend/exec/llm_crash_test.go` & `llm_request_rejected_test.go`: Transport faults vs. refusals.
- `backend/config/config_test.go`: `stream_idle_seconds` defaults.
- `backend/config/sampling_test.go`: Sampler bounds and save validation.
- `backend/llama/client_sampling_test.go`: Sampler key transmission and nil handling.
- `backend/exec/sampling_override_test.go`: Application of overrides to non-task turns.
- `backend/httputil/client_test.go`: Stream client timeout and transport isolation.

## Verification

`go test ./backend/stream/ ./backend/llama/` verifies frame grammar and token accounting. `make check-race` verifies the one-writer-per-socket rule in the stream package.
