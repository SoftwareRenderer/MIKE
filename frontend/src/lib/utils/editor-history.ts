import { redoDepth, undoDepth } from '@codemirror/commands';
import type { EditorState } from '@codemirror/state';
import type { EditorDelta } from '$lib/storage';

/**
 * An editor's undo stack as its chrome sees it: whether each direction has something, and how to ask.
 * The commands ride along because a surface like the Explorer's tab bar decorates an editor it holds no
 * handle on, so the owning editor reports what it can do rather than being addressed over a channel
 * every mounted editor is listening to.
 */
export interface EditorHistory {
    canUndo: boolean;
    canRedo: boolean;
    undo: () => void;
    redo: () => void;
}

/** Which directions this state's undo stack can move in; without the history extension it has none. */
export function historyDepths(state: EditorState): { canUndo: boolean; canRedo: boolean } {
    return { canUndo: undoDepth(state) > 0, canRedo: redoDepth(state) > 0 };
}

/**
 * The buffer a persisted undo chain claims: its base with every delta applied in order. Null when a delta falls
 * outside the text it was recorded against — building a document from the wrong bytes is worse than a fresh undo
 * stack.
 */
export function replayDeltas(base: string, deltas: EditorDelta[]): string | null {
    let doc = base;
    for (const delta of deltas) {
        if (delta.from < 0 || delta.to < delta.from || delta.to > doc.length) return null;
        doc = doc.slice(0, delta.from) + delta.insert + doc.slice(delta.to);
    }
    return doc;
}
