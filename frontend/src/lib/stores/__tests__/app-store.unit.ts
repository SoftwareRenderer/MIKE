import { describe, it, expect, vi, beforeEach } from 'vitest';

// Mock storage functions before importing helpers
vi.mock('$lib/storage', () => ({
    saveCollapsedTasks: vi.fn(async () => {}),
    pruneCollapsedTasks: vi.fn(async () => new Set<string>()),
    loadAppSetting: vi.fn(async () => false),
    saveAppSetting: vi.fn(async () => {}),
}));

import { persistCollapsedTasks, initExpandedTasks } from '$lib/stores/app.svelte';
import * as storage from '$lib/storage';

describe('persistCollapsedTasks', () => {
    beforeEach(() => {
        vi.clearAllMocks();
    });

    it('saves parent IDs that are not in the expanded set', async () => {
        const expanded = new Set(['t1', 't3']);
        const parents = new Set(['t1', 't2', 't3', 't4']);
        await persistCollapsedTasks(expanded, parents);
        expect(storage.saveCollapsedTasks).toHaveBeenCalledWith(
            expect.objectContaining({ size: 2 }),
        );
        const saved = (storage.saveCollapsedTasks as any).mock.calls[0][0];
        expect(saved.has('t2')).toBe(true);
        expect(saved.has('t4')).toBe(true);
        expect(saved.has('t1')).toBe(false);
    });

    it('saves empty set when all parents are expanded', async () => {
        const expanded = new Set(['t1', 't2']);
        const parents = new Set(['t1', 't2']);
        await persistCollapsedTasks(expanded, parents);
        const saved = (storage.saveCollapsedTasks as any).mock.calls[0][0];
        expect(saved.size).toBe(0);
    });

    it('saves all parent IDs when none are expanded', async () => {
        const expanded = new Set<string>();
        const parents = new Set(['t1', 't2', 't3']);
        await persistCollapsedTasks(expanded, parents);
        const saved = (storage.saveCollapsedTasks as any).mock.calls[0][0];
        expect(saved.size).toBe(3);
        expect(saved.has('t1')).toBe(true);
        expect(saved.has('t2')).toBe(true);
        expect(saved.has('t3')).toBe(true);
    });
});

describe('initExpandedTasks', () => {
    beforeEach(() => {
        vi.clearAllMocks();
    });

    it('returns all parent IDs minus collapsed ones', async () => {
        (storage.pruneCollapsedTasks as any).mockResolvedValue(new Set(['t2']));
        const parents = new Set(['t1', 't2', 't3']);
        const expanded = await initExpandedTasks(parents);
        expect(expanded.has('t1')).toBe(true);
        expect(expanded.has('t2')).toBe(false);
        expect(expanded.has('t3')).toBe(true);
    });

    it('prunes against its own parent set, so a stale entry cannot survive a reload', async () => {
        (storage.pruneCollapsedTasks as any).mockResolvedValue(new Set(['t2']));
        const parents = new Set(['t1', 't2', 't3']);
        const expanded = await initExpandedTasks(parents);
        expect(storage.pruneCollapsedTasks).toHaveBeenCalledWith(parents);
        expect(expanded.size).toBe(2);
        expect(expanded.has('t1')).toBe(true);
        expect(expanded.has('t3')).toBe(true);
    });

    it('returns all parents when none are collapsed', async () => {
        (storage.pruneCollapsedTasks as any).mockResolvedValue(new Set<string>());
        const parents = new Set(['t1', 't2']);
        const expanded = await initExpandedTasks(parents);
        expect(expanded.has('t1')).toBe(true);
        expect(expanded.has('t2')).toBe(true);
    });

    it('returns empty set when all parents are collapsed', async () => {
        (storage.pruneCollapsedTasks as any).mockResolvedValue(new Set(['t1', 't2']));
        const parents = new Set(['t1', 't2']);
        const expanded = await initExpandedTasks(parents);
        expect(expanded.size).toBe(0);
    });
});
