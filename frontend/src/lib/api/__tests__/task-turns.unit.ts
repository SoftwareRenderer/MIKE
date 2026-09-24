import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { installFakeIndexedDB } from '../../__tests__/fake-indexeddb';

// The prompt read is the only thing that names the prompt a window of answers leaves out, so its paging
// headers are the contract — and it must never become a transcript in the cache.

let restoreIndexedDB: (() => void) | undefined;

beforeEach(() => {
    vi.resetModules();
    restoreIndexedDB = installFakeIndexedDB();
});

afterEach(() => restoreIndexedDB?.());

const turns = [
    { id: 'u-1', created_at: '2026-01-01T00:00:00Z', text: 'first prompt' },
    { id: 'u-2', created_at: '2026-01-01T00:05:00Z', text: 'second prompt' },
];

function stubFetch(body: unknown, headers: Record<string, string> = {}) {
    const fetchMock = vi.fn().mockResolvedValue({
        ok: true,
        json: async () => body,
        headers: { get: (name: string) => headers[name.toLowerCase()] ?? null },
    });
    vi.stubGlobal('fetch', fetchMock);
    return fetchMock;
}

/** apiFetch arms the session first, so the request under test is whichever call carried the URL. */
function turnsUrl(fetchMock: ReturnType<typeof vi.fn>) {
    const call = fetchMock.mock.calls.find(c => String(c[0]).includes('/turns'));
    if (!call) throw new Error(`no /turns request among: ${fetchMock.mock.calls.map(c => String(c[0])).join(', ')}`);
    return String(call[0]);
}

describe('the prompt read', () => {
    it('answers the page and how to ask for the older ones', async () => {
        const fetchMock = stubFetch(turns, { 'x-turns-more': 'true', 'x-turns-before': 'cursor-9' });

        const { getTaskTurns } = await import('$lib/api/tasks');
        const page = await getTaskTurns('p1', 't1');

        expect(turnsUrl(fetchMock)).toContain('/api/projects/p1/tasks/t1/turns');
        expect(page.turns).toEqual(turns);
        expect(page.hasMore).toBe(true);
        expect(page.before).toBe('cursor-9');
    });

    it('sends the cursor back as it was issued', async () => {
        const fetchMock = stubFetch([], { 'x-turns-more': 'false' });

        const { getTaskTurns } = await import('$lib/api/tasks');
        const page = await getTaskTurns('p1', 't1', 'cursor 9+');

        expect(turnsUrl(fetchMock)).toContain('/turns?before=cursor%209%2B');
        expect(page.hasMore).toBe(false);
        expect(page.before).toBe('');
    });

    it('leaves the message cache alone: a prompt list is not a transcript', async () => {
        stubFetch(turns);
        const storage = await import('$lib/storage');

        const { getTaskTurns } = await import('$lib/api/tasks');
        await getTaskTurns('p1', 't1');

        expect(await storage.getBoardCache(storage.messagesCacheKey('p1', 't1'))).toBeNull();
    });

    it('rejects rather than answering with nothing', async () => {
        vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
            ok: false,
            status: 400,
            statusText: 'Bad Request',
            json: async () => ({ error: 'limit must be between 1 and 50' }),
            text: async () => '{"error":"limit must be between 1 and 50"}',
            headers: { get: () => null },
        }));

        const { getTaskTurns } = await import('$lib/api/tasks');
        await expect(getTaskTurns('p1', 't1', 'nope')).rejects.toThrow();
    });
});
