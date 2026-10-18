import { describe, it, expect } from 'vitest';
import {
    CONFLICT_OURS,
    adoptDraft,
    CONFLICT_SEPARATOR,
    CONFLICT_THEIRS,
    conflictBlocks,
    mergeTextWithMarkers,
    minimalReplace,
    reconcileExternalChange,
} from '$lib/utils/editor-merge';

/** The three lines a two-sided region is written with, in order. */
const block = (ours: string, theirs: string) =>
    [CONFLICT_OURS, ours, CONFLICT_SEPARATOR, theirs, CONFLICT_THEIRS].join('\n');

describe('reconcileExternalChange', () => {
    it('returns no-op when disk is unchanged since the editor last knew it', () => {
        expect(reconcileExternalChange('a\nb\nc', 'a\nX\nc', 'a\nb\nc')).toEqual({ kind: 'no-op' });
    });

    it('returns reload (adopt disk) when there are no unsaved edits', () => {
        expect(reconcileExternalChange('a\nb\nc', 'a\nb\nc', 'a\nb\nNEW')).toEqual({
            kind: 'reload',
            content: 'a\nb\nNEW',
        });
    });

    it('merges non-overlapping external and user edits', () => {
        const r = reconcileExternalChange('a\nb\nc\nd\n', 'a\nb\nUSER\nd\n', 'EXTERNAL\nb\nc\nd\n');
        expect(r).toEqual({ kind: 'merged', content: 'EXTERNAL\nb\nUSER\nd\n' });
    });

    it('carries both sides of a region both sides edited', () => {
        const r = reconcileExternalChange('a\nb\nc\nd\n', 'a\nb\nUSER\nd\n', 'a\nb\nEXTERNAL\nd\n');
        expect(r).toEqual({
            kind: 'conflict',
            content: `a\nb\n${block('USER', 'EXTERNAL')}\nd\n`,
        });
    });
});

describe('adoptDraft', () => {
    const DRAFT = { text: 'a\nX\nc\n', base: 'a\nb\nc\n' };

    it('paints the file when no draft was left', () => {
        expect(adoptDraft(null, 'a\nb\nc\n')).toEqual({ content: 'a\nb\nc\n', conflicted: false });
    });

    it('paints the draft when the file has not moved since', () => {
        expect(adoptDraft(DRAFT, 'a\nb\nc\n')).toEqual({ content: 'a\nX\nc\n', conflicted: false });
    });

    it('merges the draft into a file that moved elsewhere', () => {
        // The draft rewrote line 2, the file rewrote line 3: both stand, and nothing is said.
        expect(adoptDraft(DRAFT, 'a\nb\nC3\n')).toEqual({ content: 'a\nX\nC3\n', conflicted: false });
    });

    it('paints both sides when the draft and the file moved over the same line', () => {
        const a = adoptDraft(DRAFT, 'a\nOTHER\nc\n');
        expect(a.conflicted).toBe(true);
        expect(a.content).toContain('X');
        expect(a.content).toContain('OTHER');
    });

    it('keeps a draft whose base was never recorded, whatever the file says now', () => {
        const legacy = { text: 'typed and never saved', base: null };
        expect(adoptDraft(legacy, 'rewritten by someone else')).toEqual({
            content: 'typed and never saved',
            conflicted: false,
        });
    });
});

describe('mergeTextWithMarkers', () => {
    it('keeps both sides of a region both sides changed', () => {
        const m = mergeTextWithMarkers('a\nb\nc\nd\n', 'a\nB1\nB2\nd\n', 'a\nC1\nd\n');
        expect(m.conflicts).toBe(1);
        expect(m.text).toBe(`a\n${block('B1\nB2', 'C1')}\nd\n`);
    });

    it('writes both sides of a line one side deleted and the other rewrote', () => {
        const m = mergeTextWithMarkers('a\nb\nc\n', 'a\nc\n', 'a\nB\nc\n');
        expect(m.conflicts).toBe(1);
        expect(m.text).toBe(`a\n${[CONFLICT_OURS, CONFLICT_SEPARATOR, 'B', CONFLICT_THEIRS].join('\n')}\nc\n`);
    });

    it('counts one conflict per region and merges the rest silently', () => {
        const m = mergeTextWithMarkers('a\nb\nc\nd\n', 'A\nb\nc\nD\n', 'A2\nb\nc\nD2\n');
        expect(m.conflicts).toBe(2);
        expect(conflictBlocks(m.text)).toHaveLength(2);
    });

    it('writes no marker over a merge that is clean', () => {
        expect(mergeTextWithMarkers('a\nb\nc\nd\n', 'a\nb\nUSER\nd\n', 'EXTERNAL\nb\nc\nd\n'))
            .toEqual({ text: 'EXTERNAL\nb\nUSER\nd\n', conflicts: 0 });
    });

    it('is the base when neither side changed anything', () => {
        expect(mergeTextWithMarkers('a\nb\nc\n', 'a\nb\nc\n', 'a\nb\nc\n'))
            .toEqual({ text: 'a\nb\nc\n', conflicts: 0 });
    });

    it('is the other side when one side changed nothing', () => {
        expect(mergeTextWithMarkers('a\nb\nc\n', 'a\nb\nc\n', 'a\nb\nNEW\n').text).toBe('a\nb\nNEW\n');
    });

    it('merges two insertions at different boundaries', () => {
        expect(mergeTextWithMarkers('a\nb\nc\n', 'a\nX\nb\nc\n', 'a\nb\nMID\nc\n').text)
            .toBe('a\nX\nb\nMID\nc\n');
    });

    it('keeps both insertions at one boundary, and drops one both sides made the same', () => {
        expect(mergeTextWithMarkers('a\nb\nc\n', 'a\nX\nb\nc\n', 'a\nY\nb\nc\n').text)
            .toBe('a\nX\nY\nb\nc\n');
        expect(mergeTextWithMarkers('a\nb\nc\n', 'a\nX\nb\nc\n', 'a\nX\nb\nc\n').text)
            .toBe('a\nX\nb\nc\n');
    });

    it('takes the edit once when both sides made it identically', () => {
        expect(mergeTextWithMarkers('a\nb\nc\n', 'a\nX\nc\n', 'a\nX\nc\n').text).toBe('a\nX\nc\n');
    });

    it('handles a file with no trailing newline, and a one-line file', () => {
        expect(mergeTextWithMarkers('a\nb', 'a\nX\nb', 'a\nb\nc').text).toBe('a\nX\nb\nc');
        expect(mergeTextWithMarkers('x', 'y', 'x').text).toBe('y');
        expect(mergeTextWithMarkers('x', 'x', 'z').text).toBe('z');
    });
});

