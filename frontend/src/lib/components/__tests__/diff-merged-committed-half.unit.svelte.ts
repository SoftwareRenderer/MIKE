// @vitest-environment jsdom
// These tests cover which half of a file the Merged section paints, and the lines it counts there. The two
// halves are different comparisons, and an empty `committedRows` is a committed half with nothing in it.
import { describe, it, expect, afterEach, vi } from 'vitest';
import { render, cleanup } from '@testing-library/svelte';
import DiffViewer from '$lib/components/DiffViewer.svelte';
import { DiffCache } from '$lib/services/diff-cache.svelte';
import type { DiffFile, DiffRow } from '$lib/types/diff';

vi.mock('svelte/transition', () => ({ slide: vi.fn(), fade: vi.fn() }));

afterEach(cleanup);

function rows(...text: string[]): DiffRow[] {
    return text.map((t, i) => ({ lineNumber: i + 1, variant: 'added', text: t }));
}

function mixed(rows: Array<[DiffRow['variant'], string]>): DiffRow[] {
    return rows.map(([variant, text], i) => ({ lineNumber: i + 1, variant, text }));
}

function mergedText(container: HTMLElement): string {
    return [...container.querySelectorAll('.diff-line-content')].map(e => e.textContent).join(' ');
}

/** This reads the `+N -M` pair one element prints. */
function lineStats(el: Element | null | undefined): string {
    const added = el?.querySelector('.diff-added')?.textContent ?? '<none>';
    const removed = el?.querySelector('.diff-removed')?.textContent ?? '<none>';
    return `${added} ${removed}`;
}

/** This reads one rendered section: the total its header prints, and the numbers beside its files. */
function section(container: HTMLElement, kind: 'merged' | 'staged' | 'unstaged') {
    const header = container.querySelector(`.diff-section-header.diff-section-${kind}`);
    const sec = header?.closest('.diff-section');
    if (!sec) return undefined;
    return {
        total: lineStats(header?.querySelector('.diff-section-stats')),
        files: [...sec.querySelectorAll('.diff-file-header')].map(lineStats),
    };
}

function openMerged(committedFiles: DiffFile[]) {
    return render(DiffViewer, { props: { committedFiles, stagedFiles: [], unstagedFiles: [] } });
}

function file(path: string, extra: Partial<DiffFile> = {}): DiffFile {
    return { path, rows: rows('the line on screen'), added: 1, removed: 0, ...extra };
}

/** This worktree half has five added and four removed lines. */
const worktreeHalf = mixed([['added', 'w1'], ['added', 'w2'], ['added', 'w3'], ['added', 'w4'], ['added', 'w5'],
    ['removed', 'o1'], ['removed', 'o2'], ['removed', 'o3'], ['removed', 'o4']]);

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

    // A file merged in from a subtask takes no part in the worktree comparison, so its `added`/`removed`
    // are zero. Counting those would make this section say +0 -0 over the lines it is painting.
    it('says how many lines the committed half of a merged file changed', () => {
        const merged = file('child.go', {
            added: 0, removed: 0,
            committedRows: mixed([['added', 'one'], ['added', 'two'], ['removed', 'gone']]),
        });
        const { container } = openMerged([merged]);

        expect(section(container, 'merged')?.files).toEqual(['+2 -1']);
        expect(section(container, 'merged')?.total).toBe('+2 -1');
    });

    // The parent edited this file too, so it is drawn in both sections. Each half is counted by its own
    // rows, never by the other half's.
    it('counts each half of a file by its own rows', () => {
        const both = file('shared.go', {
            rows: worktreeHalf,
            added: 5, removed: 4,
            committedRows: mixed([['added', 'c1'], ['added', 'c2']]),
        });
        const { container } = render(DiffViewer, { props: { committedFiles: [both], stagedFiles: [], unstagedFiles: [both] } });

        expect(section(container, 'merged')?.files).toEqual(['+2 -0']);
        expect(section(container, 'merged')?.total).toBe('+2 -0');
        expect(section(container, 'unstaged')?.files).toEqual(['+5 -4']);
        expect(section(container, 'unstaged')?.total).toBe('+5 -4');
    });

    // A parent whose subtasks did the work: the merge broadcasts the parent's diff stats, which re-reads
    // every listed file. Re-reading must not move the numbers of the half this section draws.
    it('says how many lines a merged file changed after the diff stats update', async () => {
        const getDiffFile = vi.fn(async () => ({
            rows: worktreeHalf,
            truncated: false,
            committedRows: mixed([['added', 'one'], ['added', 'two'], ['added', 'three']]),
        }));
        const cache = new DiffCache({
            getTaskDiff: vi.fn(async () => ({
                files: { 'shared.go': { hash: 'h1', type: 'tracked', added: 5, removed: 4 } },
                short_stat: '',
                base_ref: 'base',
            })),
            getDiffFile,
            stageTaskPaths: vi.fn(async () => ({ status: 'ok', action: 'staged', applied: [] as string[], failed: [] })),
            enrichRows: (rows) => rows as DiffRow[],
        }, () => 'p', () => 't');
        await cache.loadDiff(() => {});
        cache.processStats({
            hash: 'after-merge', files: 1, additions: 5, deletions: 4,
            file_hashes: { 'shared.go': 'h2' },
        }, false);
        await vi.waitFor(() => expect(getDiffFile).toHaveBeenCalledTimes(2));

        const committedFiles = Object.values(cache.state.files).filter(f => f.committedRows && f.committedRows.length > 0);
        const { container } = openMerged(committedFiles);
        expect(section(container, 'merged')?.files).toEqual(['+3 -0']);
        expect(section(container, 'merged')?.total).toBe('+3 -0');
    });
});
