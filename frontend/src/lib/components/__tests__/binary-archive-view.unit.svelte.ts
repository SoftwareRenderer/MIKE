// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';
import DiffViewer from '$lib/components/DiffViewer.svelte';
import CommitDiffOverlay from '$lib/components/CommitDiffOverlay.svelte';
import { setCsrfToken } from '$lib/api';
import type { DiffFile } from '$lib/types/diff';
import type { GitCommit } from '$lib/types';

/**
 * A `.tar.gz` in a diff is "Binary file" like any other, but its row can offer its member list, read from
 * the side the panel is comparing. Which side that is stays the host's decision.
 */

vi.mock('svelte/transition', () => ({ slide: vi.fn(), fade: vi.fn() }));

const source = { project: 'p1', task: 't1', working: { old: 'HEAD' }, committed: { old: 'aaa111', new: 'bbb222' } };

function diffFile(path: string, extra: Partial<DiffFile> = {}): DiffFile {
    return { path, rows: [], added: 0, removed: 0, isBinary: true, fileSize: 4096, ...extra };
}

function openViewer(unstagedFiles: DiffFile[], props: Record<string, unknown> = {}) {
    return render(DiffViewer, { props: { stagedFiles: [], unstagedFiles, imageSource: source, ...props } });
}

const commit: GitCommit = { hash: 'ccc333', short: 'ccc333', subject: 's', author: 'a', date: '2026-01-01', parents: ['bbb222'] };

function openCommit(files: DiffFile[]) {
    return render(CommitDiffOverlay, {
        props: { commit, files, loading: false, projectId: 'p1', taskId: 't1', onClose: () => {}, onNavigate: () => {}, onFileSelected: () => {}, hasPrev: false, hasNext: false },
    });
}

/** The listing pane asks for the archive as soon as it renders. */
function stubListing() {
    setCsrfToken('csrf-under-test');
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({
        format: 'tar.gz',
        entries: [{ name: 'report.txt', size: 2048, mtime: '2023-11-14T22:13:20Z', isDir: false }],
    }), { status: 200, headers: { 'Content-Type': 'application/json' } })));
}

function asked(): URL {
    const calls = vi.mocked(fetch).mock.calls.filter(c => String(c[0]).includes('/api/fs/archive'));
    return new URL(String(calls.at(-1)![0]), 'http://localhost');
}

afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
});

describe('the binary notice of an archive', () => {
    it('offers the list, and no picture', () => {
        const { container } = openViewer([
            diffFile('vendor/archive.zip'),
            diffFile('docs/logo.png'),
            diffFile('notes.txt', { isBinary: false, rows: [] }),
        ]);
        expect(container.querySelectorAll('.diff-list-contents-btn')).toHaveLength(1);
        expect(container.querySelectorAll('.diff-view-image-btn')).toHaveLength(1);
        expect(container.querySelector('.diff-list-contents-btn')!.textContent).toBe('List contents');
    });

    it('offers nothing when the host has not said whose project this is', () => {
        const { container } = openViewer([diffFile('vendor/archive.zip')], { imageSource: undefined });
        expect(container.querySelectorAll('.diff-list-contents-btn')).toHaveLength(0);
    });

    it('offers nothing for an archive the change removed', () => {
        const { container } = openViewer([diffFile('vendor/archive.zip', { isDeleted: true })]);
        expect(container.querySelectorAll('.diff-list-contents-btn')).toHaveLength(0);
    });

    it('reads the working tree for the unstaged section', async () => {
        stubListing();
        const { container } = openViewer([diffFile('dist/bundle.tar.gz')]);
        await fireEvent.click(container.querySelector('.diff-list-contents-btn')!);

        await waitFor(() => expect(container.textContent).toContain('report.txt'));
        const q = asked().searchParams;
        expect([q.get('project'), q.get('task'), q.get('path'), q.get('ref')]).toEqual(['p1', 't1', 'dist/bundle.tar.gz', null]);
    });

    it('reads the committed side from the ref the host named for it', async () => {
        stubListing();
        const { container } = openViewer([], { committedFiles: [diffFile('dist/bundle.tar.gz')] });
        await fireEvent.click(container.querySelector('.diff-list-contents-btn')!);

        await waitFor(() => expect(container.textContent).toContain('report.txt'));
        expect(asked().searchParams.get('ref')).toBe('bbb222');
    });
});

// A commit's diff has one side worth listing — the commit itself — and it is the same one the picture of a
// changed image is read from.
describe('the commit overlay', () => {
    it('lists the archive as the commit holds it', async () => {
        stubListing();
        const { container } = openCommit([diffFile('a/dist/bundle.tar.gz')]);
        await fireEvent.click(container.querySelector('.diff-list-contents-btn')!);

        await waitFor(() => expect(container.textContent).toContain('report.txt'));
        const q = asked().searchParams;
        expect([q.get('path'), q.get('ref')]).toEqual(['dist/bundle.tar.gz', 'ccc333']);
    });

    it('offers nothing for an archive the commit deleted', () => {
        const { container } = openCommit([diffFile('a/dist/bundle.tar.gz', { isDeleted: true })]);
        expect(container.querySelectorAll('.diff-list-contents-btn')).toHaveLength(0);
    });
});
