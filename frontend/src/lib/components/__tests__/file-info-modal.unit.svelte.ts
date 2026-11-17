// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, cleanup, waitFor } from '@testing-library/svelte';
import FileInfoModal from '$lib/components/FileInfoModal.svelte';
import { setCsrfToken } from '$lib/api';

/**
 * What the Info dialog must get right: every asked path keeps its own row even when the file is gone, a
 * folder's figure says whether it is a total or a floor, and the line above the table counts what the
 * server measured rather than what the rows claim about themselves.
 */

const SRC = '/srv/repo/src';
const GONE = '/srv/repo/gone.txt';

const rows = [
    { name: 'main.go', path: SRC + '/main.go', kind: 'file', bytes: 2048, mode: '0640', modified: '2026-11-02T09:10:11Z', mime: 'text/plain; charset=utf-8' },
    { name: 'assets', path: SRC + '/assets', kind: 'dir', bytes: 140, items: 3, mode: '0755', modified: '2026-11-01T08:00:00Z' },
    { name: 'gone.txt', path: GONE, kind: 'missing' },
];

/** Stub the one request the dialog makes, and hand back the URLs it spent. */
function stub(answer: { status?: number; body: unknown }): URL[] {
    setCsrfToken('csrf-under-test');
    const asked: URL[] = [];
    vi.stubGlobal('fetch', vi.fn(async (input: string) => {
        asked.push(new URL(String(input), 'http://localhost'));
        return new Response(JSON.stringify(answer.body), {
            status: answer.status ?? 200,
            headers: { 'Content-Type': 'application/json' },
        });
    }));
    return asked;
}

/**
 * Open the dialog over `paths` and wait for the answer to reach the table, so a test reads a settled
 * dialog rather than the loading line.
 */
async function open(props: { paths?: string[]; ignored?: Record<string, boolean> } = {}) {
    const view = render(FileInfoModal, {
        props: { project: 'p1', paths: [SRC, GONE], onClose: () => {}, ...props },
    });
    await waitFor(() => expect(view.container.querySelector('tbody')).not.toBeNull());
    const cells = (index: number) => [...view.container.querySelectorAll('tbody tr')[index]
        .querySelectorAll('td')].map(td => td.textContent?.trim());
    return {
        ...view,
        cells,
        /** The line above the table. */
        summary: () => view.container.querySelector('.fi-summary')?.textContent?.replace(/\s+/g, ' ').trim() ?? '',
        /** The name the table draws for one row — the header can hold the same words for one selection. */
        nameAt: (index: number) => view.container.querySelectorAll('.fi-name')[index]?.textContent?.trim() ?? '',
    };
}

afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
});

describe('the rows', () => {
    it('answers one row per asked path, with what the listing does not carry', async () => {
        stub({ body: { entries: rows } });
        const { container, cells, getByTitle, nameAt } = await open();

        expect(container.querySelectorAll('tbody tr')).toHaveLength(3);
        expect(nameAt(0)).toBe('main.go');
        expect(cells(0)[0]).toContain(SRC + '/main.go');
        expect(cells(0)[0]).toContain('0640');
        expect(cells(0)[0]).toContain('text/plain');
        expect(cells(0)[1]).toBe('File');
        expect(cells(0)[2]).toBe('2.0 KB');
        expect(cells(0)[3]).not.toBe('');

        expect(cells(1)[1]).toBe('Folder');
        expect(cells(1)[2]).toContain('140 B');
        expect(cells(1)[2]).toContain('3 items');

        expect(cells(2)[1]).toBe('Not found');
        expect(cells(2)[2]).toBe('');
        expect(cells(2)[3]).toBe('');
        expect(getByTitle(SRC + '/main.go')).toBeTruthy();
    });

    it('asks for the paths the menu was opened on, in their own order', async () => {
        const asked = stub({ body: { entries: rows } });
        await open();

        expect(asked).toHaveLength(1);
        expect(asked[0].pathname).toBe('/api/fs/stat');
        expect(asked[0].searchParams.getAll('path')).toEqual([SRC, GONE]);
        expect(asked[0].searchParams.get('project')).toBe('p1');
    });

    it('counts the selection above the table, folders and gone rows named', async () => {
        stub({ body: { entries: rows } });
        const { summary } = await open();

        // 2048 + 140 bytes, one folder, one row the server could not find.
        expect(summary()).toContain('3 items');
        expect(summary()).toContain('2.1 KB');
        expect(summary()).toContain('1 folder');
        expect(summary()).toContain('1 not found');
    });

    it('marks a folder whose walk ran out as a floor, in its row and in the line above it', async () => {
        stub({ body: { entries: [{ name: 'huge', path: '/w/huge', kind: 'dir', bytes: 1024 * 1024, items: 20000, partial: true }] } });
        const { cells, summary, nameAt } = await open({ paths: ['/w/huge'] });

        expect(nameAt(0)).toBe('huge');
        expect(cells(0)[2]).toContain('at least 1.0 MB');
        // Said twice on purpose: once about the folder, once about the selection built from it.
        expect(summary()).toContain('at least 1.0 MB');
    });

    it('marks the rows git excludes, which the caller knows and this route never asks', async () => {
        stub({ body: { entries: [{ name: 'dist', path: SRC, kind: 'dir', bytes: 0, items: 0 }] } });
        const { cells, nameAt } = await open({ paths: [SRC], ignored: { [SRC]: true } });

        expect(nameAt(0)).toBe('dist');
        expect(cells(0)[1]).toContain('Git-ignored');
    });

    it('says nothing about a row the server could not state, or price as a file', async () => {
        stub({ body: { entries: [
            { name: 'wedged', path: '/w/wedged', kind: 'unknown' },
            { name: 'pipe', path: '/w/pipe', kind: 'other' },
        ] } });
        const { cells, nameAt } = await open({ paths: ['/w/wedged', '/w/pipe'] });

        expect(nameAt(0)).toBe('wedged');
        expect(cells(0)[1]).toBe('Unknown');
        expect(cells(0)[2]).toBe('');
        expect(cells(0)[3]).toBe('');
        expect(nameAt(1)).toBe('pipe');
        expect(cells(1)[1]).toBe('Other');
        expect(cells(1)[2]).toBe('');
    });

    it('shows the server\'s own sentence when it refuses the whole request', async () => {
        stub({ status: 400, body: { error: 'path is outside the allowed directory' } });
        const view = render(FileInfoModal, { props: { project: 'p1', paths: ['/etc/passwd'], onClose: () => {} } });
        await waitFor(() => expect(view.container.textContent).toContain('path is outside the allowed directory'));

        expect(view.container.querySelector('tbody')).toBeNull();
    });
});
