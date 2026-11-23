import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { installFakeIndexedDB } from '../../__tests__/fake-indexeddb';

let restoreIndexedDB: (() => void) | undefined;

beforeEach(() => {
    vi.resetModules();
    restoreIndexedDB = installFakeIndexedDB();
});

afterEach(() => restoreIndexedDB?.());

// The head read shares the messages route with the window. These are the parts of the window's contract that
// must not follow it: the conditional header, the cache write, and the cached fallback.

const openingRows = [
    { id: 'm1', task_id: 't1', thread_id: '', role: 'user', content: 'the first ask', created_at: '2026-01-01T00:00:00Z' },
    { id: 'm2', task_id: 't1', thread_id: '', role: 'assistant', content: 'and its answer', created_at: '2026-01-01T00:00:01Z' },
];

function headFetch() {
    const fetchMock = vi.fn().mockResolvedValue({
        ok: true,
        json: async () => openingRows,
        // A head answer carries neither paging header.
        headers: { get: () => null },
    });
    vi.stubGlobal('fetch', fetchMock);
    return fetchMock;
}

const PATH = '/api/projects/p1/tasks/t1/messages';

describe('the head read', () => {
    it('asks the route for the opening page and hands back its rows', async () => {
        const fetchMock = headFetch();
        const { getTaskMessageHead, MESSAGE_JUMP_LIMIT } = await import('$lib/api/tasks');

        const rows = await getTaskMessageHead('p1', 't1');

        const url = String(fetchMock.mock.calls[0][0]);
        expect(url).toContain(PATH);
        expect(url).toContain('head=1');
        expect(url).toContain(`limit=${MESSAGE_JUMP_LIMIT}`);
        expect(rows).toEqual(openingRows);
    });

    it('sends no conditional header, because the revision describes the newest window', async () => {
        const fetchMock = headFetch();
        const { getTaskMessageHead } = await import('$lib/api/tasks');

        await getTaskMessageHead('p1', 't1');

        const init = fetchMock.mock.calls[0][1] as RequestInit | undefined;
        const headers = (init?.headers ?? {}) as Record<string, string>;
        expect(headers['If-None-Match']).toBeUndefined();
    });

    it('leaves the mirrored window alone — a holed transcript is never cached', async () => {
        const storage = await import('$lib/storage');
        const mirrored = { messages: openingRows, hasMore: true, before: 'cursor-1' };
        await storage.setBoardCache(storage.messagesCacheKey('p1', 't1'), mirrored);
        headFetch();

        const { getTaskMessageHead } = await import('$lib/api/tasks');
        await getTaskMessageHead('p1', 't1');

        // The entry an offline reload opens on must stay the newest window, holes and all.
        expect(await storage.getBoardCache(storage.messagesCacheKey('p1', 't1'))).toEqual(mirrored);
    });

    it('has no cached fallback: the mirror holds the end, the reader asked for the start', async () => {
        const storage = await import('$lib/storage');
        await storage.setBoardCache(storage.messagesCacheKey('p1', 't1'), {
            messages: openingRows, hasMore: true, before: 'cursor-1',
        });
        vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('Failed to fetch')));

        const { getTaskMessageHead } = await import('$lib/api/tasks');

        await expect(getTaskMessageHead('p1', 't1')).rejects.toBeTruthy();
    });
});
