import { describe, it, expect, afterEach } from 'vitest';
import { EditorView } from '@codemirror/view';
import { EditorState } from '@codemirror/state';
import { gitGutter, scrollbarMarkerRect, hasVerticalScrollbar } from '../git-gutter';

/** Mount a minimal editor with the git gutter in jsdom and return its gutter rows.
 *  (Extensions must go into EditorState.create — the EditorView config takes no
 *  `extensions` option and would silently ignore them.) */
function mountGutterEditor(options?: { title?: string }): {
    rows: HTMLElement[];
    cleanup: () => void;
} {
    const parent = document.createElement('div');
    document.body.appendChild(parent);
    const { extension } = gitGutter(options);
    const view = new EditorView({
        state: EditorState.create({ doc: 'a\nb\nc', extensions: [extension] }),
        parent,
    });
    const rows = [...parent.querySelectorAll<HTMLElement>('.cm-gutter.cm-gitGutter [data-line]')];
    return { rows, cleanup: () => { view.destroy(); parent.remove(); } };
}

describe('gitGutter title option (gutter hover tooltip)', () => {
    let cleanup: (() => void) | null = null;
    afterEach(() => { cleanup?.(); cleanup = null; });

    it('stamps the tooltip title on every gutter row when title is set', () => {
        const mounted = mountGutterEditor({ title: 'Create a task from selected lines' });
        cleanup = mounted.cleanup;
        expect(mounted.rows.length).toBeGreaterThan(0);
        for (const row of mounted.rows) {
            expect(row.getAttribute('title')).toBe('Create a task from selected lines');
        }
    });

    it('leaves gutter rows without a title when no title is given', () => {
        const mounted = mountGutterEditor();
        cleanup = mounted.cleanup;
        expect(mounted.rows.length).toBeGreaterThan(0);
        for (const row of mounted.rows) {
            expect(row.hasAttribute('title')).toBe(false);
        }
    });
});

describe('gitGutter rows across a document change', () => {
    /** An editor whose gutter is baselined against `baseline`, with the server's markers for it. */
    function mountBaselined(doc: string, baseline: string, markers: Map<number, string[]> | null) {
        const parent = document.createElement('div');
        document.body.appendChild(parent);
        const { extension, state } = gitGutter();
        state.initialMarkers = markers;
        state.baselineContent = baseline;
        const view = new EditorView({
            state: EditorState.create({ doc, extensions: [extension] }),
            parent,
        });
        const row = (line: number) =>
            parent.querySelector<HTMLElement>(`.cm-gutter.cm-gitGutter [data-line="${line}"]`);
        return { view, row, cleanup: () => { view.destroy(); parent.remove(); } };
    }

    let cleanup: (() => void) | null = null;
    afterEach(() => { cleanup?.(); cleanup = null; });

    const doc = ['one', 'two', 'three', 'four'].join('\n');

    it('keeps the rows of lines an external change did not touch', () => {
        const mounted = mountBaselined(doc, doc, new Map([[2, ['modified']]]));
        cleanup = mounted.cleanup;
        const untouched = [1, 2, 3].map(mounted.row);
        expect(untouched.every(Boolean)).toBe(true);

        mounted.view.dispatch({
            changes: { from: mounted.view.state.doc.line(4).from, to: mounted.view.state.doc.line(4).to, insert: 'four edited' },
        });

        // Only a marker that compares equal to the one it replaced keeps that row's DOM.
        untouched.forEach((before, i) => {
            expect(mounted.row(i + 1)).toBe(before);
        });
    });

    it('draws the row of the line the change did touch', () => {
        const mounted = mountBaselined(doc, doc, null);
        cleanup = mounted.cleanup;
        const untouched = mounted.row(1);

        mounted.view.dispatch({
            changes: { from: mounted.view.state.doc.line(3).from, to: mounted.view.state.doc.line(3).to, insert: 'three edited' },
        });

        expect(mounted.row(1)).toBe(untouched);
        expect(mounted.row(3)).not.toBeNull();
        // The changed line now carries an indicator, which a plain row has no place for.
        expect(mounted.row(3)!.querySelector('span[style*="repeating-linear-gradient"], span[style*="clip-path"]')).not.toBeNull();
    });
});

