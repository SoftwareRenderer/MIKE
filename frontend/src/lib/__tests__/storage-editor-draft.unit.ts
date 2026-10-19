/**
 * A draft carries the bytes it diverged from, because reopening a file the server changed is a three-way
 * merge. A record written before the base was kept still has to read back as a draft.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { createFakeIndexedDB, installFakeIndexedDB, type FakeIDBFactory } from './fake-indexeddb';

const PATH = '/repo/src/main.go';

let fakeIndexedDB: FakeIDBFactory;
let restoreIndexedDB: (() => void) | undefined;

beforeEach(() => {
    vi.resetModules();
    fakeIndexedDB = createFakeIndexedDB();
    restoreIndexedDB = installFakeIndexedDB(fakeIndexedDB);
});

afterEach(() => restoreIndexedDB?.());

/** The store's raw contents, bypassing the module that reads them. */
async function rawStore() {
    const storage = await import('$lib/storage');
    await storage.setEditorDraft(PATH, 'warm-up', 'warm-up base');
    return fakeIndexedDB.databases.get('kanban-storage')!.stores.get('editor-drafts')!;
}

describe('the editor-drafts record', () => {
    it('reads back with the file bytes the buffer diverged from', async () => {
        const storage = await import('$lib/storage');
        await storage.setEditorDraft(PATH, 'typed in the editor\n', 'what the file held\n');

        const draft = await storage.getEditorDraft(PATH);
        expect(draft).toMatchObject({ text: 'typed in the editor\n', base: 'what the file held\n' });
    });

    it('reads a bare string an older build wrote as a draft with no base', async () => {
        const store = await rawStore();
        store.data.set(PATH, 'a buffer with no recorded base');

        const storage = await import('$lib/storage');
        expect(await storage.getEditorDraft(PATH)).toEqual({
            text: 'a buffer with no recorded base',
            base: null,
        });
    });

    it('reads a record with no buffer as no draft at all', async () => {
        const store = await rawStore();
        store.data.set(PATH, { base: 'bytes but no buffer' });

        const storage = await import('$lib/storage');
        expect(await storage.getEditorDraft(PATH)).toBeNull();
    });
});
