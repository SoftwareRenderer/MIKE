import { describe, it, expect, vi } from 'vitest';
import { StreamState } from '$lib/services/stream-state.svelte';
import { createStreamHandler, type StreamHandlerCallbacks, estimateTokens } from '$lib/services/stream-handler';

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
        // 43 chars / 4 ≈ 11 tokens
        expect(stream.state.streamTokenCount).toBe(estimateTokens('Hello world this is a batched token event'));
        // Second batched event adds more
        handleEvent({ type: 'token', data: { text: ' more tokens here' } }, false, 'p1', 't1');
        expect(stream.state.streamTokenCount).toBe(
            estimateTokens('Hello world this is a batched token event') + estimateTokens(' more tokens here'),
        );
    });

    it('reasoning event estimates tokens from batched text', () => {
        const { stream, handleEvent } = setup();
        handleEvent({ type: 'reasoning', data: { text: 'Let me think about this problem carefully' } }, false, 'p1', 't1');
        expect(stream.state.streamTokenCount).toBe(estimateTokens('Let me think about this problem carefully'));
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
});

describe('estimateTokens', () => {
    it('returns 0 for empty string', () => {
        expect(estimateTokens('')).toBe(0);
    });

    it('estimates ~1 token for short text', () => {
        expect(estimateTokens('Hi')).toBe(1);
        expect(estimateTokens('Hello')).toBe(2);
    });

    it('estimates proportionally for longer text', () => {
        // 11 chars / 4 = 2.75 → 3
        expect(estimateTokens('Hello world')).toBe(3);
        // 44 chars / 4 = 11
        expect(estimateTokens('This is a sentence with multiple words in it')).toBe(11);
    });

    it('handles newlines and spaces', () => {
        // 18 chars / 4 = 4.5 → 5
        expect(estimateTokens('Line one\nLine two')).toBe(5);
    });
});
