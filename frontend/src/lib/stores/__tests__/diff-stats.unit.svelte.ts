import { describe, it, expect, beforeEach } from 'vitest';
import { DiffStatsStore } from '$lib/stores/diff-stats.svelte';
import type { DiffStatsEntry } from '$lib/types';

function stats(hash: string): DiffStatsEntry {
    return { hash, files: 1, additions: 2, deletions: 3 };
}

describe('DiffStatsStore', () => {
    let store: DiffStatsStore;

    beforeEach(() => {
        store = new DiffStatsStore();
    });

    it('has nothing for a task it never heard of', () => {
        expect(store.get('nope')).toBeNull();
        expect(store.has('nope')).toBe(false);
    });

    it('keeps one entry per task', () => {
        store.set('t1', stats('a'));
        store.set('t2', stats('b'));
        expect(store.get('t1')?.hash).toBe('a');
        expect(store.get('t2')?.hash).toBe('b');
    });

    it('drops one entry and leaves the others', () => {
        store.set('t1', stats('a'));
        store.set('t2', stats('b'));
        store.remove('t1');
        expect(store.get('t1')).toBeNull();
        expect(store.get('t2')?.hash).toBe('b');
    });

    it('remove is a no-op for a task it never heard of', () => {
        expect(() => store.remove('nope')).not.toThrow();
    });

    it('fills only the tasks with nothing live, so a pushed frame wins over the mirror', () => {
        store.set('t1', stats('live'));
        store.fillAbsent({ t1: stats('cached'), t2: stats('cached') });
        expect(store.get('t1')?.hash).toBe('live');
        expect(store.get('t2')?.hash).toBe('cached');
    });

    it('writes nothing when the mirror has nothing to add', () => {
        store.set('t1', stats('live'));
        store.fillAbsent({ t1: stats('cached') });
        expect(store.get('t1')?.hash).toBe('live');
    });
});
