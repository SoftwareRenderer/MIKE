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

describe('LSP storage settings', () => {
    beforeEach(() => {
        vi.resetModules();
        const mockIDB = createMockIndexedDB();
        vi.stubGlobal('indexedDB', mockIDB);
    });

    it('defaults to enabled when no setting is stored', async () => {
        const { isLSPEnabled } = await import('$lib/storage');
        expect(await isLSPEnabled()).toBe(true);
    });

    it('persists and reads enabled = false', async () => {
        const { setLSPEnabled, isLSPEnabled } = await import('$lib/storage');
        await setLSPEnabled(false);
        expect(await isLSPEnabled()).toBe(false);
    });

    it('persists and reads enabled = true', async () => {
        const { setLSPEnabled, isLSPEnabled } = await import('$lib/storage');
        await setLSPEnabled(true);
        expect(await isLSPEnabled()).toBe(true);
    });

    it('toggles from enabled to disabled and back', async () => {
        const { setLSPEnabled, isLSPEnabled } = await import('$lib/storage');

        expect(await isLSPEnabled()).toBe(true);

        await setLSPEnabled(false);
        expect(await isLSPEnabled()).toBe(false);

        await setLSPEnabled(true);
        expect(await isLSPEnabled()).toBe(true);
    });
});
