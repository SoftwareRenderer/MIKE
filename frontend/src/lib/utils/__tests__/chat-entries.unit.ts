import { describe, it, expect } from 'vitest';
import { findLastUserMessageBefore, trimEntriesForRegenerate, buildCheckpointStateMap, trailingCheckpoint, mergeToolCallsIntoAssistant, computeLastUserMessageIds, getCheckpointThreads, buildEntriesFromMessages, advisoryLabel, systemNoteText, pendingToolIds, endsOnReviewResult, editSubmitAction, takeEchoMatch, buildEntryMetas, removeFailedSend, removeDismissedError } from '$lib/utils/chat-entries';
import type { WindowRow } from '$lib/utils/chat-entries';
import type { ChatEntry, Thread } from '$lib/types';
import { PENDING_SEND_ID_PREFIX } from '$lib/utils/pending-sends';

function entry(role: string, content: string): ChatEntry {
    return { id: `${role}-${content}`, role, content, isStreaming: false, createdAt: new Date().toISOString() };
}

describe('findLastUserMessageBefore', () => {
    it('finds user message immediately before the given index', () => {
        const entries = [entry('user', 'hey'), entry('assistant', 'hi')];
        expect(findLastUserMessageBefore(entries, 1)).toBe(0);
    });

    it('skips assistant and tool entries to find user', () => {
        const entries = [
            entry('user', 'hey'),
            entry('assistant', 'hi'),
            entry('assistant', 'tool result'),
            entry('assistant', 'done'),
        ];
        expect(findLastUserMessageBefore(entries, 3)).toBe(0);
    });

    it('finds the last user when there are multiple', () => {
        const entries = [
            entry('user', 'first'),
            entry('assistant', 'response'),
            entry('user', 'second'),
            entry('assistant', 'regenerate me'),
        ];
        expect(findLastUserMessageBefore(entries, 3)).toBe(2);
    });

    it('returns -1 when no user message exists before index', () => {
        const entries = [entry('assistant', 'orphan')];
        expect(findLastUserMessageBefore(entries, 0)).toBe(-1);
    });

    it('returns -1 when the first entry is the target', () => {
        const entries = [entry('assistant', 'hi'), entry('user', 'hey')];
        expect(findLastUserMessageBefore(entries, 0)).toBe(-1);
    });
});

describe('trimEntriesForRegenerate', () => {
    it('keeps user message, removes assistant response', () => {
        const entries = [
            entry('user', 'hey'),
            entry('assistant', 'hi there'),
        ];
        const trimmed = trimEntriesForRegenerate(entries, 1);
        expect(trimmed).toHaveLength(1);
        expect(trimmed[0].role).toBe('user');
        expect(trimmed[0].content).toBe('hey');
    });

    it('preserves earlier conversation turns', () => {
        const entries = [
            entry('user', 'first'),
            entry('assistant', 'response'),
            entry('user', 'second'),
            entry('assistant', 'regenerate me'),
        ];
        const trimmed = trimEntriesForRegenerate(entries, 3);
        expect(trimmed).toHaveLength(3);
        expect(trimmed[0].content).toBe('first');
        expect(trimmed[1].content).toBe('response');
        expect(trimmed[2].content).toBe('second');
    });

    it('removes assistant and tool calls after the last user message', () => {
        const entries = [
            entry('user', 'read file'),
            entry('assistant', 'reading...'),
            entry('user', 'now summarize'),
            entry('assistant', 'tool call'),
            entry('assistant', 'here is the summary'),
        ];
        const trimmed = trimEntriesForRegenerate(entries, 4);
        expect(trimmed).toHaveLength(3);
        expect(trimmed[2].content).toBe('now summarize');
    });

    it('returns unchanged when no user message exists', () => {
        const entries = [entry('assistant', 'orphan')];
        const trimmed = trimEntriesForRegenerate(entries, 0);
        expect(trimmed).toBe(entries);
    });

    it('does not mutate the original array', () => {
        const entries = [
            entry('user', 'hey'),
            entry('assistant', 'hi'),
        ];
        const trimmed = trimEntriesForRegenerate(entries, 1);
        expect(entries).toHaveLength(2);
        expect(trimmed).not.toBe(entries);
    });
});

