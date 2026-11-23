import { describe, it, expect, vi } from 'vitest';
import type { ChatEntry, StreamEvent } from '$lib/types';
import { StreamState } from '$lib/services/stream-state.svelte';
import { createStreamHandler, type StreamHandlerCallbacks } from '$lib/services/stream-handler';
import { buildCheckpointStateMap, buildEntriesFromMessages, mergeLoadedHistory, type CheckpointState } from '$lib/utils/chat-entries';

function makeCallbacks(overrides: Partial<StreamHandlerCallbacks> = {}): StreamHandlerCallbacks {
    return {
        genId: vi.fn(() => `id-${Math.random().toString(36).slice(2, 8)}`),
        scroll: vi.fn(),
        markStreaming: vi.fn(),
        markNotStreaming: vi.fn(),
        onRefreshDiff: vi.fn(),
        onThreadSwitch: vi.fn(),
        loadHistory: vi.fn(),
        updateActiveThread: vi.fn(),
        updateThreadUrl: vi.fn(),
        onNewError: vi.fn(),
        onDiverged: vi.fn(),
        ...overrides,
    };
}

// Only the frame's own field tells a stopped call from a failed one: success is false for both, and
// the card colours them yellow and red.
describe('tool_result interrupted flag', () => {
    function setup() {
        const stream = new StreamState();
        const handleEvent = createStreamHandler(stream, makeCallbacks());
        return { stream, handleEvent };
    }

    it('marks the call that was stopped, including one that never started', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'token', data: { text: '' } }, false);
        handleEvent({ type: 'tool_call', data: { tool_id: 'tc-1', tool_name: 'bash', args: { command: 'make build' } } }, false);
        handleEvent({ type: 'tool_call', data: { tool_id: 'tc-2', tool_name: 'bash', args: { command: 'make test' } } }, false);

        handleEvent({ type: 'tool_result', data: { tool_id: 'tc-1', success: false, interrupted: true, output: 'Interrupted', duration: 12 } }, false);
        handleEvent({ type: 'tool_result', data: { tool_id: 'tc-2', success: false, interrupted: true, output: 'Interrupted', duration: 0 } }, false);

        const [first, second] = stream.state.entries[0].toolCalls!;
        expect(first.interrupted).toBe(true);
        expect(first.success).toBe(false);
        expect(first.result).toBe('Interrupted');
        expect(first.isPending).toBe(false);
        expect(second.interrupted).toBe(true);
        expect(second.isPending).toBe(false);
    });

    it('clears the flag on a call that settled, so a card cannot stay yellow', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'token', data: { text: '' } }, false);
        handleEvent({ type: 'tool_call', data: { tool_id: 'tc-3', tool_name: 'bash', args: { command: 'make build' } } }, false);
        handleEvent({ type: 'tool_result', data: { tool_id: 'tc-3', success: true, interrupted: false, output: 'built', duration: 12 } }, false);

        expect(stream.state.entries[0].toolCalls![0].interrupted).toBe(false);
    });
});

// A user message interrupts the turn in flight. The rows written before the call ran decide what the
// screen may delete at the keystroke: what it drops, the answer has nowhere to land.
describe('a message that interrupts a running tool call', () => {
    function setup() {
        const stream = new StreamState();
        const handleEvent = createStreamHandler(stream, makeCallbacks());
        return { stream, handleEvent };
    }

    /** The frames of a turn whose call is now running. */
    function runningCall(handleEvent: ReturnType<typeof createStreamHandler>) {
        handleEvent({ type: 'token', data: { text: 'Building it now. ' } }, false);
        handleEvent({ type: 'tool_call', data: { tool_id: 'tc-1', tool_name: 'bash', args: { command: 'make build' } } }, false);
        handleEvent({ type: 'tool_progress', data: { tool_id: 'tc-1', message_id: 'row-1' } }, false);
    }

    const cancelled: StreamEvent = { type: 'status', data: { state: 'cancelled', activity: 'Response cancelled, processing new message' } };
    const interruptedResult: StreamEvent = { type: 'tool_result', data: { tool_id: 'tc-1', success: false, interrupted: true, output: 'Interrupted', duration: 900, message_id: 'row-1' } };

    it('names the row on the card, because an announcement alone proves no row exists', () => {
        const { stream, handleEvent } = setup();
        runningCall(handleEvent);

        expect(stream.state.entries[0].toolCalls![0].messageId).toBe('row-1');
    });

    it('keeps the turn\'s text and card, so the interrupted answer lands instead of vanishing', () => {
        const { stream, handleEvent } = setup();
        runningCall(handleEvent);

        stream.abandonInterruptedTurn(); // what sending the message does

        handleEvent(interruptedResult, false);
        handleEvent(cancelled, false);
        handleEvent({ type: 'status', data: { state: 'running', activity: 'Processing new message' } }, false);

        const entry = stream.state.entries.find(e => (e.toolCalls?.length ?? 0) > 0);
        expect(entry?.content).toBe('Building it now. ');
        expect(entry?.toolCalls).toHaveLength(1);
        expect(entry!.toolCalls![0].interrupted).toBe(true);
        expect(entry!.toolCalls![0].isPending).toBe(false);
    });

    it('drops the text and the card the model was only announcing, which no row answers', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'token', data: { text: 'Let me run the build. ' } }, false);
        handleEvent({ type: 'tool_call', data: { tool_id: 'tc-2', tool_name: 'bash', args: { command: 'make build' } } }, false);

        stream.abandonInterruptedTurn();

        expect(stream.state.entries).toEqual([]);
        // An id kept after its card goes swallows the announcement that would redraw it.
        expect(stream.state.processedToolIds.has('tc-2')).toBe(false);
        expect(stream.state.unresolvedToolIds.has('tc-2')).toBe(true);
    });

    it('leaves the yellow card of a stopped turn on a screen that never sent the message', () => {
        const { stream, handleEvent } = setup();
        runningCall(handleEvent);
        handleEvent(interruptedResult, false);

        // Nothing acted locally: the cancelled frame is all this screen is told.
        handleEvent(cancelled, false);

        expect(stream.state.entries[0].toolCalls![0].interrupted).toBe(true);
    });

    it('is what deletes the abandoned text and its card a cancelled frame arrives over', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'token', data: { text: 'Half a sentence' } }, false);
        handleEvent({ type: 'tool_call', data: { tool_id: 'tc-5', tool_name: 'bash', args: { command: 'make build' } } }, false);

        handleEvent(cancelled, false);

        expect(stream.state.entries).toEqual([]);
    });

    it('names the row on the card a history reload moved to another entry', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'token', data: { text: 'Building it now. ' } }, false);
        handleEvent({ type: 'tool_call', data: { tool_id: 'tc-1', tool_name: 'bash', args: { command: 'make build' } } }, false);
        // What loadHistory leaves behind: the row's entry id, the same tool_id, a stale ref.
        stream.state.entries = [{
            id: 'row-1', role: 'assistant', content: 'Building it now. ', isStreaming: false,
            createdAt: new Date().toISOString(),
            toolCalls: [{ toolId: 'tc-1', toolName: 'bash', args: { command: 'make build' }, isPending: true, startedAt: Date.now() }],
        }];
        stream.state.currentAssistantId = null;

        handleEvent({ type: 'tool_progress', data: { tool_id: 'tc-1', message_id: 'row-1' } }, false);

        expect(stream.state.entries[0].toolCalls![0].messageId).toBe('row-1');
    });
});

describe('tool_result truncation metadata', () => {
    function setup() {
        const cb = makeCallbacks();
        const stream = new StreamState();
        const handleEvent = createStreamHandler(stream, cb);
        return { cb, stream, handleEvent };
    }

    it('streamed bash: keeps accumulated output, stores truncation metadata', () => {
        const { stream, handleEvent } = setup();
        // Start assistant
        handleEvent({ type: 'token', data: { text: '' } }, false);
        handleEvent({ type: 'tool_call', data: { tool_id: 'tc-bash', tool_name: 'bash', args: { command: 'make build' } } }, false);
        // Several tool_output chunks accumulate the full streamed content
        handleEvent({ type: 'tool_output', data: { tool_id: 'tc-bash', text: 'Compiling module A...\n' } }, false);
        handleEvent({ type: 'tool_output', data: { tool_id: 'tc-bash', text: 'Compiling module B...\n' } }, false);
        handleEvent({ type: 'tool_output', data: { tool_id: 'tc-bash', text: 'Compiling module C...\n' } }, false);
        handleEvent({ type: 'tool_output', data: { tool_id: 'tc-bash', text: 'Build succeeded in 42s\n' } }, false);

        const fullStreamed = 'Compiling module A...\nCompiling module B...\nCompiling module C...\nBuild succeeded in 42s\n';
        expect(stream.state.entries[0].toolCalls![0].result).toBe(fullStreamed);

        // tool_result arrives with streamed=true and a truncated preview
        handleEvent({
            type: 'tool_result',
            data: {
                tool_id: 'tc-bash',
                success: true,
                output: 'Compiling module A...\n...\nBuild succeeded in 42s', // truncated preview
                duration: 42000,
                result_truncated: true,
                original_lines: 150,
                original_bytes: 8192,
                streamed: true,
                message_id: 'msg-bash-123',
            },
        }, false);

        const tc = stream.state.entries[0].toolCalls![0];
        // Should KEEP the full streamed content, NOT the truncated preview
        expect(tc.result).toBe(fullStreamed);
        expect(tc.resultTruncated).toBe(true);
        expect(tc.originalLines).toBe(150);
        expect(tc.originalBytes).toBe(8192);
        expect(tc.messageId).toBe('msg-bash-123');
        expect(tc.success).toBe(true);
        expect(tc.duration).toBe(42000);
        expect(tc.isPending).toBe(false);
    });

    it('read_file (not streamed): uses preview output and stores metadata', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'token', data: { text: '' } }, false);
        handleEvent({ type: 'tool_call', data: { tool_id: 'tc-read', tool_name: 'read_file', args: { path: 'large.go' } } }, false);

        // No tool_output events — read_file does not stream
        // tool_result with truncated preview
        handleEvent({
            type: 'tool_result',
            data: {
                tool_id: 'tc-read',
                success: true,
                output: 'package main\n\nimport "fmt"\n... (truncated)\n',
                duration: 5,
                result_truncated: true,
                original_lines: 500,
                original_bytes: 15000,
                streamed: false,
                message_id: 'msg-read-456',
            },
        }, false);

        const tc = stream.state.entries[0].toolCalls![0];
        expect(tc.result).toBe('package main\n\nimport "fmt"\n... (truncated)\n');
        expect(tc.resultTruncated).toBe(true);
        expect(tc.originalLines).toBe(500);
        expect(tc.originalBytes).toBe(15000);
        expect(tc.messageId).toBe('msg-read-456');
        expect(tc.isPending).toBe(false);
    });

    it('non-truncated tool (e.g. view_tasks): full output, no truncation fields', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'token', data: { text: '' } }, false);
        handleEvent({ type: 'tool_call', data: { tool_id: 'tc-view', tool_name: 'view_tasks', args: {} } }, false);

        handleEvent({
            type: 'tool_result',
            data: {
                tool_id: 'tc-view',
                success: true,
                output: 'Task 1: hello\nTask 2: world\n',
                duration: 10,
                // No truncation fields present
            },
        }, false);

        const tc = stream.state.entries[0].toolCalls![0];
        expect(tc.result).toBe('Task 1: hello\nTask 2: world\n');
        expect(tc.resultTruncated).toBeUndefined();
        expect(tc.originalLines).toBeUndefined();
        expect(tc.originalBytes).toBeUndefined();
        expect(tc.messageId).toBeUndefined();
        expect(tc.isPending).toBe(false);
    });

    it('streamed=true but tc.result is empty: falls back to preview', () => {
        // Client missed the tool_output stream — streamed flag is true but
        // nothing was accumulated. Should use the preview.
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'token', data: { text: '' } }, false);
        handleEvent({ type: 'tool_call', data: { tool_id: 'tc-miss', tool_name: 'bash', args: {} } }, false);
        // No tool_output events (missed the stream)

        handleEvent({
            type: 'tool_result',
            data: {
                tool_id: 'tc-miss',
                success: true,
                output: 'preview only',
                duration: 100,
                result_truncated: true,
                original_lines: 50,
                original_bytes: 2000,
                streamed: true,
                message_id: 'msg-miss-789',
            },
        }, false);

        const tc = stream.state.entries[0].toolCalls![0];
        expect(tc.result).toBe('preview only');
        expect(tc.resultTruncated).toBe(true);
        expect(tc.originalLines).toBe(50);
        expect(tc.messageId).toBe('msg-miss-789');
    });

    it('regression: DB-loaded pending tool call stores metadata', () => {
        // Simulates page refresh: entry loaded from DB with isPending=true,
        // but activeToolCalls is empty.
        const { stream, handleEvent } = setup();
        stream.state.entries = [{
            id: 'db-assistant', role: 'assistant', content: '', isStreaming: false,
            createdAt: new Date().toISOString(),
            toolCalls: [{
                toolId: 'db-tc1', toolName: 'bash', displayName: 'Execute',
                args: { command: 'make test', timeout: 60 }, raw_args: '{"command":"make test"}',
                isPending: true, startedAt: Date.now() - 10000,
            }],
        }];
        stream.state.activeToolCalls = new Map();

        handleEvent({
            type: 'tool_result',
            data: {
                tool_id: 'db-tc1',
                success: true,
                output: 'ok 123 tests',
                duration: 10000,
                result_truncated: true,
                original_lines: 200,
                original_bytes: 5000,
                streamed: false,
                message_id: 'msg-db-111',
            },
        }, false);

        const tc = stream.state.entries[0].toolCalls![0];
        expect(tc.isPending).toBe(false);
        expect(tc.result).toBe('ok 123 tests');
        expect(tc.success).toBe(true);
        expect(tc.duration).toBe(10000);
        expect(tc.resultTruncated).toBe(true);
        expect(tc.originalLines).toBe(200);
        expect(tc.originalBytes).toBe(5000);
        expect(tc.messageId).toBe('msg-db-111');
    });

    it('metadata fields only stored when present in event', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'token', data: { text: '' } }, false);
        handleEvent({ type: 'tool_call', data: { tool_id: 'tc-partial', tool_name: 'bash', args: {} } }, false);

        // Only some metadata fields present
        handleEvent({
            type: 'tool_result',
            data: {
                tool_id: 'tc-partial',
                success: true,
                output: 'partial meta',
                duration: 50,
                result_truncated: true,
                // original_lines, original_bytes, message_id absent
            },
        }, false);

        const tc = stream.state.entries[0].toolCalls![0];
        expect(tc.result).toBe('partial meta');
        expect(tc.resultTruncated).toBe(true);
        expect(tc.originalLines).toBeUndefined();
        expect(tc.originalBytes).toBeUndefined();
        expect(tc.messageId).toBeUndefined();
    });
});

