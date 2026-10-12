// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';
import ArchiveViewer from '$lib/components/ArchiveViewer.svelte';
import { setCsrfToken } from '$lib/api';

/**
 * What the pane must get right: the three things on every row — name, size, date — that a folder row is not
 * a way in, and the server's own refusal when a member is not text.
 */

const ARCHIVE = '/srv/repo/dist/bundle.tar.gz';

const listing = {
    format: 'tar.gz',
    entries: [
        { name: 'notes', size: 0, mtime: '2023-11-14T22:13:20Z', isDir: true },
        { name: 'notes/todo.md', size: 9, mtime: '2023-11-14T22:13:20Z', isDir: false },
        { name: 'report.txt', size: 2048, mtime: '2024-01-02T03:04:05Z', isDir: false },
        { name: 'blob.bin', size: 16, mtime: '2023-11-14T22:13:20Z', isDir: false },
    ],
};

function stub(routes: Record<string, { status?: number; body: unknown }>) {
    setCsrfToken('csrf-under-test');
    vi.stubGlobal('fetch', vi.fn(async (input: string) => {
        const url = new URL(String(input), 'http://localhost');
        for (const [route, answer] of Object.entries(routes)) {
            if (url.pathname !== route) continue;
            return new Response(JSON.stringify(answer.body), {
                status: answer.status ?? 200,
                headers: { 'Content-Type': 'application/json' },
            });
        }
        return new Response(JSON.stringify({ error: `unexpected request ${url.pathname}` }), { status: 404 });
    }));
}

function open(props: Record<string, unknown> = {}) {
    return render(ArchiveViewer, { props: { projectId: 'p1', path: ARCHIVE, ...props } });
}

afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
});

describe('the listing', () => {
    it('shows each member by name, size and date', async () => {
        stub({ '/api/fs/archive': { body: listing } });
        const { container, findByText } = open();
        await findByText('report.txt');

        const rows = [...container.querySelectorAll('tbody tr')];
        expect(rows).toHaveLength(4);
        const cells = (row: Element) => [...row.querySelectorAll('td')].map(td => td.textContent?.trim());
        expect(cells(rows[1])[0]).toBe('notes/todo.md');
        expect(cells(rows[1])[1]).toBe('9 B');
        expect(cells(rows[2])[1]).toBe('2.0 KB');
        expect(cells(rows[2])[2]).not.toBe('');
        // The columns are named, because a bare number in a table answers nothing.
        expect([...container.querySelectorAll('thead th')].map(th => th.textContent)).toEqual(['Name', 'Size', 'Modified']);
    });

    it('asks for the task worktree it was handed', async () => {
        stub({ '/api/fs/archive': { body: listing } });
        open({ taskId: 't1', ref: 'HEAD' });
        await waitFor(() => expect(fetch).toHaveBeenCalled());
        const url = new URL(String(vi.mocked(fetch).mock.calls[0][0]), 'http://localhost');
        expect([url.searchParams.get('project'), url.searchParams.get('task'), url.searchParams.get('path'), url.searchParams.get('ref')])
            .toEqual(['p1', 't1', ARCHIVE, 'HEAD']);
    });

    it('offers no way in for a folder row', async () => {
        stub({ '/api/fs/archive': { body: listing } });
        const { container, findByText } = open();
        await findByText('notes/');
        expect(container.querySelector('.archive-name-btn')).not.toBeNull();
        expect([...container.querySelectorAll('tbody tr')][0].querySelector('button')).toBeNull();
    });

    it('lists every member when two of them share a name', async () => {
        // tar and zip both allow one name twice, so the pane may not treat a name as an identity.
        stub({
            '/api/fs/archive': {
                body: {
                    format: 'tar',
                    entries: [
                        { name: 'a.txt', size: 5, mtime: '2023-11-14T22:13:20Z', isDir: false },
                        { name: 'a.txt', size: 6, mtime: '2023-11-14T22:13:20Z', isDir: false },
                    ],
                },
            },
        });
        const { container } = open();
        await waitFor(() => expect(container.querySelectorAll('tbody tr')).toHaveLength(2));
        expect([...container.querySelectorAll('.archive-cell-size')].map(td => td.textContent)).toEqual(['5 B', '6 B']);
    });

    it('says so when the rows it shows are not all of them', async () => {
        stub({ '/api/fs/archive': { body: { ...listing, truncated: true } } });
        const { findByText } = open();
        await findByText('Not everything is listed — reading the archive ran out of its budget.');
    });

    it('shows nothing was found for an archive that holds no files', async () => {
        stub({ '/api/fs/archive': { body: { format: 'zip', entries: [] } } });
        const { findByText } = open();
        await findByText('This archive holds no files.');
    });

    it('answers an unreadable archive with the server’s own sentence', async () => {
        stub({ '/api/fs/archive': { status: 415, body: { error: 'not a readable archive' } } });
        const { findByText, queryByText } = open();
        await findByText('not a readable archive');
        expect(queryByText('Name')).toBeNull();
    });
});

describe('one member', () => {
    it('reads the member the row names, by that name', async () => {
        stub({
            '/api/fs/archive': { body: listing },
            '/api/fs/archive/entry': { body: { content: 'hello from the archive' } },
        });
        const { container, findByText } = open();
        await findByText('report.txt');
        const row = [...container.querySelectorAll('tbody tr')][2];
        await fireEvent.click(row.querySelector('button')!);

        await waitFor(() => {
            const url = new URL(String(vi.mocked(fetch).mock.calls.at(-1)![0]), 'http://localhost');
            expect(url.pathname).toBe('/api/fs/archive/entry');
            expect(url.searchParams.get('entry')).toBe('report.txt');
        });
        // CodeMirror paints the document into its own lines; the text is there, whichever way it is split.
        await waitFor(() => expect(container.textContent).toContain('hello from the archive'));
    });

    it('goes back to the listing without asking for it again', async () => {
        stub({
            '/api/fs/archive': { body: listing },
            '/api/fs/archive/entry': { body: { content: 'x' } },
        });
        const { container, findByText, getByTitle } = open();
        await findByText('report.txt');
        await fireEvent.click(container.querySelectorAll('tbody tr')[2].querySelector('button')!);
        await waitFor(() => expect(container.querySelector('.archive-entry')).not.toBeNull());
        const asksAfterEntry = vi.mocked(fetch).mock.calls.length;
        await fireEvent.click(getByTitle('Back to the file list'));

        await findByText('report.txt');
        expect(vi.mocked(fetch).mock.calls.filter(c => String(c[0]).includes('/api/fs/archive?'))).toHaveLength(1);
        expect(vi.mocked(fetch).mock.calls.length).toBe(asksAfterEntry);
    });

    it('shows the refusal for a member the editor cannot show', async () => {
        stub({
            '/api/fs/archive': { body: listing },
            '/api/fs/archive/entry': { status: 415, body: { error: 'not a text file' } },
        });
        const { container, findByText } = open();
        await findByText('blob.bin');
        await fireEvent.click(container.querySelectorAll('tbody tr')[3].querySelector('button')!);
        await findByText('not a text file');
    });
});
