// @vitest-environment jsdom
// Which half of a file the Merged section paints. A file whose rows arrived from `/diff/file` has no
// committed half at all; the panel used to read an empty `committedRows` as one and paint "Empty file"
// over the rows it was holding, for every file of a done task's diff.
import { describe, it, expect, afterEach, vi } from 'vitest';
import { render, cleanup } from '@testing-library/svelte';
import DiffViewer from '$lib/components/DiffViewer.svelte';
import type { DiffFile, DiffRow } from '$lib/types/diff';

vi.mock('svelte/transition', () => ({ slide: vi.fn(), fade: vi.fn() }));

afterEach(cleanup);

function rows(...text: string[]): DiffRow[] {
    return text.map((t, i) => ({ lineNumber: i + 1, variant: 'added', text: t }));
}

function mergedText(container: HTMLElement): string {
    return [...container.querySelectorAll('.diff-line-content')].map(e => e.textContent).join(' ');
}

function openMerged(committedFiles: DiffFile[]) {
    return render(DiffViewer, { props: { committedFiles, stagedFiles: [], unstagedFiles: [] } });
}

function file(path: string, extra: Partial<DiffFile> = {}): DiffFile {
    return { path, rows: rows('the line on screen'), added: 1, removed: 0, ...extra };
}

describe('the Merged section', () => {
    it('paints the rows a file holds when it has no committed half', () => {
        const { container } = openMerged([file('a.ts')]);
        expect(container.querySelector('.diff-empty-file-notice')).toBeNull();
        expect(mergedText(container)).toContain('the line on screen');
    });

    it('paints the rows a file holds when its committed half came back with no rows', () => {
        const { container } = openMerged([file('a.ts', { committedRows: [] })]);
        expect(container.querySelector('.diff-empty-file-notice')).toBeNull();
        expect(mergedText(container)).toContain('the line on screen');
    });

    it('paints the committed half when the file has one', () => {
        const { container } = openMerged([file('a.ts', { committedRows: rows('what was committed') })]);
        expect(mergedText(container)).toContain('what was committed');
        expect(mergedText(container)).not.toContain('the line on screen');
    });
});