describe('the checkpoint band', () => {
    function assistant(id: string, toolCalls?: any[]): ChatEntry {
        const base: ChatEntry = { id, role: 'assistant', content: '', isStreaming: false, createdAt: new Date().toISOString() };
        if (toolCalls) base.toolCalls = toolCalls;
        return base;
    }

    function user(id: string): ChatEntry {
        return { id, role: 'user', content: 'hello', isStreaming: false, createdAt: new Date().toISOString() };
    }

    const hash = (...pairs: [string, string][]) => new Map<string, string>(pairs);
    /** The three maps a band reads, with the two empty ones spelled once. */
    const sources = (over: { pending?: string[]; labels?: [string, string][]; hashes?: [string, string][] } = {}) => ({
        pendingCheckpoints: new Set(over.pending ?? []),
        checkpointLabels: new Map(over.labels ?? []),
        checkpointCommitHashes: new Map(over.hashes ?? []),
    });

    it('names the newest saved turn before the message, never the one after it', () => {
        // A band says "this was the state when the message arrived". Pointing it forward drew a checkpoint
        // above the question that caused it, and drew that same band twice in a two-turn thread.
        const entries = [user('u1'), assistant('a1'), user('u2'), assistant('a2')];
        const map = buildCheckpointStateMap(entries, sources({ hashes: [['a1', 'c1'], ['a2', 'c2']] }));
        expect(map.get(0)).toBeNull(); // the first message has no state before it
        expect(map.get(2)).toEqual({ isPending: false, commitHash: 'c1' });
    });

    it('draws no band for a turn that captured nothing', () => {
        // No hash and nothing pending means there is no state to show: a bookmark over nothing is a lie
        // about what the Changes panel can answer.
        const entries = [user('u1'), assistant('a1', [{ toolId: 't1' }]), user('u2')];
        expect(buildCheckpointStateMap(entries, sources()).get(2)).toBeNull();
    });

    it('reports the band being written, so the spinner means the snapshot is on its way', () => {
        const entries = [user('u1'), assistant('a1'), user('u2')];
        const map = buildCheckpointStateMap(entries, sources({ pending: ['a1'], hashes: [['a1', 'c1']] }));
        expect(map.get(2)).toEqual({ isPending: true, commitLabel: undefined, commitHash: 'c1' });
    });

    it('carries the checkpoint label to the band', () => {
        const entries = [user('u1'), assistant('a1'), user('u2')];
        const map = buildCheckpointStateMap(entries, sources({ labels: [['a1', 'fix the gate']], hashes: [['a1', 'c1']] }));
        expect(map.get(2)).toEqual({ isPending: false, commitLabel: 'fix the gate', commitHash: 'c1' });
    });

    it('answers for the newest turn, which no later message carries', () => {
        // A band sits above a user message, so the newest turn has nothing above it: this is the only way to
        // reach the state the last turn left behind.
        const entries = [user('u1'), assistant('a1')];
        expect(trailingCheckpoint({
            entries, pendingCheckpoints: new Set(), checkpointLabels: new Map(), checkpointCommitHashes: hash(['a1', 'c1']),
        })).toEqual({ isPending: false, commitLabel: undefined, commitHash: 'c1' });
    });

    it('names the last assistant of a trailing turn, and nothing when the conversation ends on a question', () => {
        const tail = [user('u1'), assistant('a1'), assistant('a2')];
        expect(trailingCheckpoint({
            entries: tail, pendingCheckpoints: new Set(), checkpointLabels: new Map(), checkpointCommitHashes: hash(['a1', 'c1'], ['a2', 'c2']),
        })?.commitHash).toBe('c2');
        expect(trailingCheckpoint({
            entries: [user('u1'), assistant('a1'), user('u2')],
            pendingCheckpoints: new Set(), checkpointLabels: new Map(), checkpointCommitHashes: hash(['a1', 'c1']),
        })).toBeNull();
    });

    it('stays quiet for a trailing turn that saved nothing, and on an empty transcript', () => {
        expect(trailingCheckpoint({
            entries: [user('u1'), assistant('a1')],
            pendingCheckpoints: new Set(), checkpointLabels: new Map(), checkpointCommitHashes: new Map(),
        })).toBeNull();
        expect(trailingCheckpoint({
            entries: [], pendingCheckpoints: new Set(), checkpointLabels: new Map(), checkpointCommitHashes: new Map(),
        })).toBeNull();
    });

    it('reports nothing on an empty transcript', () => {
        expect(buildCheckpointStateMap([], sources()).size).toBe(0);
    });
});

describe('mergeToolCallsIntoAssistant', () => {
    it('merges tool_call entries into preceding assistant entry', () => {
        const entries: ChatEntry[] = [
            { id: 'a1', role: 'assistant', content: 'thinking...', isStreaming: false, createdAt: '2025-01-01T00:00:00Z' },
            { id: 'tc1', role: 'assistant', content: '', isStreaming: false, createdAt: '2025-01-01T00:00:01Z', toolCalls: [{ toolId: 't1', toolName: 'read_file', args: {} }] },
        ];
        const merged = mergeToolCallsIntoAssistant(entries);
        expect(merged).toHaveLength(1);
        expect(merged[0].toolCalls).toHaveLength(1);
        expect(merged[0].toolCalls![0].toolId).toBe('t1');
    });

    it('does not merge when there is no preceding assistant', () => {
        const entries: ChatEntry[] = [
            { id: 'tc1', role: 'assistant', content: '', isStreaming: false, createdAt: '2025-01-01T00:00:01Z', toolCalls: [{ toolId: 't1', toolName: 'read_file', args: {} }] },
        ];
        const merged = mergeToolCallsIntoAssistant(entries);
        expect(merged).toHaveLength(1);
        expect(merged[0].id).toBe('tc1');
    });

    it('leaves non-tool-call entries unchanged', () => {
        const entries: ChatEntry[] = [
            { id: 'u1', role: 'user', content: 'hello', isStreaming: false, createdAt: '2025-01-01T00:00:00Z' },
            { id: 'a1', role: 'assistant', content: 'hi', isStreaming: false, createdAt: '2025-01-01T00:00:01Z' },
        ];
        const merged = mergeToolCallsIntoAssistant(entries);
        expect(merged).toHaveLength(2);
        expect(merged[0].role).toBe('user');
        expect(merged[1].role).toBe('assistant');
    });

    it('merges multiple tool_call entries into one assistant', () => {
        const entries: ChatEntry[] = [
            { id: 'a1', role: 'assistant', content: 'thinking...', isStreaming: false, createdAt: '2025-01-01T00:00:00Z' },
            { id: 'tc1', role: 'assistant', content: '', isStreaming: false, createdAt: '2025-01-01T00:00:01Z', toolCalls: [{ toolId: 't1', toolName: 'read_file', args: {} }] },
            { id: 'tc2', role: 'assistant', content: '', isStreaming: false, createdAt: '2025-01-01T00:00:02Z', toolCalls: [{ toolId: 't2', toolName: 'write_file', args: {} }] },
        ];
        const merged = mergeToolCallsIntoAssistant(entries);
        expect(merged).toHaveLength(1);
        expect(merged[0].toolCalls).toHaveLength(2);
        expect(merged[0].toolCalls![0].toolId).toBe('t1');
        expect(merged[0].toolCalls![1].toolId).toBe('t2');
    });

    it('appends to existing toolCalls on the assistant entry', () => {
        const entries: ChatEntry[] = [
            { id: 'a1', role: 'assistant', content: 'thinking', isStreaming: false, createdAt: '2025-01-01T00:00:00Z', toolCalls: [{ toolId: 't0', toolName: 'existing', args: {} }] },
            { id: 'tc1', role: 'assistant', content: '', isStreaming: false, createdAt: '2025-01-01T00:00:01Z', toolCalls: [{ toolId: 't1', toolName: 'new', args: {} }] },
        ];
        const merged = mergeToolCallsIntoAssistant(entries);
        expect(merged[0].toolCalls).toHaveLength(2);
        expect(merged[0].toolCalls![0].toolId).toBe('t0');
        expect(merged[0].toolCalls![1].toolId).toBe('t1');
    });
});

