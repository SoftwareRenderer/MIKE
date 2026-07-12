import type { EditorView } from '@codemirror/view';

export function useCursorPosition() {
    let cursorLine = 1;
    let cursorCol = 1;

    function handleUpdate(update: EditorView.Update) {
        if (update.selectionSet) {
            const pos = update.state.selection.main.head;
            const line = update.state.doc.lineAt(pos);
            cursorLine = line.number;
            cursorCol = pos - line.from + 1;
        }
    }

    return {
        get cursorLine() { return cursorLine; },
        get cursorCol() { return cursorCol; },
        handleUpdate,
        reset() { cursorLine = 1; cursorCol = 1; },
    };
}
