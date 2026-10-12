import { describe, it, expect, vi, beforeEach } from 'vitest';

import { setCsrfToken } from '../helpers';

/**
 * The refusals are states the listing paints differently — 415 no readable archive, 413 bigger than an
 * editor shows, 404 no member of that name — so they must survive to the caller. `fetch` is stubbed.
 */

let mockFetch: ReturnType<typeof vi.fn>;

function res(status: number, body: any) {
    return {
        ok: status >= 200 && status < 300,
        status,
        headers: new Headers(),
        json: async () => body,
    };
}

function sent(index = 0): { url: URL; init: any } {
    const [url, init] = mockFetch.mock.calls[index];
    return { url: new URL(String(url), 'http://localhost'), init };
}

beforeEach(() => {
    mockFetch = vi.fn();
    (globalThis as any).fetch = mockFetch;
    setCsrfToken('csrf-under-test');
});

describe('readFsArchive', () => {
    it('asks for one path in one scope, and for the commit when it was given one', async () => {
        const { readFsArchive } = await import('$lib/api/fs');
        mockFetch.mockResolvedValue(res(200, { format: 'tar.gz', entries: [] }));

        const listing = await readFsArchive({ path: '/r/bundle.tar.gz', project: 'p1', task: 't1', ref: 'HEAD' });

        expect(listing.format).toBe('tar.gz');
        const q = sent().url.searchParams;
        expect(sent().url.pathname).toBe('/api/fs/archive');
        expect([q.get('path'), q.get('project'), q.get('task'), q.get('ref')])
            .toEqual(['/r/bundle.tar.gz', 'p1', 't1', 'HEAD']);
    });

    it('keeps the listing whole, including that it is short', async () => {
        const { readFsArchive } = await import('$lib/api/fs');
        mockFetch.mockResolvedValue(res(200, {
            format: 'zip',
            truncated: true,
            entries: [{ name: 'notes/todo.md', size: 2048, mtime: '2023-11-14T22:13:20Z', isDir: false }],
        }));

        const listing = await readFsArchive({ path: '/r/bundle.zip', project: 'p1' });

        expect(listing.truncated).toBe(true);
        expect(listing.entries[0]).toEqual({ name: 'notes/todo.md', size: 2048, mtime: '2023-11-14T22:13:20Z', isDir: false });
    });

    it('keeps "not an archive" a status the listing can answer', async () => {
        const { readFsArchive, FsReadRefused } = await import('$lib/api/fs');
        mockFetch.mockResolvedValue(res(415, { error: 'not a readable archive' }));

        const refused = await readFsArchive({ path: '/r/fake.tar.gz', project: 'p1' }).catch((e) => e);

        expect(refused).toBeInstanceOf(FsReadRefused);
        expect(refused.status).toBe(415);
        expect(refused.message).toBe('not a readable archive');
    });
});

describe('readFsArchiveEntry', () => {
    it('names the member by the name the listing showed', async () => {
        const { readFsArchiveEntry } = await import('$lib/api/fs');
        mockFetch.mockResolvedValue(res(200, { content: 'hello from the archive' }));

        const content = await readFsArchiveEntry({
            path: '/r/bundle.tar.gz', project: 'p1', task: 't1', ref: 'HEAD', entry: 'notes/todo.md',
        });

        expect(content).toBe('hello from the archive');
        const q = sent().url.searchParams;
        expect(sent().url.pathname).toBe('/api/fs/archive/entry');
        expect([q.get('entry'), q.get('task'), q.get('ref')]).toEqual(['notes/todo.md', 't1', 'HEAD']);
    });

    it('answers the empty string for a member with nothing in it', async () => {
        const { readFsArchiveEntry } = await import('$lib/api/fs');
        mockFetch.mockResolvedValue(res(200, {}));

        await expect(readFsArchiveEntry({ path: '/r/bundle.zip', project: 'p1', entry: 'empty.txt' })).resolves.toBe('');
    });

    it('keeps "no such member" and "not text" apart', async () => {
        const { readFsArchiveEntry, FsReadRefused } = await import('$lib/api/fs');
        mockFetch.mockResolvedValueOnce(res(404, { error: 'no such file in the archive' }));
        mockFetch.mockResolvedValueOnce(res(415, { error: 'not a text file' }));

        const gone = await readFsArchiveEntry({ path: '/r/b.zip', project: 'p1', entry: 'nope' }).catch((e) => e);
        const binary = await readFsArchiveEntry({ path: '/r/b.zip', project: 'p1', entry: 'blob.bin' }).catch((e) => e);

        expect(gone).toBeInstanceOf(FsReadRefused);
        expect([gone.status, binary.status]).toEqual([404, 415]);
        expect(binary.message).toBe('not a text file');
    });
});