describe('computeLastUserMessageIds', () => {
    it('returns user IDs with no assistant/tool_call after them', () => {
        const entries: ChatEntry[] = [
            entry('user', 'first'),
            entry('assistant', 'response'),
            entry('user', 'last'),
        ];
        const ids = computeLastUserMessageIds(entries);
        expect(ids).toContain('user-last');
        expect(ids).not.toContain('user-first');
    });

    it('returns empty set when all users have responses', () => {
        const entries: ChatEntry[] = [
            entry('user', 'first'),
            entry('assistant', 'response'),
            entry('user', 'second'),
            entry('assistant', 'response 2'),
        ];
        const ids = computeLastUserMessageIds(entries);
        expect(ids.size).toBe(0);
    });

    it('considers tool_calls as a response', () => {
        const entries: ChatEntry[] = [
            entry('user', 'first'),
            { id: 'tc', role: 'assistant', content: '', isStreaming: false, createdAt: new Date().toISOString(), toolCalls: [{ toolId: 't1', toolName: 'read', args: {} }] },
            entry('user', 'last'),
        ];
        const ids = computeLastUserMessageIds(entries);
        expect(ids).toContain('user-last');
        expect(ids).not.toContain('user-first');
    });

    it('returns empty set for empty entries', () => {
        expect(computeLastUserMessageIds([]).size).toBe(0);
    });

    it('returns empty set when only assistant entries exist', () => {
        const entries: ChatEntry[] = [entry('assistant', 'hi'), entry('assistant', 'bye')];
        expect(computeLastUserMessageIds(entries).size).toBe(0);
    });
});

describe('getCheckpointThreads', () => {
    function makeThread(id: string, checkpointTime?: string, parentThreadId?: string, isActive = false): Thread {
        return {
            id, task_id: 'task1', name: `thread-${id}`, is_active: isActive,
            checkpoint_message_time: checkpointTime,
            created_at: '2025-01-01T00:00:00Z',
            ...(parentThreadId ? { parent_thread_id: parentThreadId } : {}),
        };
    }

    it('returns null when no threads exist', () => {
        const e = entry('user', 'hello');
        expect(getCheckpointThreads(e, [], undefined)).toBeNull();
        expect(getCheckpointThreads(e, [], makeThread('t1', undefined, undefined, true))).toBeNull();
    });

    it('returns null when no active thread', () => {
        const e = entry('user', 'hello');
        expect(getCheckpointThreads(e, [makeThread('t1')], undefined)).toBeNull();
    });

    it('returns the active thread when its checkpoint is before entry time', () => {
        const e = { ...entry('user', 'hello'), createdAt: '2025-01-02T00:00:00Z' };
        const t1 = makeThread('t1', '2025-01-01T00:00:00Z', undefined, true);
        expect(getCheckpointThreads(e, [t1], t1)).toBe(t1);
    });

    it('walks up to parent when active thread checkpoint is after entry', () => {
        const e = { ...entry('user', 'hello'), createdAt: '2025-01-01T12:00:00Z' };
        const parent = makeThread('parent', '2025-01-01T00:00:00Z', undefined, false);
        const child = makeThread('child', '2025-01-02T00:00:00Z', 'parent', true);
        expect(getCheckpointThreads(e, [parent, child], child)).toBe(parent);
    });

    it('returns null with requireOptions=true when only one relevant thread', () => {
        const e = { ...entry('user', 'hello'), createdAt: '2025-01-02T00:00:00Z' };
        const t1 = makeThread('t1', '2025-01-01T00:00:00Z', undefined, true);
        expect(getCheckpointThreads(e, [t1], t1, true)).toBeNull();
    });

    it('returns thread with requireOptions=true when 2+ relevant threads', () => {
        const e = { ...entry('user', 'hello'), createdAt: '2025-01-02T00:00:00Z' };
        const t1 = makeThread('t1', '2025-01-01T00:00:00Z', undefined, true);
        const t2 = makeThread('t2', '2025-01-01T00:00:00Z', undefined, false);
        expect(getCheckpointThreads(e, [t1, t2], t1, true)).toBe(t1);
    });

    it('returns null when entry has no createdAt', () => {
        const e: ChatEntry = { id: 'u1', role: 'user', content: 'hello', isStreaming: false };
        const t1 = makeThread('t1', '2025-01-01T00:00:00Z', undefined, true);
        expect(getCheckpointThreads(e, [t1], t1)).toBeNull();
    });

    it('treats zero dates as before entry time', () => {
        const e = { ...entry('user', 'hello'), createdAt: '2025-01-02T00:00:00Z' };
        const t1 = makeThread('t1', '0001-01-01T00:00:00Z', undefined, true);
        expect(getCheckpointThreads(e, [t1], t1)).toBe(t1);
    });

    it('treats a same-second checkpoint as relevant despite precision mismatch', () => {
        // checkpoint_message_time is second-precision (RFC3339) while entry created_at
        // is nanosecond precision. A lexicographic compare would see "...36Z" > "...36.6Z"
        // and wrongly exclude the fork thread, hiding the thread switcher. Compare as dates.
        const e = { ...entry('user', 'make it count to 6'), createdAt: '2026-07-31T22:54:36.651306187Z' };
        const main = makeThread('main', undefined, undefined, false);
        const fork = makeThread('fork', '2026-07-31T22:54:36Z', 'main', true);
        expect(getCheckpointThreads(e, [main, fork], fork)).toBe(fork);
        // And with 2+ relevant threads, requireOptions is satisfied (main + fork).
        expect(getCheckpointThreads(e, [main, fork], fork, true)).toBe(fork);
    });
});

