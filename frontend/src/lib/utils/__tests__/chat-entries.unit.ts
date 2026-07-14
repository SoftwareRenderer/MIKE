import { describe, it, expect } from 'vitest';
import { findLastUserMessageBefore, trimEntriesForRegenerate, getCheckpointEntryCheckpointState, buildCheckpointStateMap } from '$lib/utils/chat-entries';
import type { ChatEntry } from '$lib/types';

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
