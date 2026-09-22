import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { installFakeIndexedDB } from './fake-indexeddb';

// Runs against the fake IndexedDB rather than a mock of `$lib/storage`: a module cannot intercept
// its own calls.

describe('pruneCollapsedTasks', () => {
    let restoreIndexedDB: (() => void) | undefined;

    beforeEach(() => {
        vi.resetModules();
        restoreIndexedDB = installFakeIndexedDB();
    });

    afterEach(() => restoreIndexedDB?.());

    it('keeps the entries the caller still has tasks for, in memory and on disk', async () => {
        const { saveCollapsedTasks, loadCollapsedTasks, pruneCollapsedTasks } = await import('$lib/storage');
        await saveCollapsedTasks(new Set(['t1', 't2', 't3']));

        const kept = await pruneCollapsedTasks(new Set(['t1', 't3', 't9']));

        expect([...kept].sort()).toEqual(['t1', 't3']);
        expect([...(await loadCollapsedTasks())].sort()).toEqual(['t1', 't3']);
    });

    it('leaves nothing behind when every task is gone', async () => {
        const { saveCollapsedTasks, loadCollapsedTasks, pruneCollapsedTasks } = await import('$lib/storage');
        await saveCollapsedTasks(new Set(['gone-1', 'gone-2']));

        expect((await pruneCollapsedTasks(new Set<string>())).size).toBe(0);
        expect((await loadCollapsedTasks()).size).toBe(0);
    });

    it('answers an empty store with an empty set', async () => {
        const { pruneCollapsedTasks } = await import('$lib/storage');
        expect((await pruneCollapsedTasks(new Set(['t1']))).size).toBe(0);
    });
});