describe('buildEntriesFromMessages', () => {
    it('filters out title messages', () => {
        const messages = [
            { id: 'title', role: 'title', content: 'My Task', created_at: '2025-01-01T00:00:00Z' },
            { id: 'u1', role: 'user', content: 'hello', created_at: '2025-01-01T00:00:01Z' },
        ];
        const { entries } = buildEntriesFromMessages(messages);
        expect(entries).toHaveLength(1);
        expect(entries[0].role).toBe('user');
    });

    it('builds checkpoint hashes map', () => {
        const messages = [
            { id: 'u1', role: 'user', content: 'hello', created_at: '2025-01-01T00:00:00Z' },
            { id: 'a1', role: 'assistant', content: 'hi', created_at: '2025-01-01T00:00:01Z', git_commit_hash: 'abc123' },
        ];
        const { checkpointHashes } = buildEntriesFromMessages(messages);
        expect(checkpointHashes.get('a1')).toBe('abc123');
    });

    it('handles error messages', () => {
        const messages = [
            { id: 'e1', role: 'error', content: '[api_error] Connection failed', created_at: '2025-01-01T00:00:00Z' },
        ];
        const { entries } = buildEntriesFromMessages(messages);
        expect(entries).toHaveLength(1);
        expect(entries[0].role).toBe('error');
        expect(entries[0].content).toBe('Connection failed');
        expect(entries[0].error).toEqual({ code: 'api_error', message: 'Connection failed' });
    });

    it('collapses repeated error rows into the first one', () => {
        const messages = [
            { id: 'u1', role: 'user', content: 'hello', created_at: '2025-01-01T00:00:00Z' },
            { id: 'e1', role: 'error', content: '[llm_connection_failed] no route to host', created_at: '2025-01-01T00:00:01Z' },
            { id: 'e2', role: 'error', content: '[llm_connection_failed] no route to host', created_at: '2025-01-01T00:00:02Z' },
            { id: 'e3', role: 'error', content: '[llm_connection_failed] no route to host', created_at: '2025-01-01T00:00:03Z' },
        ];
        const { entries } = buildEntriesFromMessages(messages);
        expect(entries.filter(e => e.role === 'error')).toHaveLength(1);
        expect(entries.find(e => e.role === 'error')!.id).toBe('e1');
    });

    it('keeps an identical error separated by another message', () => {
        const messages = [
            { id: 'e1', role: 'error', content: '[llm_connection_failed] no route to host', created_at: '2025-01-01T00:00:01Z' },
            { id: 'u1', role: 'user', content: 'try again', created_at: '2025-01-01T00:00:02Z' },
            { id: 'e2', role: 'error', content: '[llm_connection_failed] no route to host', created_at: '2025-01-01T00:00:03Z' },
        ];
        const { entries } = buildEntriesFromMessages(messages);
        expect(entries.filter(e => e.role === 'error')).toHaveLength(2);
    });

    it('merges tool_call entries into preceding assistant', () => {
        const messages = [
            { id: 'a1', role: 'assistant', content: 'thinking', created_at: '2025-01-01T00:00:00Z' },
            { id: 'tc1', role: 'tool_call', content: '', created_at: '2025-01-01T00:00:01Z', tool_calls: JSON.stringify({ tool_id: 't1', tool_name: 'read_file', args: { path: 'foo.ts' } }) },
        ];
        const { entries } = buildEntriesFromMessages(messages);
        expect(entries).toHaveLength(1);
        expect(entries[0].toolCalls).toHaveLength(1);
        expect(entries[0].toolCalls![0].toolId).toBe('t1');
    });

    // A reload reads the stored row: without the flag, a call the user stopped comes back as a red
    // Failed.
    it('restores the interrupted flag from the stored tool result', () => {
        const messages = [
            { id: 'a1', role: 'assistant', content: 'thinking', created_at: '2025-01-01T00:00:00Z' },
            { id: 'tc1', role: 'tool_call', content: '', created_at: '2025-01-01T00:00:01Z', tool_calls: JSON.stringify({ tool_id: 't1', tool_name: 'bash', args: { command: 'make build' } }), tool_result: JSON.stringify({ output: 'Interrupted', success: false, interrupted: true, duration: 12 }) },
            { id: 'tc2', role: 'tool_call', content: '', created_at: '2025-01-01T00:00:02Z', tool_calls: JSON.stringify({ tool_id: 't2', tool_name: 'read_file', args: { path: 'foo.ts' } }), tool_result: JSON.stringify({ output: 'content', success: true, duration: 50 }) },
        ];
        const { entries } = buildEntriesFromMessages(messages);
        const byId = (id: string) => entries[0].toolCalls!.find(tc => tc.toolId === id);
        expect(byId('t1')!.interrupted).toBe(true);
        expect(byId('t1')!.result).toBe('Interrupted');
        expect(byId('t2')!.interrupted).toBe(false);
    });

    it('parses string attachments', () => {
        const messages = [
            { id: 'u1', role: 'user', content: 'see image', created_at: '2025-01-01T00:00:00Z', attachments: JSON.stringify([{ mimeType: 'image/png', data: 'base64data' }]) },
        ];
        const { entries } = buildEntriesFromMessages(messages);
        expect(entries[0].attachments).toEqual([{ mimeType: 'image/png', data: 'base64data' }]);
    });

    it('includes stats when model_name is present', () => {
        const messages = [
            { id: 'a1', role: 'assistant', content: 'hi', created_at: '2025-01-01T00:00:00Z', model_name: 'gpt-4', prompt_tokens: 100, predicted_tokens: 10, prompt_ms: 200, predicted_ms: 50, duration: 300 },
        ];
        const { entries } = buildEntriesFromMessages(messages);
        expect(entries[0].stats).toBeDefined();
        expect(entries[0].stats!.model_name).toBe('gpt-4');
        expect(entries[0].stats!.prompt_tokens).toBe(100);
    });

    it('carries severity on system entries', () => {
        const messages = [
            { id: 's1', role: 'system', content: 'Synced main into task branch (no conflicts).', severity: 'info', created_at: '2025-01-01T00:00:00Z' },
            { id: 's2', role: 'system', content: 'Merge conflict detected in: a.ts', created_at: '2025-01-01T00:00:01Z' },
        ];
        const { entries } = buildEntriesFromMessages(messages);
        expect(entries[0].severity).toBe('info');
        // default (warning) severity is left undefined
        expect(entries[1].severity).toBeUndefined();
    });

    it('handles merge_resolution entries', () => {
        const messages = [
            { id: 'm1', role: 'merge_resolution', content: 'Merged successfully', created_at: '2025-01-01T00:00:00Z' },
        ];
        const { entries } = buildEntriesFromMessages(messages);
        expect(entries[0].mergeLabel).toBe('Merged');
    });

    it('handles merge failed entries', () => {
        const messages = [
            { id: 'm1', role: 'merge_resolution', content: 'Merge failed: conflict in file.ts', created_at: '2025-01-01T00:00:00Z' },
        ];
        const { entries } = buildEntriesFromMessages(messages);
        expect(entries[0].mergeLabel).toBe('Merge failed');
        expect(entries[0].isError).toBe(true);
    });

    it('sets startedAt on pending tool calls from created_at', () => {
        const messages = [
            { id: 'a1', role: 'assistant', content: 'thinking', created_at: '2025-01-01T00:00:00Z' },
            { id: 'tc1', role: 'tool_call', content: '', created_at: '2025-01-01T00:05:00Z', tool_calls: JSON.stringify({ tool_id: 't1', tool_name: 'bash', args: { command: 'sleep 30' } }) },
        ];
        const { entries } = buildEntriesFromMessages(messages);
        expect(entries).toHaveLength(1);
        expect(entries[0].toolCalls).toHaveLength(1);
        expect(entries[0].toolCalls![0].isPending).toBe(true);
        expect(entries[0].toolCalls![0].startedAt).toBe(new Date('2025-01-01T00:05:00Z').getTime());
    });

    it('does not set startedAt on completed tool calls', () => {
        const messages = [
            { id: 'a1', role: 'assistant', content: 'thinking', created_at: '2025-01-01T00:00:00Z' },
            { id: 'tc1', role: 'tool_call', content: '', created_at: '2025-01-01T00:05:00Z', tool_calls: JSON.stringify({ tool_id: 't1', tool_name: 'read_file', args: { path: 'foo.ts' } }), tool_result: JSON.stringify({ output: 'content', success: true, duration: 50 }) },
        ];
        const { entries } = buildEntriesFromMessages(messages);
        expect(entries).toHaveLength(1);
        expect(entries[0].toolCalls).toHaveLength(1);
        expect(entries[0].toolCalls![0].isPending).toBe(false);
        expect(entries[0].toolCalls![0].startedAt).toBeUndefined();
    });

    // Legacy fork rows carry the role but no payload: they must render nothing, not a blank card.
    it('drops a tool_call row that carries no invocation', () => {
        const messages = [
            { id: 'u1', role: 'user', content: 'count to 10', created_at: '2025-01-01T00:00:00Z' },
            { id: 'tc-legacy', role: 'tool_call', content: '', created_at: '2025-01-01T00:00:01Z' },
        ];
        const { entries } = buildEntriesFromMessages(messages);
        expect(entries).toHaveLength(1);
        expect(entries[0].id).toBe('u1');
        expect(entries.some(e => e.role === 'tool_call')).toBe(false);
    });

    it('keeps the real tool cards around a payload-less row', () => {
        const messages = [
            { id: 'a1', role: 'assistant', content: 'thinking', created_at: '2025-01-01T00:00:00Z' },
            { id: 'tc-legacy', role: 'tool_call', content: '', created_at: '2025-01-01T00:00:01Z' },
            { id: 'tc1', role: 'tool_call', content: '', created_at: '2025-01-01T00:00:02Z', tool_calls: JSON.stringify({ tool_id: 't1', tool_name: 'write_file', args: { path: 'hello.bas' } }), tool_result: JSON.stringify({ output: 'Wrote 82 bytes', success: true }) },
        ];
        const { entries } = buildEntriesFromMessages(messages);
        expect(entries).toHaveLength(1);
        expect(entries[0].toolCalls).toHaveLength(1);
        expect(entries[0].toolCalls![0].toolId).toBe('t1');
    });
});