describe('createStreamHandler', () => {
    function setup() {
        const cb = makeCallbacks();
        const stream = new StreamState();
        const handleEvent = createStreamHandler(stream, cb);
        return { cb, stream, handleEvent };
    }

    it('token event creates assistant entry and appends content', () => {
        const { cb, stream, handleEvent } = setup();
        handleEvent({ type: 'token', data: { text: 'Hello' } }, false);
        expect(stream.state.entries.length).toBe(1);
        expect(stream.state.entries[0].content).toBe('Hello');
        expect(cb.markStreaming).toHaveBeenCalled();
    });

    it('token event estimates tokens from batched text', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'token', data: { text: 'Hello world this is a batched token event' } }, false);
        // 41 chars / 4 ≈ 11 tokens
        expect(stream.state.streamTokenCount).toBe(11);
        // Second batched event adds more (17 chars / 4 ≈ 5)
        handleEvent({ type: 'token', data: { text: ' more tokens here' } }, false);
        expect(stream.state.streamTokenCount).toBe(16);
    });

    it('reasoning event estimates tokens from batched text', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'reasoning', data: { text: 'Let me think about this problem carefully' } }, false);
        expect(stream.state.streamTokenCount).toBe(11);
    });

    it('reasoning event appends to reasoningContent', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'reasoning', data: { text: 'Let me think' } }, false);
        expect(stream.state.entries[0].reasoningContent).toBe('Let me think');
    });

    it('tool_call event creates tool call entry', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'token', data: { text: '' } }, false);
        handleEvent({ type: 'tool_call', data: { tool_id: 'tc1', tool_name: 'read_file', args: { path: 'foo.ts' } } }, false);
        const entry = stream.state.entries[0];
        expect(entry.toolCalls).toHaveLength(1);
        expect(entry.toolCalls![0].toolId).toBe('tc1');
        expect(entry.toolCalls![0].isPending).toBe(true);
    });

    it('a tool_call or tool_progress frame that names no file keeps the file already known', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'tool_call', data: { tool_id: 'wf1', tool_name: 'write_file', args: null, path: 'src/file.go', raw_args: '{"path":"src/file.go","content":' } }, false);
        // What the server sends while the content is still generating: an empty path, then a frame
        // with no path key at all.
        handleEvent({ type: 'tool_progress', data: { tool_id: 'wf1', bytes_written: 512, path: '' } }, false);
        handleEvent({ type: 'tool_call', data: { tool_id: 'wf1', tool_name: 'write_file', args: null, raw_args_delta: 'more content' } }, false);

        expect(stream.state.entries[0].toolCalls![0].path).toBe('src/file.go');
    });

    it('a tool_call frame carrying no argument body keeps the arguments already streamed', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'tool_call', data: { tool_id: 'ed1', tool_name: 'edit_file', args: null, raw_args: '{"path":"src/file.go","edits":[{"oldText":"a' } }, false);
        // A re-broadcast of the stored tool_call row: neither `raw_args` nor `raw_args_delta`, which
        // said nothing about the body and must not drop the card back to naming no file.
        handleEvent({ type: 'tool_call', data: { tool_id: 'ed1', tool_name: 'edit_file', args: null } }, false);

        expect(stream.state.entries[0].toolCalls![0].raw_args).toBe('{"path":"src/file.go","edits":[{"oldText":"a');
    });

    it('regression: ask_question auto-answer re-broadcast updates resolved tool call', () => {
        // Flow for an auto-started subtask: the question tool_result arrives first,
        // then the backend re-broadcasts the auto-answer (from parent context) for the
        // same tool_id. The re-broadcast must overwrite the result in place even though
        // the tool call is no longer pending and no longer in activeToolCalls.
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'token', data: { text: '' } }, false);
        handleEvent({
            type: 'tool_call',
            data: { tool_id: 'qa1', tool_name: 'ask_question', args: { question: 'What is your favorite bird?' } },
        }, false);
        // Original result echoes the question back.
        handleEvent({
            type: 'tool_result',
            data: { tool_id: 'qa1', success: true, output: 'What is your favorite bird?', duration: 50 },
        }, false);
        // Auto-answer re-broadcast with the parent-context answer.
        handleEvent({
            type: 'tool_result',
            data: { tool_id: 'qa1', success: true, output: 'A peregrine falcon.', duration: 50, message_id: 'msg-qa-1' },
        }, false);
        const tc = stream.state.entries[0].toolCalls![0];
        expect(tc.result).toBe('A peregrine falcon.');
        expect(tc.isPending).toBe(false);
        expect(tc.messageId).toBe('msg-qa-1');
    });

    it('complete event closes assistant and sets stats', () => {
        const { cb, stream, handleEvent } = setup();
        handleEvent({ type: 'token', data: { text: 'Hi' } }, false);
        handleEvent({ type: 'complete', data: { state: 'idle', model_name: 'gpt-4', prompt_tokens: 100, predicted_tokens: 10, prompt_ms: 200, predicted_ms: 100, duration: 300, assistant_message_id: 'backend-id' } }, false);
        expect(stream.state.entries[0].isStreaming).toBe(false);
        expect(stream.state.entries[0].stats).toBeDefined();
        expect(cb.markNotStreaming).toHaveBeenCalled();
    });

    it('hint event uses the backend message id for the entry', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'hint', data: { id: 'hint-msg-1', role: 'user', content: 'Is the task complete?', created_at: '2026-01-01T00:00:00Z' } }, false);
        expect(stream.state.entries).toHaveLength(1);
        expect(stream.state.entries[0].role).toBe('user');
        expect(stream.state.entries[0].id).toBe('hint-msg-1');
        expect(stream.state.entries[0].content).toBe('Is the task complete?');
    });

    it('hint event with role system creates a system entry (not a user bubble)', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'hint', data: { id: 'retry-msg-1', role: 'system', content: 'Connection interrupted, retrying the request.', created_at: '2026-01-01T00:00:00Z' } }, false);
        expect(stream.state.entries).toHaveLength(1);
        expect(stream.state.entries[0].role).toBe('system');
        expect(stream.state.entries[0].id).toBe('retry-msg-1');
        expect(stream.state.entries[0].content).toContain('Connection interrupted');
    });

    it('hint event carries severity and kind onto the entry', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'hint', data: { id: 'mem-1', role: 'system', kind: 'memory_pressure', severity: 'warning', content: 'paused by the memory budget', created_at: '2026-01-01T00:00:00Z' } }, false);
        expect(stream.state.entries[0].severity).toBe('warning');
        expect(stream.state.entries[0].kind).toBe('memory_pressure');
    });

    it('hint event without an id falls back to genId', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'hint', data: { role: 'user', content: 'retry hint', created_at: '2026-01-01T00:00:00Z' } }, false);
        expect(stream.state.entries).toHaveLength(1);
        expect(stream.state.entries[0].id).toMatch(/^id-/); // genId() prefix from the test callback
        expect(stream.state.entries[0].content).toBe('retry hint');
    });

    it('hint event with an already-present id is skipped (replay dedup)', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'hint', data: { id: 'hint-msg-1', role: 'user', content: 'Is the task complete?', created_at: '2026-01-01T00:00:00Z' } }, false);
        // Replayed from the reconnect buffer — must not add a duplicate entry.
        handleEvent({ type: 'hint', data: { id: 'hint-msg-1', role: 'user', content: 'Is the task complete?', created_at: '2026-01-01T00:00:00Z' } }, false);
        expect(stream.state.entries).toHaveLength(1);
    });

    it('error event creates error entry', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'error', data: { message: 'Something failed', code: 'api_error' } }, false);
        const entry = stream.state.entries.find(e => e.role === 'error');
        expect(entry).toBeDefined();
        expect(entry!.content).toBe('Something failed');
    });

    it('error event keyed by the stored message id', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'error', data: { message: 'no route to host', code: 'llm_connection_failed', message_id: 'err-1' } }, false);
        expect(stream.state.entries[0].id).toBe('err-1');
    });

    it('replayed error event adds nothing', () => {
        const { cb, stream, handleEvent } = setup();
        const frame = { type: 'error' as const, data: { message: 'no route to host', code: 'llm_connection_failed', message_id: 'err-1' } };
        // The reconnect buffer and the task subscribe each replay the same frame.
        handleEvent(frame, false);
        handleEvent(frame, false);
        expect(stream.state.entries.filter(e => e.role === 'error')).toHaveLength(1);
        expect(cb.onNewError).toHaveBeenCalledTimes(1);
    });

    it('duplicate error entry loaded from history suppresses the replay', () => {
        const { stream, handleEvent } = setup();
        stream.state.entries = [{ id: 'err-1', role: 'error', content: 'no route to host', isStreaming: false, error: { code: 'llm_connection_failed', message: 'no route to host' }, createdAt: new Date().toISOString() }];
        handleEvent({ type: 'error', data: { message: 'no route to host', code: 'llm_connection_failed', message_id: 'err-1' } }, false);
        expect(stream.state.entries).toHaveLength(1);
    });

    it('same error twice in a row renders one entry, different messages keep both', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'error', data: { message: 'boom', code: 'agent_error' } }, false);
        handleEvent({ type: 'error', data: { message: 'boom', code: 'agent_error' } }, false);
        expect(stream.state.entries.filter(e => e.role === 'error')).toHaveLength(1);
        handleEvent({ type: 'error', data: { message: 'different', code: 'agent_error' } }, false);
        expect(stream.state.entries.filter(e => e.role === 'error')).toHaveLength(2);
    });

    it('queue event updates queue position', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'queue', data: { position: 3, estimated_wait_seconds: 30 } }, false);
        expect(stream.state.queuePosition).toBe(3);
        expect(stream.state.queueEstimatedWait).toBe(30);
        expect(stream.state.awaitingResponse).toBe(true);
    });

    it('stats event updates token count', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'stats', data: { model_name: 'gpt-4', prompt_tokens: 500, predicted_tokens: 50, predicted_ms: 200 } }, false);
        expect(stream.state.streamTokenCount).toBe(50);
        expect(stream.state.lastPromptTokens).toBe(500);
    });

    it('stats event corrects estimated token count from batched events', () => {
        const { stream, handleEvent } = setup();
        // Batched token events produce an estimate
        handleEvent({ type: 'token', data: { text: 'Hello world this is a batched token event' } }, false);
        const estimated = stream.state.streamTokenCount;
        // Stats event overrides with the real count from the LLM
        handleEvent({ type: 'stats', data: { model_name: 'gpt-4', prompt_tokens: 500, predicted_tokens: 15, predicted_ms: 200 } }, false);
        expect(stream.state.streamTokenCount).toBe(15);
        // The estimate (11 from 43 chars) differs from the actual LLM count (15)
        expect(estimated).toBe(11);
    });

    it('git_checkpoint_pending adds to pending set', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'git_checkpoint_pending', data: { message_id: 'msg-1' } }, false);
        expect(stream.state.pendingCheckpoints.has('msg-1')).toBe(true);
    });

    it('git_checkpoint_complete removes from pending and adds hash', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'git_checkpoint_pending', data: { message_id: 'msg-1' } }, false);
        handleEvent({ type: 'git_checkpoint_complete', data: { message_id: 'msg-1', commit_hash: 'abc123', label: 'Checkpoint 1' } }, false);
        expect(stream.state.pendingCheckpoints.has('msg-1')).toBe(false);
        expect(stream.state.checkpointLabels.get('msg-1')).toBe('Checkpoint 1');
        expect(stream.state.checkpointCommitHashes.get('msg-1')).toBe('abc123');
    });

    describe('a checkpoint captured while a later round is streaming', () => {
        /** The band the question after this turn will wear. A turn has no band of its own, so a capture in
         * flight is read through the scan the next question triggers: the hash has to land on the id that
         * entry carries once `complete` renames it to its row. */
        function bandOf(stream: StreamState): CheckpointState | null {
            const entries: ChatEntry[] = [...stream.state.entries,
                { id: 'next-question', role: 'user', content: '', isStreaming: false, createdAt: '' }];
            return buildCheckpointStateMap(entries, {
                pendingCheckpoints: stream.state.pendingCheckpoints,
                checkpointLabels: stream.state.checkpointLabels,
                checkpointCommitHashes: stream.state.checkpointCommitHashes,
            }).get(entries.length - 1) ?? null;
        }

        /** Client ids for streaming entries, so a rename to a row id is visible in the assertions. */
        function setupLive() {
            let n = 0;
            const stream = new StreamState();
            const handleEvent = createStreamHandler(stream, makeCallbacks({ genId: () => `c${++n}` }));
            const band = () => bandOf(stream);
            // The frames name the captured row, not the entry on screen.
            const captureRoundOneThenStreamTwo = (handleEvent: ReturnType<typeof createStreamHandler>) => {
                handleEvent({ type: 'token', data: { text: 'round one' } }, false);
                handleEvent({ type: 'tool_call', data: { tool_id: 'tc1', tool_name: 'write_file', args: {} } }, false);
                handleEvent({ type: 'tool_result', data: { tool_id: 'tc1', success: true, output: 'ok' } }, false);
                handleEvent({ type: 'git_checkpoint_pending', data: { message_id: 'A1' } }, false);
                handleEvent({ type: 'token', data: { text: 'round two' } }, false);
                handleEvent({ type: 'git_checkpoint_complete', data: { message_id: 'A1', commit_hash: 'h1', label: 'Wrote the file' } }, false);
            };
            return { stream, handleEvent, band, captureRoundOneThenStreamTwo };
        }

        const completeTurn = (assistant_message_id: string) =>
            ({ type: 'complete', data: { state: 'review', assistant_message_id } }) as never;

        it('names the newest capture from the first one through the end of the turn', () => {
            const { handleEvent, band, captureRoundOneThenStreamTwo } = setupLive();
            handleEvent({ type: 'user_message', data: { id: 'u1', role: 'user', content: 'do it' } }, false);
            captureRoundOneThenStreamTwo(handleEvent);
            expect(band()?.commitHash).toBe('h1');

            handleEvent({ type: 'tool_call', data: { tool_id: 'tc2', tool_name: 'bash', args: {} } }, false);
            handleEvent({ type: 'tool_result', data: { tool_id: 'tc2', success: true, output: 'ok' } }, false);
            handleEvent({ type: 'token', data: { text: 'round three' } }, false);
            handleEvent({ type: 'git_checkpoint_complete', data: { message_id: 'A2', commit_hash: 'h2' } }, false);
            expect(band()?.commitHash).toBe('h2');

            handleEvent(completeTurn('A3'), false);
            // A turn that has captured nothing yet is named by the newest state that exists.
            handleEvent({ type: 'token', data: { text: 'next turn' } }, false);
            handleEvent({ type: 'tool_call', data: { tool_id: 'tc3', tool_name: 'write_file', args: {} } }, false);
            expect(band()?.commitHash).toBe('h2');
        });

        it('is still the band after the turn ends on the entry that carried it', () => {
            // Left behind by the rename, the hash would name an entry no longer on screen.
            const { stream, handleEvent, band, captureRoundOneThenStreamTwo } = setupLive();
            captureRoundOneThenStreamTwo(handleEvent);
            expect(band()?.commitHash).toBe('h1');

            handleEvent(completeTurn('A2'), false);

            expect(stream.state.entries[stream.state.entries.length - 1].id).toBe('A2');
            expect(band()?.commitHash).toBe('h1');
            expect(stream.state.checkpointCommitHashes.has('c2')).toBe(false);
        });

        it('leaves a hash the row already carries alone when the entry is renamed to it', () => {
            const { stream, handleEvent, captureRoundOneThenStreamTwo } = setupLive();
            stream.state.checkpointCommitHashes = new Map([['A2', 'from-row']]);
            captureRoundOneThenStreamTwo(handleEvent);

            handleEvent(completeTurn('A2'), false);

            expect(stream.state.checkpointCommitHashes.get('A2')).toBe('from-row');
            expect(stream.state.checkpointCommitHashes.has('c2')).toBe(false);
        });

        it('drops the alias when a read already put the row on screen', () => {
            // Nothing reads a key whose entry is gone, so it would only accumulate.
            const { stream, handleEvent } = setupLive();
            handleEvent({ type: 'token', data: { text: 'round one' } }, false);
            handleEvent({ type: 'git_checkpoint_complete', data: { message_id: 'A1', commit_hash: 'h1' } }, false);
            stream.state.entries = [...stream.state.entries, { id: 'A1', role: 'assistant' as const, content: 'the row', isStreaming: false, createdAt: new Date().toISOString() }];

            handleEvent(completeTurn('A1'), false);

            expect(stream.state.entries.map(e => e.id)).toEqual(['A1']);
            expect(stream.state.checkpointCommitHashes.has('c1')).toBe(false);
            expect(stream.state.checkpointCommitHashes.get('A1')).toBe('h1');
        });
    });

    it('suppressEvents skips token events', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'token', data: { text: 'Hello' } }, true);
        expect(stream.state.entries.length).toBe(0);
    });

    describe('a running frame and the calls it supersedes', () => {
        it('re-draws a call whose card it discarded, because its announcement comes again', () => {
            const { stream, handleEvent } = setup();
            // An entry that is only the tool call.
            handleEvent({ type: 'tool_call', data: { tool_id: 'tc1', tool_name: 'bash', args: {} } }, false);
            handleEvent({ type: 'status', data: { state: 'running' } }, false);
            expect(stream.state.entries.length).toBe(0);

            // The announcement arrives again, and a card is owed.
            handleEvent({ type: 'tool_call', data: { tool_id: 'tc1', tool_name: 'bash', args: { command: 'make' } } }, false);
            const ids = stream.state.entries.flatMap(e => (e.toolCalls ?? []).map(tc => tc.toolId));
            expect(ids).toEqual(['tc1']);
        });

        it('still draws one card for a call that survives the frame and is announced twice', () => {
            const { stream, handleEvent } = setup();
            handleEvent({ type: 'token', data: { text: 'Working on it.' } }, false);
            handleEvent({ type: 'tool_call', data: { tool_id: 'tc1', tool_name: 'bash', args: {} } }, false);
            // The entry also holds text, so a running frame leaves it standing.
            handleEvent({ type: 'status', data: { state: 'running' } }, false);
            handleEvent({ type: 'tool_call', data: { tool_id: 'tc1', tool_name: 'bash', args: {} } }, false);
            const ids = stream.state.entries.flatMap(e => (e.toolCalls ?? []).map(tc => tc.toolId));
            expect(ids).toEqual(['tc1']);
        });

        it('leaves standing a call the database has a row for, which this frame says nothing about', () => {
            const { stream, handleEvent } = setup();
            // What a history read draws for a silent turn whose tool is still running. The announcement of
            // that call is something this screen never saw: it is broadcast once, and the replay lost it.
            const messages = [
                { id: 'u1', role: 'user', content: 'run it', created_at: '2026-09-10T10:00:00Z' },
                { id: 'a1', role: 'assistant', content: '', created_at: '2026-09-10T10:00:01Z' },
                {
                    id: 'm1', role: 'tool_call', content: '', created_at: '2026-09-10T10:00:02Z',
                    tool_calls: JSON.stringify({ tool_id: 'tc1', tool_name: 'bash', args: { command: 'make' } }),
                },
            ];
            stream.state.entries = mergeLoadedHistory(
                buildEntriesFromMessages(messages).entries, messages.map(m => m.id), [], true);
            const ids = () => stream.state.entries.flatMap(e => (e.toolCalls ?? []).map(tc => tc.toolId));
            expect(ids()).toEqual(['tc1']);

            // The turn's replay reaches the screen after the rows that describe it.
            handleEvent({ type: 'status', data: { state: 'running' } }, false);

            expect(ids()).toEqual(['tc1']);
        });

        it('reports through onDiverged the card it discarded, which no later frame can redraw', () => {
            const { cb, handleEvent } = setup();
            handleEvent({ type: 'tool_call', data: { tool_id: 'tc1', tool_name: 'bash', args: {} } }, false);
            handleEvent({ type: 'status', data: { state: 'running' } }, false);
            expect(cb.onDiverged).toHaveBeenCalledTimes(1);
        });

        it('reports nothing to a running frame that discarded no card', () => {
            const { cb, handleEvent } = setup();
            handleEvent({ type: 'token', data: { text: 'Working on it.' } }, false);
            handleEvent({ type: 'tool_call', data: { tool_id: 'tc1', tool_name: 'bash', args: {} } }, false);
            handleEvent({ type: 'status', data: { state: 'running' } }, false);
            expect(cb.onDiverged).not.toHaveBeenCalled();
        });
    });

    it('diff_changed calls onRefreshDiff callback', () => {
        const { cb, handleEvent } = setup();
        handleEvent({ type: 'diff_changed', data: { files: {} } }, false);
        expect(cb.onRefreshDiff).toHaveBeenCalled();
    });

    it('tool_call dedup via processedToolIds', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'token', data: { text: '' } }, false);
        handleEvent({ type: 'tool_call', data: { tool_id: 'tc1', tool_name: 'read_file', args: {} } }, false);
        // Duplicate tool_call with same ID should be skipped
        handleEvent({ type: 'tool_call', data: { tool_id: 'tc1', tool_name: 'read_file', args: {} } }, false);
        expect(stream.state.entries[0].toolCalls).toHaveLength(1);
    });

    it('status:running clears pending tool calls', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'token', data: { text: '' } }, false);
        handleEvent({ type: 'tool_call', data: { tool_id: 'tc1', tool_name: 'read_file', args: {} } }, false);
        handleEvent({ type: 'status', data: { state: 'running' } }, false);
        // Entry with only pending tool calls should be removed
        expect(stream.state.entries.filter(e => e.toolCalls?.some(tc => tc.isPending))).toHaveLength(0);
    });

    it('status:running sets awaitingResponse so the awaiting indicator shows', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'status', data: { state: 'running' } }, false);
        expect(stream.state.awaitingResponse).toBe(true);
    });

    it('a retry hint arms awaitingResponse and names the interruption without reading its text', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'hint', data: { id: 'retry-1', role: 'system', content: 'Connection interrupted, retrying the request.', kind: 'retry', error: 'timeout' } }, false);
        expect(stream.state.awaitingResponse).toBe(true);
        expect(stream.state.retryCount).toBe(1);
        expect(stream.state.retryError).toBe('timeout');
        expect(stream.state.entries).toHaveLength(1);
    });

    it('a retry discards the half-written turn the dead call streamed', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'token', data: { text: 'half a sent' } }, false);
        handleEvent({ type: 'tool_call', data: { tool_id: 'tc1', tool_name: 'bash', args: {} } }, false);
        expect(stream.state.activeToolCalls.size).toBe(1);
        handleEvent({ type: 'hint', data: { id: 'retry-1', role: 'system', content: 'Connection interrupted, retrying the request.', kind: 'retry', error: 'EOF' } }, false);
        const partial = stream.state.entries.filter(e => e.role === 'assistant');
        expect(partial).toHaveLength(0);
        expect(stream.state.activeToolCalls.size).toBe(0);
    });

    it('the answer that arrives on the retry replaces the discarded text and clears the retry line', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'token', data: { text: 'half a sent' } }, false);
        handleEvent({ type: 'hint', data: { id: 'retry-1', role: 'system', content: 'Connection interrupted, retrying the request.', kind: 'retry', error: 'EOF' } }, false);
        handleEvent({ type: 'token', data: { text: 'The full answer.' } }, false);
        expect(stream.state.entries.filter(e => e.role === 'assistant').map(e => e.content)).toEqual(['The full answer.']);
        expect(stream.state.retryCount).toBe(0);
        expect(stream.state.retryError).toBe('');
        expect(stream.state.awaitingResponse).toBe(false);
    });

    it('first token after status:running clears awaitingResponse', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'status', data: { state: 'running' } }, false);
        expect(stream.state.awaitingResponse).toBe(true);
        handleEvent({ type: 'token', data: { text: 'Hello' } }, false);
        expect(stream.state.awaitingResponse).toBe(false);
    });

    it('terminal statuses after running clear awaitingResponse', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'status', data: { state: 'running' } }, false);
        expect(stream.state.awaitingResponse).toBe(true);
        handleEvent({ type: 'status', data: { state: 'idle' } }, false);
        expect(stream.state.awaitingResponse).toBe(false);
    });

    it('queue position resets once the first token of the granted call arrives', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'queue', data: { position: 3, estimated_wait_seconds: 30 } }, false);
        expect(stream.state.queuePosition).toBe(3);
        // Gate slot granted — tokens start flowing, the task is no longer queued.
        handleEvent({ type: 'token', data: { text: 'Hello' } }, false);
        expect(stream.state.queuePosition).toBe(0);
        expect(stream.state.queueEstimatedWait).toBe(0);
        expect(stream.state.awaitingResponse).toBe(false);
    });

    it('status:running does not arm awaitingResponse while a response is actively streaming', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'token', data: { text: 'Hello' } }, false);
        expect(stream.state.currentAssistantId).not.toBeNull();
        // Mid-stream status (e.g. "Repetitive response detected") must not flash the indicator.
        handleEvent({ type: 'status', data: { state: 'running', activity: 'Repetitive response detected' } }, false);
        expect(stream.state.awaitingResponse).toBe(false);
    });

    it('status:running does not arm awaitingResponse while tool calls are in flight', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'token', data: { text: '' } }, false);
        handleEvent({ type: 'tool_call', data: { tool_id: 'tc1', tool_name: 'bash', args: {} } }, false);
        expect(stream.state.activeToolCalls.size).toBe(1);
        handleEvent({ type: 'status', data: { state: 'running', activity: 'Sandbox recreated for mode change (act)' } }, false);
        expect(stream.state.awaitingResponse).toBe(false);
    });

    it('a retry hint arms awaitingResponse even while a response is streaming', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'token', data: { text: 'Hello' } }, false);
        // A retried LLM call is a genuinely pending request — arm unconditionally.
        handleEvent({ type: 'hint', data: { id: 'retry-1', role: 'system', content: 'Connection interrupted, retrying the request.', kind: 'retry', error: 'timeout' } }, false);
        expect(stream.state.awaitingResponse).toBe(true);
        expect(stream.state.retryCount).toBe(1);
    });

    it('complete clears a retry line the turn never recovered from', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'hint', data: { id: 'retry-1', role: 'system', content: 'Connection interrupted, retrying the request.', kind: 'retry', error: 'EOF' } }, false);
        handleEvent({ type: 'complete', data: { state: 'idle' } }, false);
        expect(stream.state.retryCount).toBe(0);
        expect(stream.state.retryError).toBe('');
    });

    it('tool_result resolves pending tool call via activeToolCalls lookup', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'token', data: { text: '' } }, false);
        handleEvent({ type: 'tool_call', data: { tool_id: 'tc1', tool_name: 'read_file', args: {} } }, false);
        // Verify the toolCall reference is stored in activeToolCalls
        const ref = stream.state.activeToolCalls.get('tc1');
        expect(ref).toBeDefined();
        expect(ref!.toolCall.toolId).toBe('tc1');
        expect(ref!.toolCall.isPending).toBe(true);
        // tool_result resolves via activeToolCalls lookup, replaces object in entry for reactivity
        handleEvent({ type: 'tool_result', data: { tool_id: 'tc1', success: true, output: 'content', duration: 50 } }, false);
        // Entry's toolCall is a new object (replaced for Svelte 5 reactivity)
        const tc = stream.state.entries[0].toolCalls![0];
        expect(tc.result).toBe('content');
        expect(tc.isPending).toBe(false);
        expect(tc.success).toBe(true);
        expect(tc.duration).toBe(50);
        expect(stream.state.activeToolCalls.has('tc1')).toBe(false);
    });

    it('tool_output accumulates via activeToolCalls lookup', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'token', data: { text: '' } }, false);
        handleEvent({ type: 'tool_call', data: { tool_id: 'tc2', tool_name: 'bash', args: {} } }, false);
        // Streaming output via activeToolCalls lookup, replaces object in entry for reactivity
        handleEvent({ type: 'tool_output', data: { tool_id: 'tc2', text: 'line1' } }, false);
        handleEvent({ type: 'tool_output', data: { tool_id: 'tc2', text: 'line2' } }, false);
        const tc = stream.state.entries[0].toolCalls![0];
        expect(tc.result).toBe('line1line2');
        expect(tc.isPending).toBe(true); // still pending until tool_result
    });

    it('tool_progress with tool_id uses O(1) direct reference', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'token', data: { text: '' } }, false);
        handleEvent({ type: 'tool_call', data: { tool_id: 'tc3', tool_name: 'write_file', args: {} } }, false);
        // Progress via direct reference
        handleEvent({ type: 'tool_progress', data: { tool_id: 'tc3', bytes_written: 42, lines_added: 5 } }, false);
        const tc = stream.state.entries[0].toolCalls![0];
        expect(tc.bytesWritten).toBe(42);
        expect(tc.linesAdded).toBe(5);
    });

    it('tool_result ignores stale tool_id not in activeToolCalls', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'token', data: { text: '' } }, false);
        // Send result for a tool call that was never registered
        handleEvent({ type: 'tool_result', data: { tool_id: 'ghost', success: true, output: 'nope', duration: 0 } }, false);
        // No entries should have been modified
        expect(stream.state.entries[0].toolCalls).toBeUndefined();
    });

    it('tool_result resolves DB-loaded pending tool call not in activeToolCalls', () => {
        // Simulates page refresh: entry loaded from DB with isPending=true,
        // but activeToolCalls is empty (fresh StreamState).
        const { stream, handleEvent } = setup();
        stream.state.entries = [{
            id: 'db-assistant', role: 'assistant', content: '', isStreaming: false,
            createdAt: new Date().toISOString(),
            toolCalls: [{
                toolId: 'db-tc1', toolName: 'bash', displayName: 'Execute',
                args: { command: 'sleep 30s', timeout: 30 }, raw_args: '{"command":"sleep 30s","timeout":30}',
                isPending: true, startedAt: Date.now() - 5000,
            }],
        }];
        // activeToolCalls is empty — simulates post-refresh state
        stream.state.activeToolCalls = new Map();
        // tool_result arrives from WebSocket
        handleEvent({ type: 'tool_result', data: { tool_id: 'db-tc1', success: true, output: 'done', duration: 5000 } }, false);
        // Fallback search should find and resolve the pending tool call
        const tc = stream.state.entries[0].toolCalls![0];
        expect(tc.isPending).toBe(false);
        expect(tc.result).toBe('done');
        expect(tc.success).toBe(true);
        expect(tc.duration).toBe(5000);
    });

    it('tool_output ignores unknown tool_id not in activeToolCalls', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'token', data: { text: '' } }, false);
        handleEvent({ type: 'tool_call', data: { tool_id: 'tc1', tool_name: 'read_file', args: {} } }, false);
        // Output for unknown tool_id is silently ignored
        handleEvent({ type: 'tool_output', data: { tool_id: 'unknown', text: 'leak' } }, false);
        const tc = stream.state.entries[0].toolCalls![0];
        expect(tc.result).toBeUndefined();
    });

    it('activeToolCalls stores reference that stays in sync with entry', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'token', data: { text: '' } }, false);
        handleEvent({ type: 'tool_call', data: { tool_id: 'tc1', tool_name: 'read_file', args: {} } }, false);
        const ref = stream.state.activeToolCalls.get('tc1');
        const entryTc = stream.state.entries[0].toolCalls![0];
        // Mutating via the map reference updates the entry (same underlying object behind $state proxy)
        ref!.toolCall.displayName = 'Overridden';
        expect(entryTc.displayName).toBe('Overridden');
        // The map reference has the same initial data as the entry
        expect(ref!.toolCall.toolId).toBe(entryTc.toolId);
        expect(ref!.toolCall.toolName).toBe(entryTc.toolName);
    });

    describe('allocation optimizations', () => {
        it('tool_output mutates in-place without replacing toolCalls array', () => {
            const { stream, handleEvent } = setup();
            handleEvent({ type: 'token', data: { text: '' } }, false);
            handleEvent({ type: 'tool_call', data: { tool_id: 'tc2', tool_name: 'bash', args: {} } }, false);
            const toolCallsRef = stream.state.entries[0].toolCalls!;
            handleEvent({ type: 'tool_output', data: { tool_id: 'tc2', text: 'line1' } }, false);
            handleEvent({ type: 'tool_output', data: { tool_id: 'tc2', text: 'line2' } }, false);
            // Array reference unchanged
            expect(stream.state.entries[0].toolCalls).toBe(toolCallsRef);
            expect(stream.state.entries[0].toolCalls![0].result).toBe('line1line2');
        });

        it('tool_progress mutates in-place without replacing toolCalls array', () => {
            const { stream, handleEvent } = setup();
            handleEvent({ type: 'token', data: { text: '' } }, false);
            handleEvent({ type: 'tool_call', data: { tool_id: 'tc3', tool_name: 'write_file', args: {} } }, false);
            const toolCallsRef = stream.state.entries[0].toolCalls!;
            handleEvent({ type: 'tool_progress', data: { tool_id: 'tc3', bytes_written: 42 } }, false);
            // Array reference unchanged
            expect(stream.state.entries[0].toolCalls).toBe(toolCallsRef);
            expect(stream.state.entries[0].toolCalls![0].bytesWritten).toBe(42);
        });

        it('tool_progress broadcast mutates all active tool calls in-place', () => {
            const { stream, handleEvent } = setup();
            handleEvent({ type: 'token', data: { text: '' } }, false);
            handleEvent({ type: 'tool_call', data: { tool_id: 'tcA', tool_name: 'write_file', args: {} } }, false);
            handleEvent({ type: 'tool_call', data: { tool_id: 'tcB', tool_name: 'edit_file', args: {} } }, false);
            const toolCallsRef = stream.state.entries[0].toolCalls!;
            // Broadcast progress (no tool_id)
            handleEvent({ type: 'tool_progress', data: { bytes_written: 100 } }, false);
            // Array reference unchanged
            expect(stream.state.entries[0].toolCalls).toBe(toolCallsRef);
            expect(stream.state.entries[0].toolCalls![0].bytesWritten).toBe(100);
            expect(stream.state.entries[0].toolCalls![1].bytesWritten).toBe(100);
        });

        it('token events do not replace entries array (push in-place)', () => {
            const { stream, handleEvent } = setup();
            // First token creates the entry
            handleEvent({ type: 'token', data: { text: 'Hello' } }, false);
            const entriesRef = stream.state.entries;
            // Subsequent tokens should NOT replace the array
            handleEvent({ type: 'token', data: { text: ' world' } }, false);
            expect(stream.state.entries).toBe(entriesRef);
            expect(stream.state.entries[0].content).toBe('Hello world');
        });
    });

    describe('sandbox build progress consolidation', () => {
        function setup() {
            const cb = makeCallbacks();
            const stream = new StreamState();
            const handleEvent = createStreamHandler(stream, cb);
            return { cb, stream, handleEvent };
        }

        it('status:building_image creates a single buildLog entry (not a plain system message)', () => {
            const { stream, handleEvent } = setup();
            handleEvent({ type: 'status', data: { state: 'building_image', activity: 'Building sandbox image from Dockerfile' } }, false);
            expect(stream.state.entries).toHaveLength(1);
            const entry = stream.state.entries[0];
            expect(entry.role).toBe('system');
            expect(entry.buildLog).toBeDefined();
            expect(entry.buildLog!.title).toBe('Building sandbox image from Dockerfile...');
            expect(entry.buildLog!.lines).toEqual([]);
        });

        it('build_progress lines accumulate into a single entry', () => {
            const { stream, handleEvent } = setup();
            handleEvent({ type: 'status', data: { state: 'building_image' } }, false);
            handleEvent({ type: 'build_progress', data: { line: 'Step 1/5 : FROM node:20' } }, false);
            handleEvent({ type: 'build_progress', data: { line: 'Step 2/5 : RUN npm ci' } }, false);
            handleEvent({ type: 'build_progress', data: { line: 'Step 3/5 : COPY . .' } }, false);
            expect(stream.state.entries).toHaveLength(1);
            expect(stream.state.entries[0].buildLog!.lines).toEqual([
                'Step 1/5 : FROM node:20',
                'Step 2/5 : RUN npm ci',
                'Step 3/5 : COPY . .',
            ]);
        });

        it('build_progress without a prior status creates the box entry on demand', () => {
            const { stream, handleEvent } = setup();
            handleEvent({ type: 'build_progress', data: { line: 'Step 1/5 : FROM node:20' } }, false);
            handleEvent({ type: 'build_progress', data: { line: 'Step 2/5 : RUN npm ci' } }, false);
            expect(stream.state.entries).toHaveLength(1);
            expect(stream.state.entries[0].buildLog!.lines).toHaveLength(2);
        });

        it('status:pulling_image creates a buildLog entry with the pull title', () => {
            const { stream, handleEvent } = setup();
            handleEvent({ type: 'status', data: { state: 'pulling_image', activity: 'Pulling ubuntu:24.04' } }, false);
            handleEvent({ type: 'build_progress', data: { line: 'Pulling fs layer...' } }, false);
            expect(stream.state.entries).toHaveLength(1);
            expect(stream.state.entries[0].buildLog!.title).toBe('Pulling ubuntu:24.04...');
            expect(stream.state.entries[0].buildLog!.lines).toEqual(['Pulling fs layer...']);
        });

        it('status:running marks the active buildLog entry as done', () => {
            const { stream, handleEvent } = setup();
            handleEvent({ type: 'status', data: { state: 'building_image' } }, false);
            handleEvent({ type: 'build_progress', data: { line: 'naming to sandbox-image' } }, false);
            handleEvent({ type: 'status', data: { state: 'running', activity: 'Sandbox container started' } }, false);
            expect(stream.state.entries).toHaveLength(1);
            expect(stream.state.entries[0].buildLog!.done).toBe(true);
        });

        it('a new build after a completed one starts a fresh box entry', () => {
            const { stream, handleEvent } = setup();
            handleEvent({ type: 'status', data: { state: 'building_image' } }, false);
            handleEvent({ type: 'build_progress', data: { line: 'first build line' } }, false);
            handleEvent({ type: 'status', data: { state: 'running' } }, false);
            // Second build on a later task run
            handleEvent({ type: 'status', data: { state: 'building_image' } }, false);
            handleEvent({ type: 'build_progress', data: { line: 'second build line' } }, false);
            expect(stream.state.entries).toHaveLength(2);
            expect(stream.state.entries[0].buildLog!.done).toBe(true);
            expect(stream.state.entries[1].buildLog!.done).toBeUndefined();
            expect(stream.state.entries[1].buildLog!.lines).toEqual(['second build line']);
        });

        it('build_progress caps the line count to the last N lines', () => {
            const { stream, handleEvent } = setup();
            handleEvent({ type: 'status', data: { state: 'building_image' } }, false);
            for (let i = 0; i < 250; i++) {
                handleEvent({ type: 'build_progress', data: { line: `line ${i}` } }, false);
            }
            expect(stream.state.entries[0].buildLog!.lines).toHaveLength(200);
            expect(stream.state.entries[0].buildLog!.lines[0]).toBe('line 50');
            expect(stream.state.entries[0].buildLog!.lines[199]).toBe('line 249');
        });

        it('setup_failed error marks the active buildLog entry done (stops the spinner)', () => {
            const { stream, handleEvent } = setup();
            handleEvent({ type: 'status', data: { state: 'building_image' } }, false);
            handleEvent({ type: 'build_progress', data: { line: 'ERROR: failed to solve' } }, false);
            handleEvent({ type: 'error', data: { code: 'setup_failed', message: 'failed to build sandbox image' } }, false);
            expect(stream.state.entries[0].buildLog!.done).toBe(true);
        });
    });
});



