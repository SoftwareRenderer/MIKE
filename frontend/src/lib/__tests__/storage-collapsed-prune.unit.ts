import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { createFakeIndexedDB, installFakeIndexedDB } from './fake-indexeddb';
import type { FakeIDBFactory } from './fake-indexeddb';

// Runs against the fake IndexedDB rather than a mock of `$lib/storage`: a module cannot intercept
// its own calls.

let fakeIndexedDB: FakeIDBFactory;

/** The `ids` value sitting in the collapsed-tasks store, so a prune that answers correctly
 * without writing is still caught. */
function storedIds(): string[] {
    for (const db of fakeIndexedDB.databases.values()) {
        const store = db.stores.get('collapsed-tasks');
        if (store) return [...(store.data.get('ids') ?? [])];
    }
    throw new Error('no collapsed-tasks store');
}

describe('pruneCollapsedTasks', () => {
    let restoreIndexedDB: (() => void) | undefined;

    beforeEach(() => {
        vi.resetModules();
        fakeIndexedDB = createFakeIndexedDB();
        restoreIndexedDB = installFakeIndexedDB(fakeIndexedDB);
    });

    afterEach(() => restoreIndexedDB?.());

    it('keeps the entries the caller still has tasks for, in memory and on disk', async () => {
        const { saveCollapsedTasks, pruneCollapsedTasks } = await import('$lib/storage');
        await saveCollapsedTasks(new Set(['t1', 't2', 't3']));

        const kept = await pruneCollapsedTasks(new Set(['t1', 't3', 't9']));

        expect([...kept].sort()).toEqual(['t1', 't3']);
        expect(storedIds().sort()).toEqual(['t1', 't3']);
    });

    it('leaves nothing behind when every task is gone', async () => {
        const { saveCollapsedTasks, pruneCollapsedTasks } = await import('$lib/storage');
        await saveCollapsedTasks(new Set(['gone-1', 'gone-2']));

        expect((await pruneCollapsedTasks(new Set<string>())).size).toBe(0);
        expect(storedIds()).toEqual([]);
    });

    it('answers an empty store with an empty set', async () => {
        const { pruneCollapsedTasks } = await import('$lib/storage');
        expect((await pruneCollapsedTasks(new Set(['t1']))).size).toBe(0);
    });
});
