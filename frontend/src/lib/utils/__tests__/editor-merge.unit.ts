import { describe, it, expect } from 'vitest';
import { mergeText, minimalReplace, reconcileExternalChange } from '$lib/utils/editor-merge';

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

    it('reports a conflict when both sides edit the same region', () => {
        const r = reconcileExternalChange('a\nb\nc\nd\n', 'a\nb\nUSER\nd\n', 'a\nb\nEXTERNAL\nd\n');
        expect(r).toEqual({ kind: 'conflict' });
    });
});

describe('mergeText', () => {
    it('combines independent user and external edits', () => {
        expect(mergeText('a\nb\nc\nd\n', 'a\nb\nUSER\nd\n', 'EXTERNAL\nb\nc\nd\n')).toBe(
            'EXTERNAL\nb\nUSER\nd\n',
        );
    });

    it('returns null on a same-line conflict', () => {
        expect(mergeText('a\nb\nc\nd\n', 'a\nb\nUSER\nd\n', 'a\nb\nEXTERNAL\nd\n')).toBeNull();
    });

    it('returns base when nothing changed', () => {
        expect(mergeText('a\nb\nc\n', 'a\nb\nc\n', 'a\nb\nc\n')).toBe('a\nb\nc\n');
    });

    it('adopts the external change when the user made no edits', () => {
        expect(mergeText('a\nb\nc\n', 'a\nb\nc\n', 'a\nb\nNEW\n')).toBe('a\nb\nNEW\n');
    });

    it('merges two pure insertions at different positions', () => {
        expect(mergeText('a\nb\nc\n', 'a\nX\nb\nc\n', 'a\nb\nMID\nc\n')).toBe('a\nX\nb\nMID\nc\n');
    });

    it('keeps both insertions when both sides insert at the same boundary', () => {
        expect(mergeText('a\nb\nc\n', 'a\nX\nb\nc\n', 'a\nY\nb\nc\n')).toBe('a\nX\nY\nb\nc\n');
    });

    it('dedupes an identical insertion made by both sides', () => {
        expect(mergeText('a\nb\nc\n', 'a\nX\nb\nc\n', 'a\nX\nb\nc\n')).toBe('a\nX\nb\nc\n');
    });

    it('flags a conflict when both sides replace the same region differently', () => {
        expect(mergeText('a\nb\nc\nd\n', 'a\nB1\nB2\nd\n', 'a\nC1\nd\n')).toBeNull();
    });

    it('combines a deletion with an external insertion elsewhere', () => {
        expect(mergeText('a\nb\nc\n', 'a\nc\n', 'a\nY\nb\nc\n')).toBe('a\nY\nc\n');
    });

    it('handles files without a trailing newline', () => {
        expect(mergeText('a\nb', 'a\nX\nb', 'a\nb\nc')).toBe('a\nX\nb\nc');
    });

    it('handles a single-line file', () => {
        expect(mergeText('x', 'y', 'x')).toBe('y');
        expect(mergeText('x', 'x', 'z')).toBe('z');
    });

    it('produces the user edit when both sides made the identical edit', () => {
        expect(mergeText('a\nb\nc\n', 'a\nX\nc\n', 'a\nX\nc\n')).toBe('a\nX\nc\n');
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