// A history reload (loadHistory) replaces the streaming buffer entry with a
// DB-loaded one: new entry id (message UUID), SAME tool_id. Every activeToolCalls
// ref captured while streaming therefore points at a detached copy. These are the
// "bash card shows Running although the command finished" regressions.
describe('stale activeToolCalls ref after a history reload', () => {
    function setup() {
        const cb = makeCallbacks();
        const stream = new StreamState();
        const handleEvent = createStreamHandler(stream, cb);
        return { cb, stream, handleEvent };
    }

    /** Mirror what loadHistory() does to an in-flight tool call's entry. */
    function simulateHistoryReload(stream: StreamState, toolId: string, toolName = 'bash') {
        stream.state.entries = [{
            id: 'db-msg-1', role: 'assistant', content: '', isStreaming: false,
            createdAt: new Date().toISOString(),
            toolCalls: [{ toolId, toolName, args: { command: 'npm test' }, isPending: true, startedAt: Date.now(), messageId: 'db-msg-1' }],
        }];
        stream.state.currentAssistantId = null;
    }

    function startBash(handleEvent: ReturnType<typeof createStreamHandler>) {
        handleEvent({ type: 'token', data: { text: '' } }, false);
        handleEvent({ type: 'tool_call', data: { tool_id: 'tc1', tool_name: 'bash', args: { command: 'npm test' } } }, false);
    }

    it('regression: tool_result clears pending when the entry was replaced by the DB entry', () => {
        const { stream, handleEvent } = setup();
        startBash(handleEvent);
        simulateHistoryReload(stream, 'tc1');

        handleEvent({ type: 'tool_result', data: { tool_id: 'tc1', success: true, output: '23 passing', duration: 4200, streamed: true, message_id: 'db-msg-1' } }, false);

        const tc = stream.state.entries[0].toolCalls![0];
        expect(tc.isPending).toBe(false);          // ← the card stops saying "Running"
        expect(tc.result).toBe('23 passing');
        expect(tc.duration).toBe(4200);
        expect(stream.state.activeToolCalls.has('tc1')).toBe(false);
    });

    it('regression: tool_output after a reload reaches the visible card instead of a detached copy', () => {
        const { stream, handleEvent } = setup();
        startBash(handleEvent);
        simulateHistoryReload(stream, 'tc1');

        handleEvent({ type: 'tool_output', data: { tool_id: 'tc1', text: 'building...\n' } }, false);
        handleEvent({ type: 'tool_output', data: { tool_id: 'tc1', text: 'done.\n' } }, false);

        expect(stream.state.entries[0].toolCalls![0].result).toBe('building...\ndone.\n');
    });

    it('regression: streamed output survives the tool_result preview', () => {
        const { stream, handleEvent } = setup();
        startBash(handleEvent);
        simulateHistoryReload(stream, 'tc1');

        handleEvent({ type: 'tool_output', data: { tool_id: 'tc1', text: 'full build log\n' } }, false);
        handleEvent({ type: 'tool_result', data: { tool_id: 'tc1', success: true, output: '…preview…', duration: 10, streamed: true } }, false);

        const tc = stream.state.entries[0].toolCalls![0];
        expect(tc.result).toBe('full build log\n');
        expect(tc.isPending).toBe(false);
    });

    it('tool_progress after a reload still updates the visible card', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'tool_call', data: { tool_id: 'wf1', tool_name: 'write_file', args: { path: 'a.ts' } } }, false);
        simulateHistoryReload(stream, 'wf1', 'write_file');

        handleEvent({ type: 'tool_progress', data: { tool_id: 'wf1', bytes_written: 4096 } }, false);

        expect(stream.state.entries[0].toolCalls![0].bytesWritten).toBe(4096);
    });

    it('a tool_call update after a reload updates the DB-loaded card in place (no duplicate)', () => {
        const { stream, handleEvent } = setup();
        startBash(handleEvent);
        simulateHistoryReload(stream, 'tc1');

        handleEvent({ type: 'tool_call', data: { tool_id: 'tc1', tool_name: 'bash', args: { command: 'npm test', timeout: 120 }, raw_args: '{"command":"npm test","timeout":120}' } }, false);

        expect(stream.state.entries).toHaveLength(1);
        expect(stream.state.entries[0].id).toBe('db-msg-1');
        expect(stream.state.entries[0].toolCalls).toHaveLength(1);
        expect(stream.state.entries[0].toolCalls![0].args).toEqual({ command: 'npm test', timeout: 120 });
    });

    it('reconcileActiveToolCalls re-points refs at the live entries', () => {
        const { stream, handleEvent } = setup();
        startBash(handleEvent);
        simulateHistoryReload(stream, 'tc1');

        stream.reconcileActiveToolCalls();

        expect(stream.state.activeToolCalls.get('tc1')?.entryId).toBe('db-msg-1');
        // And the re-pointed ref is usable: output lands on the live card.
        handleEvent({ type: 'tool_output', data: { tool_id: 'tc1', text: 'x\n' } }, false);
        expect(stream.state.entries[0].toolCalls![0].result).toBe('x\n');
    });

    it('reconcileActiveToolCalls drops refs whose tool call is gone from the entries', () => {
        const { stream, handleEvent } = setup();
        startBash(handleEvent);
        // loadHistory removes pending tool calls of a non-running task entirely.
        stream.state.entries = [{ id: 'db-msg-9', role: 'user', content: 'hi', isStreaming: false, createdAt: new Date().toISOString() }];

        stream.reconcileActiveToolCalls();

        expect(stream.state.activeToolCalls.size).toBe(0);
        // A late result for a vanished tool call must not resurrect it or throw.
        handleEvent({ type: 'tool_result', data: { tool_id: 'tc1', success: true, output: 'late', duration: 1 } }, false);
        expect(stream.state.entries.find(e => e.toolCalls?.length)).toBeUndefined();
    });
});

