// @vitest-environment jsdom
// Which binary files get a "View image" button, driven through the real components. A .zip and a
// deleted .png are both "Binary file" in the panel, and neither has a picture to show.
import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';
import DiffViewer from '$lib/components/DiffViewer.svelte';
import CommitDiffOverlay from '$lib/components/CommitDiffOverlay.svelte';
import type { DiffFile } from '$lib/types/diff';
import type { GitCommit } from '$lib/types';

vi.mock('svelte/transition', () => ({ slide: vi.fn(), fade: vi.fn() }));

const source = { project: 'p1', task: 't1', working: { old: 'HEAD' } };

function diffFile(path: string, extra: Partial<DiffFile> = {}): DiffFile {
    return { path, rows: [], added: 0, removed: 0, isBinary: true, fileSize: 2048, ...extra };
}

function openViewer(unstagedFiles: DiffFile[], props: Record<string, unknown> = {}) {
    return render(DiffViewer, {
        props: { stagedFiles: [], unstagedFiles, imageSource: source, ...props },
    });
}

afterEach(cleanup);

describe('the binary notice', () => {
    it('offers the picture for an image and nothing else', () => {
        const { container } = openViewer([
            diffFile('docs/logo.png'),
            diffFile('vendor/archive.zip'),
            diffFile('gone.png', { isDeleted: true }),
        ]);
        const buttons = [...container.querySelectorAll<HTMLButtonElement>('.diff-view-image-btn')];
        expect(buttons).toHaveLength(1);
        expect(buttons[0].textContent).toBe('View image');
    });

    it('offers nothing when the host has not said whose project this is', () => {
        const { container } = openViewer([diffFile('docs/logo.png')], { imageSource: undefined });
        expect(container.querySelectorAll('.diff-view-image-btn')).toHaveLength(0);
    });

    it('offers nothing when the image itself is not expanded, like any other file body', async () => {
        const { container } = openViewer([diffFile('docs/logo.png')]);
        fireEvent.click(container.querySelector('.diff-file-header')!);
        await waitFor(() => expect(container.querySelectorAll('.diff-view-image-btn')).toHaveLength(0));
        expect(container.textContent).not.toContain('View image');
    });
});

describe('the modal the button opens', () => {
    function images(container: HTMLElement): HTMLImageElement[] {
        return [...container.querySelectorAll<HTMLImageElement>('img')];
    }

    it('shows both sides of a changed image — the old one from the comparison’s other end', async () => {
        const { container } = openViewer([diffFile('docs/logo.png')]);
        await fireEvent.click(container.querySelector('.diff-view-image-btn')!);

        const imgs = images(container);
        expect(imgs).toHaveLength(2);
        const [oldImg, newImg] = imgs;
        expect(new URL(oldImg.getAttribute('src')!, 'http://x').searchParams.get('ref')).toBe('HEAD');
        expect(new URL(newImg.getAttribute('src')!, 'http://x').searchParams.has('ref')).toBe(false);
        for (const img of imgs) {
            const src = new URL(img.getAttribute('src')!, 'http://x');
            expect(src.pathname).toBe('/api/fs/raw');
            expect(src.searchParams.get('project')).toBe('p1');
            expect(src.searchParams.get('task')).toBe('t1');
            expect(src.searchParams.get('path')).toBe('docs/logo.png');
        }
    });

    it('shows one side for an image the change created', async () => {
        const { container } = openViewer([diffFile('docs/new.png', { isNewFile: true })]);
        await fireEvent.click(container.querySelector('.diff-view-image-btn')!);
        expect(images(container)).toHaveLength(1);
    });

    it('reads the committed section from the refs the host named for it', async () => {
        const { container } = openViewer([], {
            committedFiles: [diffFile('docs/logo.png')],
            imageSource: { project: 'p1', task: 't1', working: { old: 'HEAD' }, committed: { old: 'aaa111', new: 'bbb222' } },
        });
        await fireEvent.click(container.querySelector('.diff-view-image-btn')!);

        const srcs = images(container).map((i) => new URL(i.getAttribute('src')!, 'http://x').searchParams.get('ref'));
        expect(srcs).toEqual(['aaa111', 'bbb222']);
    });

    it('closes again', async () => {
        const { container } = openViewer([diffFile('docs/logo.png')]);
        await fireEvent.click(container.querySelector('.diff-view-image-btn')!);
        expect(images(container)).toHaveLength(2);
        await fireEvent.click(container.querySelector('.close-btn')!);
        expect(images(container)).toHaveLength(0);
    });
});

// The commit overlay takes its two sides from the commit and its parent.
describe('the commit overlay', () => {
    const commit: GitCommit = { hash: 'ccc333', short: 'ccc333', subject: 's', author: 'a', date: '2026-01-01', parents: ['bbb222'] };

    function openCommit(files: DiffFile[]) {
        return render(CommitDiffOverlay, {
            props: { commit, files, loading: false, projectId: 'p1', taskId: 't1', onClose: () => {}, onNavigate: () => {}, onFileSelected: () => {}, hasPrev: false, hasNext: false },
        });
    }

    it('reads the changed image between the commit and its parent', async () => {
        const { container } = openCommit([diffFile('a/docs/logo.png')]);
        await fireEvent.click(container.querySelector('.diff-view-image-btn')!);

        const srcs = [...container.querySelectorAll('img')].map((i) => new URL(i.getAttribute('src')!, 'http://x'));
        expect(srcs).toHaveLength(2);
        expect(srcs.map((u) => u.searchParams.get('ref'))).toEqual(['bbb222', 'ccc333']);
        // The a/ b/ prefixes are git's patch notation, not part of the path.
        for (const u of srcs) expect(u.searchParams.get('path')).toBe('docs/logo.png');
    });

    it('shows one side for an image the commit created', async () => {
        const { container } = openCommit([diffFile('a/new.png', { isNewFile: true })]);
        await fireEvent.click(container.querySelector('.diff-view-image-btn')!);
        const srcs = [...container.querySelectorAll('img')].map((i) => new URL(i.getAttribute('src')!, 'http://x'));
        expect(srcs).toHaveLength(1);
        expect(srcs[0].searchParams.get('ref')).toBe('ccc333');
    });

    it('offers nothing for an image the commit deleted', () => {
        const { container } = openCommit([diffFile('a/gone.png', { isDeleted: true })]);
        expect(container.querySelectorAll('.diff-view-image-btn')).toHaveLength(0);
    });
});