describe('scrollbarMarkerRect', () => {
    /** One marker's band. `layout` is the measured block of its line, absent when the fallback
     * (1 doc line = 1 visual row) has to place it. */
    interface Placement {
        name: string;
        line: number;
        total: number;
        canvas: number;
        layout?: { top: number; height: number; contentHeight: number };
        boundary?: boolean;
        y?: number;
        h?: number;
    }

    // The one decision: a marker covers its line's block, scaled to the content the canvas draws,
    // and a boundary mark sits at that block's bottom edge. Measured block or estimated row is the
    // only thing that changes between the rows.
    const placements: Placement[] = [
        // Estimated row: the band center of (line - 0.5) / total, one band = canvas / total.
        { name: 'the first line at the top band center', line: 1, total: 10, canvas: 100, y: (1 - 0.5) / 10 * 100, h: 100 / 10 - 0.5 },
        { name: 'a middle line at its band center', line: 5, total: 10, canvas: 100, y: (5 - 0.5) / 10 * 100 },
        { name: 'the last line at the bottom band center', line: 10, total: 10, canvas: 100, y: (10 - 0.5) / 10 * 100 },
        { name: 'a single-line document', line: 1, total: 1, canvas: 100, y: (1 - 0.5) / 1 * 100, h: 100 / 1 - 0.5 },
        // 600 / 10000 - 0.5 is under a pixel, and a marker nobody can see is a marker that lies about
        // the file having changes in it.
        { name: 'a band no thinner than 1px in a large document', line: 500, total: 10000, canvas: 600, h: 1 },
        {
            name: 'the measured block center when layout is provided',
            line: 1, total: 10, canvas: 100, layout: { top: 20, height: 10, contentHeight: 200 },
            y: (20 + 5) / 200 * 100, h: 10 / 200 * 100 - 0.5,
        },
        {
            // A line wrapped over 3 visual rows: its block is 3x the default height.
            name: 'a wrapped line (taller block) at its real position',
            line: 5, total: 100, canvas: 100, layout: { top: 400, height: 30, contentHeight: 1000 },
            y: (400 + 15) / 1000 * 100, h: 30 / 1000 * 100 - 0.5,
        },
        {
            // A line after a fold: block.top already reflects the collapsed height.
            name: 'a folded-away region accounted for by the collapsed block positions',
            line: 900, total: 1000, canvas: 100, layout: { top: 500, height: 10, contentHeight: 600 }, y: (500 + 5) / 600 * 100,
        },
        {
            name: 'a boundary mark at the block bottom edge',
            line: 2, total: 10, canvas: 100, layout: { top: 20, height: 10, contentHeight: 200 }, boundary: true, y: (20 + 10) / 200 * 100,
        },
        {
            name: 'a boundary mark at the gap edge',
            line: 2, total: 10, canvas: 100, boundary: true, y: 2 / 10 * 100,
        },
    ];

    it.each(placements)('places $name', ({ line, total, canvas, layout, boundary, y, h }) => {
        const band = scrollbarMarkerRect(line, total, canvas, layout, boundary);
        if (y !== undefined) expect(band.y).toBeCloseTo(y, 5);
        if (h !== undefined) expect(band.h).toBeCloseTo(h, 5);
    });

    it('clamps a boundary mark on the last line inside the canvas', () => {
        // Estimated row: 10/10*100 = 100. Measured block: (990 + 10)/1000*100 = 100. Both must land
        // at height - 1 rather than be drawn outside the canvas.
        expect(scrollbarMarkerRect(10, 10, 100, undefined, true).y).toBe(99);
        expect(
            scrollbarMarkerRect(100, 100, 100, { top: 990, height: 10, contentHeight: 1000 }, true).y,
        ).toBe(99);
    });
});

describe('hasVerticalScrollbar', () => {
    // One decision: the scrollbar exists when the content is strictly taller than the viewport.
    it.each([
        ['content overflows', 1000, 500, true],
        ['content overflows by a pixel', 501, 500, true],
        ['content fits exactly', 500, 500, false],
        ['content is smaller than the viewport', 499, 500, false],
        ['an unmeasured pane', 0, 0, false],
    ])('%s', (_name, scrollHeight, clientHeight, expected) => {
        expect(hasVerticalScrollbar(scrollHeight, clientHeight)).toBe(expected);
    });
});
