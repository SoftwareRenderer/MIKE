import { describe, it, expect, beforeEach, vi } from 'vitest';

// ─── Minimal in-memory IndexedDB mock (same pattern as offline-cache.unit.ts) ──

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

function mockRejectedFetch(name = 'TypeError') {
    const err: any = new Error(name === 'AbortError' ? 'The operation was aborted' : 'Failed to fetch');
    err.name = name;
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(err));
}

function mockServerErrorFetch() {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: false, status: 500, json: async () => ({ error: 'internal server error' }) }));
}

describe('sendTaskMessage queueing on network failure', () => {
    beforeEach(() => {
        vi.resetModules();
        vi.stubGlobal('indexedDB', createMockIndexedDB());
    });

    it('queues a sendTaskMessage action and returns a queued result when the network fails (TypeError)', async () => {
        const { sendTaskMessage } = await import('$lib/api/tasks');
        const { loadQueue } = await import('$lib/api/queue');
        mockRejectedFetch('TypeError');

        const result = await sendTaskMessage('p1', 't1', 'hello world', 'model-a', 'http://llm:8010/v1');

        expect(result.queued).toBe(true);
        expect(result.actionId).toBeTruthy();
        expect(result.content).toBe('hello world');

        const queue = await loadQueue();
        expect(queue).toHaveLength(1);
        expect(queue[0].type).toBe('sendTaskMessage');
        expect(queue[0].params).toMatchObject({
            projectId: 'p1',
            taskId: 't1',
            content: 'hello world',
            modelName: 'model-a',
            serverUrl: 'http://llm:8010/v1',
        });
    });

    // Used to be a test that a timeout (AbortError) queues the message. There is no client-side timeout
    // any more, and an abort is no longer a network event: it means somebody stopped this request.
    it('does NOT queue a send the user cancelled (AbortError), and the rejection reaches the caller', async () => {
        const { sendTaskMessage } = await import('$lib/api/tasks');
        const { loadQueue } = await import('$lib/api/queue');
        mockRejectedFetch('AbortError');

        await expect(sendTaskMessage('p1', 't1', 'cancelled message')).rejects.toThrow();

        const queue = await loadQueue();
        expect(queue).toHaveLength(0);
    });

    it('does NOT queue and rejects on non-transient server errors (HTTP 500)', async () => {
        const { sendTaskMessage } = await import('$lib/api/tasks');
        const { loadQueue } = await import('$lib/api/queue');
        mockServerErrorFetch();

        await expect(sendTaskMessage('p1', 't1', 'server error message')).rejects.toBeTruthy();

        const queue = await loadQueue();
        expect(queue).toHaveLength(0);
    });
});
