/**
 * Mock llama.cpp OpenAI-compatible API server for e2e tests.
 * 
 * Serves pre-recorded SSE streaming responses on POST /chat/completions.
 * 
 * Response routing:
 * - Tools in request + "list"/"file"/"directory" in messages → tool_call_response.json (fast)
 * - "stop" in messages → text_response_slow.json (100ms between chunks for stop button test)
 * - "long"/"detailed"/"write" in messages → text_response_long.json (fast)
 * - Default → text_response.json (fast)
 * 
 * Usage:
 *   node frontend/tests/mock-llm.js [port]
 *   Default port: 9999
 */

import http from 'node:http';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const FIXTURES_DIR = path.join(__dirname, 'fixtures');
const PORT = parseInt(process.argv[2] || '9999', 10);

// Load fixtures
function loadFixture(name) {
    return JSON.parse(fs.readFileSync(path.join(FIXTURES_DIR, `${name}.json`), 'utf8'));
}

const textResponse = loadFixture('text_response');
const textResponseLong = loadFixture('text_response_long');
const titleLong = loadFixture('title_long');
const textResponseSlow = loadFixture('text_response_slow');
const toolCallResponse = loadFixture('tool_call_response');
const toolCallFail = loadFixture('tool_call_fail');
const toolCallStreaming = loadFixture('tool_call_streaming');
const toolCallWithText = loadFixture('tool_call_with_text');
const toolCallComplete = loadFixture('tool_call_complete');
const toolCallReadFile = loadFixture('tool_call_read_file');
const toolCallLoop = loadFixture('tool_call_loop');
const toolCallTaskComplete = loadFixture('tool_call_task_complete');
const textLoop = loadFixture('text_loop');
const phraseRepeat = loadFixture('phrase_repeat');
const toolCallCreateTask = loadFixture('tool_call_create_task');
const toolCallEditFile = loadFixture('tool_call_edit_file');
const toolCallRename = loadFixture('tool_call_rename');
const toolCallRenameTs = loadFixture('tool_call_rename_ts');
const toolCallSummarize = loadFixture('tool_call_summarize');
const textResponseReasoning = loadFixture('text_response_reasoning');
const toolCallSandboxSleep = loadFixture('tool_call_sandbox_sleep');
const toolCallBashLong = loadFixture('tool_call_bash_long');
const toolCallLongRunning = loadFixture('tool_call_long_running');
const toolCallLongArgsSleep = loadFixture('tool_call_long_args_sleep');
const toolCallSlow = loadFixture('tool_call_slow');
const toolCallSandboxMemhog = loadFixture('tool_call_sandbox_memhog');

let requestCount = 0;
// Prompts already failed once (fail-once e2e).
const failOncePrompts = new Set();
let loopToolCount = 0;  // per-scenario counter for tool loop test
let loopTextCount = 0;  // per-scenario counter for text loop test
let loopPhraseCount = 0;  // per-scenario counter for phrase loop test
// NOTE: autoContinueCount removed — use per-conversation tool result counting instead

/**
 * Build a non-streaming JSON response (matching real LLM behavior for stream: false).
 * Uses the selected fixture chunks to derive content + usage.
 */
function buildNonStreamingResponse(parsed, chunks) {
    const isTitleGen = (parsed.messages || []).some(
        m => m.role === 'system' && /title.generator/i.test(m.content)
    );

    // Extract content from text chunks
    let content = '';
    for (const chunk of chunks) {
        for (const choice of chunk.choices || []) {
            if (choice.delta?.content) {
                content += choice.delta.content;
            }
        }
    }

    // For title generation, produce a short title from the user prompt
    if (isTitleGen && !content) {
        const userMsg = (parsed.messages || []).filter(m => m.role === 'user').pop();
        content = userMsg?.content?.slice(0, 40) || 'Generated Title';
    }

    const lastChunk = chunks[chunks.length - 1];
    const usage = lastChunk?.usage || {
        prompt_tokens: 30,
        completion_tokens: isTitleGen ? 5 : 8,
        total_tokens: isTitleGen ? 35 : 38,
        prompt_tokens_details: { cached_tokens: 0 }
    };
    // Include cache_tokens field for context tracking tests
    if (!usage.cache_tokens) {
        usage.cache_tokens = usage.prompt_tokens_details?.cached_tokens || 0;
    }
    const timings = lastChunk?.timings || {
        prompt_ms: 150,
        predicted_ms: isTitleGen ? 50 : 180,
        prompt_per_token_ms: 0,
        predicted_per_token_ms: 0,
        prompt_per_second: 0,
        predicted_per_second: 0
    };

    return JSON.stringify({
        choices: [{
            message: { content: content },
            finish_reason: 'stop'
        }],
        usage: usage,
        timings: timings
    });
}

