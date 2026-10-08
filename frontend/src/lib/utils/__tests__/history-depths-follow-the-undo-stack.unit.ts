import { describe, it, expect } from 'vitest';
import { EditorState } from '@codemirror/state';
import { history, redo, undo } from '@codemirror/commands';
import { historyDepths } from '../editor-history';

/**
 * The buttons dim from `historyDepths` read off the editor's own state, so this walks a real undo stack
 * rather than a table of expected booleans. No editor is mounted: the undo and redo commands only ask
 * their target for a state and a dispatch.
 */

function withHistory(doc = 'alpha'): EditorState {
    return EditorState.create({ doc, extensions: [history()] });
}

function typed(state: EditorState, text: string): EditorState {
    return state.update({ changes: { from: 0, insert: text } }).state;
}

/** One step, dispatched into this state the way a view dispatches it. */
function step(state: EditorState, direction: 'undo' | 'redo'): EditorState {
    let next = state;
    const command = direction === 'undo' ? undo : redo;
    command({ state, dispatch: (tr) => { next = state.update(tr).state; return true; } });
    return next;
}

describe('historyDepths', () => {
    it('finds nothing to do in a document nobody has typed in', () => {
        expect(historyDepths(withHistory())).toEqual({ canUndo: false, canRedo: false });
    });

    it('finds something to undo, and nothing to redo, after a change', () => {
        const state = typed(withHistory(), 'x');
        expect(historyDepths(state)).toEqual({ canUndo: true, canRedo: false });
    });

    it('finds something to redo once the change is undone, and nothing left to undo', () => {
        const undone = step(typed(withHistory(), 'x'), 'undo');
        expect(undone.doc.toString()).toBe('alpha');
        expect(historyDepths(undone)).toEqual({ canUndo: false, canRedo: true });
    });

    it('returns to undoable, and no longer redoable, after the redo', () => {
        const redone = step(step(typed(withHistory(), 'x'), 'undo'), 'redo');
        expect(redone.doc.toString()).toBe('xalpha');
        expect(historyDepths(redone)).toEqual({ canUndo: true, canRedo: false });
    });

    it('finds no stack at all in a state built without the history extension', () => {
        const state = EditorState.create({ doc: 'alpha' });
        expect(historyDepths(typed(state, 'x'))).toEqual({ canUndo: false, canRedo: false });
    });
});