describe('advisoryLabel', () => {
    // The card title comes from the kind the executor recorded, never from severity:
    // both producers post this change send severity "error", so titling by severity drew a
    // merge conflict as "Workspace diagnostics".
    it('titles each known kind', () => {
        expect(advisoryLabel('diagnostics')).toBe('Workspace diagnostics');
        expect(advisoryLabel('merge_conflict')).toBe('Merge conflict');
        expect(advisoryLabel('auto_answer')).toBe('Automated answer (parent context)');
    });

    it('falls back to the generic note for unknown or missing kinds', () => {
        expect(advisoryLabel('something_new')).toBe('Environment note');
        expect(advisoryLabel(undefined)).toBe('Environment note');
    });

    it('survives the history reload (kind comes off the row, not the live event)', () => {
        const messages = [
            { id: 'adv-1', role: 'advisory', content: 'Merge conflict in b.go', severity: 'error', kind: 'merge_conflict', created_at: '2025-01-01T00:00:00Z' },
            { id: 'adv-2', role: 'advisory', content: 'no kind row', created_at: '2025-01-01T00:00:01Z' },
            { id: 'adv-3', role: 'advisory', content: 'Yes — the docs are in scope.', kind: 'auto_answer', created_at: '2025-01-01T00:00:02Z' },
        ];
        const { entries } = buildEntriesFromMessages(messages);
        expect(entries[0].kind).toBe('merge_conflict');
        expect(advisoryLabel(entries[0].kind)).toBe('Merge conflict');
        expect(advisoryLabel(entries[1].kind)).toBe('Environment note');
        expect(advisoryLabel(entries[2].kind)).toBe('Automated answer (parent context)');
        // Never rendered as something the user said.
        expect(entries.every(e => e.role === 'advisory')).toBe(true);
    });
});

