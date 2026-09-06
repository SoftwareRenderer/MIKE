/**
 * Signing out clears every store and the kanban-* pre-paint hints. gc() only prunes
 * tasks that no longer exist, so without this the next person to open the browser
 * inherits the last one's messages, diffs, drafts and queued writes.
 * In-memory IndexedDB mock, same pattern as storage-offline-cache.unit.ts.
 */
import { describe, it, expect, vi, beforeEach } from 'vitest';

interface MockRequest { result: any; onsuccess: (() => void) | null; onerror: (() => void) | null }
interface MockStore {
    data: Map<string, any>;
    get(key: string): MockRequest;
    put(value: any, key: string): MockRequest;
    delete(key: string): MockRequest;
    getAllKeys(): MockRequest;
}
interface MockTransaction {
    objectStore(name: string): MockStore;
    oncomplete: (() => void) | null;
    onerror: ((e: any) => void) | null;
}
interface MockDB {
    objectStoreNames: DOMStringList;
    stores: Map<string, MockStore>;
    createObjectStore(name: string): MockStore;
    transaction(name: string, mode: string): MockTransaction;
}
interface MockOpenRequest {
    result: MockDB;
    onupgradeneeded: ((e: any) => void) | null;
    onsuccess: (() => void) | null;
    onerror: (() => void) | null;
}

class MockDOMStringList {
    private _items: string[];
    constructor(items: string[]) { this._items = items; }
    get length() { return this._items.length; }
    item(index: number) { return this._items[index] ?? null; }
    contains(s: string) { return this._items.includes(s); }
    [Symbol.iterator]() { return this._items[Symbol.iterator](); }
}

function makeRequest(): MockRequest { return { result: null, onsuccess: null, onerror: null }; }
function fireRequest(req: MockRequest, result: any) {
    req.result = result;
    queueMicrotask(() => { if (req.onsuccess) req.onsuccess(); });
}

function createMockIndexedDB(): typeof indexedDB {
    const databases = new Map<string, MockDB>();
    const mockIDB: any = {
        open(name: string, version: number): MockOpenRequest {
            const req: MockOpenRequest = { result: null as any, onupgradeneeded: null, onsuccess: null, onerror: null };
            let db = databases.get(name);
            const isUpgrade = !db;
            if (!db) {
                const stores = new Map<string, MockStore>();
                db = {
                    objectStoreNames: new MockDOMStringList([]) as unknown as DOMStringList,
                    stores,
                    createObjectStore(storeName: string): MockStore {
                        const store: MockStore = {
                            data: new Map(),
                            get(key) {
                                const r = makeRequest();
                                fireRequest(r, stores.get(storeName)!.data.get(key) ?? undefined);
                                return r;
                            },
                            put(value, key) {
                                const r = makeRequest();
                                stores.get(storeName)!.data.set(key, value);
                                fireRequest(r, key);
                                return r;
                            },
                            delete(key) {
                                const r = makeRequest();
                                stores.get(storeName)!.data.delete(key);
                                fireRequest(r, undefined);
                                return r;
                            },
                            getAllKeys() {
                                const r = makeRequest();
                                fireRequest(r, [...stores.get(storeName)!.data.keys()]);
                                return r;
                            },
                        };
                        stores.set(storeName, store);
                        (db as MockDB).objectStoreNames = new MockDOMStringList([...stores.keys()]) as unknown as DOMStringList;
                        return store;
                    },
                    transaction(storeName: string, _mode: string) {
                        const store = stores.get(storeName);
                        if (!store) throw new Error(`Unknown store: ${storeName}`);
                        const tx: MockTransaction = {
                            objectStore(sn) {
                                if (sn === storeName) return store;
                                throw new Error(`Wrong store: ${sn}`);
                            },
                            oncomplete: null,
                            onerror: null,
                        };
                        queueMicrotask(() => { if (tx.oncomplete) tx.oncomplete(); });
                        return tx;
                    },
                };
                databases.set(name, db!);
            }
            req.result = db!;
            queueMicrotask(() => {
                if (isUpgrade && req.onupgradeneeded) {
                    req.onupgradeneeded({ target: req, oldVersion: 0, newVersion: version } as any);
                }
                if (req.onsuccess) req.onsuccess();
            });
            return req;
        },
    };
    return mockIDB;
}

beforeEach(() => {
    vi.resetModules();
    vi.stubGlobal('indexedDB', createMockIndexedDB());
    localStorage.clear();
});

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
});