describe('advisory events', () => {
    function setup() {
        const cb = makeCallbacks();
        const stream = new StreamState();
        return { cb, stream, handleEvent: createStreamHandler(stream, cb) };
    }

    it('renders as a note, never as a user message', () => {
        const { stream, handleEvent } = setup();
        handleEvent({
            type: 'advisory',
            data: { id: 'adv-1', kind: 'diagnostics', severity: 'error', text: 'LSP diagnostics: 3 errors in 2 files', created_at: '2026-01-01T00:00:00Z' },
        }, false);

        expect(stream.state.entries.length).toBe(1);
        const entry = stream.state.entries[0];
        expect(entry.role).toBe('advisory');
        expect(entry.severity).toBe('error');
        // kind is what the card is titled with, so it has to survive the event → entry hop
        // (and the row's kind column) or a reload silently retitles the note.
        expect(entry.kind).toBe('diagnostics');
        expect(entry.content).toContain('3 errors in 2 files');
        // The row's own timestamp, not the moment the frame was handled.
        expect(entry.createdAt).toBe('2026-01-01T00:00:00Z');
        expect(entry.isStreaming).toBe(false);
        // The whole point: nothing here may look like something the user said.
        expect(stream.state.entries.some(e => e.role === 'user')).toBe(false);
    });

    it('is deduplicated by id (history reload must not double-render)', () => {
        const { stream, handleEvent } = setup();
        const event = { type: 'advisory' as const, data: { id: 'adv-1', severity: 'warning', text: 'Merge conflict in b.go' } };
        handleEvent(event, false);
        handleEvent(event, false);
        expect(stream.state.entries.length).toBe(1);
    });

    it('ends the card the turn is over: later output opens a card BELOW the note', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'token', data: { text: 'I will fix it.' } }, false);
        // The note is flushed at the turn boundary, and the executor writes its row
        // BETWEEN the turn's rows and the next assistant row — so the live view must
        // break the card here too, or the note stays the newest entry while the
        // assistant keeps writing into the card above it (and a reload moves it back).
        handleEvent({ type: 'advisory', data: { id: 'adv-1', kind: 'diagnostics', severity: 'error', text: 'LSP diagnostics: 2 errors' } }, false);
        handleEvent({ type: 'token', data: { text: 'Fixing those now.' } }, false);

        expect(stream.state.entries.map(e => e.role)).toEqual(['assistant', 'advisory', 'assistant']);
        const [turn, note, next] = stream.state.entries;
        expect(turn.content).toBe('I will fix it.');
        expect(turn.isStreaming).toBe(false);
        expect(next.content).toBe('Fixing those now.');
        // The whole complaint in one assertion: the note is not the last message.
        expect(stream.state.entries[stream.state.entries.length - 1]!.id).toBe(next.id);
        expect(note.role).toBe('advisory');
    });

    it('leaves a pending tool call resolvable — the late result still lands on its card', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'token', data: { text: 'Editing.' } }, false);
        handleEvent({ type: 'tool_call', data: { tool_id: 'tc1', tool_name: 'edit_file', args: {} } }, false);
        handleEvent({ type: 'advisory', data: { id: 'adv-1', kind: 'diagnostics', text: '2 errors' } }, false);
        handleEvent({ type: 'tool_result', data: { tool_id: 'tc1', success: true, output: 'ok', duration: 12 } }, false);

        expect(stream.state.entries.length).toBe(2);
        const tc = stream.state.entries[0]!.toolCalls![0];
        expect(tc.result).toBe('ok');
        expect(tc.isPending).toBe(false);
    });

    it('is inserted before a message the client has not sent yet', () => {
        const { stream, handleEvent } = setup();
        // Typed during the diagnostics pass: the server has not accepted it, so its row
        // will be written after the note's row — it must stay last.
        stream.state.entries = [{ id: 'u1', role: 'user', content: 'and then?', isStreaming: false, queued: true, createdAt: new Date().toISOString() }];
        handleEvent({ type: 'advisory', data: { id: 'adv-1', kind: 'diagnostics', text: '2 errors' } }, false);

        expect(stream.state.entries.map(e => e.id)).toEqual(['adv-1', 'u1']);
    });

    it('a cut note lands beside the divider it explains, not below what arrived in between', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'summarization', data: { id: 'sum-1', summary: 'the summary', created_at: '2026-01-01T00:00:00Z' } }, false);
        // This browser wrote the send while the summary was being answered. It carries no `queued` flag
        // to insert before, so the note's frame would otherwise land below it.
        stream.state.entries = [...stream.state.entries, { id: 'queued:act-1', role: 'user', content: 'and then?', isStreaming: false, createdAt: new Date().toISOString() }];
        // The stored read gives the two rows one instant, so a reload draws them together. The live view
        // has to agree, otherwise the note jumps on the next read.
        handleEvent({ type: 'advisory', data: { id: 'adv-1', kind: 'context_cut', text: 'Your conversation was compacted into the summary', created_at: '2026-01-01T00:00:00Z' } }, false);

        expect(stream.state.entries.map(e => e.id)).toEqual(['sum-1', 'adv-1', 'queued:act-1']);
    });
});

