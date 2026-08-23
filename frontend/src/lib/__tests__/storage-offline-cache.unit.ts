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

class MockDOMStringList {
    private _items: string[];
    constructor(items: string[]) { this._items = items; }
    get length() { return this._items.length; }
    item(index: number) { return this._items[index] ?? null; }
    contains(s: string) { return this._items.includes(s); }
    [Symbol.iterator]() { return this._items[Symbol.iterator](); }
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
                databases.set(name, db!);
            }

            req.result = db!;

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

describe('offline content cache keys', () => {
    beforeEach(() => {
        vi.resetModules();
        const mockIDB = createMockIndexedDB();
        vi.stubGlobal('indexedDB', mockIDB);
    });

    it('produces namespaced keys for messages, full messages, and diffs', async () => {
        const { messagesCacheKey, messageFullCacheKey, diffCacheKey, doneDiffCacheKey, doneStatsCacheKey } = await import('$lib/storage');

        expect(messagesCacheKey('proj-a', 'task-1')).toBe('messages:proj-a:task-1');
        expect(messageFullCacheKey('proj-a', 'task-1', 'msg-9')).toBe('message-full:proj-a:task-1:msg-9');
        expect(diffCacheKey('proj-a', 'task-1')).toBe('diff:proj-a:task-1');
        expect(doneDiffCacheKey('proj-a', 'task-1', 'sha-tip')).toBe('diff:proj-a:task-1:sha-tip');
        expect(doneStatsCacheKey('proj-a', 'task-1')).toBe('done-stats:proj-a:task-1');
    });
});

describe('offline content cache GC', () => {
    beforeEach(() => {
        vi.resetModules();
        const mockIDB = createMockIndexedDB();
        vi.stubGlobal('indexedDB', mockIDB);
    });

    it('removes stale message/diff entries for the current project, keeps valid, other projects, and board keys', async () => {
        const { setBoardCache, getBoardCache, gc } = await import('$lib/storage');

        const pid = 'proj-a';
        const validTask = 'task-valid';
        const staleTask = 'task-stale';
        const otherProject = 'proj-b';

        await setBoardCache(`messages:${pid}:${validTask}`, []);
        await setBoardCache(`messages:${pid}:${staleTask}`, []);
        await setBoardCache(`message-full:${pid}:${staleTask}:msg1`, {});
        await setBoardCache(`diff:${pid}:${staleTask}`, {});
        // Done-task caches (4-part done-diff key and done-stats key) follow the
        // same task-id-at-parts[2] sweep rule.
        await setBoardCache(`diff:${pid}:${staleTask}:sha-tip`, { diff: 'x' });
        await setBoardCache(`diff:${pid}:${validTask}:sha-tip`, { diff: 'x' });
        await setBoardCache(`done-stats:${pid}:${staleTask}`, { updated_at: 't', stats: {} });
        await setBoardCache(`done-stats:${pid}:${validTask}`, { updated_at: 't', stats: {} });
        // Same stale task id but under a different project — must be preserved
        await setBoardCache(`messages:${otherProject}:${staleTask}`, []);
        await setBoardCache(`board:${pid}`, { tasks: [] });
        await setBoardCache('projects', []);

        await gc(new Set([validTask]), undefined, undefined, pid);

        expect(await getBoardCache(`messages:${pid}:${validTask}`)).not.toBeNull();
        expect(await getBoardCache(`messages:${pid}:${staleTask}`)).toBeNull();
        expect(await getBoardCache(`message-full:${pid}:${staleTask}:msg1`)).toBeNull();
        expect(await getBoardCache(`diff:${pid}:${staleTask}`)).toBeNull();
        expect(await getBoardCache(`diff:${pid}:${staleTask}:sha-tip`)).toBeNull();
        expect(await getBoardCache(`diff:${pid}:${validTask}:sha-tip`)).not.toBeNull();
        expect(await getBoardCache(`done-stats:${pid}:${staleTask}`)).toBeNull();
        expect(await getBoardCache(`done-stats:${pid}:${validTask}`)).not.toBeNull();
        expect(await getBoardCache(`messages:${otherProject}:${staleTask}`)).not.toBeNull();
        expect(await getBoardCache(`board:${pid}`)).not.toBeNull();
        expect(await getBoardCache('projects')).not.toBeNull();
    });

    it('leaves message/diff caches untouched when no projectId is provided', async () => {
        const { setBoardCache, getBoardCache, gc } = await import('$lib/storage');

        await setBoardCache('messages:p:t', []);
        await setBoardCache('diff:p:t', {});

        await gc(new Set([]), undefined, undefined, undefined);

        expect(await getBoardCache('messages:p:t')).not.toBeNull();
        expect(await getBoardCache('diff:p:t')).not.toBeNull();
    });
});
