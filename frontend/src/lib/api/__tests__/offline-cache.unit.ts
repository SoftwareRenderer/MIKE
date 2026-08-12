import { describe, it, expect, beforeEach, vi } from 'vitest';

// ─── Minimal in-memory IndexedDB mock (same pattern as storage-editor-tabs.unit.ts) ──

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
interface MockRequest { result: any; onsuccess: (() => void) | null; onerror: ((e: any) => void) | null; }
interface MockOpenRequest { result: MockDB; onupgradeneeded: ((e: any) => void) | null; onsuccess: (() => void) | null; onerror: ((e: any) => void) | null; }

class MockDOMStringList {
    private _items: string[];
    constructor(items: string[]) { this._items = items; }
    get length() { return this._items.length; }
    item(index: number) { return this._items[index] ?? null; }
    contains(s: string) { return this._items.includes(s); }
    [Symbol.iterator]() { return this._items[Symbol.iterator](); }
}
function makeRequest(): MockRequest { return { result: null, onsuccess: null, onerror: null }; }
function fireRequest(req: MockRequest, result: any) { req.result = result; queueMicrotask(() => { if (req.onsuccess) req.onsuccess(); }); }

function createMockIndexedDB(): typeof indexedDB {
    const databases = new Map<string, MockDB>();
    const mockIDB: any = {
        open(name: string, _version: number): MockOpenRequest {
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
                            get(key) { const r = makeRequest(); fireRequest(r, stores.get(storeName)!.data.get(key) ?? undefined); return r; },
                            put(value, key) { const r = makeRequest(); stores.get(storeName)!.data.set(key, value); fireRequest(r, key); return r; },
                            delete(key) { const r = makeRequest(); stores.get(storeName)!.data.delete(key); fireRequest(r, undefined); return r; },
                            getAllKeys() { const r = makeRequest(); fireRequest(r, [...stores.get(storeName)!.data.keys()]); return r; },
                        };
                        stores.set(storeName, store);
                        (db as MockDB).objectStoreNames = new MockDOMStringList([...stores.keys()]) as unknown as DOMStringList;
                        return store;
                    },
                    transaction(storeName, _mode) {
                        const store = stores.get(storeName);
                        if (!store) throw new Error(`Unknown store: ${storeName}`);
                        const tx: MockTransaction = {
                            objectStore(sn) { if (sn === storeName) return store; throw new Error(`Wrong store: ${sn}`); },
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
            if (isUpgrade) {
                queueMicrotask(() => {
                    if (req.onupgradeneeded) req.onupgradeneeded({ target: req, oldVersion: 0, newVersion: _version } as any);
                    if (req.onsuccess) req.onsuccess();
                });
            } else {
                queueMicrotask(() => { if (req.onsuccess) req.onsuccess(); });
            }
            return req;
        },
    };
    return mockIDB;
}

// ─── Fixtures ────────────────────────────────────────────────────────────────

const messages = [
    { id: 'm1', task_id: 't1', thread_id: '', role: 'user', content: 'hello', created_at: '2026-01-01T00:00:00Z' },
    { id: 'm2', task_id: 't1', thread_id: '', role: 'assistant', content: 'hi there', created_at: '2026-01-01T00:00:01Z' },
];

const diff = { diff: '--- a\n+++ b\n@@ -1 +1 @@\n-old\n+new', untracked: [], short_stat: '1 file changed' };

function mockResolvedFetch(body: any) {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, json: async () => body }));
}
function mockRejectedFetch() {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('Failed to fetch')));
}

describe('offline message cache', () => {
    beforeEach(() => {
        vi.resetModules();
        vi.stubGlobal('indexedDB', createMockIndexedDB());
    });

    it('returns cached messages (marked from cache) when network fails', async () => {
        const storage = await import('$lib/storage');
        const { isFromCache } = await import('$lib/api/cache');
        mockRejectedFetch();
        await storage.setBoardCache(storage.messagesCacheKey('p1', 't1'), messages);

        const { getTaskMessages } = await import('$lib/api/tasks');
        const result = await getTaskMessages('p1', 't1');

        // Copy the array to drop the cache-marker symbol on the array itself before deep-compare
        expect([...result]).toEqual(messages);
        expect(isFromCache(result)).toBe(true);
    });

    it('caches successfully fetched messages', async () => {
        const storage = await import('$lib/storage');
        mockResolvedFetch(messages);

        const { getTaskMessages } = await import('$lib/api/tasks');
        const result = await getTaskMessages('p1', 't1');

        expect(result).toEqual(messages);
        const cached = await storage.getBoardCache(storage.messagesCacheKey('p1', 't1'));
        expect(cached).toEqual(messages);
    });

    it('rejects when network fails and no cache exists', async () => {
        mockRejectedFetch();
        const { getTaskMessages } = await import('$lib/api/tasks');
        await expect(getTaskMessages('p1', 't1')).rejects.toBeTruthy();
    });
});

describe('offline diff cache', () => {
    beforeEach(() => {
        vi.resetModules();
        vi.stubGlobal('indexedDB', createMockIndexedDB());
    });

    it('returns the cached current diff (marked from cache) when network fails', async () => {
        const storage = await import('$lib/storage');
        const { isFromCache } = await import('$lib/api/cache');
        mockRejectedFetch();
        await storage.setBoardCache(storage.diffCacheKey('p1', 't1'), diff);

        const { getTaskDiff } = await import('$lib/api/git');
        const result = await getTaskDiff('p1', 't1');

        expect(result).toMatchObject(diff);
        expect(isFromCache(result)).toBe(true);
    });

    it('caches the current diff on success', async () => {
        const storage = await import('$lib/storage');
        mockResolvedFetch(diff);

        const { getTaskDiff } = await import('$lib/api/git');
        const result = await getTaskDiff('p1', 't1');

        expect(result).toEqual(diff);
        const cached = await storage.getBoardCache(storage.diffCacheKey('p1', 't1'));
        expect(cached).toEqual(diff);
    });

    it('does NOT fall back to cache for non-current (checkpoint) diff requests', async () => {
        const storage = await import('$lib/storage');
        mockRejectedFetch();
        await storage.setBoardCache(storage.diffCacheKey('p1', 't1'), diff);

        const { getTaskDiff } = await import('$lib/api/git');
        // checkpoint diff request must reject even though a current-diff cache exists
        await expect(getTaskDiff('p1', 't1', { checkpointIndex: 0 })).rejects.toBeTruthy();
    });
});