describe('conflictBlocks', () => {
    it('spans a complete triple', () => {
        const text = `keep\n${block('mine', 'theirs')}\nkeep\n`;
        expect(conflictBlocks(text)).toEqual([{ startLine: 2, endLine: 6 }]);
    });

    it('reports nothing for a block left open', () => {
        expect(conflictBlocks(`${CONFLICT_OURS}\nmine\n${CONFLICT_SEPARATOR}\ntheirs\n`)).toEqual([]);
    });

    it('reports nothing for a divider of equals in ordinary text', () => {
        expect(conflictBlocks('Title\n=======\nSome prose\n')).toEqual([]);
    });

    it('reports nothing once the markers have been removed', () => {
        expect(conflictBlocks('mine\n')).toEqual([]);
    });
});

describe('minimalReplace', () => {
  /** The change is only worth having if applying it really yields `after`. */
  const apply = (before: string, { from, to, insert }: { from: number; to: number; insert: string }) =>
    before.slice(0, from) + insert + before.slice(to);

  const cases: Array<[string, string, string]> = [
    ['one line changed', 'a\nb\nc', 'a\nB\nc'],
    ['line appended', 'a\nb\nc', 'a\nb\nc\nd'],
    ['line prepended', 'a\nb\nc', 'z\na\nb\nc'],
    ['line deleted', 'a\nb\nc', 'a\nc'],
    ['two separate edits', '1\n2\n3\n4\n5', '1\nX\n3\nY\n5'],
    ['first line changed', 'a\nb\nc', 'X\nb\nc'],
    ['last line changed', 'a\nb\nc', 'a\nb\nX'],
    ['whole file replaced', 'a\nb\nc', 'nothing\nthe same'],
    ['empty file filled', '', 'a\nb'],
    ['file emptied', 'a\nb\nc', ''],
    ['trailing newline added', 'a\nb', 'a\nb\n'],
    ['trailing newline removed', 'a\nb\n', 'a\nb'],
    ['adjacent lines rewritten', 'a\nb\nc\nd', 'a\nP\nQ\nd'],
    ['single line file', 'x', 'y'],
    ['appended to a single line', 'x', 'x\ny'],
  ];

  for (const [name, before, after] of cases) {
    it(`${name}: the change reproduces the file`, () => {
      expect(apply(before, minimalReplace(before, after))).toBe(after);
    });
  }

  it('replaces nothing for unchanged content', () => {
    expect(minimalReplace('a\nb\nc', 'a\nb\nc')).toEqual({ from: 5, to: 5, insert: '' });
  });

  it('leaves the unchanged lines of a single-line edit outside the change', () => {
    const change = minimalReplace('a\nb\nc', 'a\nB\nc');
    expect('a\nb\nc'.slice(0, change.from)).toBe('a\n');
    expect('a\nb\nc'.slice(change.to)).toBe('\nc');
    expect(change.insert).toBe('B');
  });

  it('does not reach past the lines an append adds', () => {
    expect(minimalReplace('a\nb\nc', 'a\nb\nc\nd')).toEqual({ from: 5, to: 5, insert: '\nd' });
  });

  it('does not rewrite the document when only its end differs by a newline', () => {
    expect(minimalReplace('a\nb', 'a\nb\n')).toEqual({ from: 3, to: 3, insert: '\n' });
  });

  it('works from an empty document', () => {
    expect(minimalReplace('', 'a\nb')).toEqual({ from: 0, to: 0, insert: 'a\nb' });
  });
});