describe('lock_wait — a click queued behind work it cannot see', () => {
    function setup() {
        const stream = new StreamState();
        const cb = makeCallbacks();
        const handleEvent = createStreamHandler(stream, cb);
        return { stream, handleEvent };
    }

    it('names the wait by the lock that owns it', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'lock_wait', data: { domain: 'worktree', waited_seconds: 12, acquired: false } }, false);
        expect(stream.state.lockWait).toEqual({ domain: 'worktree', seconds: 12 });
    });

    it('ends the wait when the lock is taken', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'lock_wait', data: { domain: 'stash', waited_seconds: 3, acquired: false } }, false);
        handleEvent({ type: 'lock_wait', data: { domain: 'stash', waited_seconds: 6, acquired: true } }, false);
        expect(stream.state.lockWait).toBeNull();
    });

    it('still reports a wait whose elapsed time grew (the repeats are the liveness signal)', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'lock_wait', data: { domain: 'stash', waited_seconds: 3, acquired: false } }, false);
        handleEvent({ type: 'lock_wait', data: { domain: 'stash', waited_seconds: 9, acquired: false } }, false);
        expect(stream.state.lockWait).toEqual({ domain: 'stash', seconds: 9 });
    });

    it('names an unrecognised domain rather than dropping the wait', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'lock_wait', data: { waited_seconds: 4, acquired: false } }, false);
        expect(stream.state.lockWait).toEqual({ domain: 'other', seconds: 4 });
    });

    // The other ending: called off before the lock, which only the caller knows.
    it('ends the wait when the caller is called off', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'lock_wait', data: { domain: 'stash', waited_seconds: 9, phase: 'first_token' } }, false);

        handleEvent({ type: 'lock_wait', data: { domain: 'stash', waited_seconds: 9, acquired: false, withdrawn: true } }, false);

        expect(stream.state.lockWait).toBeNull();
    });
});

