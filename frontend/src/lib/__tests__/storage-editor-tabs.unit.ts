import { describe, it, expect, beforeEach, vi } from 'vitest';

// ─── Minimal in-memory IndexedDB mock ────────────────────────────────────────

interface MockStore {
    data: Map<string, any>;
    get(key: string): MockRequest;
    put(value: any, key: string): MockRequest;
    delete(key: string): MockRequest;
    getAllKeys(): MockRequest;
}

interface MockDB {
    objectStoreNames: DOMStringList;
    stores: Map<string, MockStore>;
    createObjectStore(name: string): MockStore;
    transaction(storeName: string, _mode: string): MockTransaction;
}

interface MockTransaction {
    objectStore(storeName: string): MockStore;
    oncomplete: (() => void) | null;
    onerror: ((e: any) => void) | null;
}

interface MockRequest {
    result: any;
    onsuccess: (() => void) | null;
    onerror: ((e: any) => void) | null;
}

interface MockOpenRequest {
    result: MockDB;
    onupgradeneeded: ((e: any) => void) | null;
    onsuccess: (() => void) | null;
    onerror: ((e: any) => void) | null;
}

class MockDOMStringList implements DOMStringList {
    private _items: string[];
    constructor(items: string[]) { this._items = items; }
    get length() { return this._items.length; }
    item(index: number) { return this._items[index] ?? null; }
    contains(s: string) { return this._items.includes(s); }
    *[Symbol.iterator]() { return this._items[Symbol.iterator](); }
}

function makeRequest(): MockRequest {
    return { result: null, onsuccess: null, onerror: null };
}

function fireRequest(req: MockRequest, result: any) {
    req.result = result;
    queueMicrotask(() => {
        if (req.onsuccess) req.onsuccess();
    });
}

function createMockIndexedDB(): typeof indexedDB {
    const databases = new Map<string, MockDB>();

    const mockIDB: any = {
        open(name: string, _version: number): MockOpenRequest {
            const req: MockOpenRequest = {
                result: null as any,
                onupgradeneeded: null,
                onsuccess: null,
                onerror: null,
            };

            let db = databases.get(name);
            const isUpgrade = !db;

            if (!db) {
                const stores = new Map<string, MockStore>();
                db = {
                    objectStoreNames: new MockDOMStringList([]),
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
                                fireRequest(r);
                                return r;
                            },
                            getAllKeys() {
                                const r = makeRequest();
                                fireRequest(r, [...stores.get(storeName)!.data.keys()]);
                                return r;
                            },
                        };
                        stores.set(storeName, store);
                        db.objectStoreNames = new MockDOMStringList([...stores.keys()]);
                        return store;
                    },
                    transaction(storeName, _mode) {
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
                        queueMicrotask(() => {
                            if (tx.oncomplete) tx.oncomplete();
                        });
                        return tx;
                    },
                };
                databases.set(name, db);
            }

            req.result = db;

            if (isUpgrade) {
                queueMicrotask(() => {
                    if (req.onupgradeneeded) {
                        req.onupgradeneeded({
                            target: req,
                            oldVersion: 0,
                            newVersion: _version,
                        } as any);
                    }
                    if (req.onsuccess) req.onsuccess();
                });
            } else {
                queueMicrotask(() => {
                    if (req.onsuccess) req.onsuccess();
                });
            }

            return req;
        },
    };

    return mockIDB;
}

// ─── Tests ───────────────────────────────────────────────────────────────────

describe('editor-tabs storage', () => {
    beforeEach(() => {
        vi.resetModules();
        const mockIDB = createMockIndexedDB();
        vi.stubGlobal('indexedDB', mockIDB);
    });

    it('saveEditorTabs and loadEditorTabs round-trips correctly', async () => {
        const { saveEditorTabs, loadEditorTabs } = await import('$lib/storage');

        const state = {
            openFiles: [
                { path: '/home/user/project/src/main.go', targetLine: 42 },
                { path: '/home/user/project/README.md', targetLine: 0 },
            ],
            activeFilePath: '/home/user/project/src/main.go',
            pinnedFiles: ['/home/user/project/src/main.go'],
        };

        await saveEditorTabs('project-123', state);

        const loaded = await loadEditorTabs('project-123');
        expect(loaded).not.toBeNull();
        expect(loaded!.openFiles).toEqual(state.openFiles);
        expect(loaded!.activeFilePath).toEqual(state.activeFilePath);
        expect(loaded!.pinnedFiles).toEqual(state.pinnedFiles);
    });

    it('loadEditorTabs returns null for unknown project', async () => {
        const { loadEditorTabs } = await import('$lib/storage');

        const loaded = await loadEditorTabs('nonexistent-project');
        expect(loaded).toBeNull();
    });

    it('clearEditorTabs removes stored state', async () => {
        const { saveEditorTabs, loadEditorTabs, clearEditorTabs } = await import('$lib/storage');

        await saveEditorTabs('project-456', {
            openFiles: [{ path: '/a/b', targetLine: 0 }],
            activeFilePath: '/a/b',
            pinnedFiles: [],
        });

        const before = await loadEditorTabs('project-456');
        expect(before).not.toBeNull();

        await clearEditorTabs('project-456');

        const after = await loadEditorTabs('project-456');
        expect(after).toBeNull();
    });

    it('overwrites previous state on save', async () => {
        const { saveEditorTabs, loadEditorTabs } = await import('$lib/storage');

        await saveEditorTabs('project-789', {
            openFiles: [{ path: '/old', targetLine: 0 }],
            activeFilePath: '/old',
            pinnedFiles: [],
        });

        await saveEditorTabs('project-789', {
            openFiles: [{ path: '/new', targetLine: 10 }],
            activeFilePath: '/new',
            pinnedFiles: ['/new'],
        });

        const loaded = await loadEditorTabs('project-789');
        expect(loaded!.openFiles).toEqual([{ path: '/new', targetLine: 10 }]);
        expect(loaded!.activeFilePath).toBe('/new');
        expect(loaded!.pinnedFiles).toEqual(['/new']);
    });

    it('isolates state between different projects', async () => {
        const { saveEditorTabs, loadEditorTabs } = await import('$lib/storage');

        await saveEditorTabs('proj-a', {
            openFiles: [{ path: '/a/file', targetLine: 1 }],
            activeFilePath: '/a/file',
            pinnedFiles: [],
        });

        await saveEditorTabs('proj-b', {
            openFiles: [{ path: '/b/file', targetLine: 2 }],
            activeFilePath: '/b/file',
            pinnedFiles: ['/b/file'],
        });

        const a = await loadEditorTabs('proj-a');
        const b = await loadEditorTabs('proj-b');

        expect(a!.openFiles[0].path).toBe('/a/file');
        expect(b!.openFiles[0].path).toBe('/b/file');
        expect(b!.pinnedFiles).toEqual(['/b/file']);
    });

    it('handles empty openFiles array', async () => {
        const { saveEditorTabs, loadEditorTabs } = await import('$lib/storage');

        await saveEditorTabs('empty-proj', {
            openFiles: [],
            activeFilePath: '',
            pinnedFiles: [],
        });

        const loaded = await loadEditorTabs('empty-proj');
        expect(loaded!.openFiles).toEqual([]);
        expect(loaded!.activeFilePath).toBe('');
        expect(loaded!.pinnedFiles).toEqual([]);
    });
});