/**
 * Fill timings.prompt_n/cache_n/predicted_n from usage — fixtures only set
 * prompt_ms/predicted_ms, and the client zeroes usage without the token counts.
 */
function enrichTimings(chunk) {
    if (!chunk.timings || !chunk.usage) return chunk;
    const t = { ...chunk.timings };
    if (t.prompt_n === undefined && t.cache_n === undefined && t.predicted_n === undefined) {
        const cached = chunk.usage.prompt_tokens_details?.cached_tokens || 0;
        t.prompt_n = (chunk.usage.prompt_tokens || 0) - cached;
        t.cache_n = cached;
        t.predicted_n = chunk.usage.completion_tokens || 0;
    }
    return { ...chunk, timings: t };
}

/**
 * Select fixture based on request body content.
 * Returns { chunks, chunkDelay } where chunkDelay is ms between SSE frames.
 */
// Explicit fixture pin: a prompt containing "llm-fixture:<name>" gets exactly that fixture.
// The keyword table below is otherwise unreachable in the common case — the plan-first skill
// is in every system prompt, so plan routing (see isPlanMode) answers first and a spec asking
// for "edit" silently gets create_task. A pin says what the test means and keeps every other
// spec's routing untouched.
const FIXTURES_BY_NAME = {
    // name: [chunks, chunkDelay] — the delay mirrors the keyword table, so a pinned slow
    // fixture is still slow (pinning text_slow at 1ms streams the whole turn in ~16ms and
    // any test that waits for a mid-stream state waits forever).
    text: [textResponse, 1],
    text_long: [textResponseLong, 1],
    text_slow: [textResponseSlow, 100],
    text_loop: [textLoop, 1],
    phrase_repeat: [phraseRepeat, 1],
    tool_call_edit_file: [toolCallEditFile, 1],
    tool_call_read_file: [toolCallReadFile, 1],
    tool_call_response: [toolCallResponse, 1],
    tool_call_create_task: [toolCallCreateTask, 1],
    tool_call_complete: [toolCallTaskComplete, 1],
    tool_call_streaming: [toolCallStreaming, 1],
    tool_call_fail: [toolCallFail, 1],
    tool_call_loop: [toolCallLoop, 1],
    text_response_reasoning: [textResponseReasoning, 1],
    // Title routing streams the selected fixture's content, so a spec that needs a long title, and so a long
    // branch name, pins this one instead of hoping the keyword route answers something long.
    title_long: [titleLong, 1],
    // The tool-call-header-overflow probe: arguments long enough to be truncated AND a call that runs
    // long enough for the elapsed counter to appear. No existing fixture is both.
    tool_call_long_args_sleep: [toolCallLongArgsSleep, 1],
};

/**
 * Tool results written after the last user message: how many tool calls the turn now running has already
 * made. Tool results earlier in the conversation belong to turns that are over.
 */
function toolResultsThisTurn(messages) {
    const lastUser = messages.map(m => m.role).lastIndexOf('user');
    return messages.slice(lastUser + 1).filter(m => m.role === 'tool').length;
}

/** Whether a fixture streams a tool call, which is what makes the turn that received it ask again. */
function streamsToolCall(chunks) {
    return chunks.some(c => (c.choices || []).some(ch => ch.delta?.tool_calls || ch.message?.tool_calls));
}

