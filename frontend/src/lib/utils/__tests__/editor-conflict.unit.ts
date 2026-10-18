// @vitest-environment jsdom
// The highlight follows the text: tinting arrives with the marker lines and leaves when a person deletes
// them, so it never remembers a conflict the buffer no longer holds.
import { describe, it, expect } from 'vitest';
import { EditorState } from '@codemirror/state';
import { EditorView } from '@codemirror/view';
import { CONFLICT_OURS, CONFLICT_SEPARATOR, CONFLICT_THEIRS } from '$lib/utils/editor-merge';
import { conflictField, conflictHighlight, nextConflictLine } from '$lib/utils/editor-conflict';

const BLOCK = [CONFLICT_OURS, 'mine', CONFLICT_SEPARATOR, 'theirs', CONFLICT_THEIRS].join('\n');

function viewWith(doc: string): EditorView {
    return new EditorView({ state: EditorState.create({ doc, extensions: conflictHighlight() }) });
}

/** Line numbers (1-based) the highlight covers. */
function tintedLines(view: EditorView): number[] {
    const lines: number[] = [];
    const set = view.state.field(conflictField);
    set.between(0, view.state.doc.length, (from) => {
        lines.push(view.state.doc.lineAt(from).number);
    });
    return [...new Set(lines)].sort((a, b) => a - b);
}

describe('the conflict highlight', () => {
    it('tints every line between the markers and none outside them', () => {
        const view = viewWith(`header\n${BLOCK}\nfooter\n`);
        expect(tintedLines(view)).toEqual([2, 3, 4, 5, 6]);
    });

    it('tints nothing in a buffer that holds no markers', () => {
        expect(tintedLines(viewWith('a\nb\n=======\nc\n'))).toEqual([]);
    });

    it('tints nothing once the person has resolved the block', () => {
        const view = viewWith(`header\n${BLOCK}\nfooter\n`);
        expect(tintedLines(view)).toHaveLength(5);
        view.dispatch({
            changes: { from: view.state.doc.line(1).from, to: view.state.doc.length, insert: 'resolved\n' },
        });
        expect(tintedLines(view)).toEqual([]);
    });

    it('tints each block of a buffer with two', () => {
        const view = viewWith(`${BLOCK}\nkeep\n${BLOCK}\n`);
        expect(tintedLines(view)).toEqual([1, 2, 3, 4, 5, 7, 8, 9, 10, 11]);
    });
});

describe('nextConflictLine', () => {
    const doc = `a\n${BLOCK}\nkeep\n${BLOCK}\n`;

    it('names the first block after the caret', () => {
        expect(nextConflictLine(doc, 1)).toBe(2);
        expect(nextConflictLine(doc, 6)).toBe(8);
    });

    it('wraps to the first block', () => {
        expect(nextConflictLine(doc, 11)).toBe(2);
    });

    it('says nothing when there is nothing to jump to', () => {
        expect(nextConflictLine('a\nb\n', 1)).toBe(0);
    });
});
