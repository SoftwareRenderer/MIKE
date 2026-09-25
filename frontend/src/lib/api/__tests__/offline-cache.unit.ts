import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { installFakeIndexedDB } from '../../__tests__/fake-indexeddb';

let restoreIndexedDB: (() => void) | undefined;

beforeEach(() => {
    vi.resetModules();
    restoreIndexedDB = installFakeIndexedDB();
});

afterEach(() => restoreIndexedDB?.());

// ─── Fixtures ────────────────────────────────────────────────────────────────

const messages = [
    { id: 'm1', task_id: 't1', thread_id: '', role: 'user', content: 'hello', created_at: '2026-01-01T00:00:00Z' },
    { id: 'm2', task_id: 't1', thread_id: '', role: 'assistant', content: 'hi there', created_at: '2026-01-01T00:00:01Z' },
];

const diff = { diff: '--- a\n+++ b\n@@ -1 +1 @@\n-old\n+new', untracked: [], short_stat: '1 file changed' };

function mockResolvedFetch(body: any, headers: Record<string, string> = {}) {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
        ok: true,
        json: async () => body,
        headers: { get: (name: string) => headers[name.toLowerCase()] ?? null },
    }));
}
function mockRejectedFetch() {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('Failed to fetch')));
}

/** The window headers the messages route answers with. */
const windowHeaders = { 'x-messages-more': 'false' };