function selectResponse(parsed) {
    const hasTools = parsed.tools && parsed.tools.length > 0;
    const messages = parsed.messages || [];

    // Use only the LAST user message for routing.
    // This avoids false matches from conversation history (e.g., the initial
    // task prompt persisted as a user message in earlier turns).
    const userMessages = messages
        .filter(m => m.role === 'user')
        .map(m => (m.content || '').toLowerCase());
    const lastUserMsg = userMessages.length > 0 ? userMessages[userMessages.length - 1] : '';

    // Whether the conversation already contains tool results (used by plan-mode
    // routing below and by the tool-result routing further down).
    const hasToolResults = messages.some(m => m.role === 'tool');

    const pin = lastUserMsg.match(/llm-fixture:([a-z0-9_-]+)/);
    if (pin) {
        const pinned = FIXTURES_BY_NAME[pin[1]];
        if (pinned) {
            // Served only while this turn holds no tool result, which is the rule every keyword probe encodes as
            // `hasToolResults ? toolCallComplete : probe`. The pin test outranks every termination rule below, so
            // an unbounded pin repeats until a supervisor or the spec's teardown stops the task: measured at 156
            // calls for one prompt, one every ~60 ms, each rewriting the worktree and each holding the task's
            // container — and its 1024 MB admission charge — open.
            if (streamsToolCall(pinned[0]) && toolResultsThisTurn(messages) > 0) {
                console.error(`[mock-llm] pin "${pin[1]}" served once, its tool result is back: ending the turn`);
                return { chunks: toolCallComplete, chunkDelay: pinned[1] };
            }
            return { chunks: pinned[0], chunkDelay: pinned[1] };
        }
        console.error(`[mock-llm] unknown llm-fixture:"${pin[1]}" — falling through to keyword routing`);
    }

    // Bash-card reconnect probe: the long-running bash call keeps the tool in
    // flight while the spec drops the live WebSocket, so loadHistory() replaces the
    // streaming entry with the DB-loaded (still pending) one before tool_result
    // arrives — the exact condition that used to leave the card on "Running".
    // Follow-up (tool result present): plain text ends the turn.
    if (hasTools && /bash-reconnect-probe/i.test(lastUserMsg)) {
        return hasToolResults
            ? { chunks: toolCallComplete, chunkDelay: 1 }
            : { chunks: toolCallBashLong, chunkDelay: 1 };
    }

    // Slow tool-call stream probe: streamed argument-slice by argument-slice (~4s) so a stop lands
    // while the call is still being generated. Follow-up (tool result present): plain text.
    if (hasTools && /slow-tool-probe/i.test(lastUserMsg)) {
        return hasToolResults
            ? { chunks: toolCallComplete, chunkDelay: 1 }
            : { chunks: toolCallSlow, chunkDelay: 300 };
    }

    // Long-running tool probe: the call runs for 20s, so the spec has a wide window where it has started
    // and has no result. Follow-up (tool result present): plain text ends the turn.
    if (hasTools && /long-running-probe/i.test(lastUserMsg)) {
        return hasToolResults
            ? { chunks: toolCallComplete, chunkDelay: 1 }
            : { chunks: toolCallLongRunning, chunkDelay: 1 };
    }

    // Sandbox memory e2e probes (keyword-gated to the probe tasks' prompts).
    // Must stay ABOVE the plan-mode check below: when prompts/skills/ exists,
    // the skill name "plan-first" is in every system prompt and also matches
    // that regex, which would hijack the probe tasks into the plan fixtures.
    // First call: a bash tool call that runs in the sandbox (sleep keeps the
    // container alive; memhog allocates past the memory limit so the cgroup
    // OOM killer fires). Follow-up (tool result present): plain text ends the
    // turn and the task stays in_progress (deterministic in any environment).
    const sandboxProbeFixture =
        /sandbox-memhog-probe/i.test(lastUserMsg) ? toolCallSandboxMemhog :
        /sandbox-sleep-probe/i.test(lastUserMsg) ? toolCallSandboxSleep : null;
    if (hasTools && sandboxProbeFixture) {
        return hasToolResults
            ? { chunks: toolCallComplete, chunkDelay: 1 }
            : { chunks: sandboxProbeFixture, chunkDelay: 1 };
    }
    // Probe termination: once the probe command's result is back, a text
    // response triggers the loop detector's "Is the task complete?" user
    // message — the probe keyword is no longer the last user message. The
    // command's marker output identifies the probe conversation; end the turn
    // with request_review (deterministic in every environment).
    if (hasTools && /is the task complete\?/i.test(lastUserMsg) &&
        messages.some(m => m.role === 'tool' && /SANDBOX-(SLEEP|MEMHOG)-PROBE-DONE/.test(m.content || ''))) {
        return { chunks: toolCallTaskComplete, chunkDelay: 1 };
    }

    // Tool-args rendering probe: a tool call whose args the UI is EXPECTED to show.
    // Must stay ABOVE the plan-mode check (the plan-first skill is in every system prompt,
    // so an ordinary "list files" prompt routes to create_task — whose args the UI hides on
    // purpose, see HIDE_ARGS_FOR_TOOLS). read_file executes harmlessly; the follow-up ends
    // the turn in review.
    // Not gated on hasToolResults the way the other probes are: a chat message arrives in a
    // thread that already has tool results from the task's initial turn, so that gate would
    // answer the probe with text on its very first call. The turn still ends by itself — the
    // loop detector's "Is the task complete?" becomes the last user message, which no longer
    // matches, and plan routing then calls request_review.
    if (hasTools && /read-file-probe/i.test(lastUserMsg)) {
        return { chunks: toolCallReadFile, chunkDelay: 1 };
    }

    // Plan mode: LLM creates subtasks via create_task tool, then calls request_review.
    // First call (no tool results yet): return create_task tool call.
    // After create_task result: return request_review to end planning.
    const isPlanMode = messages.some(m => m.role === 'system' && /plan[- ]?(mode|only|first)/i.test(m.content || ''));
    if (isPlanMode && hasTools) {
        if (hasToolResults) {
            // After create_task tool result, signal plan complete
            return { chunks: toolCallTaskComplete, chunkDelay: 1 };
        }
        return { chunks: toolCallCreateTask, chunkDelay: 1 };
    }

    // Loop detection tests: return repeated responses to trigger loop detection.
    // Tool loop: return same tool call (head /tmp/test_file.txt) 2x, then request_review.
    // Text loop: return similar text 3x, then request_review.
    // Phrase loop: return response with repeated phrases, then request_review after hint.
    // System hint messages (injected by backend) break the loop.
    const hasSystemHint = messages.some(m => m.role === 'system' && /twice in a row|similar responses|repeated yourself/i.test(m.content || ''));
    if (/loop-test-tool/i.test(lastUserMsg)) {
        if (hasSystemHint || loopToolCount >= 2) {
            loopToolCount = 0;
            return { chunks: toolCallTaskComplete, chunkDelay: 1 };
        }
        loopToolCount++;
        return { chunks: toolCallLoop, chunkDelay: 1 };
    }
    if (/loop-test-text/i.test(lastUserMsg)) {
        if (hasSystemHint) {
            loopTextCount = 0;
            return { chunks: toolCallTaskComplete, chunkDelay: 1 };
        }
        loopTextCount++;
        return { chunks: textLoop, chunkDelay: 1 };
    }
    if (/loop-test-phrase/i.test(lastUserMsg)) {
        if (hasSystemHint) {
            loopPhraseCount = 0;
            return { chunks: toolCallTaskComplete, chunkDelay: 1 };
        }
        loopPhraseCount++;
        return { chunks: phraseRepeat, chunkDelay: 1 };
    }

    // Auto-summarize + "Proceed" test: a tool-call turn whose reported usage
    // exceeds 90% of the model context window (mock-model: 8192), forcing the
    // executor to auto-summarize mid-turn and inject a "Proceed" user message.
    if (hasTools && /summarize-now/i.test(lastUserMsg)) {
        return { chunks: toolCallSummarize, chunkDelay: 1 };
    }
    // After auto-summarization the executor feeds the compressed context back to
    // the LLM with the injected "Proceed" user message — answer with plain text
    // so the turn completes (the tool result was compressed into the summary).
    if (/^proceed$/i.test(lastUserMsg.trim())) {
        return { chunks: textResponse, chunkDelay: 1 };
    }

    // If there are tool results in the conversation, the LLM should respond
    // with text (not another tool call) to avoid infinite tool-call loops.
    // After enough tool cycles, call request_review to terminate.
    // Use per-conversation counting (tool message count) instead of a global counter.
    if (hasToolResults) {
        const toolResultCount = messages.filter(m => m.role === 'tool').length;
        // After 2+ tool results, signal task complete to terminate the executor
        if (toolResultCount >= 4) {
            return { chunks: toolCallTaskComplete, chunkDelay: 1 };
        }
        return { chunks: toolCallComplete, chunkDelay: 1 };
    }

    // Failing tool call: message mentions file that does not exist
    if (hasTools && /does not exist/i.test(lastUserMsg)) {
        return { chunks: toolCallFail, chunkDelay: 1 };
    }

    // Streaming tool call scenario: tools provided and message mentions "streaming" or "multi"
    // This simulates real LLM behavior where tool calls are split across multiple SSE chunks
    if (hasTools && /streaming|multi.*chunk|split.*tool/i.test(lastUserMsg)) {
        return { chunks: toolCallStreaming, chunkDelay: 1 };
    }

    // LSP rename tool calls: TS variant first (its keyword contains the Go keyword)
    if (hasTools && /rename-test-ts/i.test(lastUserMsg)) {
        return { chunks: toolCallRenameTs, chunkDelay: 1 };
    }
    if (hasTools && /rename-test/i.test(lastUserMsg)) {
        return { chunks: toolCallRename, chunkDelay: 1 };
    }

    // Edit file tool call: tools provided and message mentions "edit"
    if (hasTools && /\bedit\b/i.test(lastUserMsg)) {
        return { chunks: toolCallEditFile, chunkDelay: 1 };
    }

    // Tool call with text content: LLM sends tool name as text before the tool call
    // This simulates the "tool name printed twice" bug
    if (hasTools && /text.*tool|tool.*text|toolname.*dup/i.test(lastUserMsg)) {
        return { chunks: toolCallWithText, chunkDelay: 1 };
    }

    // Tool call scenario: tools provided and message mentions file/directory operations
    if (hasTools && /\blist\b|\bdirectory\b|\bread\b|\bcommand\b|\bgit\b|\bexec\b/i.test(lastUserMsg)) {
        return { chunks: toolCallResponse, chunkDelay: 1 };
    }

    // Slow response: for stop button test and cancel-during-streaming test (needs streaming to be visible)
    // Also slow for commit preview messages so cancel-on-type tests can intercept the stream
    if (/stop button|stop-test|cancel-test|interrupt-test/i.test(lastUserMsg) ||
        /Conversation context:|Git diff/i.test(lastUserMsg)) {
        return { chunks: textResponseSlow, chunkDelay: 100 };
    }

    // Long response scenario: message asks for detailed/long output
    if (/long|detailed|write a|explain|describe/i.test(lastUserMsg)) {
        return { chunks: textResponseLong, chunkDelay: 1 };
    }

    // Reasoning response: message mentions "reasoning" to test the reasoning block UI
    if (/reasoning test|show reasoning|reasoning-block/i.test(lastUserMsg)) {
        return { chunks: textResponseReasoning, chunkDelay: 1 };
    }

    // Default: simple text response
    return { chunks: textResponse, chunkDelay: 1 };
}

