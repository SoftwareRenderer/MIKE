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
        const mounted = mountGutterEditor({ title: 'Create a task from these lines' });
        cleanup = mounted.cleanup;
        expect(mounted.rows.length).toBeGreaterThan(0);
        for (const row of mounted.rows) {
            expect(row.getAttribute('title')).toBe('Create a task from these lines');
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

describe('scrollbarMarkerRect (fallback: 1 doc line = 1 visual row)', () => {
    it('positions the first line at the top band center', () => {
        const { y, h } = scrollbarMarkerRect(1, 10, 100);
        expect(y).toBeCloseTo(5, 5); // (1 - 0.5) / 10 * 100
        expect(h).toBeCloseTo(9.5, 5); // 100/10 - 0.5
    });

    it('positions a middle line at its band center', () => {
        const { y } = scrollbarMarkerRect(5, 10, 100);
        expect(y).toBeCloseTo(45, 5); // 4.5 / 10 * 100
    });

    it('positions the last line at the bottom band center', () => {
        const { y } = scrollbarMarkerRect(10, 10, 100);
        expect(y).toBeCloseTo(95, 5); // 9.5 / 10 * 100
    });

    it('handles a single-line document', () => {
        const { y, h } = scrollbarMarkerRect(1, 1, 100);
        expect(y).toBeCloseTo(50, 5);
        expect(h).toBeCloseTo(99.5, 5);
    });

    it('keeps bands at least 1px tall for large documents', () => {
        const { h } = scrollbarMarkerRect(500, 10000, 600);
        expect(h).toBe(1); // 600/10000 - 0.5 < 1 → clamped
    });

    it('places boundary (removed) marks at the gap edge', () => {
        const { y } = scrollbarMarkerRect(2, 10, 100, undefined, true);
        expect(y).toBeCloseTo(20, 5); // 2 / 10 * 100
    });

    it('clamps a boundary mark on the last line inside the canvas', () => {
        const { y } = scrollbarMarkerRect(10, 10, 100, undefined, true);
        expect(y).toBe(99); // 10/10*100 = 100 → clamped to height - 1
    });
});

describe('scrollbarMarkerRect (layout-aware: wraps + folds)', () => {
    const canvasHeight = 100;

    it('uses the measured block center when layout is provided', () => {
        const { y, h } = scrollbarMarkerRect(1, 10, canvasHeight, {
            top: 20,
            height: 10,
            contentHeight: 200,
        });
        expect(y).toBeCloseTo(12.5, 5); // (20 + 5) / 200 * 100
        expect(h).toBeCloseTo(4.5, 5); // 10/200*100 - 0.5
    });

    it('maps a wrapped line (taller block) to its real position', () => {
        // A line wrapped over 3 visual rows: its block is 3x the default height.
        const { y, h } = scrollbarMarkerRect(5, 100, canvasHeight, {
            top: 400,
            height: 30,
            contentHeight: 1000,
        });
        expect(y).toBeCloseTo(41.5, 5); // (400 + 15) / 1000 * 100
        expect(h).toBeCloseTo(2.5, 5); // 30/1000*100 - 0.5
    });

    it('accounts for folded regions by using collapsed block positions', () => {
        // A line after a fold: its block.top already reflects the collapsed height.
        const { y } = scrollbarMarkerRect(900, 1000, canvasHeight, {
            top: 500,
            height: 10,
            contentHeight: 600,
        });
        expect(y).toBeCloseTo(84.1666, 3); // (500 + 5) / 600 * 100
    });

    it('places boundary (removed) marks at the block bottom edge', () => {
        const { y } = scrollbarMarkerRect(2, 10, canvasHeight, {
            top: 20,
            height: 10,
            contentHeight: 200,
        }, true);
        expect(y).toBeCloseTo(15, 5); // (20 + 10) / 200 * 100
    });

    it('clamps a boundary mark on the last block inside the canvas', () => {
        const { y } = scrollbarMarkerRect(100, 100, canvasHeight, {
            top: 990,
            height: 10,
            contentHeight: 1000,
        }, true);
        expect(y).toBe(99); // (990 + 10) / 1000 * 100 = 100 → clamped
    });
});

describe('hasVerticalScrollbar', () => {
    it('returns true when content overflows the viewport', () => {
        expect(hasVerticalScrollbar(1000, 500)).toBe(true);
        expect(hasVerticalScrollbar(501, 500)).toBe(true);
    });

    it('returns false when content fits exactly', () => {
        expect(hasVerticalScrollbar(500, 500)).toBe(false);
    });

    it('returns false when content is smaller than the viewport', () => {
        expect(hasVerticalScrollbar(499, 500)).toBe(false);
        expect(hasVerticalScrollbar(0, 0)).toBe(false);
    });
});
