import { describe, it, expect, vi, beforeEach } from 'vitest';

import { setCsrfToken } from '../helpers';

/**
 * The fs routes answer one editor and three explorers, and two of their refusals are states rather than
 * faults: 413 is "bigger than an editor shows" and 404 is "gone". A caller that cannot see the status
 * paints an error line where it means to show its too-large or not-found screen.
 *
 * Everything else the caller needs to keep saying is one of two things: the server's own sentence, or the
 * message of the action the user took (a save, a created file), which is why `writeFsFile` takes it.
 *
 * `fetch` is stubbed — no network.
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

/** The request at `index`, as the API layer put it on the wire. */
function sent(index = 0): { url: URL; init: any } {
    const [url, init] = mockFetch.mock.calls[index];
    return { url: new URL(String(url), 'http://localhost'), init };
}

beforeEach(() => {
    mockFetch = vi.fn();
    (globalThis as any).fetch = mockFetch;
    // A token in hand keeps ensureCsrfToken from spending its own request before the one under test.
    setCsrfToken('csrf-under-test');
});

describe('readFsFile', () => {
    it('keeps 404 and 413 as statuses the caller can answer', async () => {
        const { readFsFile, FsReadRefused } = await import('$lib/api/fs');
        mockFetch.mockResolvedValueOnce(res(404, { error: 'file not found' }));
        mockFetch.mockResolvedValueOnce(res(413, { error: 'file too large (max 1MB)' }));

        const gone = await readFsFile({ path: '/r/a.go', project: 'p1' }).catch((e) => e);
        const tooBig = await readFsFile({ path: '/r/big.go', project: 'p1' }).catch((e) => e);

        expect(gone).toBeInstanceOf(FsReadRefused);
        expect(gone.status).toBe(404);
        expect(gone.message).toBe('file not found');
        expect(tooBig.status).toBe(413);
        expect(tooBig.message).toBe('file too large (max 1MB)');
    });

    it('asks for the task worktree and the ref it was given', async () => {
        const { readFsFile } = await import('$lib/api/fs');
        mockFetch.mockResolvedValue(res(200, { content: 'package main' }));

        const file = await readFsFile({ path: '/r/a.go', project: 'p1', task: 't1', ref: 'HEAD' });

        expect(file.content).toBe('package main');
        const q = sent().url.searchParams;
        expect(sent().url.pathname).toBe('/api/fs/read');
        expect([q.get('path'), q.get('project'), q.get('task'), q.get('ref')])
            .toEqual(['/r/a.go', 'p1', 't1', 'HEAD']);
    });
});

describe('writeFsFile', () => {
    it('sends the CSRF token and the session cookie the caller used to add by hand', async () => {
        const { writeFsFile } = await import('$lib/api/fs');
        mockFetch.mockResolvedValue(res(200, { status: 'written' }));

        await writeFsFile({ project: 'p1', task: 't1', path: '/r/a.go', content: 'x', fallback: 'Failed to save' });

        const { url, init } = sent();
        expect(url.pathname).toBe('/api/fs/write');
        expect([url.searchParams.get('project'), url.searchParams.get('task')]).toEqual(['p1', 't1']);
        expect(init.headers.get('X-CSRF-Token')).toBe('csrf-under-test');
        expect(init.credentials).toBe('include');
        expect(JSON.parse(init.body)).toEqual({ path: '/r/a.go', content: 'x' });
    });

    it('says the server\'s reason when it has one and the caller\'s otherwise', async () => {
        const { writeFsFile } = await import('$lib/api/fs');
        mockFetch.mockResolvedValueOnce(res(403, { error: 'path is outside the allowed host directories' }));
        mockFetch.mockResolvedValueOnce(res(500, {}));

        await expect(writeFsFile({ project: 'p1', path: '/r/a.go', content: '', fallback: 'Failed to create file' }))
            .rejects.toThrow('path is outside the allowed host directories');
        await expect(writeFsFile({ project: 'p1', path: '/r/a.go', content: '', fallback: 'Failed to create file' }))
            .rejects.toThrow('Failed to create file');
    });
});

describe('createFsDirectory', () => {
    it('sends no scope at all for the host picker, which the declared directories jail', async () => {
        const { createFsDirectory } = await import('$lib/api/fs');
        mockFetch.mockResolvedValue(res(200, {}));

        await createFsDirectory({ path: '/home/dev/new' });

        expect(sent().url.search).toBe('');
    });
});

describe('resolveHomeDirectory', () => {
    it('answers the home the server reports', async () => {
        const { resolveHomeDirectory } = await import('$lib/api/fs');
        mockFetch.mockResolvedValue(res(200, { home: '/root' }));

        expect(await resolveHomeDirectory()).toBe('/root');
    });

    it('answers /home rather than rejecting when the lookup fails or says nothing', async () => {
        const { resolveHomeDirectory } = await import('$lib/api/fs');
        mockFetch.mockResolvedValueOnce(res(500, { error: 'internal error' }));

        expect(await resolveHomeDirectory()).toBe('/home');

        mockFetch.mockResolvedValueOnce(res(200, {}));
        expect(await resolveHomeDirectory()).toBe('/home');
    });
});

describe('searchFsPaths', () => {
    it('sends the one query shape both callers share', async () => {
        const { searchFsPaths } = await import('$lib/api/fs');
        mockFetch.mockResolvedValue(res(200, { root: '/w', results: [{ path: 'a.go', line: 3 }] }));

        const answer = await searchFsPaths({
            query: 'a', project: 'p1', task: 't1', content: true, matchCase: true, wholeWord: true, regex: true,
        });

        expect(answer.root).toBe('/w');
        expect(sent().url.search).toBe('?q=a&project=p1&task=t1&content=1&case=1&word=1&regex=1');
    });
});

describe('downloadURL', () => {
    it('sends one path key per path, which is how a selection travels', async () => {
        const { downloadURL } = await import('$lib/api/fs');

        const url = new URL(downloadURL({
            project: 'p1', task: 't1', paths: ['/w/src', '/w/a note.txt', '/w/b&c.txt'],
        }), 'http://localhost');

        expect(url.pathname).toBe('/api/fs/download');
        expect(url.searchParams.getAll('path')).toEqual(['/w/src', '/w/a note.txt', '/w/b&c.txt']);
        expect(url.searchParams.get('project')).toBe('p1');
        expect(url.searchParams.get('task')).toBe('t1');
    });

    it('sends no task for a project\'s own checkout', async () => {
        const { downloadURL } = await import('$lib/api/fs');

        const url = new URL(downloadURL({ project: 'p1', paths: ['/w/a.go'] }), 'http://localhost');

        expect(url.searchParams.has('task')).toBe(false);
    });
});
