/**
 * Which runs of text in an open document are hex colors, and what a picked color is written back as — two
 * recognizers, because a tree grammar reports a `ColorLiteral` node and a manifest's stream grammar has no
 * tree to ask; `docs/languages.md` owns the rules both follow.
 */
import { syntaxTree } from '@codemirror/language';
import type { EditorState, Text } from '@codemirror/state';

export interface ColorRange {
    from: number;
    to: number;
    /** The token as the file wrote it, not normalized. */
    color: string;
}

export interface RGB {
    r: number;
    g: number;
    b: number;
}

export interface HexColor extends RGB {
    /** `#rrggbb`, lowercase — what a color input takes. */
    value: string;
    /** The alpha digits as written: one for `#rgba`, two for `#rrggbbaa`. */
    alpha: string | null;
    /** Upper case in the color's own digits; an upper-case alpha pair does not make a color upper case. */
    upper: boolean;
    /** The 3- or 4-digit form. */
    shorthand: boolean;
}

/** Shared by the parser and the scanner, so they cannot disagree about where a run of digits ends. */
const HEX = '[0-9a-fA-F]';

/** The four spellings accepted: `#rgb`, `#rgba`, `#rrggbb`, `#rrggbbaa`. */
const HEX_COLOR = new RegExp(`^#(${HEX}{3,4}|${HEX}{6}|${HEX}{8})$`);

/** A run of hex digits long enough to be a color, with no further hex digit after it. */
function hexScanner(): RegExp {
    return new RegExp(`#${HEX}{3,8}(?!${HEX})`, 'g');
}

/** The tree node name a grammar gives a hex color. */
const COLOR_NODE = 'ColorLiteral';

/** A hex color token, or null for anything else — every color text read out of a file passes through here
 *  before it reaches the DOM. */
export function parseHexColor(text: string): HexColor | null {
    const match = HEX_COLOR.exec(text);
    if (!match) return null;
    const digits = match[1];
    const shorthand = digits.length <= 4;
    const rgb = shorthand ? digits.slice(0, 3) : digits.slice(0, 6);
    const expanded = shorthand ? rgb.replace(/./g, (c) => c + c) : rgb;
    const lower = expanded.toLowerCase();
    return {
        r: parseInt(lower.slice(0, 2), 16),
        g: parseInt(lower.slice(2, 4), 16),
        b: parseInt(lower.slice(4, 6), 16),
        value: '#' + lower,
        alpha: digits.length === rgb.length ? null : digits.slice(rgb.length),
        upper: /[A-F]/.test(rgb),
        shorthand,
    };
}

/** `#rrggbb`, lowercase, from three bytes. */
function digitsOf({ r, g, b }: RGB): string {
    const byte = (n: number) => n.toString(16).padStart(2, '0');
    return byte(r) + byte(g) + byte(b);
}

/** The `#rgb` form of six digits, or null when they cannot be written that way. */
function collapse(digits: string): string | null {
    const pairs = [digits.slice(0, 2), digits.slice(2, 4), digits.slice(4, 6)];
    if (pairs.some((p) => p[0] !== p[1])) return null;
    return pairs.map((p) => p[0]).join('');
}

/**
 * The new color in the old token's clothes: the case kept, the shorthand kept when the color fits it, and
 * the alpha digits kept unless `alpha` brings its own — a color input answers six digits with no alpha.
 */
export function formatHexColor(original: string, rgb: RGB, alpha?: string | null): string {
    const parsed = parseHexColor(original);
    const digits = digitsOf(rgb);
    if (!parsed) return '#' + digits;
    const short = parsed.shorthand ? collapse(digits) : null;
    const body = short ?? digits;
    const kept = alpha === undefined ? parsed.alpha : alpha;
    const token = '#' + body + (kept ?? '');
    return parsed.upper ? token.toUpperCase() : token;
}

/**
 * Colors the grammar reports between `from` and `to`, nested trees included — an HTML `<style>` block.
 *
 * @knipignore Measured on its own, so a grammar that stops reporting colors fails as itself.
 */
export function treeColorRanges(state: EditorState, from: number, to: number): ColorRange[] {
    const ranges: ColorRange[] = [];
    syntaxTree(state).iterate({
        from,
        to,
        enter: (node) => {
            if (node.name === COLOR_NODE) {
                const color = state.sliceDoc(node.from, node.to);
                if (node.from >= from && node.to <= to && parseHexColor(color)) {
                    ranges.push({ from: node.from, to: node.to, color });
                }
            }
            return true;
        },
    });
    return ranges;
}