describe('systemNoteText', () => {
    it('words each known system-note kind from the catalog', () => {
        expect(systemNoteText('memory_pressure', 'server text')).toBe(
            'Task has been temporarily paused due to memory pressure, and will automatically resume');
        expect(systemNoteText('memory_resumed', 'server text')).toBe(
            'Memory freed up, so the task resumed automatically');
    });

    it('shows an unknown or missing kind its own text', () => {
        expect(systemNoteText('retry', 'Connection interrupted, retrying the request.')).toBe(
            'Connection interrupted, retrying the request.');
        expect(systemNoteText(undefined, 'Merge conflict detected during commit')).toBe(
            'Merge conflict detected during commit');
    });
});

describe('pendingToolIds', () => {
    function withCalls(id: string, calls: Array<{ toolId: string; isPending: boolean }>): ChatEntry {
        return {
            id, role: 'assistant', content: '', isStreaming: false, createdAt: new Date().toISOString(),
            toolCalls: calls.map(c => ({ toolId: c.toolId, toolName: 'bash', isPending: c.isPending })),
        } as unknown as ChatEntry;
    }

    it('names the calls a rebuild left waiting for an answer', () => {
        const entries = [
            withCalls('a', [{ toolId: 'call-1', isPending: true }]),
            withCalls('b', [{ toolId: 'call-2', isPending: false }]),
        ];
        expect([...pendingToolIds(entries)]).toEqual(['call-1']);
    });

    it('settles every call of a rebuild that carries none waiting', () => {
        const entries = [entry('assistant', 'the turn that was stopped')];
        expect(pendingToolIds(entries).size).toBe(0);
    });

    it('reports each call of one entry separately', () => {
        const entries = [withCalls('a', [
            { toolId: 'call-1', isPending: false },
            { toolId: 'call-2', isPending: true },
        ])];
        expect([...pendingToolIds(entries)]).toEqual(['call-2']);
    });
});

