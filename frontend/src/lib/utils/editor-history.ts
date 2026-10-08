import { redoDepth, undoDepth } from '@codemirror/commands';
import type { EditorState } from '@codemirror/state';

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