describe('offline message cache', () => {
    it('returns the cached window (marked from cache) when network fails', async () => {
        const storage = await import('$lib/storage');
        const { isFromCache } = await import('$lib/api/cache');
        mockRejectedFetch();
        const cachedWindow = { messages, hasMore: true, before: 'cursor-1' };
        await storage.setBoardCache(storage.messagesCacheKey('p1', 't1'), cachedWindow);

        const { getTaskMessages } = await import('$lib/api/tasks');
        const result = await getTaskMessages('p1', 't1');

        expect(result.messages).toEqual(messages);
        // The cached copy carries what the server said about the window, not just its rows.
        expect(result.before).toBe('cursor-1');
        expect(isFromCache(result)).toBe(true);
    });

    it('leaves the cached window alone when an older one is fetched', async () => {
        const storage = await import('$lib/storage');
        const newest = { messages, hasMore: true, before: 'cursor-1' };
        await storage.setBoardCache(storage.messagesCacheKey('p1', 't1'), newest);
        vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
            ok: true, json: async () => [], headers: { get: () => null },
        }));

        const { getTaskMessages } = await import('$lib/api/tasks');
        await getTaskMessages('p1', 't1', 'cursor-1');

        // An offline reload opens on this entry, so it must stay the newest window.
        expect(await storage.getBoardCache(storage.messagesCacheKey('p1', 't1'))).toEqual(newest);
    });

    it('caches the window the server described', async () => {
        const storage = await import('$lib/storage');
        mockResolvedFetch(messages, { ...windowHeaders, 'x-messages-more': 'true', 'x-messages-before': 'cursor-9' });

        const { getTaskMessages } = await import('$lib/api/tasks');
        const result = await getTaskMessages('p1', 't1');

        expect(result).toEqual({ messages, hasMore: true, before: 'cursor-9' });
        // The cache is written beside the read, so the entry appears shortly after the window lands.
        await vi.waitFor(async () => {
            expect(await storage.getBoardCache(storage.messagesCacheKey('p1', 't1'))).toEqual(result);
        });
    });

    it('keeps the rows this browser already saw above a smaller window', async () => {
        const storage = await import('$lib/storage');
        const older = Array.from({ length: 8 }, (_, i) => ({
            id: `old-${i}`, task_id: 't1', thread_id: '', role: 'user', content: `older ${i}`,
            created_at: `2026-01-01T00:00:0${i}Z`,
        }));
        const seed = { messages: older, hasMore: false, before: '' };
        await storage.setBoardCache(storage.messagesCacheKey('p1', 't1'), seed);
        mockResolvedFetch(messages, windowHeaders);

        const { getTaskMessages } = await import('$lib/api/tasks');
        await getTaskMessages('p1', 't1');

        // An offline reload opens on the bigger of the two reads.
        await vi.waitFor(async () => {
            const cached = await storage.getBoardCache<any>(storage.messagesCacheKey('p1', 't1'));
            expect(cached?.messages.map((m: any) => m.id)).toEqual([...older.map(m => m.id), 'm1', 'm2']);
        });
    });

    it('sends the cursor of the window it is asking for', async () => {
        const fetchMock = vi.fn().mockResolvedValue({
            ok: true, json: async () => [], headers: { get: () => null },
        });
        vi.stubGlobal('fetch', fetchMock);

        const { getTaskMessages } = await import('$lib/api/tasks');
        await getTaskMessages('p1', 't1', 'a/b+c');

        // apiFetch resolves the CSRF session first, so the messages request is the call after it.
        const url = fetchMock.mock.calls.map(c => String(c[0])).find(u => u.includes('/messages'));
        expect(url).toContain('/projects/p1/tasks/t1/messages?before=a%2Fb%2Bc');
    });

    it('rejects when network fails and no cache exists', async () => {
        mockRejectedFetch();
        const { getTaskMessages } = await import('$lib/api/tasks');
        await expect(getTaskMessages('p1', 't1')).rejects.toBeTruthy();
    });

    it('returns and caches the newest window revision', async () => {
        const storage = await import('$lib/storage');
        mockResolvedFetch(messages, { ...windowHeaders, etag: '"window-1"' });

        const { getTaskMessages } = await import('$lib/api/tasks');
        const result = await getTaskMessages('p1', 't1');

        expect(result.revision).toBe('"window-1"');
        await vi.waitFor(async () => {
            expect(await storage.getBoardCache(storage.messagesCacheKey('p1', 't1'))).toEqual(result);
        });
    });

    it('revalidates a known newest-window revision', async () => {
        const fetchMock = vi.fn().mockResolvedValue({
            ok: true,
            status: 200,
            json: async () => messages,
            headers: { get: (name: string) => name.toLowerCase() === 'etag' ? '"window-1"' : null },
        });
        vi.stubGlobal('fetch', fetchMock);

        const { getTaskMessages } = await import('$lib/api/tasks');
        const result = await getTaskMessages('p1', 't1', '', '"window-1"');

        const messagesCall = fetchMock.mock.calls.find(([url]) => String(url).includes('/messages'));
        expect(messagesCall).toBeTruthy();
        expect(new Headers((messagesCall![1] as RequestInit).headers).get('If-None-Match')).toBe('"window-1"');
        expect(result.unchanged).toBeUndefined();
        expect(result.revision).toBe('"window-1"');
    });

    it('asks about no revision for an older page', async () => {
        const fetchMock = vi.fn().mockResolvedValue({
            ok: true,
            status: 200,
            json: async () => messages,
            headers: { get: (name: string) => name.toLowerCase() === 'etag' ? '"page-2"' : null },
        });
        vi.stubGlobal('fetch', fetchMock);

        const { getTaskMessages } = await import('$lib/api/tasks');
        const result = await getTaskMessages('p1', 't1', 'cursor-1', '"window-1"');

        const messagesCall = fetchMock.mock.calls.find(([url]) => String(url).includes('/messages'));
        expect(new Headers((messagesCall![1] as RequestInit).headers).get('If-None-Match')).toBeNull();
        expect(result.revision).toBeUndefined();
    });

    it('treats a direct 304 as the current window without parsing a body', async () => {
        vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
            ok: false,
            status: 304,
            json: async () => { throw new Error('a 304 has no body'); },
            headers: { get: (name: string) => name.toLowerCase() === 'etag' ? '"window-1"' : null },
        }));

        const { getTaskMessages } = await import('$lib/api/tasks');
        const result = await getTaskMessages('p1', 't1', '', '"window-1"');

        expect(result).toMatchObject({ unchanged: true, revision: '"window-1"', messages: [] });
    });

    it('never offers a cached fallback as a live revision', async () => {
        const storage = await import('$lib/storage');
        await storage.setBoardCache(storage.messagesCacheKey('p1', 't1'), {
            messages, hasMore: false, before: '', revision: '"stale"',
        });
        mockRejectedFetch();

        const { getTaskMessages } = await import('$lib/api/tasks');
        const result = await getTaskMessages('p1', 't1');

        expect(result.messages).toEqual(messages);
        expect(result.revision).toBeUndefined();
        expect(result.unchanged).toBeUndefined();
    });
});

describe('offline diff cache', () => {
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
