/**
 * The two `collapsed-*` records of `$lib/storage`.
 *
 *  - **collapsed diff files** are keyed by view: the working diff and a checkpoint's diff can be
 *    open at the same time over two different comparisons, and each panel prunes its list to the
 *    paths it lists. With one record for both, each panel erases the other's state — and
 *    `working` keeps the key the record has always had, so a reader's existing state is still what
 *    the live panel reads back.
 *  - **collapsed tasks** are pruned to the tasks the caller still has. The read is done through
 *    the fake IndexedDB rather than a mock of `$lib/storage`: a module cannot intercept its own
 *    calls, and the check that carries the weight is the raw one — the value in the store, not
 *    only the value returned.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { createFakeIndexedDB, fakeKeysByStore, installFakeIndexedDB } from './fake-indexeddb';
import type { FakeIDBFactory } from './fake-indexeddb';

let fakeIndexedDB: FakeIDBFactory;
let restoreIndexedDB: (() => void) | undefined;

beforeEach(() => {
    vi.resetModules();
    fakeIndexedDB = createFakeIndexedDB();
    restoreIndexedDB = installFakeIndexedDB(fakeIndexedDB);
});

afterEach(() => restoreIndexedDB?.());

/** Keys one object store holds, whichever database the fake put them in. */
function keysIn(store: string): string[] {
    return Object.entries(fakeKeysByStore(fakeIndexedDB))
        .filter(([name]) => name.endsWith(`/${store}`))
        .flatMap(([, keys]) => keys);
}

/** The `ids` value sitting in the collapsed-tasks store. */
function storedIds(): string[] {
    for (const db of fakeIndexedDB.databases.values()) {
        const store = db.stores.get('collapsed-tasks');
        if (store) return [...(store.data.get('ids') ?? [])];
    }
    throw new Error('no collapsed-tasks store');
}

describe('collapsed diff files, one record per view', () => {
    it('keeps the collapsed files of the working panel and the checkpoint panel apart', async () => {
        const storage = await import('$lib/storage');
        await storage.saveCollapsedDiffFiles('working', new Set(['a.go']));
        await storage.saveCollapsedDiffFiles('checkpoint', new Set(['b.go']));

        expect(await storage.loadCollapsedDiffFiles('working')).toEqual(new Set(['a.go']));
        expect(await storage.loadCollapsedDiffFiles('checkpoint')).toEqual(new Set(['b.go']));

        await storage.saveCollapsedDiffFiles('working', new Set(['a.go', 'c.go']));
        expect(await storage.loadCollapsedDiffFiles('checkpoint')).toEqual(new Set(['b.go']));
    });

    it('reads the working view from the key the record has always had', async () => {
        const storage = await import('$lib/storage');
        await storage.saveCollapsedDiffFiles('working', new Set(['a.go']));
        await storage.saveCollapsedDiffFiles('checkpoint', new Set(['b.go']));

        expect(keysIn('collapsed-diff-files').sort()).toEqual(['ids', 'ids:checkpoint']);
        expect(await storage.loadCollapsedDiffFiles('working')).toEqual(new Set(['a.go']));
    });
});

describe('pruneCollapsedTasks', () => {
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
