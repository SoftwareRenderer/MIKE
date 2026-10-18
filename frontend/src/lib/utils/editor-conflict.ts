/** The text is the only record of a conflict: a person resolves it by deleting lines, and the highlight follows. */
import { EditorState, StateField, type Extension } from '@codemirror/state';
import { Decoration, EditorView, type DecorationSet } from '@codemirror/view';
import { completeBlocks, conflictBlocks, CONFLICT_OURS, markerOf, type MarkerKind } from './editor-merge';

const blockLine = Decoration.line({ class: 'cm-conflict-line' });
const markerLine = Decoration.line({ class: 'cm-conflict-marker' });

/** One range per line of every block; `Decoration.set` is told they are in document order. */
function blockDecorations(state: EditorState): DecorationSet {
    const marks: (MarkerKind | null)[] = [];
    for (const line of state.doc.iterLines()) marks.push(markerOf(line));
    const set = [];
    for (const block of completeBlocks(marks)) {
        for (let n = block.startLine; n <= block.endLine; n++) {
            const line = state.doc.line(Math.min(n, state.doc.lines));
            set.push(blockLine.range(line.from));
            if (markerOf(line.text)) set.push(markerLine.range(line.from));
        }
    }
    return Decoration.set(set, true);
}

/** @knipignore The highlight test reads the set this field paints rather than the DOM it reaches. */
export const conflictField = StateField.define<DecorationSet>({
    create: (state) => blockDecorations(state),
    update: (value, tr) => (tr.docChanged ? blockDecorations(tr.state) : value),
    provide: (field) => EditorView.decorations.from(field),
});

/** Palette tokens only, so a `css` extension that retints the app moves the warning with it. */
const conflictTheme = EditorView.baseTheme({
    '.cm-conflict-line': { background: 'var(--color-status-red-bg)' },
    '.cm-conflict-marker': { color: 'var(--color-status-red)', fontWeight: '700' },
});

/** Everything the editor needs to show conflict blocks in its buffer. */
export function conflictHighlight(): Extension[] {
    return [conflictField, conflictTheme];
}

export function countConflicts(text: string): number {
    // A block cannot exist without its opening marker, so a buffer with none pays no line scan.
    return text.includes(CONFLICT_OURS) ? conflictBlocks(text).length : 0;
}

/** The next block's first line after `afterLine`, or 0. Wraps, so "next" at the last block gives the first. */
export function nextConflictLine(text: string, afterLine: number): number {
    const blocks = conflictBlocks(text);
    const ahead = blocks.find(b => b.startLine > afterLine);
    return (ahead ?? blocks[0])?.startLine ?? 0;
}
