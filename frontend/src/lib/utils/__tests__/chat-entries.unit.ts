import { describe, it, expect } from 'vitest';
import { findLastUserMessageBefore, trimEntriesForRegenerate } from '$lib/utils/chat-entries';
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
