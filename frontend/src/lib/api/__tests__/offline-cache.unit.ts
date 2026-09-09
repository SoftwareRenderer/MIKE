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

describe('done-task diff cache (immutable head-ref diff)', () => {
    beforeEach(() => {
        vi.resetModules();
        vi.stubGlobal('indexedDB', createMockIndexedDB());
    });

    it('serves the cached done diff without any network call', async () => {
        const storage = await import('$lib/storage');
        const { isFromCache } = await import('$lib/api/cache');
        mockRejectedFetch();
        const fetchMock = vi.mocked(fetch);
        await storage.setBoardCache(storage.doneDiffCacheKey('p1', 't1', 'sha-tip'), diff);

        const { getTaskDiff } = await import('$lib/api/git');
        const result = await getTaskDiff('p1', 't1', { headRef: 'sha-tip' });

        expect(result).toMatchObject(diff);
        expect(isFromCache(result)).toBe(true);
        expect(fetchMock).not.toHaveBeenCalled();
    });

    it('persists the done diff after a successful fetch', async () => {
        const storage = await import('$lib/storage');
        mockResolvedFetch(diff);

        const { getTaskDiff } = await import('$lib/api/git');
        const result = await getTaskDiff('p1', 't1', { headRef: 'sha-tip' });

        expect(result).toEqual(diff);
        const cached = await storage.getBoardCache(storage.doneDiffCacheKey('p1', 't1', 'sha-tip'));
        expect(cached).toEqual(diff);
    });

    it('falls back to the cached done diff when the network fails', async () => {
        const storage = await import('$lib/storage');
        const { isFromCache } = await import('$lib/api/cache');
        await storage.setBoardCache(storage.doneDiffCacheKey('p1', 't1', 'sha-tip'), diff);
        mockRejectedFetch();

        const { getTaskDiff } = await import('$lib/api/git');
        const result = await getTaskDiff('p1', 't1', { headRef: 'sha-tip' });

        // Wait: cache-first means the cached copy is returned BEFORE the fetch
        // is even attempted, so this is indistinguishable from a hit — both
        // must yield the marked cached copy.
        expect(result).toMatchObject(diff);
        expect(isFromCache(result)).toBe(true);
    });

    it('does NOT use the done cache for checkpoint, custom-base, or full requests', async () => {
        const storage = await import('$lib/storage');
        mockRejectedFetch();
        await storage.setBoardCache(storage.doneDiffCacheKey('p1', 't1', 'sha-tip'), diff);

        const { getTaskDiff } = await import('$lib/api/git');
        // Each of these bypasses the done cache, so a network failure rejects
        // even though the done cache exists.
        await expect(getTaskDiff('p1', 't1', { headRef: 'sha-tip', full: true })).rejects.toBeTruthy();
        await expect(getTaskDiff('p1', 't1', { headRef: 'sha-tip', baseRef: 'sha-base' })).rejects.toBeTruthy();
        await expect(getTaskDiff('p1', 't1', { headRef: 'sha-tip', checkpointIndex: 0 })).rejects.toBeTruthy();
    });
});

