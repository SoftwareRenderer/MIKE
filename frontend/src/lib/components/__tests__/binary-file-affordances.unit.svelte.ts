// @vitest-environment jsdom
// The two controls a binary row can wear, and the bytes each one reads. A `.zip` and a deleted `.png` are
// both "Binary file" in the panel, and neither has a picture or a listing to show; which side of the
// comparison the control reads is the host's decision, so every one of these reads the request back.
import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';
import DiffViewer from '$lib/components/DiffViewer.svelte';
import CommitDiffOverlay from '$lib/components/CommitDiffOverlay.svelte';
import { setCsrfToken } from '$lib/api';
import type { DiffFile } from '$lib/types/diff';
import type { GitCommit } from '$lib/types';

vi.mock('svelte/transition', () => ({ slide: vi.fn(), fade: vi.fn() }));

const source = { project: 'p1', task: 't1', working: { old: 'HEAD' }, committed: { old: 'aaa111', new: 'bbb222' } };

function diffFile(path: string, extra: Partial<DiffFile> = {}): DiffFile {
    return { path, rows: [], added: 0, removed: 0, isBinary: true, fileSize: 4096, ...extra };
}

/** One row of each kind the panel must tell apart. */
function everyKind(prefix = ''): DiffFile[] {
    return [
        diffFile(`${prefix}docs/logo.png`),
        diffFile(`${prefix}vendor/bundle.tar.gz`),
        diffFile(`${prefix}gone.png`, { isDeleted: true }),
        diffFile(`${prefix}vendor/old.zip`, { isDeleted: true }),
        diffFile(`${prefix}notes.txt`, { isBinary: false, rows: [] }),
    ];
}

function openViewer(props: Record<string, unknown> = {}) {
    return render(DiffViewer, {
        props: { stagedFiles: [], unstagedFiles: [], imageSource: source, ...props },
    });
}

const commit: GitCommit = { hash: 'ccc333', short: 'ccc333', subject: 's', author: 'a', date: '2026-01-01', parents: ['bbb222'] };

function openCommit(files: DiffFile[]) {
    return render(CommitDiffOverlay, {
        props: { commit, files, loading: false, projectId: 'p1', taskId: 't1', onClose: () => {}, onNavigate: () => {}, onFileSelected: () => {}, hasPrev: false, hasNext: false },
    });
}

const pictureButtons = (container: HTMLElement) => [...container.querySelectorAll<HTMLElement>('.diff-view-image-btn')];
const listingButtons = (container: HTMLElement) => [...container.querySelectorAll<HTMLElement>('.diff-list-contents-btn')];

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

/** Every picture the open modal shows, as the address it reads. */
function images(container: HTMLElement): URL[] {
    return [...container.querySelectorAll<HTMLImageElement>('img')]
        .map((i) => new URL(i.getAttribute('src')!, 'http://x'));
}

afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
});

describe('the control a binary row wears', () => {
    it('gives the picture to the image and the list to the archive, and nothing else', () => {
        const { container } = openViewer({ unstagedFiles: everyKind() });
        expect(pictureButtons(container).map((b) => b.textContent)).toEqual(['View image']);
        expect(listingButtons(container).map((b) => b.textContent)).toEqual(['List contents']);
    });

    it('offers the picture to a binary row the server gave no size for', () => {
        // A tracked binary in a comparison with no annotations arrives as git's marker row and no `file_size`.
        const marker = { lineNumber: null, variant: 'context' as const, text: 'Binary files a/docs/logo.png and b/docs/logo.png differ' };
        const { container } = openViewer({
            unstagedFiles: [diffFile('docs/logo.png', { fileSize: undefined, rows: [marker] })],
        });

        expect(pictureButtons(container).map((b) => b.textContent)).toEqual(['View image']);
        expect(container.querySelector('.diff-binary-notice')?.textContent).toContain('Binary file');
        expect(container.textContent).not.toContain('Binary files a/');
    });

    it('gives the commit overlay the same pair, on the same rule', () => {
        const { container } = openCommit(everyKind('a/'));
        expect(pictureButtons(container).map((b) => b.textContent)).toEqual(['View image']);
        expect(listingButtons(container).map((b) => b.textContent)).toEqual(['List contents']);
    });

    it('offers neither control when the host has not said whose project this is', () => {
        const { container } = openViewer({
            unstagedFiles: [diffFile('docs/logo.png'), diffFile('vendor/bundle.tar.gz')],
            imageSource: undefined,
        });
        expect(pictureButtons(container)).toHaveLength(0);
        expect(listingButtons(container)).toHaveLength(0);
    });

    it('offers nothing while the file body is collapsed, like any other body', async () => {
        const { container } = openViewer({
            unstagedFiles: [diffFile('docs/logo.png'), diffFile('vendor/bundle.tar.gz')],
        });
        for (const header of container.querySelectorAll('.diff-file-header')) {
            fireEvent.click(header);
        }
        await waitFor(() => {
            expect(pictureButtons(container)).toHaveLength(0);
            expect(listingButtons(container)).toHaveLength(0);
        });
        expect(container.textContent).not.toContain('View image');
    });
});

