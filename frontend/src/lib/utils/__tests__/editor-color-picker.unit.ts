import { describe, it, expect, afterEach, vi } from 'vitest';
import { EditorState } from '@codemirror/state';
import { EditorView, type DecorationSet } from '@codemirror/view';
import { css } from '@codemirror/lang-css';
import { colorDecorations, colorPicker, colorTargetAt, type ColorTarget } from '../editor-color-picker';
import type { ColorMode } from '../editor-color';

const DOC = 'a { color: #f00; background: #11223344 } #nav { color: #fff }';

function mount(doc: string, mode: ColorMode) {
    const parent = document.createElement('div');
    document.body.appendChild(parent);
    const onPick = vi.fn();
    const onClose = vi.fn();
    const view = new EditorView({
        state: EditorState.create({
            doc,
            extensions: [...(mode === 'grammar' ? [css()] : []), ...colorPicker({ mode, onPick, onClose })],
        }),
        parent,
    });
    const destroy = () => {
        view.destroy();
        parent.remove();
    };
    return { view, parent, onPick, onClose, destroy };
}

/** jsdom ships no layout: a Range has no boxes, and CodeMirror measures one while handling the very mouse
 *  event under test, so the e2e spec is what measures the real boxes. */
Object.assign(Range.prototype, {
    getClientRects: () => [],
    getBoundingClientRect: () => ({ x: 0, y: 0, width: 0, height: 0, top: 0, left: 0, right: 0, bottom: 0 }),
});

/** Where a pointer landed and where a token is, which are the two layout questions a jsdom test answers. */
function measure(view: EditorView, pos: number) {
    const hit = vi.spyOn(view, 'posAtCoords').mockReturnValue(pos);
    const box = vi.spyOn(view, 'coordsAtPos').mockImplementation((at) => ({
        left: at,
        right: at + 8,
        top: 4,
        bottom: 20,
    }));
    return { x: pos, y: 4, restore: () => { hit.mockRestore(); box.mockRestore(); } };
}

function press(view: EditorView, at: { x: number; y: number }, init: MouseEventInit = {}) {
    view.contentDOM.dispatchEvent(
        new MouseEvent('mousedown', { bubbles: true, button: 0, clientX: at.x, clientY: at.y, ...init }),
    );
}

function release(view: EditorView, at: { x: number; y: number }, init: MouseEventInit = {}) {
    view.contentDOM.dispatchEvent(
        new MouseEvent('click', { bubbles: true, button: 0, detail: 1, clientX: at.x, clientY: at.y, ...init }),
    );
}

/** The click a reader means: press and release on the same pixel, no modifier, one click. */
function click(view: EditorView, at: { x: number; y: number }, init: MouseEventInit = {}) {
    press(view, at, init);
    release(view, at, init);
}

/** What a decoration set covers, as the text under each range. */
function covered(set: DecorationSet, doc: string): string[] {
    const out: string[] = [];
    set.between(0, doc.length, (from, to) => { out.push(doc.slice(from, to)); });
    return out;
}

let cleanup: (() => void) | null = null;
afterEach(() => {
    cleanup?.();
    cleanup = null;
});

describe('colorDecorations', () => {
    it('underlines each color with the color it spells', () => {
        const state = EditorState.create({ doc: DOC, extensions: [css()] });
        const set = colorDecorations(state, 'grammar', 0, DOC.length);
        const marks: { text: string; style: string }[] = [];
        set.between(0, DOC.length, (from, to, mark) => {
            marks.push({ text: DOC.slice(from, to), style: (mark.spec.attributes as Record<string, string>).style });
        });
        expect(marks).toEqual([
            { text: '#f00', style: 'text-decoration-color: #ff0000' },
            { text: '#11223344', style: 'text-decoration-color: #112233' },
            { text: '#fff', style: 'text-decoration-color: #ffffff' },
        ]);
    });

    it('covers a viewport window, not the whole document', () => {
        const state = EditorState.create({ doc: DOC, extensions: [css()] });
        const window = colorDecorations(state, 'grammar', 0, DOC.indexOf('#11223344'));
        expect(covered(window, DOC)).toEqual(['#f00']);
    });

    it('marks a style block in a document whose grammar builds no tree', () => {
        const doc = '<style>.a { color: #f0f }</style>';
        const state = EditorState.create({ doc });
        const set = colorDecorations(state, 'style-block', 0, doc.length);
        expect(covered(set, doc)).toEqual(['#f0f']);
    });

});

