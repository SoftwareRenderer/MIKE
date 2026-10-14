/**
 * The editor's half of the color picker: which tokens are underlined and clickable, and what a click on one
 * reports — it never writes to the document, and `FileEditor/ColorPicker.svelte` is the card a click opens.
 */
import { Decoration, EditorView, ViewPlugin, type DecorationSet, type ViewUpdate } from '@codemirror/view';
import { RangeSetBuilder, type EditorState, type Extension } from '@codemirror/state';
import { colorRangeAt, colorRanges, hasStyleBlock, parseHexColor, type ColorMode, type ColorRange } from './editor-color';

/** The color the pointer chose, and the box its first line sits in, in viewport coordinates. */
export interface ColorTarget {
    from: number;
    to: number;
    /** The token as the file wrote it. */
    color: string;
    left: number;
    top: number;
    bottom: number;
}

/** How the editor reaches the card; `onClose()` may fire when the card was never open. */
export interface ColorPickerHandlers {
    mode: ColorMode;
    onPick: (target: ColorTarget) => void;
    onClose: () => void;
}

/** A press further than this from its release was a drag or a selection, not a click on a value. */
const CLICK_TOLERANCE = 4;

/** Each value is underlined in its own color, which `markFor()` sets per token. */
const colorValueTheme = EditorView.baseTheme({
    '.cm-color-value': {
        textDecoration: 'underline',
        textDecorationThickness: '2px',
        textUnderlineOffset: '2px',
        cursor: 'pointer',
    },
});

function markFor(value: string): Decoration {
    return Decoration.mark({ class: 'cm-color-value', attributes: { style: `text-decoration-color: ${value}` } });
}

/**
 * One mark per color token, its underline taken from the parsed color rather than the file's text.
 *
 * @knipignore The plugin's measure step; tests read its ranges and underline directly.
 */
export function colorDecorations(state: EditorState, mode: ColorMode, from: number, to: number): DecorationSet {
    const builder = new RangeSetBuilder<Decoration>();
    // The builder needs the ranges in order, and neither recognizer promises them in order.
    const ranges = colorRanges(state, mode, from, to).sort((a, b) => a.from - b.from);
    for (const range of ranges) {
        const parsed = parseHexColor(range.color);
        if (parsed) builder.add(range.from, range.to, markFor(parsed.value));
    }
    return builder.finish();
}

/** The box a range occupies; its two ends can sit on different lines once the line wraps. */
function boxOf(view: EditorView, range: ColorRange): Pick<ColorTarget, 'left' | 'top' | 'bottom'> | null {
    const start = view.coordsAtPos(range.from);
    const end = view.coordsAtPos(Math.max(range.from, range.to - 1), -1);
    if (!start || !end) return null;
    return {
        left: Math.min(start.left, end.left),
        top: Math.min(start.top, end.top),
        bottom: Math.max(start.bottom, end.bottom),
    };
}

/**
 * The target a position lands on, or null when the text there is not a color.
 *
 * @knipignore The click handler's decision, measured with the box stubbed — jsdom has no layout.
 */
export function colorTargetAt(view: EditorView, mode: ColorMode, pos: number): ColorTarget | null {
    const range = colorRangeAt(view.state, mode, pos);
    if (!range) return null;
    // No box means the token is off the screen: nothing to hang a card on.
    const box = boxOf(view, range);
    return box ? { ...box, from: range.from, to: range.to, color: range.color } : null;
}

const modifierPressed = (event: MouseEvent) =>
    event.ctrlKey || event.metaKey || event.altKey || event.shiftKey;

export function colorPicker(handlers: ColorPickerHandlers): Extension[] {
    let press: { x: number; y: number } | null = null;

    /**
     * Whether a `style-block` walk can find anything here, read once per document version — a file with no
     * `<style>` tag would otherwise read backwards over its whole text to learn that it found nothing.
     */
    let styleBlock = true;
    const reread = (view: EditorView) => {
        styleBlock = handlers.mode !== 'style-block' || hasStyleBlock(view.state.doc);
    };

    const marks = ViewPlugin.fromClass(
        class {
            decorations: DecorationSet;

            constructor(view: EditorView) {
                reread(view);
                this.decorations = this.measure(view);
            }

            update(update: ViewUpdate) {
                if (update.docChanged) reread(update.view);
                if (!update.docChanged && !update.viewportChanged) return;
                this.decorations = this.measure(update.view);
                handlers.onClose();
            }

            private measure(view: EditorView): DecorationSet {
                if (!styleBlock) return Decoration.none;
                return colorDecorations(view.state, handlers.mode, view.viewport.from, view.viewport.to);
            }
        },
        { decorations: (value) => value.decorations },
    );

    const clicks = EditorView.domEventHandlers({
        mousedown(event: MouseEvent) {
            press =
                event.button === 0 && !modifierPressed(event)
                    ? { x: event.clientX, y: event.clientY }
                    : null;
            return false;
        },
        click(event: MouseEvent, view: EditorView) {
            const start = press;
            press = null;
            // A second click selects a word and a drag selects text, so neither opens the card; the click is
            // never cancelled, so the caret still lands where it was put.
            if (!start || event.detail > 1 || modifierPressed(event)) return false;
            if (Math.abs(event.clientX - start.x) + Math.abs(event.clientY - start.y) > CLICK_TOLERANCE) {
                return false;
            }
            if (!styleBlock) return false;
            // The `false` asks for the nearest position, so a click between two glyphs names one of them.
            const pos = view.posAtCoords({ x: event.clientX, y: event.clientY }, false);
            const target = colorTargetAt(view, handlers.mode, pos);
            if (target) handlers.onPick(target);
            return false;
        },
    });

    return [marks, clicks, colorValueTheme];
}
