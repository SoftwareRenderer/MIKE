/**
 * Shared in-memory IndexedDB double for the client-storage unit tests.
 *
 * Contract: every `createFakeIndexedDB()` call owns an empty set of databases, so a test that wants a
 * cold store makes a new one (together with `vi.resetModules()`, which drops `$lib/storage`'s cached
 * `dbPromise`). `installFakeIndexedDB()` swaps it in as `globalThis.indexedDB` and hands back the
 * undo — jsdom has no IndexedDB of its own, so undoing means deleting the property, which puts
 * `typeof indexedDB === 'undefined'` back for the next test file.
 *
 * Scope is the surface `$lib/storage` touches, and nothing else. Requests and transactions only ever
 * succeed: no path here fires `onerror`, there is no `deleteDatabase`, `abort()` or `tx.error`. A
 * storage.ts error branch therefore cannot be exercised through this double.
 * A transaction completes when its own requests have drained — not when it was opened — because a
 * read-modify-write (`updateQueue`) queues its second request from the first request's handler.
 */

export interface FakeRequest {
    result: any;
    onsuccess: (() => void) | null;
    onerror: ((e: any) => void) | null;
}

export interface FakeStore {
    data: Map<string, any>;
    get(key: string): FakeRequest;
    put(value: any, key: string): FakeRequest;
    delete(key: string): FakeRequest;
    getAllKeys(): FakeRequest;
}

/**
 * One store's operations, bound to the transaction that asked for them — as in IndexedDB, where a request belongs
 * to the transaction its store was taken from. The count of outstanding requests is what `oncomplete` waits on.
 */
function bindStore(data: Map<string, any>, onSettle: (settled: () => void) => void): Omit<FakeStore, 'data'> {
    function request(value: () => any): FakeRequest {
        const req: FakeRequest = { result: null, onsuccess: null, onerror: null };
        onSettle(() => {
            req.result = value();
            if (req.onsuccess) req.onsuccess();
        });
        return req;
    }
    return {
        get: (key) => request(() => data.get(key) ?? undefined),
        put: (value, key) => request(() => { data.set(key, value); return key; }),
        delete: (key) => request(() => { data.delete(key); return undefined; }),
        getAllKeys: () => request(() => [...data.keys()]),
    };
}

export interface FakeTransaction {
    objectStore(storeName: string): FakeStore;
    oncomplete: (() => void) | null;
    onerror: ((e: any) => void) | null;
}

export interface FakeDB {
    objectStoreNames: DOMStringList;
    stores: Map<string, FakeStore>;
    createObjectStore(name: string): FakeStore;
    transaction(storeName: string, mode: string): FakeTransaction;
}

export interface FakeOpenRequest {
    result: FakeDB;
    onupgradeneeded: ((e: any) => void) | null;
    onsuccess: (() => void) | null;
    onerror: ((e: any) => void) | null;
}

export interface FakeIDBFactory {
    open(name: string, version: number): FakeOpenRequest;
    /** Test-only view, like `FakeDB.stores`: the databases this factory has created, by name. */
    databases: Map<string, FakeDB>;
}

class FakeDOMStringList {
    private _items: string[];
    constructor(items: string[]) { this._items = items; }
    get length() { return this._items.length; }
    item(index: number) { return this._items[index] ?? null; }
    contains(s: string) { return this._items.includes(s); }
    [Symbol.iterator]() { return this._items[Symbol.iterator](); }
}

function stringList(items: string[]): DOMStringList {
    return new FakeDOMStringList(items) as unknown as DOMStringList;
}

export function createFakeIndexedDB(): FakeIDBFactory {
    const databases = new Map<string, FakeDB>();

    const mockIDB: any = {
        databases,
        open(name: string, version: number): FakeOpenRequest {
            const req: FakeOpenRequest = {
                result: null as any,
                onupgradeneeded: null,
                onsuccess: null,
                onerror: null,
            };

            let db = databases.get(name);
            const isUpgrade = !db;

            if (!db) {
                const stores = new Map<string, FakeStore>();
                db = {
                    objectStoreNames: stringList([]),
                    stores,
                    createObjectStore(storeName: string): FakeStore {
                        const data = new Map<string, any>();
                        // Outside a transaction (the upgrade path) nobody is waiting on completion.
                        const store: FakeStore = { data, ...bindStore(data, (run) => queueMicrotask(run)) };
                        stores.set(storeName, store);
                        (db as FakeDB).objectStoreNames = stringList([...stores.keys()]);
                        return store;
                    },
                    transaction(storeName, _mode) {
                        const store = stores.get(storeName);
                        if (!store) throw new Error(`Unknown store: ${storeName}`);
                        const tx: FakeTransaction = {
                            objectStore(sn) {
                                if (sn !== storeName) throw new Error(`Wrong store: ${sn}`);
                                return {
                                    ...bindStore(store.data, settle),
                                    get data() { return store.data; },
                                };
                            },
                            oncomplete: null,
                            onerror: null,
                        };
                        // The count of this transaction's outstanding requests: a handler that queues another
                        // request keeps the transaction open until that one lands too.
                        let outstanding = 0;
                        function settle(run: () => void) {
                            outstanding++;
                            queueMicrotask(() => {
                                run();
                                outstanding--;
                                if (outstanding > 0) return;
                                queueMicrotask(() => {
                                    if (outstanding === 0 && tx.oncomplete) tx.oncomplete();
                                });
                            });
                        }
                        return tx;
                    },
                };
                databases.set(name, db);
            }

            req.result = db;

            if (isUpgrade) {
                queueMicrotask(() => {
                    if (req.onupgradeneeded) {
                        req.onupgradeneeded({ target: req, oldVersion: 0, newVersion: version } as any);
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

    return mockIDB as FakeIDBFactory;
}

/**
 * Keys every store of every database holds, keyed `<db>/<store>`. Assertions that a sweep left
 * nothing behind read this instead of the module's own getters, which answer `null` for a store the
 * sweep never touched too.
 */
export function fakeKeysByStore(factory: FakeIDBFactory): Record<string, string[]> {
    const contents: Record<string, string[]> = {};
    for (const [dbName, db] of factory.databases) {
        for (const [storeName, store] of db.stores) {
            contents[`${dbName}/${storeName}`] = [...store.data.keys()];
        }
    }
    return contents;
}

/** Publishes a fake as `globalThis.indexedDB`; the returned function puts things back. */
export function installFakeIndexedDB(factory: FakeIDBFactory = createFakeIndexedDB()): () => void {
    const target = globalThis as { indexedDB?: unknown };
    const had = 'indexedDB' in target;
    const previous = target.indexedDB;

    target.indexedDB = factory;

    return () => {
        if (had) target.indexedDB = previous;
        else delete target.indexedDB;
    };
}