const server = http.createServer((req, res) => {
    // GET /models or GET /v1/models — return mock model list with context windows
    if (req.method === 'GET' && (req.url === '/models' || req.url === '/v1/models')) {
        const modelsResponse = JSON.stringify({
            object: 'list',
            data: [
                { id: 'mock-model', object: 'model', created: 1700000000, owned_by: 'test', max_model_len: 8192 },
                { id: 'mock-model-large', object: 'model', created: 1700000000, owned_by: 'test', max_model_len: 32768 },
                { id: 'mock-model-small', object: 'model', created: 1700000000, owned_by: 'test' }, // no context window
            ]
        });
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(modelsResponse);
        return;
    }

    if (req.method === 'POST' && (req.url === '/chat/completions' || req.url === '/v1/chat/completions')) {
        requestCount++;
        let body = '';
        req.on('data', chunk => { body += chunk.toString(); });
        req.on('end', () => {
            try {
                const parsed = JSON.parse(body);
                const { chunks, chunkDelay } = selectResponse(parsed);
                const fixtureName = chunks === toolCallFail ? 'tool_call_fail' :
                                    chunks === toolCallStreaming ? 'tool_call_streaming' :
                                    chunks === toolCallEditFile ? 'tool_call_edit_file' :
                                    chunks === toolCallRename ? 'tool_call_rename' :
                                    chunks === toolCallWithText ? 'tool_call_with_text' :
                                    chunks === toolCallLoop ? 'tool_call_loop' :
                                    chunks === toolCallTaskComplete ? 'tool_call_request_review' :
                                    chunks === textLoop ? 'text_loop' :
                                    chunks === phraseRepeat ? 'phrase_repeat' :
                                    chunks === toolCallResponse ? 'tool_call' :
                                    chunks === toolCallSandboxSleep ? 'tool_call_sandbox_sleep' :
                                    chunks === toolCallSandboxMemhog ? 'tool_call_sandbox_memhog' :
                                    chunks === toolCallLongArgsSleep ? 'tool_call_long_args_sleep' :
                                    chunks === toolCallComplete ? 'tool_call_complete' :
                                    chunks === toolCallReadFile ? 'tool_call_read_file' :
                                    chunks === textResponseSlow ? 'text_slow' :
                                    chunks === textResponseLong ? 'text_long' :
                                    chunks === titleLong ? 'title_long' : 'text';
                const msgs = parsed.messages || [];
                const lastUser = msgs.filter(m => m.role === 'user').pop();
                console.error(`[mock-llm] request #${requestCount}: routing to ${fixtureName} (delay=${chunkDelay}ms) stream=${parsed.stream} last_user="${(lastUser?.content || '').slice(0, 80)}..."`);

                // Fail-once (connection-retry e2e): first streaming request for a
                // "fail-once-test" prompt → 500; retries succeed. Never fails
                // non-streaming requests (title generation).
                if (parsed.stream !== false && /fail-once-test/i.test(lastUser?.content || '')) {
                    const key = lastUser.content;
                    if (!failOncePrompts.has(key)) {
                        failOncePrompts.add(key);
                        console.error(`[mock-llm] fail-once: failing first attempt for "${key.slice(0, 40)}..."`);
                        res.writeHead(500, { 'Content-Type': 'application/json' });
                        res.end(JSON.stringify({ error: 'Simulated transient LLM failure' }));
                        return;
                    }
                    console.error(`[mock-llm] fail-once: serving success on retry for "${key.slice(0, 40)}..."`);
                }

                // Check for initial delay: header override or keyword-based routing
                let initialDelay = parseInt(req.headers['x-initial-delay'] || '50', 10);
                if (/slow-start/i.test(lastUser?.content || '')) {
                    initialDelay = 3000; // 3s delay for tests that need a warm-up window
                }

                // Non-streaming response (stream: false) — return JSON, not SSE
                if (parsed.stream === false) {
                    const responseBody = buildNonStreamingResponse(parsed, chunks);
                    res.writeHead(200, { 'Content-Type': 'application/json' });
                    res.end(responseBody);
                    return;
                }

                // SSE headers
                res.writeHead(200, {
                    'Content-Type': 'text/event-stream',
                    'Cache-Control': 'no-cache',
                    'Connection': 'keep-alive',
                    'X-Accel-Buffering': 'no',
                });

                // Initial delay to simulate LLM warm-up (default 50ms, overridable via x-initial-delay header)
                setTimeout(() => {
                    let i = 0;
                    const sendNext = () => {
                        if (i >= chunks.length) {
                            res.write('data: [DONE]\n\n');
                            res.end();
                            return;
                        }
                        res.write(`data: ${JSON.stringify(enrichTimings(chunks[i]))}\n\n`);
                        i++;
                        setTimeout(sendNext, chunkDelay);
                    };
                    sendNext();
                }, initialDelay);
            } catch (e) {
                res.writeHead(400, { 'Content-Type': 'application/json' });
                res.end(JSON.stringify({ error: 'Invalid request body' }));
            }
        });
    } else {
        res.writeHead(404, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ error: 'Not found' }));
    }
});

server.listen(PORT, () => {
    console.error(`Mock LLM server listening on port ${PORT}`);
});

// Graceful shutdown
process.on('SIGTERM', () => {
    console.error(`Mock LLM server shutting down (handled ${requestCount} requests)`);
    server.close();
    process.exit(0);
});

process.on('SIGINT', () => {
    console.error(`Mock LLM server shutting down (handled ${requestCount} requests)`);
    server.close();
    process.exit(0);
});