describe('unresolvedToolIds — what this screen owes a card for', () => {
    function setup() {
        const stream = new StreamState();
        const handleEvent = createStreamHandler(stream, makeCallbacks());
        return { stream, handleEvent };
    }

    it('settles a call when its answer lands on the card', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'tool_call', data: { tool_id: 'tc1', tool_name: 'bash', args: {} } }, false);
        expect([...stream.state.unresolvedToolIds]).toEqual(['tc1']);

        handleEvent({ type: 'tool_result', data: { tool_id: 'tc1', success: true, output: 'ok', duration: 5 } }, false);
        expect(stream.state.unresolvedToolIds.size).toBe(0);
    });

    it('keeps the call owed when the pending card is cleared from under it', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'token', data: { text: '' } }, false);
        handleEvent({ type: 'tool_call', data: { tool_id: 'tc1', tool_name: 'bash', args: {} } }, false);
        // The card goes; the announcement is still something this screen saw and cannot show.
        handleEvent({ type: 'status', data: { state: 'running' } }, false);
        expect([...stream.state.unresolvedToolIds]).toEqual(['tc1']);
    });

    it('counts an answer that arrived with no card as a missing card', () => {
        const { stream, handleEvent } = setup();
        // The tool_call frame was never delivered — only its result was.
        handleEvent({ type: 'tool_result', data: { tool_id: 'tc1', success: true, output: 'ok', duration: 5 } }, false);
        expect([...stream.state.unresolvedToolIds]).toEqual(['tc1']);
    });

    it('is empty again after a thread switch, whose reload rebuilds from the database', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'tool_call', data: { tool_id: 'tc1', tool_name: 'bash', args: {} } }, false);
        stream.resetStreaming();
        expect(stream.state.unresolvedToolIds.size).toBe(0);
    });
});