describe('endsOnReviewResult', () => {
    // The rows are read as the cache holds them: JSON in the two payload columns, one row per stored message.
    function reviewRow(): WindowRow {
        return {
            role: 'tool_call',
            tool_calls: JSON.stringify({ tool_name: 'request_review', args: { summary: 'Done.' } }),
            tool_result: '{"output":"Task complete"}',
        };
    }

    it('reads a window whose newest row is an answered request_review as a finished turn', () => {
        const rows: WindowRow[] = [{ role: 'user' }, reviewRow()];
        expect(endsOnReviewResult(rows)).toBe(true);
    });

    it('paints nothing when the card has no result yet — the call is the turn still running', () => {
        const running = reviewRow();
        delete running.tool_result;
        expect(endsOnReviewResult([running])).toBe(false);
    });

    it('walks past the advisory rows that follow the card', () => {
        // A diagnostics or auto-answer row is written after the run ended; the turn still finished.
        const rows: WindowRow[] = [reviewRow(), { role: 'advisory', kind: 'diagnostics' }];
        expect(endsOnReviewResult(rows)).toBe(true);
    });

    it('paints nothing when the newest row is a user message nothing has answered', () => {
        const rows: WindowRow[] = [reviewRow(), { role: 'user' }];
        expect(endsOnReviewResult(rows)).toBe(false);
    });

    it('paints nothing when a call is still waiting for its result', () => {
        // The card may be below it, but a call with no `tool_result` is work that can still move.
        const rows: WindowRow[] = [reviewRow(), { role: 'tool_call', tool_calls: '{"tool_name":"bash"}' }];
        expect(endsOnReviewResult(rows)).toBe(false);
    });

    it('paints nothing for a window whose newest card is a different tool', () => {
        const asked = reviewRow();
        asked.tool_calls = JSON.stringify({ tool_name: 'ask_question' });
        expect(endsOnReviewResult([asked])).toBe(false);
    });

    it('paints nothing when the card payload will not read', () => {
        // Unparseable evidence is not evidence: the safe answer is to wait for the read.
        const broken = reviewRow();
        broken.tool_calls = '{"tool_name"';
        expect(endsOnReviewResult([broken])).toBe(false);
    });

    it('paints nothing for a window with no content row at all', () => {
        expect(endsOnReviewResult([])).toBe(false);
        expect(endsOnReviewResult([{ role: 'title' }])).toBe(false);
    });
});

describe('editSubmitAction', () => {
    // The label and the callback are one decision, so every case names what the click does, not what it
    // is called: a backlog task starts nothing, only the last message re-runs, everything else forks.
    const shapes = {
        backlogFirst: { isFirstUserMessage: true, isLastUserMessage: true, taskStatus: 'backlog' },
        backlogMiddle: { isFirstUserMessage: false, isLastUserMessage: false, taskStatus: 'backlog' },
        firstAndLast: { isFirstUserMessage: true, isLastUserMessage: true, taskStatus: 'in_progress' },
        firstOnly: { isFirstUserMessage: true, isLastUserMessage: false, taskStatus: 'review' },
        last: { isFirstUserMessage: false, isLastUserMessage: true, taskStatus: 'in_progress' },
        middle: { isFirstUserMessage: false, isLastUserMessage: false, taskStatus: 'review' },
    };

    it('saves every message of a backlog task, whatever its position', () => {
        // Both edit handlers return after persisting the text when the task has not run, so a backlog
        // middle message that promised "New Thread" would fork nothing.
        expect(editSubmitAction(shapes.backlogFirst)).toBe('save');
        expect(editSubmitAction(shapes.backlogMiddle)).toBe('save');
    });

    it('sends the last message of a started task again', () => {
        expect(editSubmitAction(shapes.last)).toBe('send');
    });

    it('forks from the first message even when it is also the last', () => {
        // Re-answering the opening question must not erase the answer that follows it.
        expect(editSubmitAction(shapes.firstAndLast)).toBe('fork');
    });

    it('forks from a first message that has been answered', () => {
        expect(editSubmitAction(shapes.firstOnly)).toBe('fork');
    });

    it('forks from a message in the middle of the thread', () => {
        expect(editSubmitAction(shapes.middle)).toBe('fork');
    });

    it('saves in place when the caller owns the text', () => {
        // The summarized context and the mobile box rewrite where they stand regardless of position.
        expect(editSubmitAction({ ...shapes.middle, forcedSave: true })).toBe('save');
        expect(editSubmitAction({ ...shapes.last, forcedSave: true })).toBe('save');
    });
});

describe('takeEchoMatch', () => {
    const user = (id: string, content: string): ChatEntry =>
        ({ id, role: 'user', content, isStreaming: false, createdAt: new Date().toISOString() });
    const transcript = [user('old-row', 'deploy it'), user('a7k3m2qp', 'deploy it')];

    it('renames the optimistic entry the id names, not the older identical message', () => {
        // Two sends of the same words are one echo each; the content rule would hand the row id to the
        // older message and leave the newer one a permanent stranger.
        expect(takeEchoMatch(transcript, { entryId: 'a7k3m2qp', content: 'deploy it' })?.id).toBe('a7k3m2qp');
    });

    it('answers nothing when the id no longer resolves', () => {
        // The transcript was rebuilt from rows while the request was in flight: appending the echo is the
        // honest answer, renaming some other message is not.
        expect(takeEchoMatch(transcript, { entryId: 'gone-1', content: 'deploy it' })).toBeNull();
    });

    it('matches the newest identical message when no id was carried', () => {
        // A regenerate or a fork: the question is already a row, and the newest copy is the one answered.
        expect(takeEchoMatch(transcript, { content: 'deploy it' })?.id).toBe('a7k3m2qp');
    });

    it('leaves a row derived from the offline queue alone', () => {
        // That row is the queue's projection of a send; adopting it as a persisted row leaves it queued.
        const only = [user(`${PENDING_SEND_ID_PREFIX}act-1`, 'deploy it')];
        expect(takeEchoMatch(only, { content: 'deploy it' })).toBeNull();
    });
});

