// @vitest-environment jsdom
// Which text a diff row paints. A diff has one gutter, so a removed row's number names a line of the old
// file while every other row's names the new one — look HTML up by that number and a row paints another line.
// `$lib/highlighter` is mocked to echo each line back, so that shows in the DOM.
import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';
import DiffViewer from '$lib/components/DiffViewer.svelte';
import type { DiffFile, DiffRow } from '$lib/types/diff';

vi.mock('svelte/transition', () => ({ slide: vi.fn(), fade: vi.fn() }));

/** Each line list the component asked the highlighter to draw. */
const probe = vi.hoisted(() => ({ asked: [] as string[][] }));

vi.mock('$lib/highlighter', () => ({
    highlightLinesAsync: vi.fn(async (lines: string[]) => {
        probe.asked.push(lines);
        return lines.map(l => `<span class="hl">${l}</span>`);
    }),
}));

function row(lineNumber: number | null, variant: DiffRow['variant'], text: string): DiffRow {
    return { lineNumber, variant, text };
}

function diffFile(path: string, rows: DiffRow[], extra: Partial<DiffFile> = {}): DiffFile {
    return {
        path,
        rows,
        added: rows.filter(r => r.variant === 'added').length,
        removed: rows.filter(r => r.variant === 'removed').length,
        ...extra,
    };
}

/** A four-line comment rewritten as one, so removed numbers collide with context numbers. */
function collapsedCommentRows(): DiffRow[] {
    return [
        row(30, 'context', 'factoryRegistry = build()'),
        row(31, 'context', ''),
        row(32, 'removed', '// RegisterAdapterFactory overrides how the adapter is produced. A test seam,'),
        row(33, 'removed', '// not a configuration path: nothing in the server calls it,'),
        row(34, 'removed', '// and a real extension adapter is'),
        row(35, 'removed', '// always its own WASM module.'),
        row(31, 'added', '// RegisterAdapterFactory overrides the adapter a name maps to. A test seam.'),
        row(32, 'context', 'func RegisterAdapterFactory(name string, fn Factory) {'),
        row(33, 'context', 'adapterRegistry.Lock()'),
        row(34, 'context', 'defer adapterRegistry.Unlock()'),
        row(35, 'context', 'adapterRegistry.factories[name] = fn'),
        row(36, 'context', '}'),
    ];
}

function viewer(props: Record<string, unknown>) {
    return render(DiffViewer, {
        props: { stagedFiles: [], unstagedFiles: [], stagedUntracked: [], unstagedUntracked: [], ...props },
    });
}

/** The drawn rows' text, with the +/- marker dropped. */
function paintedRows(container: HTMLElement): string[] {
    return [...container.querySelectorAll<HTMLElement>('.diff-line-content-wrapper .diff-row')]
        .map(el => (el.textContent ?? '').replace(/^[+-]/, '').trim());
}

/** The mock wraps every line it draws in a `.hl` span. */
function colored(container: HTMLElement): boolean {
    return container.querySelector('.hl') !== null;
}

/** Collapse a file and open it again. The view reads its collapsed-file record out of IndexedDB when it
 *  mounts and writes that over what it holds, so these waits keep each click clear of the arrival. */
async function collapseAndReopen(container: HTMLElement, index = 0): Promise<void> {
    await new Promise(resolve => setTimeout(resolve, 0));
    const header = container.querySelectorAll<HTMLButtonElement>('.diff-file-header')[index];
    await fireEvent.click(header);
    await waitFor(() => expect(container.querySelectorAll('.diff-row')).toHaveLength(0));
    await fireEvent.click(header);
    await waitFor(() => expect(container.querySelectorAll('.diff-row').length).toBeGreaterThan(0));
}

function texts(rows: DiffRow[]): string[] {
    return rows.map(r => (r.text ?? '').trim());
}

afterEach(() => {
    cleanup();
    probe.asked.length = 0;
});

describe('the text a diff row paints', () => {
    it('paints each row of a collapsed comment its own line, not the context line sharing its number', async () => {
        const rows = collapsedCommentRows();
        const { container } = viewer({ unstagedFiles: [diffFile('backend/agent/loader.go', rows)] });

        await waitFor(() => expect(paintedRows(container)).toEqual(texts(rows)));

        // A collapse and reopen runs the highlight pass over the file again; the rows keep their own text.
        await collapseAndReopen(container);
        expect(paintedRows(container)).toEqual(texts(rows));
    });

    it('paints each row of a pure deletion its own line', async () => {
        const rows = [
            row(9, 'context', 'func keep() {'),
            row(10, 'removed', 'gone.first()'),
            row(11, 'removed', 'gone.second()'),
            row(12, 'removed', 'gone.third()'),
            row(10, 'context', 'kept()'),
            row(11, 'context', 'returns()'),
        ];
        const { container } = viewer({ stagedFiles: [diffFile('a.go', rows)] });

        await waitFor(() => expect(paintedRows(container)).toEqual(texts(rows)));
    });
});

describe('when a file gets its syntax colors', () => {
    it('colors a file that is open when the panel first paints, with no collapse/expand round trip', async () => {
        const rows = collapsedCommentRows();
        const { container } = viewer({ unstagedFiles: [diffFile('backend/agent/loader.go', rows)] });

        await waitFor(() => expect(colored(container)).toBe(true));
        expect(probe.asked.join('\n')).toContain('adapterRegistry.Lock()');
    });

    it('colors the committed section from the rows it draws', async () => {
        const committed = [
            row(1, 'context', 'committed context'),
            row(2, 'removed', 'committed removal'),
            row(1, 'added', 'committed addition'),
        ];
        const { container } = viewer({
            committedFiles: [diffFile('c.go', [row(1, 'context', 'working tree row')], { committedRows: committed })],
        });

        await waitFor(() => expect(colored(container)).toBe(true));
        expect(paintedRows(container)).toEqual(texts(committed));
        expect(probe.asked.join('\n')).toContain('committed removal');
    });
});