describe('summarization frame', () => {
    function setup() {
        const stream = new StreamState();
        const cb = makeCallbacks();
        const handleEvent = createStreamHandler(stream, cb);
        return { stream, handleEvent };
    }

    it('adds a summary the client has not seen, and updates in place one it has', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'summarization', data: { id: 'sum-1', summary: 'the first text', created_at: '2026-01-01T00:00:00Z' } }, false);
        expect(stream.state.entries.map(e => [e.id, e.content])).toEqual([['sum-1', 'the first text']]);

        // The edit is a broadcast of the same row: the entry keeps its place in the conversation.
        handleEvent({ type: 'summarization', data: { id: 'sum-1', summary: 'the edited text' } }, false);
        expect(stream.state.entries).toHaveLength(1);
        expect(stream.state.entries[0].content).toBe('the edited text');
        expect(stream.state.entries[0].createdAt).toBe('2026-01-01T00:00:00Z');

        handleEvent({ type: 'summarization', data: { id: 'sum-2', summary: 'a newer summary' } }, false);
        expect(stream.state.entries.map(e => e.id)).toEqual(['sum-1', 'sum-2']);
    });

    it('keeps the stored text when an edit frame carries none', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'summarization', data: { id: 'sum-1', summary: 'the stored text' } }, false);
        handleEvent({ type: 'summarization', data: { id: 'sum-1', summary: '' } }, false);
        expect(stream.state.entries[0].content).toBe('the stored text');
    });

    it('moves the context meter to the compressed window the frame reports', () => {
        const { stream, handleEvent } = setup();
        // What the conversation cost before it was compressed.
        stream.state.lastPromptTokens = 30000;
        stream.state.contextState = { used: 30000, limit: 32000 };

        handleEvent({ type: 'summarization', data: { id: 'sum-1', summary: 'the summary', context_used: 4000 } }, false);
        expect(stream.state.contextState.used).toBe(4000);
        expect(stream.state.lastPromptTokens).toBe(4000);
    });

    it('leaves the meter alone when the frame carries no estimate', () => {
        const { stream, handleEvent } = setup();
        stream.state.lastPromptTokens = 30000;
        stream.state.contextState = { used: 30000, limit: 32000 };

        handleEvent({ type: 'summarization', data: { id: 'sum-1', summary: 'the summary', context_used: 4000 } }, false);
        // An edit reports no number, and neither does a summary that arrives without one.
        handleEvent({ type: 'summarization', data: { id: 'sum-1', summary: 'the edited summary' } }, false);
        handleEvent({ type: 'summarization', data: { id: 'sum-2', summary: 'another summary' } }, false);

        expect(stream.state.contextState.used).toBe(4000);
        expect(stream.state.lastPromptTokens).toBe(4000);
    });
});

describe('merge_resolution frame', () => {
    function setup() {
        const stream = new StreamState();
        const handleEvent = createStreamHandler(stream, makeCallbacks());
        return { stream, handleEvent };
    }

    it('draws a resolution row once when the frame is redelivered', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'merge_resolution', data: { id: 'row-1', content: 'Merged into main', created_at: '2026-01-01T00:00:00Z' } }, false);
        expect(stream.state.entries.map(e => e.id)).toEqual(['row-1']);

        // A replay after a resubscribe is the same row of the conversation, not a second resolution.
        handleEvent({ type: 'merge_resolution', data: { id: 'row-1', content: 'Merged into main' } }, false);
        expect(stream.state.entries.map(e => e.id)).toEqual(['row-1']);
    });

    it('adds nothing when the history read drew the row before the frame arrived', () => {
        const { stream, handleEvent } = setup();
        const rows = [{ id: 'row-1', role: 'merge_resolution', content: 'Merged into main', created_at: '2026-01-01T00:00:00Z' }];
        const built = buildEntriesFromMessages(rows);
        stream.state.entries = mergeLoadedHistory(built.entries, rows.map(r => r.id), stream.state.entries, false);
        expect(stream.state.entries.map(e => e.id)).toEqual(['row-1']);

        // The merge's own frame is what a commit-and-done click answers with, so it can land after the read
        // it triggered. Appending it would put one row's id at two indexes, and the keyed each refuses.
        handleEvent({ type: 'merge_resolution', data: { id: 'row-1', content: 'Merged into main' } }, false);
        expect(stream.state.entries.map(e => e.id)).toEqual(['row-1']);
    });

    it('appends a resolution no read has drawn', () => {
        const { stream, handleEvent } = setup();
        const rows = [{ id: 'row-1', role: 'assistant', content: 'the answer', created_at: '2026-01-01T00:00:00Z' }];
        const built = buildEntriesFromMessages(rows);
        stream.state.entries = mergeLoadedHistory(built.entries, rows.map(r => r.id), stream.state.entries, false);

        handleEvent({ type: 'merge_resolution', data: { id: 'row-2', content: 'Merge failed: conflict in a.ts', is_error: true, created_at: '2026-01-01T00:01:00Z' } }, false);
        expect(stream.state.entries.map(e => [e.id, e.role])).toEqual([['row-1', 'assistant'], ['row-2', 'merge_resolution']]);
        expect(stream.state.entries[1].isError).toBe(true);
    });
});

// A collapsed turn's reasoning never arrives: what arrives is how much of it there is. The screen says
// "still thinking, at this rate" without seeing a word, and still places a tool card at the offset the
// reasoning had reached when the call emitted it.
describe('a collapsed turn announces its reasoning size, never its text', () => {
    function setup() {
        const stream = new StreamState();
        const handleEvent = createStreamHandler(stream, makeCallbacks());
        return { stream, handleEvent };
    }

    it('counts what it was never sent', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'reasoning_progress', data: { chars: 120 } }, false);
        handleEvent({ type: 'reasoning_progress', data: { chars: 200 } }, false);

        const entry = stream.state.entries[0];
        expect(entry.role).toBe('assistant');
        expect(entry.hasReasoning).toBe(true);
        expect(entry.reasoningContent).toBeUndefined();
        expect(entry.reasoningPending?.chars).toBe(200);
        expect(entry.reasoningPending?.startedAt).toBeTypeOf('number');
    });

    it('keeps the rate honest across a reconnect, where the replayed state frame is the newest total', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'reasoning_progress', data: { chars: 500 } }, false);
        handleEvent({ type: 'reasoning_progress', data: { chars: 40 } }, false);

        expect(stream.state.entries[0].reasoningPending?.chars).toBe(500);
    });

    it('prices the window by what the text would have cost', () => {
        const { stream, handleEvent } = setup();
        stream.state.lastPromptTokens = 1000;
        handleEvent({ type: 'reasoning_progress', data: { chars: 400 } }, false);
        expect(stream.state.streamTokenCount).toBe(100);
        expect(stream.state.contextState.used).toBe(1100);

        handleEvent({ type: 'reasoning_progress', data: { chars: 800 } }, false);
        expect(stream.state.streamTokenCount).toBe(200);
        expect(stream.state.contextState.used).toBe(1200);
    });

    it('stamps a card with the offset the reasoning had reached, not with the text it never got', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'reasoning_progress', data: { chars: 240 } }, false);
        handleEvent({ type: 'tool_call', data: { tool_id: 'tc-1', tool_name: 'bash', args: { command: 'ls' } } }, false);

        expect(stream.state.entries[0].toolCalls![0].reasoningIndex).toBe(240);
    });

    it('starts no entry for a count that arrived with the events suppressed', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'reasoning_progress', data: { chars: 120 } }, true);

        expect(stream.state.entries).toEqual([]);
        expect(stream.state.streamTokenCount).toBe(0);
    });

    // The count that closes a hidden call is what "Show reasoning" spends: without the row's id the click
    // asks the store for nothing.
    it('names the row whose reasoning the reader may still ask for', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'reasoning_progress', data: { chars: 300 } }, false);
        const streamedId = stream.state.entries[0]!.id;

        handleEvent({ type: 'reasoning_progress', data: { chars: 900, message_id: 'row-7' } }, false);

        const entry = stream.state.entries[0];
        expect(entry.id).toBe('row-7');
        expect(streamedId).not.toBe('row-7');
        // Nothing more is arriving to average, so the rate is over.
        expect(entry.reasoningPending).toBeUndefined();
        expect(entry.hasReasoning).toBe(true);
        expect(entry.reasoningContent).toBeUndefined();
    });

    it('drops the streaming copy once the read has drawn the row a count names', () => {
        const { stream, handleEvent } = setup();
        const rows = [{ id: 'row-7', role: 'assistant', content: '', created_at: '2026-01-01T00:00:00Z' }];
        const built = buildEntriesFromMessages(rows);
        stream.state.entries = mergeLoadedHistory(built.entries, rows.map(r => r.id), stream.state.entries, false);

        handleEvent({ type: 'reasoning_progress', data: { chars: 400 } }, false);
        expect(stream.state.entries).toHaveLength(2);

        // One row, two entries: the keyed each refuses that, and the streaming half holds only a count.
        handleEvent({ type: 'reasoning_progress', data: { chars: 900, message_id: 'row-7' } }, false);
        expect(stream.state.entries.map(e => e.id)).toEqual(['row-7']);
    });

    // What the buffer hands a view arriving mid-tool-call, in order: the counts the call announced while it
    // reasoned, then the frame that closed the count. The last one decides.
    it('leaves nothing standing as thinking when the replay ends on a closed count', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'reasoning_progress', data: { chars: 400 } }, false);
        handleEvent({ type: 'reasoning_progress', data: { chars: 900, message_id: 'row-7' } }, false);

        const entry = stream.state.entries[0]!;
        expect(entry.id).toBe('row-7');
        expect(entry.reasoningPending).toBeUndefined();
        expect(entry.isStreaming).toBe(false);
    });

    it('opens a fresh entry for the next call, whose row the count has not named', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'reasoning_progress', data: { chars: 900, message_id: 'row-7' } }, false);

        handleEvent({ type: 'token', data: { text: 'the answer' } }, false);

        // The second call is its own row, so its text cannot land under the first call's row.
        expect(stream.state.entries.map(e => [e.role, e.content])).toEqual([['assistant', ''], ['assistant', 'the answer']]);
        expect(stream.state.entries[1]!.id).not.toBe('row-7');
    });
});