describe('buildEntryMetas', () => {
    const user = (id: string): ChatEntry =>
        ({ id, role: 'user', content: `ask ${id}`, isStreaming: false, createdAt: new Date().toISOString() });
    const assistant = (id: string): ChatEntry =>
        ({ id, role: 'assistant', content: `answer ${id}`, isStreaming: false, createdAt: new Date().toISOString() });

    // Two turns, each checkpointed: the hash is what makes an assistant a checkpoint at all.
    const twoTurns = [user('u1'), assistant('a1'), user('u2'), assistant('a2')];
    const hashes = new Map([['a1', 'c1'], ['a2', 'c2']]);
    const base = {
        entries: twoTurns,
        pendingCheckpoints: new Set<string>(),
        checkpointLabels: new Map<string, string>(),
        checkpointCommitHashes: hashes,
        threads: [],
        lastUserMessageIds: new Set(['u2']),
        streaming: false,
    };
    const metas = (over: Record<string, unknown> = {}) => buildEntryMetas({ ...base, ...over } as never);

    it('calls the oldest loaded row the first message only when nothing older is unloaded', () => {
        expect(metas()[0].isFirstUserMessage).toBe(true);
        // The same window with older rows still to fetch: entry 0 is the oldest *loaded* row, and treating
        // it as the thread's first hides its delete button and sends an edit down the wrong path.
        expect(metas({ hasEarlier: true })[0].isFirstUserMessage).toBe(false);
    });

    it('never calls an assistant row the first message', () => {
        expect(metas({ entries: [assistant('a1'), user('u1')], lastUserMessageIds: new Set(['u1']) })[0].isFirstUserMessage).toBe(false);
    });

    it('marks only the newest unanswered prompt as the last message', () => {
        expect(metas().map(m => m.isLastUserMessage)).toEqual([false, false, true, false]);
    });

    it('leaves no prompt marked last while a turn is in flight', () => {
        // Anything may still arrive for it, so "send this again" is not yet a true statement.
        expect(metas({ streaming: true }).every(m => !m.isLastUserMessage)).toBe(true);
    });

    it('puts each checkpoint on one band, at the message that follows the turn that captured it', () => {
        // A band points back, never forward: a1 belongs above u2 (the message its work preceded), a2 is the
        // trailing band, so no checkpoint is drawn twice and none above the question that caused it.
        expect(metas().map(m => m.cpState?.commitHash ?? null)).toEqual([null, null, 'c1', null]);
        expect(trailingCheckpoint({
            entries: twoTurns,
            pendingCheckpoints: base.pendingCheckpoints,
            checkpointLabels: base.checkpointLabels,
            checkpointCommitHashes: hashes,
        })).toEqual({ isPending: false, commitLabel: undefined, commitHash: 'c2' });
    });
});

describe('a send that failed for good', () => {
    const copy = { failed: 'The message could not be sent' };
    const userRow = (id: string, content: string, extra: Partial<ChatEntry> = {}): ChatEntry =>
        ({ id, role: 'user', content, isStreaming: false, createdAt: new Date().toISOString(), ...extra });
    // The same words asked twice: only the row this send wrote may go.
    const twice = [userRow('m1', 'deploy it'), { ...userRow('a1', 'not yet'), role: 'assistant' }, userRow('opt-9', 'deploy it')];

    it('removes the optimistic row by its id and leaves the identical older question', () => {
        const after = removeFailedSend(twice, { entryId: 'opt-9', copy });
        expect(after.filter(e => e.role === 'user').map(e => e.id)).toEqual(['m1']);
    });

    it('removes exactly one row when the send carried no text', () => {
        // The filter this replaced matched on content, and an attachments-only send has none: it removed
        // every textless user row it could find and left the notice beside a message still on screen.
        const attachmentsOnly = [userRow('m1', '', { attachments: [{ mimeType: 'image/png', data: 'AAA' }] }), userRow('opt-9', '')];
        const after = removeFailedSend(attachmentsOnly, { entryId: 'opt-9', copy });
        expect(after.filter(e => e.role === 'user')).toHaveLength(1);
    });

    it('appends a notice the dismiss handler can tell from a server row', () => {
        const after = removeFailedSend([userRow('opt-9', 'deploy it')], { entryId: 'opt-9', copy });
        const notice = after[after.length - 1]!;
        expect([notice.role, notice.error?.code, notice.local]).toEqual(['error', 'send_failed', true]);
        expect(notice.content).toBe(copy.failed);
    });
});

describe('dismissing one entry', () => {
    const notice: ChatEntry = { id: 'n1', role: 'error', content: 'queued', isStreaming: false, error: { code: 'send_queued', message: 'queued' }, createdAt: '', local: true };
    const persisted: ChatEntry = { id: 'm2', role: 'error', content: 'boom', isStreaming: false, error: { code: 'llm_error', message: 'boom' }, createdAt: '' };
    const queuedRow: ChatEntry = { id: 'q1', role: 'user', content: 'waiting', isStreaming: false, createdAt: '', queued: true };
    const transcript = [notice, persisted, queuedRow];

    it('drops a notice this browser wrote without asking the server', () => {
        const { entries, deleteId } = removeDismissedError(transcript, 'n1');
        expect(deleteId).toBeNull();
        expect(entries.map(e => e.id)).toEqual(['m2', 'q1']);
    });

    it('asks the server for a row that is really in the database', () => {
        const { entries, deleteId } = removeDismissedError(transcript, 'm2');
        expect(deleteId).toBe('m2');
        // The drawing changes only once the delete answers, which is the caller's step.
        expect(entries.map(e => e.id)).toEqual(['n1', 'm2', 'q1']);
    });

    it('leaves a queued row alone — its send is still waiting in the queue', () => {
        const { entries, deleteId } = removeDismissedError(transcript, 'q1');
        expect(deleteId).toBeNull();
        expect(entries).toHaveLength(3);
    });
});