describe('diff panel load through DiffCache (the caller that owns the cache)', () => {
    const parsedDoneDiff = {
        diff: 'diff --git a/a.ts b/a.ts\n+one',
        parsed_files: [{ path: 'a.ts', rows: [{ lineNumber: 1, variant: 'added', text: 'one' }], added: 1, removed: 0 }],
        untracked: [],
        short_stat: '1 file changed',
    };

    /** A DiffCache wired to the real api function, so the cache gates are the ones under test. */
    async function newPanelCache() {
        const { getTaskDiff } = await import('$lib/api/git');
        const { DiffCache } = await import('$lib/services/diff-cache.svelte');
        return new DiffCache(
            {
                getTaskDiff,
                getDiffFile: async () => ({ rows: [] }),
                stageFile: async () => ({ status: 'ok', action: 'stage' }),
                enrichRows: rows => rows as never,
            },
            () => 'p1',
            () => 't1',
        );
    }

    it('persists the done diff loadDiff fetched and serves the next load with no network', async () => {
        const storage = await import('$lib/storage');
        mockResolvedFetch(parsedDoneDiff);
        const fetchMock = vi.mocked(fetch);

        const cache = await newPanelCache();
        await cache.loadDiff(() => {}, { headRef: 'sha-tip' });
        expect(Object.keys(cache.state.files)).toEqual(['a.ts']);
        const cached = await storage.getBoardCache(storage.doneDiffCacheKey('p1', 't1', 'sha-tip'));
        expect(cached).toMatchObject(parsedDoneDiff);

        fetchMock.mockClear();
        const reopened = await newPanelCache();
        await reopened.loadDiff(() => {}, { headRef: 'sha-tip' });
        expect(fetchMock).not.toHaveBeenCalled();
        expect(Object.keys(reopened.state.files)).toEqual(['a.ts']);
    });

    it('does not let a stored body without parsed files answer a parsed request', async () => {
        const storage = await import('$lib/storage');
        // A stored body from before parsed_files: patch text only.
        await storage.setBoardCache(
            storage.doneDiffCacheKey('p1', 't1', 'sha-tip'),
            { diff: parsedDoneDiff.diff, untracked: [], short_stat: '1 file changed' },
        );
        mockResolvedFetch(parsedDoneDiff);

        const cache = await newPanelCache();
        await cache.loadDiff(() => {}, { headRef: 'sha-tip' });
        expect(vi.mocked(fetch)).toHaveBeenCalled();
        expect(Object.keys(cache.state.files)).toEqual(['a.ts']);
    });

    it('caches the current worktree diff even though the panel asks for the parsed view', async () => {
        const storage = await import('$lib/storage');
        const manifest = { files: { 'a.ts': { hash: 'h1', type: 'tracked', added: 1, removed: 0 } }, untracked: [], short_stat: '1 file changed' };
        mockResolvedFetch(manifest);

        const cache = await newPanelCache();
        await cache.loadDiff(() => {});
        const cached = await storage.getBoardCache(storage.diffCacheKey('p1', 't1'));
        expect(cached).toMatchObject(manifest);
    });
});

describe('a diff cache hit is for a disconnected read only', () => {
    beforeEach(() => {
        vi.resetModules();
        vi.stubGlobal('indexedDB', createMockIndexedDB());
    });

    function mockAbortedFetch() {
        vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new DOMException('The user aborted a request.', 'AbortError')));
    }

    it('lets an aborted current-diff request fail rather than render the cached worktree diff', async () => {
        const storage = await import('$lib/storage');
        await storage.setBoardCache(storage.diffCacheKey('p1', 't1'), diff);
        mockAbortedFetch();

        const { getTaskDiff } = await import('$lib/api/git');
        await expect(getTaskDiff('p1', 't1', { parsed: true })).rejects.toBeTruthy();
    });

    it('still serves the cached current diff when the server cannot be reached at all', async () => {
        const storage = await import('$lib/storage');
        const { isFromCache } = await import('$lib/api/cache');
        await storage.setBoardCache(storage.diffCacheKey('p1', 't1'), diff);
        mockRejectedFetch();

        const { getTaskDiff } = await import('$lib/api/git');
        const result = await getTaskDiff('p1', 't1', { parsed: true });
        expect(result).toMatchObject(diff);
        expect(isFromCache(result)).toBe(true);
    });

    it('serves the stored done diff on an abort, because a pinned diff cannot have changed', async () => {
        const storage = await import('$lib/storage');
        const { isFromCache } = await import('$lib/api/cache');
        await storage.setBoardCache(storage.doneDiffCacheKey('p1', 't1', 'sha-tip'), parsedDoneDiffBody);
        mockAbortedFetch();

        const { getTaskDiff } = await import('$lib/api/git');
        const result = await getTaskDiff('p1', 't1', { headRef: 'sha-tip', parsed: true });
        expect(result).toMatchObject(parsedDoneDiffBody);
        expect(isFromCache(result)).toBe(true);
    });
});

const parsedDoneDiffBody = {
    diff: 'diff --git a/a.ts b/a.ts\n+one',
    parsed_files: [{ path: 'a.ts', rows: [{ lineNumber: 1, variant: 'added', text: 'one' }], added: 1, removed: 0 }],
    untracked: [],
    short_stat: '1 file changed',
};
