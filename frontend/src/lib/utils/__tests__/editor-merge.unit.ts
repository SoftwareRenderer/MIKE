import { describe, it, expect } from 'vitest';
import { mergeText, reconcileExternalChange } from '$lib/utils/editor-merge';

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