const STYLE_TAG_SOURCE = '<(/?)style\\b[^<>]*>';

/** The backward walk reads the text in 64KB windows that overlap by 1KB, so a tag split across a window
 *  edge is still read whole, and gives up after 16 windows — the editor's whole 1MB read cap. */
const TAG_WINDOW = 64_000;
const TAG_WINDOW_OVERLAP = 1024;
const TAG_WINDOW_MAX = 16;

/** Whether `pos` lies inside a style region, decided by the nearest tag before it; null when the walk ran
 *  out of document. */
function insideStyleBlock(doc: Text, pos: number): boolean | null {
    const pattern = new RegExp(STYLE_TAG_SOURCE, 'gi');
    for (let window = 0; window < TAG_WINDOW_MAX; window++) {
        const end = pos - window * TAG_WINDOW;
        const start = Math.max(0, end - TAG_WINDOW - (window ? TAG_WINDOW_OVERLAP : 0));
        if (end <= 0) return null;
        const text = doc.sliceString(start, end);
        pattern.lastIndex = 0;
        let last: RegExpExecArray | null = null;
        let found: RegExpExecArray | null;
        while ((found = pattern.exec(text))) last = found;
        if (last) return last[1] !== '/';
        if (start === 0) return null;
    }
    return null;
}

/** Whether the document has a style region anywhere in it — the cheap question that gates the region walk,
 *  read one line at a time so a big file costs no more than its text. */
export function hasStyleBlock(doc: Text): boolean {
    const tag = new RegExp(STYLE_TAG_SOURCE, 'i');
    for (let n = 1; n <= doc.lines; n++) {
        if (tag.test(doc.line(n).text)) return true;
    }
    return false;
}

/**
 * Colors inside `<style>` regions between `from` and `to`, for a document whose grammar builds no tree: tags
 * decide the region, so a `</style>` written inside a string in a script block does end it.
 */
function styleBlockColorRanges(doc: Text, from: number, to: number): ColorRange[] {
    const tags = new RegExp(STYLE_TAG_SOURCE, 'gi');
    const hex = hexScanner();
    const ranges: ColorRange[] = [];
    let inside = insideStyleBlock(doc, from) ?? false;

    let line = doc.lineAt(from);
    for (;;) {
        const text = line.text;
        let region = 0;
        tags.lastIndex = 0;
        let tag: RegExpExecArray | null;
        const emit = (start: number, end: number) => {
            if (!inside) return;
            hex.lastIndex = 0;
            const base = line.from;
            let found: RegExpExecArray | null;
            while ((found = hex.exec(text.slice(start, end)))) {
                const at = base + start + found.index;
                if (at >= from && at + found[0].length <= to && parseHexColor(found[0])) {
                    ranges.push({ from: at, to: at + found[0].length, color: found[0] });
                }
            }
        };
        while ((tag = tags.exec(text))) {
            emit(region, tag.index);
            inside = tag[1] !== '/';
            region = tag.index + tag[0].length;
        }
        emit(region, text.length);
        if (line.to >= to || line.number === doc.lines) break;
        line = doc.line(line.number + 1);
    }
    return ranges;
}

/** How a document's colors are found: from its tree, or from `<style>` tags in a document that has none. */
export type ColorMode = 'grammar' | 'style-block';

/** The ranges of `mode` between `from` and `to`; the `style-block` walk reads back to the tag that opened
 *  its region, so the caller keeps the `hasStyleBlock()` veto. */
export function colorRanges(
    state: EditorState,
    mode: ColorMode,
    from: number,
    to: number,
): ColorRange[] {
    return mode === 'grammar' ? treeColorRanges(state, from, to) : styleBlockColorRanges(state.doc, from, to);
}

/** The color token a position lands on, read over the whole line so a click agrees with what was
 *  underlined even when that pass lagged a parse. */
export function colorRangeAt(state: EditorState, mode: ColorMode, pos: number): ColorRange | null {
    const line = state.doc.lineAt(pos);
    return (
        colorRanges(state, mode, line.from, line.to).find((r) => pos >= r.from && pos <= r.to) ?? null
    );
}
