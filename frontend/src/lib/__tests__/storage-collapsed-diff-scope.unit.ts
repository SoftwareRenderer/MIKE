/**
 * The working diff and a checkpoint's diff can be open at the same time over two different comparisons, and
 * each view prunes its collapsed list to the paths it lists. With one record for both, each panel erases the
 * other's state, so the record is keyed by view — and `working` keeps the key the record has always had, so a
 * reader's existing state is still what the live panel reads back.
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
