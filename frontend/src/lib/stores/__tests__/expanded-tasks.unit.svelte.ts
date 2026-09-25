// The collapsed set is this browser's own: what it writes, and what survives a reload.
import { describe, it, expect, vi, beforeEach } from 'vitest';

vi.mock('$lib/storage', () => ({
    saveCollapsedTasks: vi.fn(async () => {}),
    pruneCollapsedTasks: vi.fn(async () => new Set<string>()),
}));

import { ExpandedTasksStore } from '$lib/stores/expanded-tasks.svelte';
import * as storage from '$lib/storage';

describe('ExpandedTasksStore', () => {
    let store: ExpandedTasksStore;

    beforeEach(() => {
        vi.clearAllMocks();
        store = new ExpandedTasksStore();
    });

    it('starts with nothing expanded', () => {
        expect(store.all.size).toBe(0);
        expect(store.has('t1')).toBe(false);
    });

    it('toggles a task in and back out', () => {
        store.toggle('t1');
        expect(store.has('t1')).toBe(true);
        store.toggle('t1');
        expect(store.has('t1')).toBe(false);
    });

    it('leaves the previous set alone when it adds', () => {
        store.add('t1');
        const before = store.all;
        store.add('t2');
        expect(before.has('t2')).toBe(false);
        expect(store.all.has('t2')).toBe(true);
    });

    it('writes the parents it does not hold as collapsed', async () => {
        store.replace(new Set(['t1', 't3']));
        await store.persist(new Set(['t1', 't2', 't3', 't4']), new Set(['t1', 't2', 't3', 't4']));
        const saved = (storage.saveCollapsedTasks as any).mock.calls[0][0] as Set<string>;
        expect([...saved].sort()).toEqual(['t2', 't4']);
    });

    it('writes every parent when none are expanded', async () => {
        await store.persist(new Set(['t1', 't2']), new Set(['t1', 't2']));
        const saved = (storage.saveCollapsedTasks as any).mock.calls[0][0] as Set<string>;
        expect(saved.size).toBe(2);
    });

    it('prunes the stored entries for tasks that no longer exist', async () => {
        const allIds = new Set(['t1', 't2']);
        await store.persist(new Set(['t1']), allIds);
        expect(storage.pruneCollapsedTasks).toHaveBeenCalledWith(allIds);
    });

    it('expands every parent that is not stored as collapsed', async () => {
        (storage.pruneCollapsedTasks as any).mockResolvedValue(new Set(['t2']));
        await store.load(new Set(['t1', 't2', 't3']), new Set(['t1', 't2', 't3']));
        expect([...store.all].sort()).toEqual(['t1', 't3']);
    });

    it('expands all parents when nothing is stored', async () => {
        (storage.pruneCollapsedTasks as any).mockResolvedValue(new Set<string>());
        await store.load(new Set(['t1', 't2']), new Set(['t1', 't2']));
        expect(store.all.size).toBe(2);
    });

    it('leaves nothing expanded when every parent is stored as collapsed', async () => {
        (storage.pruneCollapsedTasks as any).mockResolvedValue(new Set(['t1', 't2']));
        await store.load(new Set(['t1', 't2']), new Set(['t1', 't2']));
        expect(store.all.size).toBe(0);
    });

    it('prunes against the whole task list before judging the parents', async () => {
        const allIds = new Set(['t1', 't2', 'gone']);
        const parents = new Set(['t1', 't2']);
        await store.load(parents, allIds);
        const pruned = (storage.pruneCollapsedTasks as any).mock.calls.map((c: unknown[]) => c[0]);
        expect(pruned[0]).toBe(allIds);
        expect(pruned[1]).toBe(parents);
    });
});
