import { describe, it, expect } from 'vitest';
import { EditorState } from '@codemirror/state';
import { css } from '@codemirror/lang-css';
import { html } from '@codemirror/lang-html';
import { colorRangeAt, colorRanges, formatHexColor, hasStyleBlock, parseHexColor, treeColorRanges } from '../editor-color';

const stateOf = (doc: string, grammar: 'css' | 'html') =>
    EditorState.create({ doc, extensions: [grammar === 'css' ? css() : html()] });

/** Offsets of every color a document reports, as the text itself. */
function tokens(doc: string, grammar: 'css' | 'html'): string[] {
    const state = stateOf(doc, grammar);
    return treeColorRanges(state, 0, doc.length).map((r) => doc.slice(r.from, r.to));
}

/** Colors a stream grammar's document reports, with the mode dispatch standing in for it. */
function regionTokens(doc: string, from = 0, to = doc.length): string[] {
    const state = EditorState.create({ doc });
    return colorRanges(state, 'style-block', from, to).map((r) => doc.slice(r.from, r.to));
}

describe('parseHexColor', () => {
    it('reads every hex spelling with its alpha digits verbatim', () => {
        expect(parseHexColor('#fff')).toMatchObject({ r: 255, g: 255, b: 255, value: '#ffffff', alpha: null, shorthand: true });
        expect(parseHexColor('#112233')).toMatchObject({ r: 17, g: 34, b: 51, value: '#112233', shorthand: false });
        expect(parseHexColor('#f00a')).toMatchObject({ value: '#ff0000', alpha: 'a', shorthand: true });
        expect(parseHexColor('#AABBCCdd')).toMatchObject({ value: '#aabbcc', alpha: 'dd', upper: true });
        // Case is the color's own: an upper-case alpha pair does not make the color upper-case.
        expect(parseHexColor('#112233AA')).toMatchObject({ value: '#112233', alpha: 'AA', upper: false });
    });

    it('refuses anything that is not a color', () => {
        for (const text of ['#ff', '#fffff', '#1234567', 'nav', '#', '##fff', '#fffzzz', '', 'g00']) {
            expect(parseHexColor(text), text).toBeNull();
        }
    });
});

describe('formatHexColor', () => {
    const WHITE = { r: 255, g: 255, b: 255 };
    const TEAL = { r: 0x11, g: 0xb0, b: 0xcc };

    it('keeps a shorthand token shorthand when the color fits it', () => {
        expect(formatHexColor('#fff', WHITE)).toBe('#fff');
        expect(formatHexColor('#FFF', { r: 0xaa, g: 0xbb, b: 0xcc })).toBe('#ABC');
    });

    it('widens a shorthand token that cannot hold the color, in the original case', () => {
        expect(formatHexColor('#fff', TEAL)).toBe('#11b0cc');
        expect(formatHexColor('#FFF', TEAL)).toBe('#11B0CC');
    });

    it('carries the alpha digits over untouched', () => {
        expect(formatHexColor('#112233aa', WHITE)).toBe('#ffffffaa');
        expect(formatHexColor('#F00A', { r: 0, g: 255, b: 0 })).toBe('#0F0A');
    });

    it('writes lowercase six digits when it has nothing to learn from', () => {
        expect(formatHexColor('not a color', TEAL)).toBe('#11b0cc');
    });
});

describe('treeColorRanges', () => {
    it('reads colors and nothing else out of a stylesheet', () => {
        expect(tokens('a { color: #f00; background: #11223344 }', 'css')).toEqual(['#f00', '#11223344']);
    });

    it('leaves an id selector and a comment alone', () => {
        expect(tokens('#nav { color: #fff } /* #abc */', 'css')).toEqual(['#fff']);
    });

    it('reads a style attribute as text, not a color', () => {
        expect(tokens('<div style="color: #f00">x</div>', 'html')).toEqual([]);
    });

    it('follows the CSS parser into an html style block', () => {
        expect(tokens('<style>a{color:#f00}</style>', 'html')).toEqual(['#f00']);
    });

    it('reports only the colors inside the requested range', () => {
        const doc = 'a { color: #f00; background: #00f }';
        const state = stateOf(doc, 'css');
        const first = treeColorRanges(state, 0, 20);
        expect(first.map((r) => doc.slice(r.from, r.to))).toEqual(['#f00']);
    });
});

describe('colors inside a <style> region', () => {
    it('reads colors out of a style block and not out of the script beside it', () => {
        const doc = "<script>const c = '#ff0000'</script>\n<style>.a { color: #f0f }</style>";
        expect(regionTokens(doc)).toEqual(['#f0f']);
    });

    it('ignores a color outside any block', () => {
        expect(regionTokens('const c = "#ff0000"')).toEqual([]);
    });

    it('tolerates tags in either case and with attributes', () => {
        expect(regionTokens('<STYLE lang="scss">.a{color:#aaa}</STYLE>')).toEqual(['#aaa']);
    });

    it('keeps decorating a viewport that starts mid-stylesheet', () => {
        const doc = '<style>\n' + 'x'.repeat(200) + '\n.a { color: #f0f }\n</style>';
        const from = doc.indexOf('.a');
        expect(regionTokens(doc, from, doc.length)).toEqual(['#f0f']);
    });

    it('treats an unclosed block as running to the end of the file', () => {
        expect(regionTokens('<style>\n.a{color:#123}\n.b{color:#456}')).toEqual(['#123', '#456']);
    });

    it('walks back across a window boundary to find the block it is inside', () => {
        const doc = '<style>\n' + 'x'.repeat(70_000) + '\n.a { color: #f0f }';
        const from = doc.indexOf('.a');
        expect(regionTokens(doc, from, doc.length)).toEqual(['#f0f']);
    });

    it('reports only the colors inside the requested range', () => {
        const doc = '<style>.a{color:#f00;background:#00f}</style>';
        const second = doc.indexOf('#00f');
        expect(regionTokens(doc, second, doc.length)).toEqual(['#00f']);
    });

    it('stops at a closing tag', () => {
        const doc = '<style>.a{color:#f00}</style>const d = "#00f"';
        expect(regionTokens(doc)).toEqual(['#f00']);
    });
});

describe('hasStyleBlock', () => {
    const doc = (text: string) => EditorState.create({ doc: text }).doc;

    it('answers the one question the region walk is gated on', () => {
        expect(hasStyleBlock(doc('a\n<style>\n.b{color:#f0f}\n</style>'))).toBe(true);
        expect(hasStyleBlock(doc('<STYLE lang="css">'))).toBe(true);
        expect(hasStyleBlock(doc('const a = "#f00"; // <styl'))).toBe(false);
    });
});

describe('colorRangeAt', () => {
    it('finds the token a position lands on, and nothing off it', () => {
        const doc = 'a { color: #f00; }';
        const state = stateOf(doc, 'css');
        const at = doc.indexOf('#f00');
        expect(colorRangeAt(state, 'grammar', at + 2)).toEqual({ from: at, to: at + 4, color: '#f00' });
        expect(colorRangeAt(state, 'grammar', doc.indexOf('color'))).toBeNull();
    });
});
