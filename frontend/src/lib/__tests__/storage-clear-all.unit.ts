/**
 * Signing out clears every store and the kanban-* pre-paint hints. gc() only prunes
 * tasks that no longer exist, so without this the next person to open the browser
 * inherits the last one's messages, diffs, drafts and queued writes.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { createFakeIndexedDB, fakeKeysByStore, installFakeIndexedDB } from './fake-indexeddb';
import type { FakeIDBFactory } from './fake-indexeddb';
import type { ActionType } from '$lib/api/queue';

let fakeIndexedDB: FakeIDBFactory;
let restoreIndexedDB: (() => void) | undefined;

beforeEach(() => {
    vi.resetModules();
    fakeIndexedDB = createFakeIndexedDB();
    restoreIndexedDB = installFakeIndexedDB(fakeIndexedDB);
    localStorage.clear();
});

afterEach(() => restoreIndexedDB?.());

/** Keys one object store holds, whichever database the fake put it in. */
function keysIn(store: string): string[] {
    return Object.entries(fakeKeysByStore(fakeIndexedDB))
        .filter(([name]) => name.endsWith(`/${store}`))
        .flatMap(([, keys]) => keys);
}

/** Every `<db>/<store>` that still holds a key. */
function storesWithKeys(): string[] {
    return Object.entries(fakeKeysByStore(fakeIndexedDB))
        .filter(([, keys]) => keys.length > 0)
        .map(([name]) => name);
}

describe('clearAll (sign-out sweep)', () => {
    it('erases every store and the pre-paint hints', async () => {
        const storage = await import('$lib/storage');

        // Fill the stores through the module's own API.
        await storage.setDraftText('task-1', 'a prompt the next user must not read');
        await storage.setEditorDraft('/repo/src/main.go', 'package main');
        await storage.saveEditorHistory('/repo/src/main.go', 'base', []);
        await storage.saveUnsavedFiles('proj-a', ['/repo/src/main.go']);
        await storage.saveQueue([{ type: 'updateConfig', params: { config: { providers: [] } }, id: 'a1', timestamp: 1 }]);
        await storage.saveAppSetting('locale-offer', { pack: 'x', outcome: 'installed' });
        await storage.saveDraftAttachments('task-1', []);
        await storage.setBoardCache('messages:proj-a:task-1', [{ content: 'diff of a private repo' }]);
        await storage.saveExplorerLayout({ leftWidth: 200, rightWidth: 400 } as any);
        await storage.saveCollapsedTasks(new Set(['task-1']));
        await storage.saveCollapsedSections(new Set(['sec']));
        await storage.saveCollapsedDiffFiles(new Set(['a.go']));
        await storage.saveEditorTabs('proj-a', { openFiles: [], activeFilePath: '', pinnedFiles: [] });

        // The two hints index.html pre-paints from.
        localStorage.setItem('kanban-theme-css', '/extensions/a/b/theme.css');
        localStorage.setItem('kanban-i18n-locale', 'fr');
        // A key this module does not own must survive.
        localStorage.setItem('not-mine', 'keep');

        await storage.clearAll();

        expect(await storage.getDraftText('task-1')).toBeNull();
        expect(await storage.getEditorDraft('/repo/src/main.go')).toBeNull();
        expect(await storage.loadEditorHistory('/repo/src/main.go')).toBeNull();
        expect(await storage.loadUnsavedFiles('proj-a')).toEqual([]);
        expect(await storage.loadQueue()).toEqual([]);
        expect(await storage.loadAppSetting('locale-offer', null)).toBeNull();
        expect(await storage.loadDraftAttachments('task-1')).toEqual([]);
        expect(await storage.getBoardCache('messages:proj-a:task-1')).toBeNull();
        // Defaults, not the 200/400 that was saved.
        expect(await storage.loadExplorerLayout())
            .toEqual({ leftWidth: 250, rightWidth: 180, splitRatio: 0.5 });
        expect(await storage.loadCollapsedTasks().then((s) => s.size)).toBe(0);
        expect(await storage.loadCollapsedSections().then((s) => s.size)).toBe(0);
        expect(await storage.loadCollapsedDiffFiles().then((s) => s.size)).toBe(0);
        expect(await storage.loadEditorTabs('proj-a')).toBeNull();

        expect(localStorage.getItem('kanban-theme-css')).toBeNull();
        expect(localStorage.getItem('kanban-i18n-locale')).toBeNull();
        expect(localStorage.getItem('not-mine')).toBe('keep');
    });

    it('is safe when nothing was ever stored', async () => {
        const storage = await import('$lib/storage');
        await expect(storage.clearAll()).resolves.toBeUndefined();
    });

    it('empties the object stores themselves, not only the getters that read them', async () => {
        const storage = await import('$lib/storage');
        const { sendTaskMessage } = await import('$lib/api/tasks');
        const networkDown = Object.assign(new Error('Failed to fetch'), { name: 'TypeError' });
        vi.stubGlobal('fetch', vi.fn().mockRejectedValue(networkDown));

        const queued = await sendTaskMessage('proj-a', 'task-1', 'a send made while offline');
        await storage.setBoardCache(storage.messagesCacheKey('proj-a', 'task-1'), [{ content: 'a cached body' }]);
        expect(queued.queued).toBe(true);

        expect(keysIn('offline-queue')).toEqual(['actions']);
        expect(keysIn('board-cache')).toEqual(['messages:proj-a:task-1']);
        const stores = Object.keys(fakeKeysByStore(fakeIndexedDB)).sort();

        await storage.clearAll();

        // The getters above answer null for a store the sweep never reached too, so the check that
        // carries this test is the raw one: no key anywhere, and no store dropped instead of emptied.
        expect(storesWithKeys()).toEqual([]);
        expect(Object.keys(fakeKeysByStore(fakeIndexedDB)).sort()).toEqual(stores);
    });

    it('cannot erase a queued write the queue module still holds in memory — clearQueue must run too', async () => {
        const storage = await import('$lib/storage');
        const { enqueueAction, clearQueue } = await import('$lib/api/queue');
        const send = (content: string): { type: ActionType; params: Record<string, any> } => ({
            type: 'sendTaskMessage', params: { projectId: 'proj-a', taskId: 'task-1', content } });

        await enqueueAction(send('written while offline'));
        expect(await storage.loadQueue()).toHaveLength(1);

        await storage.clearAll();
        expect(await storage.loadQueue()).toEqual([]);

        // The next enqueue persists the whole in-memory array, and the erased write came back with it.
        await enqueueAction(send('the next write'));
        expect((await storage.loadQueue()).map((a) => a.params.content))
            .toEqual(['written while offline', 'the next write']);

        clearQueue();
        await storage.clearAll();
        await enqueueAction({ type: 'stopTask', params: { projectId: 'proj-a', taskId: 'task-1' } });
        expect((await storage.loadQueue()).map((a) => a.type)).toEqual(['stopTask']);
        expect(keysIn('offline-queue')).toEqual(['actions']);
    });
});