describe('the underline in the DOM', () => {
    it('puts the class on the token, and on nothing else', () => {
        const mounted = mount(DOC, 'grammar');
        cleanup = mounted.destroy;
        const marked = [...mounted.parent.querySelectorAll('.cm-color-value')].map((el) => el.textContent);
        expect(marked).toEqual(['#f00', '#11223344', '#fff']);
    });

    it('marks the style block of a document whose grammar builds no tree', () => {
        const mounted = mount('<style>.a { color: #f0f }</style>', 'style-block');
        cleanup = mounted.destroy;
        expect([...mounted.parent.querySelectorAll('.cm-color-value')].map((el) => el.textContent)).toEqual(['#f0f']);
    });

    it('underlines nothing, and clicks nothing, in a tree-less file with no style region', () => {
        const doc = 'const bg = "#ff0000"; // #00f too';
        const mounted = mount(doc, 'style-block');
        const laid = measure(mounted.view, doc.indexOf('#ff0000') + 2);
        cleanup = () => { laid.restore(); mounted.destroy(); };
        expect(mounted.parent.querySelectorAll('.cm-color-value')).toHaveLength(0);
        click(mounted.view, { x: laid.x, y: laid.y });
        expect(mounted.onPick).not.toHaveBeenCalled();
    });
});

describe('colorTargetAt', () => {
    it('names the token a position lands on, including its right edge', () => {
        const mounted = mount(DOC, 'grammar');
        cleanup = mounted.destroy;
        const at = DOC.indexOf('#f00');
        const laid = measure(mounted.view, at + 2);
        expect(colorTargetAt(mounted.view, 'grammar', at + 2)).toMatchObject({ from: at, to: at + 4, color: '#f00' });
        expect(colorTargetAt(mounted.view, 'grammar', at + 4)).toMatchObject({ color: '#f00' });
        expect(colorTargetAt(mounted.view, 'grammar', DOC.indexOf('color'))).toBeNull();
        laid.restore();
    });

    it('has no target where the token has no box', () => {
        const mounted = mount(DOC, 'grammar');
        cleanup = mounted.destroy;
        const offscreen = vi.spyOn(mounted.view, 'coordsAtPos').mockReturnValue(null);
        expect(colorTargetAt(mounted.view, 'grammar', DOC.indexOf('#f00') + 2)).toBeNull();
        offscreen.mockRestore();
    });
});

describe('a click on a color', () => {
    it('opens the picker on the token, where the token is', () => {
        const mounted = mount(DOC, 'grammar');
        const at = DOC.indexOf('#f00');
        const laid = measure(mounted.view, at + 2);
        cleanup = () => { laid.restore(); mounted.destroy(); };

        click(mounted.view, { x: laid.x, y: laid.y });
        const target: ColorTarget | undefined = mounted.onPick.mock.calls[0]?.[0];
        expect(target).toMatchObject({ from: at, to: at + 4, color: '#f00', left: at, bottom: 20 });
    });

    it('leaves text that is not a color alone', () => {
        const mounted = mount(DOC, 'grammar');
        const laid = measure(mounted.view, DOC.indexOf('color'));
        cleanup = () => { laid.restore(); mounted.destroy(); };

        click(mounted.view, { x: laid.x, y: laid.y });
        expect(mounted.onPick).not.toHaveBeenCalled();
    });

    it('treats a shift-click as a selection', () => {
        const mounted = mount(DOC, 'grammar');
        const laid = measure(mounted.view, DOC.indexOf('#f00') + 2);
        cleanup = () => { laid.restore(); mounted.destroy(); };

        click(mounted.view, { x: laid.x, y: laid.y }, { shiftKey: true });
        expect(mounted.onPick).not.toHaveBeenCalled();
    });

    it('treats a press far from its release as a drag', () => {
        const mounted = mount(DOC, 'grammar');
        const laid = measure(mounted.view, DOC.indexOf('#f00') + 2);
        cleanup = () => { laid.restore(); mounted.destroy(); };

        press(mounted.view, { x: 0, y: 0 });
        release(mounted.view, { x: 40, y: 4 });
        expect(mounted.onPick).not.toHaveBeenCalled();
    });

    it('treats the second click of a double-click as a word selection', () => {
        const mounted = mount(DOC, 'grammar');
        const laid = measure(mounted.view, DOC.indexOf('#f00') + 2);
        cleanup = () => { laid.restore(); mounted.destroy(); };

        press(mounted.view, { x: laid.x, y: laid.y });
        click(mounted.view, { x: laid.x, y: laid.y }, { detail: 2 });
        expect(mounted.onPick).not.toHaveBeenCalled();
    });
});

describe('what closes the picker', () => {
    it('reports a close when the document or the viewport moves under it', () => {
        const mounted = mount(DOC, 'grammar');
        cleanup = mounted.destroy;
        expect(mounted.onClose).not.toHaveBeenCalled();
        mounted.view.dispatch({ changes: { from: 0, to: 1, insert: 'b' } });
        expect(mounted.onClose).toHaveBeenCalled();
    });
});