describe('the picture the image button opens', () => {
    it('shows both sides of a changed image — the old one from the comparison’s other end', async () => {
        const { container } = openViewer({ unstagedFiles: [diffFile('docs/logo.png')] });
        await fireEvent.click(container.querySelector('.diff-view-image-btn')!);

        const srcs = images(container);
        expect(srcs).toHaveLength(2);
        const [oldImg, newImg] = srcs;
        expect(oldImg.searchParams.get('ref')).toBe('HEAD');
        expect(newImg.searchParams.has('ref')).toBe(false);
        for (const src of [oldImg, newImg]) {
            expect(src.pathname).toBe('/api/fs/raw');
            expect(src.searchParams.get('project')).toBe('p1');
            expect(src.searchParams.get('task')).toBe('t1');
            expect(src.searchParams.get('path')).toBe('docs/logo.png');
        }
    });

    it('shows one side for an image the change created', async () => {
        const { container } = openViewer({ unstagedFiles: [diffFile('docs/new.png', { isNewFile: true })] });
        await fireEvent.click(container.querySelector('.diff-view-image-btn')!);
        expect(images(container)).toHaveLength(1);
    });

    it('reads the committed section from the refs the host named for it', async () => {
        const { container } = openViewer({
            committedFiles: [diffFile('docs/logo.png')],
            imageSource: source,
        });
        await fireEvent.click(container.querySelector('.diff-view-image-btn')!);
        expect(images(container).map((u) => u.searchParams.get('ref'))).toEqual(['aaa111', 'bbb222']);
    });

    it('closes again', async () => {
        const { container } = openViewer({ unstagedFiles: [diffFile('docs/logo.png')] });
        await fireEvent.click(container.querySelector('.diff-view-image-btn')!);
        expect(images(container)).toHaveLength(2);
        await fireEvent.click(container.querySelector('.close-btn')!);
        expect(images(container)).toHaveLength(0);
    });

    it('reads the changed image between a commit and its parent', async () => {
        const { container } = openCommit([diffFile('a/docs/logo.png')]);
        await fireEvent.click(container.querySelector('.diff-view-image-btn')!);

        const srcs = images(container);
        expect(srcs).toHaveLength(2);
        expect(srcs.map((u) => u.searchParams.get('ref'))).toEqual(['bbb222', 'ccc333']);
        // The a/ b/ prefixes are git's patch notation, not part of the path.
        for (const u of srcs) expect(u.searchParams.get('path')).toBe('docs/logo.png');
    });

    it('shows one side for an image a commit created', async () => {
        const { container } = openCommit([diffFile('a/new.png', { isNewFile: true })]);
        await fireEvent.click(container.querySelector('.diff-view-image-btn')!);
        const srcs = images(container);
        expect(srcs).toHaveLength(1);
        expect(srcs[0].searchParams.get('ref')).toBe('ccc333');
    });
});

describe('the listing the archive button opens', () => {
    it('reads the working tree for the unstaged section', async () => {
        stubListing();
        const { container } = openViewer({ unstagedFiles: [diffFile('dist/bundle.tar.gz')] });
        await fireEvent.click(container.querySelector('.diff-list-contents-btn')!);

        await waitFor(() => expect(container.textContent).toContain('report.txt'));
        const q = asked().searchParams;
        expect([q.get('project'), q.get('task'), q.get('path'), q.get('ref')]).toEqual(['p1', 't1', 'dist/bundle.tar.gz', null]);
    });

    it('reads the committed side from the ref the host named for it', async () => {
        stubListing();
        const { container } = openViewer({ committedFiles: [diffFile('dist/bundle.tar.gz')] });
        await fireEvent.click(container.querySelector('.diff-list-contents-btn')!);

        await waitFor(() => expect(container.textContent).toContain('report.txt'));
        expect(asked().searchParams.get('ref')).toBe('bbb222');
    });

    it('lists the archive as the commit holds it', async () => {
        stubListing();
        const { container } = openCommit([diffFile('a/dist/bundle.tar.gz')]);
        await fireEvent.click(container.querySelector('.diff-list-contents-btn')!);

        await waitFor(() => expect(container.textContent).toContain('report.txt'));
        const q = asked().searchParams;
        expect([q.get('path'), q.get('ref')]).toEqual(['dist/bundle.tar.gz', 'ccc333']);
    });
});