// `complete` names the assistant row, and a run can close its streaming entry before that frame lands — a
// steering hint, or the tool result that ends the call. An entry left under its generated id owns no row.
describe('the entry complete names', () => {
    function setup() {
        const stream = new StreamState();
        const handleEvent = createStreamHandler(stream, makeCallbacks());
        return { stream, handleEvent };
    }

    const complete = (assistant_message_id: string): StreamEvent => ({
        type: 'complete',
        data: { state: 'idle', model_name: 'm', prompt_tokens: 1, predicted_tokens: 2, prompt_ms: 3, predicted_ms: 4, duration: 5, assistant_message_id },
    });

    it('renames the entry a steering hint closed mid-run', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'token', data: { text: 'thinking' } }, false);
        handleEvent({ type: 'hint', data: { id: 'h-1', role: 'user', content: 'Is the task complete?', created_at: '2026-01-01T00:00:00Z' } }, false);
        handleEvent({ type: 'token', data: { text: ' the answer' } }, false);
        handleEvent({ type: 'tool_call', data: { tool_id: 'tc-1', tool_name: 'bash', args: { command: 'ls' } } }, false);
        handleEvent({ type: 'tool_result', data: { tool_id: 'tc-1', success: true, output: 'ok', duration: 2 } }, false);
        const streamedId = stream.state.entries[stream.state.entries.length - 1]!.id;

        handleEvent(complete('row-9'), false);

        expect(stream.state.entries[stream.state.entries.length - 1]!.id).toBe('row-9');
        expect(streamedId).not.toBe('row-9');
    });

    it('names no row twice: what a count already named keeps that name', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'reasoning_progress', data: { chars: 900, message_id: 'row-7' } }, false);
        handleEvent({ type: 'token', data: { text: 'the answer' } }, false);

        handleEvent(complete('row-9'), false);

        // The reasoning row keeps the id the count gave it.
        expect(stream.state.entries.map(e => e.id)).toEqual(['row-7', 'row-9']);
    });

    it('names no entry an earlier call of the same run closed', () => {
        const { stream, handleEvent } = setup();
        // Call one says its piece and asks for a card; the answer closes that entry.
        handleEvent({ type: 'token', data: { text: 'first' } }, false);
        handleEvent({ type: 'tool_call', data: { tool_id: 'tc-1', tool_name: 'bash', args: { command: 'ls' } } }, false);
        handleEvent({ type: 'tool_result', data: { tool_id: 'tc-1', success: true, output: 'ok', duration: 2 } }, false);
        const firstEntry = stream.state.entries[0]!.id;

        // Call two reasons behind the collapse, so its row is named while it streams.
        handleEvent({ type: 'reasoning_progress', data: { chars: 900, message_id: 'row-9' } }, false);

        handleEvent(complete('row-9'), false);

        // Only the run's last call owns that row.
        expect(stream.state.entries.map(e => e.id)).toEqual([firstEntry, 'row-9']);
        expect(firstEntry).not.toBe('row-9');
    });

    it('names nothing from the turn before the prompt that started this one', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'token', data: { text: 'an earlier answer' } }, false);
        handleEvent({ type: 'tool_call', data: { tool_id: 'tc-1', tool_name: 'bash', args: { command: 'ls' } } }, false);
        handleEvent({ type: 'tool_result', data: { tool_id: 'tc-1', success: true, output: 'ok', duration: 2 } }, false);
        handleEvent({ type: 'user_message', data: { id: 'row-0', role: 'user', content: 'and now?' } }, false);

        handleEvent(complete('row-9'), false);

        // The earlier turn's entry keeps the id the stream gave it.
        const earlierId = stream.state.entries[0]!.id;
        expect(earlierId).not.toBe('row-9');
        expect(stream.state.entries.map(e => e.id)).toEqual([earlierId, 'row-0']);
    });
});

// The five states `terminalTurnStates` (backend/stream/app_stream.go) counts as ending the run. Nothing on
// screen may keep claiming work is in flight once one of them lands.
const ENDED_STATES = ['paused', 'stopped', 'failed', 'idle', 'completed'];

describe('the status frame that ends a run', () => {
    function setup() {
        const stream = new StreamState();
        const cb = makeCallbacks();
        const handleEvent = createStreamHandler(stream, cb);
        return { stream, handleEvent, markStreaming: cb.markStreaming as ReturnType<typeof vi.fn> };
    }

    // A count left open over a run that ended turns the ring forever beside a row that says `paused`.
    it.each(ENDED_STATES)('leaves nothing drawn as still thinking after %s', (state) => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'reasoning_progress', data: { chars: 400 } }, false);
        handleEvent({ type: 'reasoning_progress', data: { chars: 900 } }, false);

        handleEvent({ type: 'status', data: { state, activity: 'Task stopped' } }, false);

        expect(stream.state.entries).toEqual([]);
        for (const e of stream.state.entries) {
            expect(e.isStreaming).toBe(false);
            expect(e.reasoningPending).toBeUndefined();
        }
    });

    // What the run already said stays said: only text no row will ever hold is dropped.
    it('keeps the cards of calls whose rows exist, and draws none of them as arriving', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'token', data: { text: 'I will run that' } }, false);
        handleEvent({ type: 'tool_call', data: { tool_id: 'tc-1', tool_name: 'bash', args: { command: 'ls' } } }, false);
        handleEvent({ type: 'tool_call', data: { tool_id: 'tc-2', tool_name: 'bash', args: { command: 'pwd' } } }, false);
        handleEvent({ type: 'tool_result', data: { tool_id: 'tc-1', success: true, output: 'ok', duration: 1, message_id: 'row-3' } }, false);
        handleEvent({ type: 'reasoning_progress', data: { chars: 300 } }, false);

        handleEvent({ type: 'status', data: { state: 'paused', activity: 'Task paused by user' } }, false);

        const answered = stream.state.entries.find(e => e.toolCalls?.some(tc => tc.toolId === 'tc-1'));
        expect(answered).toBeDefined();
        expect(answered!.content).toBe('I will run that');
        expect(answered!.isStreaming).toBe(false);
        expect(answered!.reasoningPending).toBeUndefined();
        // Its count was never closed by the server, so the screen never learned a row holds that reasoning.
        expect(answered!.hasReasoning).toBe(false);
    });

    // The wait line is drawn from `lockWait` alone, so the frame that ended the run ends the wait too.
    it.each(ENDED_STATES)('ends the wait line beside %s', (state) => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'lock_wait', data: { domain: 'model', waited_seconds: 12, phase: 'first_token' } }, false);
        handleEvent({ type: 'queue', data: { position: 3, estimated_wait_seconds: 20 } }, false);
        handleEvent({ type: 'prompt_progress', data: { total: 100, cache: 0, processed: 40, time_ms: 10 } }, false);

        handleEvent({ type: 'status', data: { state } }, false);

        expect(stream.state.lockWait).toBeNull();
        expect(stream.state.queuePosition).toBe(0);
        expect(stream.state.promptProgress).toBe(0);
    });

    // A cancelled call flushes what it buffered after the transition lands, and a late count is not a new
    // turn: re-opening an entry puts the notice, the cursor and the card's Running badge back.
    it.each(ENDED_STATES)('mutes the late run frames of the turn that ended with %s', (state) => {
        const { stream, handleEvent, markStreaming } = setup();
        handleEvent({ type: 'status', data: { state } }, false);

        handleEvent({ type: 'reasoning_progress', data: { chars: 800 } }, false);
        handleEvent({ type: 'token', data: { text: 'half a sentence' } }, false);
        handleEvent({ type: 'tool_call', data: { tool_id: 'tc-late', tool_name: 'bash', args: {} } }, false);

        expect(stream.state.entries).toEqual([]);
        expect(markStreaming).not.toHaveBeenCalled();
    });

    // The mute covers a run's output, nothing wider: a reader's own words still land after the ending.
    it('still shows what the reader and a settled card say', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'tool_call', data: { tool_id: 'tc-1', tool_name: 'bash', args: {} } }, false);
        // The row names the call, so the card is a row's and survives the ending.
        handleEvent({ type: 'tool_progress', data: { tool_id: 'tc-1', message_id: 'row-5' } }, false);
        handleEvent({ type: 'status', data: { state: 'paused' } }, false);

        handleEvent({ type: 'user_message', data: { id: 'row-9', role: 'user', content: 'why did it stop?' } }, false);
        handleEvent({ type: 'tool_result', data: { tool_id: 'tc-1', success: false, interrupted: true, output: 'Stopped', duration: 1 } }, false);

        expect(stream.state.entries.map(e => [e.role, e.content])).toEqual([['assistant', ''], ['user', 'why did it stop?']]);
        expect(stream.state.entries[0]!.toolCalls![0].isPending).toBe(false);
        expect(stream.state.entries[0]!.toolCalls![0].interrupted).toBe(true);
    });

    // A resumed task must never be muted: the frame that starts a run is what lifts the mute.
    it.each([['the running status frame', 'running'], ['the hint that announces a retry', 'retry']] as const)(
        'honours the frames again after %s', (_label, kind) => {
            const { stream, handleEvent } = setup();
            handleEvent({ type: 'status', data: { state: 'paused' } }, false);

            if (kind === 'running') {
                handleEvent({ type: 'status', data: { state: 'running' } }, false);
            } else {
                handleEvent({ type: 'hint', data: { id: 'h-1', role: 'system', kind: 'retry', content: 'Retrying', created_at: '2026-01-01T00:00:00Z' } }, false);
            }
            handleEvent({ type: 'reasoning_progress', data: { chars: 800 } }, false);

            expect(stream.state.entries[stream.state.entries.length - 1]?.reasoningPending?.chars).toBe(800);
        });
});
