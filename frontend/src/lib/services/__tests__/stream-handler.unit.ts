import { describe, it, expect, vi } from 'vitest';
import { StreamState } from '$lib/services/stream-state.svelte';
import { createStreamHandler, type StreamHandlerCallbacks } from '$lib/services/stream-handler';

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
        createTaskMessage: vi.fn(async () => {}),
        ...overrides,
    };
}

describe('StreamState', () => {
    it('starts with empty state', () => {
        const s = new StreamState();
        expect(s.state.entries).toEqual([]);
        expect(s.state.currentAssistantId).toBeNull();
        expect(s.state.awaitingResponse).toBe(false);
    });

    it('ensureAssistantEntry creates a new entry', () => {
        const s = new StreamState();
        const entry = s.ensureAssistantEntry(() => 'test-id');
        expect(entry.id).toBe('test-id');
        expect(entry.role).toBe('assistant');
        expect(s.state.entries.length).toBe(1);
    });

    it('closeCurrentAssistant sets isStreaming to false', () => {
        const s = new StreamState();
        s.ensureAssistantEntry(() => 'test-id');
        s.closeCurrentAssistant();
        expect(s.state.entries[0].isStreaming).toBe(false);
        expect(s.state.currentAssistantId).toBeNull();
    });

    it('reset clears all state', () => {
        const s = new StreamState();
        s.ensureAssistantEntry(() => 'test-id');
        s.state.awaitingResponse = true;
        s.state.queuePosition = 5;
        s.reset();
        expect(s.state.entries).toEqual([]);
        expect(s.state.awaitingResponse).toBe(false);
        expect(s.state.queuePosition).toBe(0);
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
        handleEvent({ type: 'token', data: { text: '' } }, false, 'p1', 't1');
        // tool_call for bash
        handleEvent({ type: 'tool_call', data: { tool_id: 'tc-bash', tool_name: 'bash', args: { command: 'make build' } } }, false, 'p1', 't1');
        // Several tool_output chunks accumulate the full streamed content
        handleEvent({ type: 'tool_output', data: { tool_id: 'tc-bash', text: 'Compiling module A...\n' } }, false, 'p1', 't1');
        handleEvent({ type: 'tool_output', data: { tool_id: 'tc-bash', text: 'Compiling module B...\n' } }, false, 'p1', 't1');
        handleEvent({ type: 'tool_output', data: { tool_id: 'tc-bash', text: 'Compiling module C...\n' } }, false, 'p1', 't1');
        handleEvent({ type: 'tool_output', data: { tool_id: 'tc-bash', text: 'Build succeeded in 42s\n' } }, false, 'p1', 't1');

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
        }, false, 'p1', 't1');

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
        handleEvent({ type: 'token', data: { text: '' } }, false, 'p1', 't1');
        handleEvent({ type: 'tool_call', data: { tool_id: 'tc-read', tool_name: 'read_file', args: { path: 'large.go' } } }, false, 'p1', 't1');

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
        }, false, 'p1', 't1');

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
        handleEvent({ type: 'token', data: { text: '' } }, false, 'p1', 't1');
        handleEvent({ type: 'tool_call', data: { tool_id: 'tc-view', tool_name: 'view_tasks', args: {} } }, false, 'p1', 't1');

        handleEvent({
            type: 'tool_result',
            data: {
                tool_id: 'tc-view',
                success: true,
                output: 'Task 1: hello\nTask 2: world\n',
                duration: 10,
                // No truncation fields present
            },
        }, false, 'p1', 't1');

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
        handleEvent({ type: 'token', data: { text: '' } }, false, 'p1', 't1');
        handleEvent({ type: 'tool_call', data: { tool_id: 'tc-miss', tool_name: 'bash', args: {} } }, false, 'p1', 't1');
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
        }, false, 'p1', 't1');

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
        }, false, 'p1', 't1');

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
        handleEvent({ type: 'token', data: { text: '' } }, false, 'p1', 't1');
        handleEvent({ type: 'tool_call', data: { tool_id: 'tc-partial', tool_name: 'bash', args: {} } }, false, 'p1', 't1');

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
        }, false, 'p1', 't1');

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
        handleEvent({ type: 'token', data: { text: 'Hello' } }, false, 'p1', 't1');
        expect(stream.state.entries.length).toBe(1);
        expect(stream.state.entries[0].content).toBe('Hello');
        expect(cb.markStreaming).toHaveBeenCalled();
    });

    it('token event estimates tokens from batched text', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'token', data: { text: 'Hello world this is a batched token event' } }, false, 'p1', 't1');
        // 41 chars / 4 ≈ 11 tokens
        expect(stream.state.streamTokenCount).toBe(11);
        // Second batched event adds more (17 chars / 4 ≈ 5)
        handleEvent({ type: 'token', data: { text: ' more tokens here' } }, false, 'p1', 't1');
        expect(stream.state.streamTokenCount).toBe(16);
    });

    it('reasoning event estimates tokens from batched text', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'reasoning', data: { text: 'Let me think about this problem carefully' } }, false, 'p1', 't1');
        expect(stream.state.streamTokenCount).toBe(11);
    });

    it('reasoning event appends to reasoningContent', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'reasoning', data: { text: 'Let me think' } }, false, 'p1', 't1');
        expect(stream.state.entries[0].reasoningContent).toBe('Let me think');
    });

    it('tool_call event creates tool call entry', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'token', data: { text: '' } }, false, 'p1', 't1');
        handleEvent({ type: 'tool_call', data: { tool_id: 'tc1', tool_name: 'read_file', args: { path: 'foo.ts' } } }, false, 'p1', 't1');
        const entry = stream.state.entries[0];
        expect(entry.toolCalls).toHaveLength(1);
        expect(entry.toolCalls![0].toolId).toBe('tc1');
        expect(entry.toolCalls![0].isPending).toBe(true);
    });

    it('tool_result event resolves pending tool call', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'token', data: { text: '' } }, false, 'p1', 't1');
        handleEvent({ type: 'tool_call', data: { tool_id: 'tc1', tool_name: 'read_file', args: {} } }, false, 'p1', 't1');
        handleEvent({ type: 'tool_result', data: { tool_id: 'tc1', success: true, output: 'file content', duration: 100 } }, false, 'p1', 't1');
        const tc = stream.state.entries[0].toolCalls![0];
        expect(tc.result).toBe('file content');
        expect(tc.isPending).toBe(false);
        expect(tc.success).toBe(true);
    });

    it('complete event closes assistant and sets stats', () => {
        const { cb, stream, handleEvent } = setup();
        handleEvent({ type: 'token', data: { text: 'Hi' } }, false, 'p1', 't1');
        handleEvent({ type: 'complete', data: { state: 'idle', model_name: 'gpt-4', prompt_tokens: 100, predicted_tokens: 10, prompt_ms: 200, predicted_ms: 100, duration: 300, assistant_message_id: 'backend-id' } }, false, 'p1', 't1');
        expect(stream.state.entries[0].isStreaming).toBe(false);
        expect(stream.state.entries[0].stats).toBeDefined();
        expect(cb.markNotStreaming).toHaveBeenCalled();
    });

    it('hint event uses the backend message id for the entry', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'hint', data: { id: 'hint-msg-1', content: 'Is the task complete?', created_at: '2026-01-01T00:00:00Z' } }, false, 'p1', 't1');
        expect(stream.state.entries).toHaveLength(1);
        expect(stream.state.entries[0].role).toBe('user');
        expect(stream.state.entries[0].id).toBe('hint-msg-1');
        expect(stream.state.entries[0].content).toBe('Is the task complete?');
    });

    it('hint event without an id falls back to genId', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'hint', data: { content: 'retry hint', created_at: '2026-01-01T00:00:00Z' } }, false, 'p1', 't1');
        expect(stream.state.entries).toHaveLength(1);
        expect(stream.state.entries[0].id).toMatch(/^id-/); // genId() prefix from the test callback
        expect(stream.state.entries[0].content).toBe('retry hint');
    });

    it('hint event with an already-present id is skipped (replay dedup)', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'hint', data: { id: 'hint-msg-1', content: 'Is the task complete?', created_at: '2026-01-01T00:00:00Z' } }, false, 'p1', 't1');
        // Replayed from the reconnect buffer — must not add a duplicate entry.
        handleEvent({ type: 'hint', data: { id: 'hint-msg-1', content: 'Is the task complete?', created_at: '2026-01-01T00:00:00Z' } }, false, 'p1', 't1');
        expect(stream.state.entries).toHaveLength(1);
    });

    it('error event creates error entry', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'error', data: { message: 'Something failed', code: 'api_error' } }, false, 'p1', 't1');
        const entry = stream.state.entries.find(e => e.role === 'error');
        expect(entry).toBeDefined();
        expect(entry!.content).toBe('Something failed');
    });

    it('error event skips ws_error and stopped codes', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'error', data: { message: 'ws error', code: 'ws_error' } }, false, 'p1', 't1');
        handleEvent({ type: 'error', data: { message: 'stopped', code: 'stopped' } }, false, 'p1', 't1');
        expect(stream.state.entries.filter(e => e.role === 'error')).toHaveLength(0);
    });

    it('queue event updates queue position', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'queue', data: { position: 3, estimated_wait_seconds: 30 } }, false, 'p1', 't1');
        expect(stream.state.queuePosition).toBe(3);
        expect(stream.state.queueEstimatedWait).toBe(30);
        expect(stream.state.awaitingResponse).toBe(true);
    });

    it('stats event updates token count', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'stats', data: { model_name: 'gpt-4', prompt_tokens: 500, predicted_tokens: 50, predicted_ms: 200 } }, false, 'p1', 't1');
        expect(stream.state.streamTokenCount).toBe(50);
        expect(stream.state.lastPromptTokens).toBe(500);
    });

    it('stats event corrects estimated token count from batched events', () => {
        const { stream, handleEvent } = setup();
        // Batched token events produce an estimate
        handleEvent({ type: 'token', data: { text: 'Hello world this is a batched token event' } }, false, 'p1', 't1');
        const estimated = stream.state.streamTokenCount;
        // Stats event overrides with the real count from the LLM
        handleEvent({ type: 'stats', data: { model_name: 'gpt-4', prompt_tokens: 500, predicted_tokens: 15, predicted_ms: 200 } }, false, 'p1', 't1');
        expect(stream.state.streamTokenCount).toBe(15);
        // The estimate (11 from 43 chars) differs from the actual LLM count (15)
        expect(estimated).toBe(11);
    });

    it('git_checkpoint_pending adds to pending set', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'git_checkpoint_pending', data: { message_id: 'msg-1' } }, false, 'p1', 't1');
        expect(stream.state.pendingCheckpoints.has('msg-1')).toBe(true);
    });

    it('git_checkpoint_complete removes from pending and adds hash', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'git_checkpoint_pending', data: { message_id: 'msg-1' } }, false, 'p1', 't1');
        handleEvent({ type: 'git_checkpoint_complete', data: { message_id: 'msg-1', commit_hash: 'abc123', label: 'Checkpoint 1' } }, false, 'p1', 't1');
        expect(stream.state.pendingCheckpoints.has('msg-1')).toBe(false);
        expect(stream.state.checkpointLabels.get('msg-1')).toBe('Checkpoint 1');
        expect(stream.state.checkpointCommitHashes.get('msg-1')).toBe('abc123');
    });

    it('suppressEvents skips token events', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'token', data: { text: 'Hello' } }, true, 'p1', 't1');
        expect(stream.state.entries.length).toBe(0);
    });

    it('diff_changed calls onRefreshDiff callback', () => {
        const { cb, handleEvent } = setup();
        handleEvent({ type: 'diff_changed', data: { files: {} } }, false, 'p1', 't1');
        expect(cb.onRefreshDiff).toHaveBeenCalled();
    });

    it('tool_call dedup via processedToolIds', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'token', data: { text: '' } }, false, 'p1', 't1');
        handleEvent({ type: 'tool_call', data: { tool_id: 'tc1', tool_name: 'read_file', args: {} } }, false, 'p1', 't1');
        // Duplicate tool_call with same ID should be skipped
        handleEvent({ type: 'tool_call', data: { tool_id: 'tc1', tool_name: 'read_file', args: {} } }, false, 'p1', 't1');
        expect(stream.state.entries[0].toolCalls).toHaveLength(1);
    });

    it('status:running clears pending tool calls', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'token', data: { text: '' } }, false, 'p1', 't1');
        handleEvent({ type: 'tool_call', data: { tool_id: 'tc1', tool_name: 'read_file', args: {} } }, false, 'p1', 't1');
        handleEvent({ type: 'status', data: { state: 'running' } }, false, 'p1', 't1');
        // Entry with only pending tool calls should be removed
        expect(stream.state.entries.filter(e => e.toolCalls?.some(tc => tc.isPending))).toHaveLength(0);
    });

    it('status:running sets awaitingResponse so the awaiting indicator shows', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'status', data: { state: 'running' } }, false, 'p1', 't1');
        expect(stream.state.awaitingResponse).toBe(true);
    });

    it('status:running with retry activity also arms awaitingResponse', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'status', data: { state: 'running', activity: 'Connection interrupted (timeout), retrying... (attempt 2/3)' } }, false, 'p1', 't1');
        expect(stream.state.awaitingResponse).toBe(true);
        expect(stream.state.retryCount).toBe(2);
        expect(stream.state.retryError).toBe('timeout');
    });

    it('first token after status:running clears awaitingResponse', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'status', data: { state: 'running' } }, false, 'p1', 't1');
        expect(stream.state.awaitingResponse).toBe(true);
        handleEvent({ type: 'token', data: { text: 'Hello' } }, false, 'p1', 't1');
        expect(stream.state.awaitingResponse).toBe(false);
    });

    it('terminal statuses after running clear awaitingResponse', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'status', data: { state: 'running' } }, false, 'p1', 't1');
        expect(stream.state.awaitingResponse).toBe(true);
        handleEvent({ type: 'status', data: { state: 'idle' } }, false, 'p1', 't1');
        expect(stream.state.awaitingResponse).toBe(false);
    });

    it('tool_result resolves pending tool call via activeToolCalls lookup', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'token', data: { text: '' } }, false, 'p1', 't1');
        handleEvent({ type: 'tool_call', data: { tool_id: 'tc1', tool_name: 'read_file', args: {} } }, false, 'p1', 't1');
        // Verify the toolCall reference is stored in activeToolCalls
        const ref = stream.state.activeToolCalls.get('tc1');
        expect(ref).toBeDefined();
        expect(ref!.toolCall.toolId).toBe('tc1');
        expect(ref!.toolCall.isPending).toBe(true);
        // tool_result resolves via activeToolCalls lookup, replaces object in entry for reactivity
        handleEvent({ type: 'tool_result', data: { tool_id: 'tc1', success: true, output: 'content', duration: 50 } }, false, 'p1', 't1');
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
        handleEvent({ type: 'token', data: { text: '' } }, false, 'p1', 't1');
        handleEvent({ type: 'tool_call', data: { tool_id: 'tc2', tool_name: 'bash', args: {} } }, false, 'p1', 't1');
        // Streaming output via activeToolCalls lookup, replaces object in entry for reactivity
        handleEvent({ type: 'tool_output', data: { tool_id: 'tc2', text: 'line1' } }, false, 'p1', 't1');
        handleEvent({ type: 'tool_output', data: { tool_id: 'tc2', text: 'line2' } }, false, 'p1', 't1');
        const tc = stream.state.entries[0].toolCalls![0];
        expect(tc.result).toBe('line1line2');
        expect(tc.isPending).toBe(true); // still pending until tool_result
    });

    it('tool_progress with tool_id uses O(1) direct reference', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'token', data: { text: '' } }, false, 'p1', 't1');
        handleEvent({ type: 'tool_call', data: { tool_id: 'tc3', tool_name: 'write_file', args: {} } }, false, 'p1', 't1');
        // Progress via direct reference
        handleEvent({ type: 'tool_progress', data: { tool_id: 'tc3', bytes_written: 42, lines_added: 5 } }, false, 'p1', 't1');
        const tc = stream.state.entries[0].toolCalls![0];
        expect(tc.bytesWritten).toBe(42);
        expect(tc.linesAdded).toBe(5);
    });

    it('tool_progress without tool_id broadcasts to all active tool calls', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'token', data: { text: '' } }, false, 'p1', 't1');
        handleEvent({ type: 'tool_call', data: { tool_id: 'tcA', tool_name: 'write_file', args: {} } }, false, 'p1', 't1');
        handleEvent({ type: 'tool_call', data: { tool_id: 'tcB', tool_name: 'edit_file', args: {} } }, false, 'p1', 't1');
        // Broadcast progress (no tool_id) — both active tool calls get updated
        handleEvent({ type: 'tool_progress', data: { bytes_written: 100 } }, false, 'p1', 't1');
        const entry = stream.state.entries[0];
        expect(entry.toolCalls![0].bytesWritten).toBe(100);
        expect(entry.toolCalls![1].bytesWritten).toBe(100);
    });

    it('tool_result ignores stale tool_id not in activeToolCalls', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'token', data: { text: '' } }, false, 'p1', 't1');
        // Send result for a tool call that was never registered
        handleEvent({ type: 'tool_result', data: { tool_id: 'ghost', success: true, output: 'nope', duration: 0 } }, false, 'p1', 't1');
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
        handleEvent({ type: 'tool_result', data: { tool_id: 'db-tc1', success: true, output: 'done', duration: 5000 } }, false, 'p1', 't1');
        // Fallback search should find and resolve the pending tool call
        const tc = stream.state.entries[0].toolCalls![0];
        expect(tc.isPending).toBe(false);
        expect(tc.result).toBe('done');
        expect(tc.success).toBe(true);
        expect(tc.duration).toBe(5000);
    });

    it('tool_output ignores unknown tool_id not in activeToolCalls', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'token', data: { text: '' } }, false, 'p1', 't1');
        handleEvent({ type: 'tool_call', data: { tool_id: 'tc1', tool_name: 'read_file', args: {} } }, false, 'p1', 't1');
        // Output for unknown tool_id is silently ignored
        handleEvent({ type: 'tool_output', data: { tool_id: 'unknown', text: 'leak' } }, false, 'p1', 't1');
        const tc = stream.state.entries[0].toolCalls![0];
        expect(tc.result).toBeUndefined();
    });

    it('activeToolCalls stores reference that stays in sync with entry', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'token', data: { text: '' } }, false, 'p1', 't1');
        handleEvent({ type: 'tool_call', data: { tool_id: 'tc1', tool_name: 'read_file', args: {} } }, false, 'p1', 't1');
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
        it('tool_result mutates tc through live entries proxy for Svelte reactivity', () => {
            const { stream, handleEvent } = setup();
            handleEvent({ type: 'token', data: { text: '' } }, false, 'p1', 't1');
            handleEvent({ type: 'tool_call', data: { tool_id: 'tc1', tool_name: 'read_file', args: {} } }, false, 'p1', 't1');
            handleEvent({ type: 'tool_result', data: { tool_id: 'tc1', success: true, output: 'content', duration: 50 } }, false, 'p1', 't1');
            expect(stream.state.entries[0].toolCalls![0].toolId).toBe('tc1');
            expect(stream.state.entries[0].toolCalls![0].result).toBe('content');
            expect(stream.state.entries[0].toolCalls![0].isPending).toBe(false);
            expect(stream.state.entries[0].toolCalls![0].success).toBe(true);
            expect(stream.state.entries[0].toolCalls![0].duration).toBe(50);
        });

        it('tool_output mutates in-place without replacing toolCalls array', () => {
            const { stream, handleEvent } = setup();
            handleEvent({ type: 'token', data: { text: '' } }, false, 'p1', 't1');
            handleEvent({ type: 'tool_call', data: { tool_id: 'tc2', tool_name: 'bash', args: {} } }, false, 'p1', 't1');
            const toolCallsRef = stream.state.entries[0].toolCalls!;
            handleEvent({ type: 'tool_output', data: { tool_id: 'tc2', text: 'line1' } }, false, 'p1', 't1');
            handleEvent({ type: 'tool_output', data: { tool_id: 'tc2', text: 'line2' } }, false, 'p1', 't1');
            // Array reference unchanged
            expect(stream.state.entries[0].toolCalls).toBe(toolCallsRef);
            expect(stream.state.entries[0].toolCalls![0].result).toBe('line1line2');
        });

        it('tool_progress mutates in-place without replacing toolCalls array', () => {
            const { stream, handleEvent } = setup();
            handleEvent({ type: 'token', data: { text: '' } }, false, 'p1', 't1');
            handleEvent({ type: 'tool_call', data: { tool_id: 'tc3', tool_name: 'write_file', args: {} } }, false, 'p1', 't1');
            const toolCallsRef = stream.state.entries[0].toolCalls!;
            handleEvent({ type: 'tool_progress', data: { tool_id: 'tc3', bytes_written: 42 } }, false, 'p1', 't1');
            // Array reference unchanged
            expect(stream.state.entries[0].toolCalls).toBe(toolCallsRef);
            expect(stream.state.entries[0].toolCalls![0].bytesWritten).toBe(42);
        });

        it('tool_progress broadcast mutates all active tool calls in-place', () => {
            const { stream, handleEvent } = setup();
            handleEvent({ type: 'token', data: { text: '' } }, false, 'p1', 't1');
            handleEvent({ type: 'tool_call', data: { tool_id: 'tcA', tool_name: 'write_file', args: {} } }, false, 'p1', 't1');
            handleEvent({ type: 'tool_call', data: { tool_id: 'tcB', tool_name: 'edit_file', args: {} } }, false, 'p1', 't1');
            const toolCallsRef = stream.state.entries[0].toolCalls!;
            // Broadcast progress (no tool_id)
            handleEvent({ type: 'tool_progress', data: { bytes_written: 100 } }, false, 'p1', 't1');
            // Array reference unchanged
            expect(stream.state.entries[0].toolCalls).toBe(toolCallsRef);
            expect(stream.state.entries[0].toolCalls![0].bytesWritten).toBe(100);
            expect(stream.state.entries[0].toolCalls![1].bytesWritten).toBe(100);
        });

        it('token events do not replace entries array (push in-place)', () => {
            const { stream, handleEvent } = setup();
            // First token creates the entry
            handleEvent({ type: 'token', data: { text: 'Hello' } }, false, 'p1', 't1');
            const entriesRef = stream.state.entries;
            // Subsequent tokens should NOT replace the array
            handleEvent({ type: 'token', data: { text: ' world' } }, false, 'p1', 't1');
            expect(stream.state.entries).toBe(entriesRef);
            expect(stream.state.entries[0].content).toBe('Hello world');
        });

        it('100 token events produce no array allocations', () => {
            const { stream, handleEvent } = setup();
            handleEvent({ type: 'token', data: { text: 'first' } }, false, 'p1', 't1');
            const entriesRef = stream.state.entries;
            const entryRef = stream.state.entries[0];
            for (let i = 0; i < 100; i++) {
                handleEvent({ type: 'token', data: { text: ` token${i}` } }, false, 'p1', 't1');
            }
            // Entries array and entry object should be the same references
            expect(stream.state.entries).toBe(entriesRef);
            expect(stream.state.entries[0]).toBe(entryRef);
            expect(stream.state.entries[0].content).toContain(' token99');
            expect(stream.state.entries.length).toBe(1);
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
            handleEvent({ type: 'status', data: { state: 'building_image', activity: 'Building sandbox image from Dockerfile' } }, false, 'p1', 't1');
            expect(stream.state.entries).toHaveLength(1);
            const entry = stream.state.entries[0];
            expect(entry.role).toBe('system');
            expect(entry.buildLog).toBeDefined();
            expect(entry.buildLog!.title).toBe('Building sandbox image from Dockerfile...');
            expect(entry.buildLog!.lines).toEqual([]);
        });

        it('build_progress lines accumulate into a single entry', () => {
            const { stream, handleEvent } = setup();
            handleEvent({ type: 'status', data: { state: 'building_image' } }, false, 'p1', 't1');
            handleEvent({ type: 'build_progress', data: { line: 'Step 1/5 : FROM node:20' } }, false, 'p1', 't1');
            handleEvent({ type: 'build_progress', data: { line: 'Step 2/5 : RUN npm ci' } }, false, 'p1', 't1');
            handleEvent({ type: 'build_progress', data: { line: 'Step 3/5 : COPY . .' } }, false, 'p1', 't1');
            expect(stream.state.entries).toHaveLength(1);
            expect(stream.state.entries[0].buildLog!.lines).toEqual([
                'Step 1/5 : FROM node:20',
                'Step 2/5 : RUN npm ci',
                'Step 3/5 : COPY . .',
            ]);
        });

        it('build_progress without a prior status creates the box entry on demand', () => {
            const { stream, handleEvent } = setup();
            handleEvent({ type: 'build_progress', data: { line: 'Step 1/5 : FROM node:20' } }, false, 'p1', 't1');
            handleEvent({ type: 'build_progress', data: { line: 'Step 2/5 : RUN npm ci' } }, false, 'p1', 't1');
            expect(stream.state.entries).toHaveLength(1);
            expect(stream.state.entries[0].buildLog!.lines).toHaveLength(2);
        });

        it('status:pulling_image creates a buildLog entry with the pull title', () => {
            const { stream, handleEvent } = setup();
            handleEvent({ type: 'status', data: { state: 'pulling_image', activity: 'Pulling ubuntu:24.04' } }, false, 'p1', 't1');
            handleEvent({ type: 'build_progress', data: { line: 'Pulling fs layer...' } }, false, 'p1', 't1');
            expect(stream.state.entries).toHaveLength(1);
            expect(stream.state.entries[0].buildLog!.title).toBe('Pulling ubuntu:24.04...');
            expect(stream.state.entries[0].buildLog!.lines).toEqual(['Pulling fs layer...']);
        });

        it('status:running marks the active buildLog entry as done', () => {
            const { stream, handleEvent } = setup();
            handleEvent({ type: 'status', data: { state: 'building_image' } }, false, 'p1', 't1');
            handleEvent({ type: 'build_progress', data: { line: 'naming to sandbox-image' } }, false, 'p1', 't1');
            handleEvent({ type: 'status', data: { state: 'running', activity: 'Sandbox container started' } }, false, 'p1', 't1');
            expect(stream.state.entries).toHaveLength(1);
            expect(stream.state.entries[0].buildLog!.done).toBe(true);
        });

        it('a new build after a completed one starts a fresh box entry', () => {
            const { stream, handleEvent } = setup();
            handleEvent({ type: 'status', data: { state: 'building_image' } }, false, 'p1', 't1');
            handleEvent({ type: 'build_progress', data: { line: 'first build line' } }, false, 'p1', 't1');
            handleEvent({ type: 'status', data: { state: 'running' } }, false, 'p1', 't1');
            // Second build on a later task run
            handleEvent({ type: 'status', data: { state: 'building_image' } }, false, 'p1', 't1');
            handleEvent({ type: 'build_progress', data: { line: 'second build line' } }, false, 'p1', 't1');
            expect(stream.state.entries).toHaveLength(2);
            expect(stream.state.entries[0].buildLog!.done).toBe(true);
            expect(stream.state.entries[1].buildLog!.done).toBeUndefined();
            expect(stream.state.entries[1].buildLog!.lines).toEqual(['second build line']);
        });

        it('build_progress caps the line count to the last N lines', () => {
            const { stream, handleEvent } = setup();
            handleEvent({ type: 'status', data: { state: 'building_image' } }, false, 'p1', 't1');
            for (let i = 0; i < 250; i++) {
                handleEvent({ type: 'build_progress', data: { line: `line ${i}` } }, false, 'p1', 't1');
            }
            expect(stream.state.entries[0].buildLog!.lines).toHaveLength(200);
            expect(stream.state.entries[0].buildLog!.lines[0]).toBe('line 50');
            expect(stream.state.entries[0].buildLog!.lines[199]).toBe('line 249');
        });

        it('setup_failed error marks the active buildLog entry done (stops the spinner)', () => {
            const { stream, handleEvent } = setup();
            handleEvent({ type: 'status', data: { state: 'building_image' } }, false, 'p1', 't1');
            handleEvent({ type: 'build_progress', data: { line: 'ERROR: failed to solve' } }, false, 'p1', 't1');
            handleEvent({ type: 'error', data: { code: 'setup_failed', message: 'failed to build sandbox image' } }, false, 'p1', 't1');
            expect(stream.state.entries[0].buildLog!.done).toBe(true);
        });
    });
});


