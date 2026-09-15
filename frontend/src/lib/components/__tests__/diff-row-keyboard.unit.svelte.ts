// @vitest-environment jsdom
// Which diff rows are a jump-to-line control, driven through the real component. A row that answers
// the mouse must answer the keyboard too, in the tracked and the untracked rendering alike, and its
// hover must light the gutter slot it actually sits in.
import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';
import DiffViewer from '$lib/components/DiffViewer.svelte';
import type { DiffFile, DiffRow } from '$lib/types/diff';

vi.mock('svelte/transition', () => ({ slide: vi.fn(), fade: vi.fn() }));

function row(lineNumber: number | null, variant: DiffRow['variant'], text: string): DiffRow {
    return { lineNumber, variant, text };
}

function diffFile(path: string, rows: DiffRow[]): DiffFile {
    return {
        path,
        rows,
        added: rows.filter(r => r.variant === 'added').length,
        removed: rows.filter(r => r.variant === 'removed').length,
    };
}

const changed: DiffRow[] = [row(1, 'added', 'one'), row(2, 'removed', 'two')];

/** 14 context lines between two changes: 8 of them are out of every change's radius, so they
 *  arrive as one collapsible block, with 3 kept on each side. */
const betweenChanges: DiffRow[] = Array.from({ length: 14 }, (_, i) => row(10 + i, 'context', `ctx ${i}`));

function tracked(props: Record<string, unknown> = {}) {
    return viewer({ stagedFiles: [diffFile('a.ts', changed)], ...props });
}

function untracked(props: Record<string, unknown> = {}) {
    return viewer({ unstagedUntracked: [{ path: 'new.ts', file: diffFile('new.ts', changed) }], ...props });
}

function viewer(props: Record<string, unknown>) {
    return render(DiffViewer, {
        props: { stagedFiles: [], unstagedFiles: [], stagedUntracked: [], unstagedUntracked: [], ...props },
    });
}

/** The one file whose middle collapses: gutter slots 0-3, the block at 4, the tail from 13. */
function collapsingFile(props: Record<string, unknown> = {}) {
    const rows = [row(1, 'added', 'one'), ...betweenChanges, row(99, 'added', 'last')];
    return viewer({ unstagedFiles: [diffFile('b.ts', rows)], ...props });
}

function gutterRows(container: HTMLElement): HTMLElement[] {
    return [...container.querySelectorAll<HTMLElement>('.diff-gutter-row')];
}

function litSlot(container: HTMLElement): number {
    return gutterRows(container).findIndex(el => el.classList.contains('diff-gutter-hover'));
}

afterEach(cleanup);

describe('a changed row as a control', () => {
    it('jumps to the line from Enter and from Space, tracked and untracked alike', async () => {
        for (const [open, path] of [[tracked, 'a.ts'], [untracked, 'new.ts']] as const) {
            const onLineClick = vi.fn();
            const { container } = open({ onLineClick });
            const rows = [...container.querySelectorAll<HTMLElement>('.diff-row-clickable')];
            expect(rows).toHaveLength(2);

            await fireEvent.keyDown(rows[0], { key: 'Enter' });
            expect(onLineClick).toHaveBeenLastCalledWith(path, 1);
            onLineClick.mockClear();

            // Space has to preventDefault or it scrolls the diff column instead of jumping.
            const space = new KeyboardEvent('keydown', { key: ' ', bubbles: true, cancelable: true });
            rows[1].dispatchEvent(space);
            await waitFor(() => expect(onLineClick).toHaveBeenCalledTimes(1));
            expect(onLineClick.mock.calls[0]).toEqual([path, 2]);
            expect(space.defaultPrevented).toBe(true);
        }
    });

    it('is the only row with a role and a place in the focus order', () => {
        const { container } = tracked({ onLineClick: vi.fn() });
        const rows = [...container.querySelectorAll<HTMLElement>('.diff-row')];
        expect(rows).toHaveLength(2);
        for (const el of rows) {
            expect(el.getAttribute('role')).toBe('button');
            expect(el.getAttribute('tabindex')).toBe('0');
        }
    });

    it('takes no role and no tabindex when nothing jumps anywhere', () => {
        for (const open of [tracked, untracked]) {
            const { container } = open({ onLineClick: undefined });
            const rows = [...container.querySelectorAll<HTMLElement>('.diff-row')];
            expect(rows.length).toBeGreaterThan(0);
            for (const el of rows) {
                expect(el.hasAttribute('role')).toBe(false);
                expect(el.hasAttribute('tabindex')).toBe(false);
            }
        }
    });
});

describe('hovering a row', () => {
    it('lights the gutter slot of a plain row', async () => {
        const { container } = collapsingFile({ onLineClick: vi.fn() });
        await fireEvent.mouseEnter(container.querySelectorAll<HTMLElement>('.diff-row')[2]);
        await waitFor(() => expect(litSlot(container)).toBe(2));
    });

    it('lights the block’s own slot for the collapse control', async () => {
        const { container } = collapsingFile({ onLineClick: vi.fn() });
        await fireEvent.mouseEnter(container.querySelector<HTMLButtonElement>('.diff-collapse-btn')!);
        await waitFor(() => expect(litSlot(container)).toBe(4));
    });

    it('lights the slot an expanded context row was inserted at, not the block’s own', async () => {
        const { container } = collapsingFile({ onLineClick: vi.fn() });
        const before = gutterRows(container).length;
        await fireEvent.click(container.querySelector<HTMLButtonElement>('.diff-collapse-btn')!);
        await waitFor(() => expect(gutterRows(container)).toHaveLength(before + 8));

        // The kept context lines come first, so the block's own first row is the fourth. The gutter
        // wears the same variant class, so only the line column counts.
        const blockFirst = container.querySelectorAll<HTMLElement>('.diff-line-content-wrapper .diff-row-context')[3];
        // The block's first row is the fourth context row of the file, and only context lines are
        // ever collapsed — so it carries no +/- prefix of its own.
        expect((blockFirst.textContent ?? '').trim()).toBe('ctx 3');
        expect(blockFirst.querySelector('.diff-prefix')?.textContent).not.toMatch(/[+-]/);
        await fireEvent.mouseEnter(blockFirst);
        await waitFor(() => expect(litSlot(container)).toBe(5));
    });
});
