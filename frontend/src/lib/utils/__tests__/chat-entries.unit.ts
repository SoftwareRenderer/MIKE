import { describe, it, expect } from 'vitest';
import { findLastUserMessageBefore, trimEntriesForRegenerate, getCheckpointEntryCheckpointState, buildCheckpointStateMap, mergeToolCallsIntoAssistant, computeLastUserMessageIds, getCheckpointThreads, buildEntriesFromMessages } from '$lib/utils/chat-entries';
import type { ChatEntry, Thread } from '$lib/types';

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

describe('buildCheckpointStateMap', () => {
    function assistant(id: string, toolCalls?: any[], hash?: string): ChatEntry {
        const base: ChatEntry = { id, role: 'assistant', content: '', isStreaming: false, createdAt: new Date().toISOString() };
        if (toolCalls) base.toolCalls = toolCalls;
        return base;
    }

    function user(id: string): ChatEntry {
        return { id, role: 'user', content: 'hello', isStreaming: false, createdAt: new Date().toISOString() };
    }

    /** Helper to verify buildCheckpointStateMap matches per-entry getCheckpointEntryCheckpointState. */
    function assertMatches(entries: ChatEntry[], pending: Set<string>, labels: Map<string, string>, hashes: Map<string, string>) {
        const map = buildCheckpointStateMap(entries, pending, labels, hashes);
        for (let i = 0; i < entries.length; i++) {
            if (entries[i].role === 'user') {
                const expected = getCheckpointEntryCheckpointState(i, entries, pending, labels, hashes);
                expect(map.get(i)).toEqual(expected);
            }
        }
    }

    it('returns empty map for entries with no user messages', () => {
        const entries = [assistant('a1')];
        const map = buildCheckpointStateMap(entries, new Set(), new Map(), new Map());
        expect(map.size).toBe(0);
    });

    it('returns null when no assistant with tool calls exists', () => {
        const entries = [user('u1'), assistant('a1')];
        const map = buildCheckpointStateMap(entries, new Set(), new Map(), new Map());
        expect(map.get(0)).toBeNull();
    });

    it('finds forward assistant with tool calls', () => {
        const entries = [user('u1'), assistant('a1', [{ toolId: 't1' }])];
        const pending = new Set<string>();
        const labels = new Map<string, string>();
        const hashes = new Map<string, string>();
        const map = buildCheckpointStateMap(entries, pending, labels, hashes);
        expect(map.get(0)).toEqual({ isPending: false, commitHash: undefined });
    });

    it('finds forward assistant with checkpoint hash', () => {
        const entries = [user('u1'), assistant('a1')];
        const hashes = new Map<string, string>();
        hashes.set('a1', 'abc123');
        const map = buildCheckpointStateMap(entries, new Set(), new Map(), hashes);
        expect(map.get(0)).toEqual({ isPending: false, commitHash: 'abc123' });
    });

    it('forward scan stops at first assistant even without tool calls', () => {
        const entries = [
            user('u1'),
            assistant('a1'),          // no tool calls — forward scan stops here
            assistant('a2', [{ toolId: 't1' }]), // has tool calls but forward already stopped
        ];
        const map = buildCheckpointStateMap(entries, new Set(), new Map(), new Map());
        expect(map.get(0)).toBeNull();
    });

    it('backward scan finds previous assistant with tool calls', () => {
        const entries = [
            user('u1'),
            assistant('a1', [{ toolId: 't1' }]),
            user('u2'),
            assistant('a2'),          // no tool calls — forward stops here for u2
        ];
        const map = buildCheckpointStateMap(entries, new Set(), new Map(), new Map());
        // u2: forward stops at a2 (no tool calls), backward finds a1
        expect(map.get(2)).toEqual({ isPending: false, commitHash: undefined });
    });

    it('backward scan finds assistant before the user when forward scan fails', () => {
        const entries = [
            assistant('a0', [{ toolId: 't0' }]),
            user('u1'),
            assistant('a1'),          // no tool calls — forward stops here for u1
        ];
        const map = buildCheckpointStateMap(entries, new Set(), new Map(), new Map());
        // u1: forward stops at a1 (no tool calls), backward scan finds a0 (has tool calls)
        // No previous user entry to bound backward scan, so a0 is visible.
        expect(map.get(1)).toEqual({ isPending: false, commitHash: undefined });
    });

    it('backward scan is bounded by previous user entry', () => {
        const entries = [
            assistant('a0', [{ toolId: 't0' }]),
            user('u1'),
            assistant('a1'),          // no tool calls — forward stops here for u1
            user('u2'),
            assistant('a2'),          // no tool calls — forward stops here for u2
        ];
        const map = buildCheckpointStateMap(entries, new Set(), new Map(), new Map());
        // u1: forward stops at a1, backward finds a0
        expect(map.get(1)).toEqual({ isPending: false, commitHash: undefined });
        // u2: forward stops at a2, backward bounded by u1 — a1 has no tool calls, a0 is before u1
        // so backward scan finds nothing
        expect(map.get(3)).toBeNull();
    });

    it('handles pending checkpoints correctly', () => {
        const entries = [user('u1'), assistant('a1', [{ toolId: 't1' }])];
        const pending = new Set<string>();
        pending.add('a1');
        const map = buildCheckpointStateMap(entries, pending, new Map(), new Map());
        expect(map.get(0)).toEqual({ isPending: true, commitHash: undefined });
    });

    it('handles commit labels', () => {
        const entries = [user('u1'), assistant('a1', [{ toolId: 't1' }])];
        const labels = new Map<string, string>();
        labels.set('a1', 'fix: bug');
        const map = buildCheckpointStateMap(entries, new Set(), labels, new Map());
        expect(map.get(0)).toEqual({ isPending: false, commitLabel: 'fix: bug', commitHash: undefined });
    });

    it('matches per-entry function across multiple users with mixed assistants', () => {
        const entries = [
            user('u1'),
            assistant('a1', [{ toolId: 't1' }]),
            user('u2'),
            assistant('a2'),
            user('u3'),
            assistant('a3', [{ toolId: 't3' }]),
            assistant('a4'),
        ];
        const hashes = new Map<string, string>();
        hashes.set('a1', 'hash1');
        hashes.set('a3', 'hash3');
        assertMatches(entries, new Set(), new Map(), hashes);
    });

    it('handles empty entries array', () => {
        const map = buildCheckpointStateMap([], new Set(), new Map(), new Map());
        expect(map.size).toBe(0);
    });

    it('handles single user entry with no assistants', () => {
        const entries = [user('u1')];
        const map = buildCheckpointStateMap(entries, new Set(), new Map(), new Map());
        expect(map.get(0)).toBeNull();
    });

    it('forward scan for second user does not use assistant consumed by first user', () => {
        const entries = [
            user('u1'),
            assistant('a1', [{ toolId: 't1' }]),
            user('u2'),
        ];
        const map = buildCheckpointStateMap(entries, new Set(), new Map(), new Map());
        // u1: forward finds a1 (has tool calls) → uses it
        expect(map.get(0)).toEqual({ isPending: false, commitHash: undefined });
        // u2: no forward assistant, backward bounded by u1 finds a1 (has tool calls, between u1 and u2)
        expect(map.get(2)).toEqual({ isPending: false, commitHash: undefined });
    });

    it('backward scan returns null when no qualifying assistant between users', () => {
        const entries = [
            user('u1'),
            assistant('a1'),          // no tool calls
            user('u2'),
        ];
        const map = buildCheckpointStateMap(entries, new Set(), new Map(), new Map());
        // u1: no forward qualifying assistant, no backward qualifying assistant
        expect(map.get(0)).toBeNull();
        // u2: no forward assistant, backward bounded by u1 — a1 has no tool calls
        expect(map.get(2)).toBeNull();
    });

    it('non-qualifying forward assistant does not block next user from backward scanning', () => {
        const entries = [
            user('u1'),
            assistant('a1', [{ toolId: 't1' }]),
            user('u2'),
            assistant('a2'),          // no tool calls — forward stops for u2
            user('u3'),
            assistant('a3', [{ toolId: 't3' }]),
        ];
        const map = buildCheckpointStateMap(entries, new Set(), new Map(), new Map());
        // u1: forward finds a1 → uses it
        expect(map.get(0)).toEqual({ isPending: false, commitHash: undefined });
        // u2: forward stops at a2 (no tool calls), backward bounded by u1 finds a1
        expect(map.get(2)).toEqual({ isPending: false, commitHash: undefined });
        // u3: forward finds a3 → uses it
        expect(map.get(4)).toEqual({ isPending: false, commitHash: undefined });
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

    it('filters out stopped error messages', () => {
        const messages = [
            { id: 'e1', role: 'error', content: '[stopped] Task was stopped', created_at: '2025-01-01T00:00:00Z' },
        ];
        const { entries } = buildEntriesFromMessages(messages);
        expect(entries).toHaveLength(0);
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
});
