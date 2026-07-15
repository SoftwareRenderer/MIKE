import { describe, it, expect, vi, beforeEach } from 'vitest';

// Mock storage functions before importing helpers
vi.mock('$lib/storage', () => ({
    loadCollapsedTasks: vi.fn(async () => new Set<string>()),
    saveCollapsedTasks: vi.fn(async () => {}),
    loadAppSetting: vi.fn(async () => false),
    saveAppSetting: vi.fn(async () => {}),
}));

import { persistCollapsedTasks, cleanupCollapsedTasks, initExpandedTasks } from '$lib/stores/app.svelte';
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

describe('cleanupCollapsedTasks', () => {
    beforeEach(() => {
        vi.clearAllMocks();
    });

    it('removes stale task IDs that no longer exist', async () => {
        (storage.loadCollapsedTasks as any).mockResolvedValue(new Set(['t1', 't2', 't3']));
        const allIds = new Set(['t1', 't3']); // t2 is stale
        const result = await cleanupCollapsedTasks(allIds);
        expect(result.has('t1')).toBe(true);
        expect(result.has('t2')).toBe(false);
        expect(result.has('t3')).toBe(true);
        expect(storage.saveCollapsedTasks).toHaveBeenCalled();
    });

    it('does not save when no stale entries exist', async () => {
        (storage.loadCollapsedTasks as any).mockResolvedValue(new Set(['t1', 't2']));
        const allIds = new Set(['t1', 't2', 't3']);
        await cleanupCollapsedTasks(allIds);
        expect(storage.saveCollapsedTasks).not.toHaveBeenCalled();
    });

    it('returns cleaned set', async () => {
        (storage.loadCollapsedTasks as any).mockResolvedValue(new Set(['stale1', 'stale2']));
        const allIds = new Set<string>(); // none exist
        const result = await cleanupCollapsedTasks(allIds);
        expect(result.size).toBe(0);
        expect(storage.saveCollapsedTasks).toHaveBeenCalled();
    });
});

describe('initExpandedTasks', () => {
    beforeEach(() => {
        vi.clearAllMocks();
    });

    it('returns all parent IDs minus collapsed ones', async () => {
        (storage.loadCollapsedTasks as any).mockResolvedValue(new Set(['t2']));
        const parents = new Set(['t1', 't2', 't3']);
        const expanded = await initExpandedTasks(parents);
        expect(expanded.has('t1')).toBe(true);
        expect(expanded.has('t2')).toBe(false);
        expect(expanded.has('t3')).toBe(true);
    });

    it('removes stale collapsed entries before computing', async () => {
        (storage.loadCollapsedTasks as any).mockResolvedValue(new Set(['t2', 'stale']));
        const parents = new Set(['t1', 't2', 't3']);
        const expanded = await initExpandedTasks(parents);
        expect(expanded.size).toBe(2);
        expect(expanded.has('t1')).toBe(true);
        expect(expanded.has('t3')).toBe(true);
        // saveCollapsedTasks should be called with cleaned set
        const saved = (storage.saveCollapsedTasks as any).mock.calls[0][0];
        expect(saved.has('stale')).toBe(false);
    });

    it('returns all parents when none are collapsed', async () => {
        (storage.loadCollapsedTasks as any).mockResolvedValue(new Set<string>());
        const parents = new Set(['t1', 't2']);
        const expanded = await initExpandedTasks(parents);
        expect(expanded.has('t1')).toBe(true);
        expect(expanded.has('t2')).toBe(true);
    });

    it('returns empty set when all parents are collapsed', async () => {
        (storage.loadCollapsedTasks as any).mockResolvedValue(new Set(['t1', 't2']));
        const parents = new Set(['t1', 't2']);
        const expanded = await initExpandedTasks(parents);
        expect(expanded.size).toBe(0);
    });
});
