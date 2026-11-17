import { describe, it, expect } from 'vitest';
import { enrichRowsWithInlineSegments, buildDisplayItems, jumpTargets, truncatePath, buildSideBySideRows, buildModifiedSegments, getStatusBadge } from '$lib/utils/diff-parser';
import type { DiffFile, DiffRow } from '$lib/types/diff';

function makeDiffFile(overrides: Partial<DiffFile> = {}): DiffFile {
    return {
        path: 'test.ts',
        rows: [],
        added: 0,
        removed: 0,
        ...overrides,
    };
}

describe('enrichRowsWithInlineSegments', () => {
    function row(variant: 'context' | 'added' | 'removed', text: string) {
        return { lineNumber: 1, variant, text };
    }

    it('leaves context and added-only rows unchanged', () => {
        const rows = [row('context', 'ctx'), row('added', 'new line')];
        const result = enrichRowsWithInlineSegments(rows);
        expect(result).toHaveLength(2);
        expect(result[0].segments).toBeUndefined();
        expect(result[1].segments).toBeUndefined();
    });

    /** What a row was handed: its own text rebuilt from the segments, and the tones that shaped it. */
    function side(row: { segments?: { text: string; tone: string }[] }) {
        return [(row.segments ?? []).map((s) => s.text).join(''), (row.segments ?? []).map((s) => s.tone)];
    }

    it('pairs a removed row with the added row under it', () => {
        // Which rows get segments is this function's decision; what the segments say is
        // `buildModifiedSegments`'s claim, proved there on this same pair. What belongs here is that
        // each row is handed its OWN side — painting the new text over the removed row is the bug.
        const rows = [row('removed', 'hello world'), row('added', 'hello there')];
        const result = enrichRowsWithInlineSegments(rows);
        expect(result).toHaveLength(2);
        expect(side(result[0])).toEqual(['hello world', ['context', 'removed']]);
        expect(side(result[1])).toEqual(['hello there', ['context', 'added']]);
    });

    it('passes through pure deletions without pairing', () => {
        const rows = [row('removed', 'deleted line')];
        const result = enrichRowsWithInlineSegments(rows);
        expect(result).toHaveLength(1);
        expect(result[0].segments).toBeUndefined();
    });

    it('pairs only up to the minimum block size', () => {
        const rows = [
            row('removed', 'old1'),
            row('removed', 'old2'),
            row('added', 'new1'),
        ];
        const result = enrichRowsWithInlineSegments(rows);
        expect(result).toHaveLength(3);
        expect(result[0].segments).toBeDefined(); // old1 paired with new1
        expect(result[1].segments).toBeUndefined(); // old2 unpaired
    });

    it('skips rows that already have segments', () => {
        const rows = [
            { lineNumber: 1, variant: 'removed' as const, text: 'old', segments: [{ key: 'k', text: 'old', tone: 'removed' as const }] },
            { lineNumber: 2, variant: 'added' as const, text: 'new' },
        ];
        const result = enrichRowsWithInlineSegments(rows);
        // Should not overwrite existing segments
        expect(result[0].segments).toBe(rows[0].segments);
    });

    it('leaves a pair too long to word-diff as whole removed/added rows', () => {
        const long = 'x'.repeat(5000);
        const result = enrichRowsWithInlineSegments([row('removed', long), row('added', `${long}y`)]);
        expect(result[0].segments).toBeUndefined();
        expect(result[1].segments).toBeUndefined();
        expect(result[0].text).toBe(long);
    });
});

describe('buildModifiedSegments', () => {
    /** The pair's segments, failing the test when the parser declined to word-diff a pair this small. */
    function wordDiff(oldText: string, newText: string) {
        const segments = buildModifiedSegments(oldText, newText);
        if (!segments) throw new Error(`no segments for a ${oldText.length}/${newText.length}-char pair`);
        return segments;
    }

    it('identifies unchanged prefix, removed word, and added word', () => {
        const { oldSegments, newSegments } = wordDiff('hello world', 'hello there');
        // Old: "hello " (context) + "world" (removed)
        expect(oldSegments).toHaveLength(2);
        expect(oldSegments[0].tone).toBe('context');
        expect(oldSegments[0].text).toBe('hello ');
        expect(oldSegments[1].tone).toBe('removed');
        expect(oldSegments[1].text).toBe('world');
        // New: "hello " (context) + "there" (added)
        expect(newSegments).toHaveLength(2);
        expect(newSegments[0].tone).toBe('context');
        expect(newSegments[0].text).toBe('hello ');
        expect(newSegments[1].tone).toBe('added');
        expect(newSegments[1].text).toBe('there');
    });

    it('handles completely different lines', () => {
        const { oldSegments, newSegments } = wordDiff('abc', 'xyz');
        expect(oldSegments).toHaveLength(1);
        expect(oldSegments[0].tone).toBe('removed');
        expect(oldSegments[0].text).toBe('abc');
        expect(newSegments).toHaveLength(1);
        expect(newSegments[0].tone).toBe('added');
        expect(newSegments[0].text).toBe('xyz');
    });

    it('handles identical lines as all context', () => {
        const { oldSegments, newSegments } = wordDiff('same line', 'same line');
        expect(oldSegments).toHaveLength(1);
        expect(oldSegments[0].tone).toBe('context');
        expect(oldSegments[0].text).toBe('same line');
        expect(newSegments).toHaveLength(1);
        expect(newSegments[0].tone).toBe('context');
        expect(newSegments[0].text).toBe('same line');
    });

    it('handles empty strings', () => {
        const { oldSegments, newSegments } = wordDiff('', '');
        expect(oldSegments).toHaveLength(0);
        expect(newSegments).toHaveLength(0);
    });

    it('handles multi-word changes', () => {
        const { oldSegments, newSegments } = wordDiff('foo bar baz', 'foo qux baz');
        const oldTones = oldSegments.map(s => s.tone);
        const newTones = newSegments.map(s => s.tone);
        // foo (context) + bar (removed) + baz (context)
        expect(oldTones[0]).toBe('context');
        expect(oldTones[oldTones.length - 1]).toBe('context');
        expect(oldTones).toContain('removed');
        // foo (context) + qux (added) + baz (context)
        expect(newTones[0]).toBe('context');
        expect(newTones[newTones.length - 1]).toBe('context');
        expect(newTones).toContain('added');
    });

    // The word-diff is superlinear: a 40 000-char pair never finished, and the diff panel and every edit
    // card call this on the main thread while rendering. See MAX_WORD_DIFF_CHARS.
    it('declines a pair whose length the word-diff cannot afford', () => {
        const long = 'word '.repeat(2000); // 10 000 chars
        expect(buildModifiedSegments(long, long + 'changed')).toBeNull();
        expect(buildModifiedSegments('short', long)).toBeNull();
    });

    it('word-diffs a pair a reader can wait for', () => {
        const a = 'word '.repeat(100); // 500 chars
        const { oldSegments } = wordDiff(a, a + 'changed');
        expect(oldSegments.some(s => s.tone === 'context')).toBe(true);
    });
});

describe('buildDisplayItems', () => {
    function row(variant: 'context' | 'added' | 'removed', text = 'line'): DiffRow {
        return { lineNumber: 1, variant, text };
    }

    it('returns row items for all changed lines', () => {
        const rows = [row('added', '+'), row('removed', '-')];
        const items = buildDisplayItems(rows);
        expect(items).toHaveLength(2);
        expect(items[0].type).toBe('row');
        expect(items[1].type).toBe('row');
    });

    // One decision: a run of context longer than the collapse minimum becomes one block, and what is
    // drawn around it keeps its position in the file's own rows — the reader clicks a row by that
    // number, and the reader cannot see a row the panel folded away.
    it('collapses one long context run and indexes what it leaves drawn', () => {
        const rows = [
            row('removed', '-'),
            ...Array.from({ length: 20 }, (_, i) => row('context', `c${i}`)),
            row('added', '+'),
        ];
        const items = buildDisplayItems(rows);
        const blocks = items.filter(i => i.type === 'collapsed');
        expect(blocks).toHaveLength(1);
        // CONTEXT_RADIUS = 3: the three rows beside each change stay, the 14 between them fold.
        const block = blocks[0]!.type === 'collapsed' ? blocks[0]!.block : null;
        expect(block?.startIndex).toBe(4);
        expect(block?.rows.map(r => r.text)).toEqual(rows.slice(4, 18).map(r => r.text));
        expect(items.filter(i => i.type === 'row').map(i => (i.type === 'row' ? i.rowIndex : -1)))
            .toEqual([0, 1, 2, 3, 18, 19, 20, 21]);
    });

    // A run shorter than the minimum is drawn whole — whether it lies beside one change or is
    // squeezed between two, where folding either side would leave nothing around the other.
    it.each([
        ['context on both sides of one change', [
            ...Array.from({ length: 3 }, () => row('context')),
            row('added', '+'),
            ...Array.from({ length: 3 }, () => row('context')),
        ]],
        ['context between two changes', [
            row('added', '+'),
            ...Array.from({ length: 5 }, () => row('context')),
            row('added', '+'),
        ]],
    ])('draws %s whole', (_name, rows) => {
        const items = buildDisplayItems(rows);
        expect(items.filter(i => i.type === 'collapsed')).toHaveLength(0);
        expect(items.map(i => (i.type === 'row' ? i.rowIndex : -1)))
            .toEqual(rows.map((_, index) => index));
    });
});

describe('jumpTargets', () => {
    function row(lineNumber: number | null, variant: DiffRow['variant']): DiffRow {
        return { lineNumber, variant, text: 'x' };
    }

    it('opens a context or added row at its own line', () => {
        const rows = [row(4, 'context'), row(5, 'added')];
        expect(jumpTargets(rows)).toEqual([4, 5]);
    });

    it('opens a removed row at the new-side line the deletion landed on', () => {
        // The old file's 31-32 sit against the new file's 31, so the deletion happened just above it.
        const rows = [row(30, 'context'), row(31, 'removed'), row(32, 'removed'), row(31, 'context'), row(32, 'context')];
        expect(jumpTargets(rows)).toEqual([30, 31, 31, 31, 32]);
    });

    it('opens a deletion at the end of the hunk at the new-side line it followed', () => {
        const rows = [row(5, 'context'), row(6, 'removed'), row(7, 'removed')];
        expect(jumpTargets(rows)).toEqual([5, 5, 5]);
    });

    it('reads a mixed hunk against the new side, never against its own number', () => {
        const rows = [row(1, 'context'), row(2, 'removed'), row(2, 'added'), row(3, 'removed')];
        expect(jumpTargets(rows)).toEqual([1, 2, 2, 2]);
    });

    it('offers nothing for a file with no new side', () => {
        const rows = [row(1, 'removed'), row(2, 'removed')];
        expect(jumpTargets(rows)).toEqual([null, null]);
    });

    it('skips a row with no number of its own in both directions', () => {
        const rows = [row(9, 'context'), row(10, 'removed'), row(null, 'context'), row(null, 'context')];
        expect(jumpTargets(rows)).toEqual([9, 9, null, null]);
    });
});

describe('truncatePath', () => {
    it('returns path unchanged when within max length', () => {
        expect(truncatePath('short/path.ts', 48)).toBe('short/path.ts');
    });

    it('returns path unchanged when exactly at max length', () => {
        const exact = 'x'.repeat(48);
        expect(truncatePath(exact, 48)).toBe(exact);
    });

    it('keeps the full file name and truncates the directory part', () => {
        const long = 'src/very/long/nested/directory/structure/that/exceeds/limit/file.ts';
        const result = truncatePath(long, 48);
        expect(result).toBe('src/very…/structure/that/exceeds/limit/file.ts');
        expect(result.length).toBeLessThanOrEqual(48);
        expect(result).toContain('…');
        expect(result.endsWith('file.ts')).toBe(true);
        // tail starts at a segment boundary, never mid-segment
        expect(result.slice(result.indexOf('…') + 1).startsWith('/')).toBe(true);
    });

    it('falls back to just /filename when the tail cannot be extended', () => {
        const long = 'a'.repeat(40) + '/file.txt';
        const result = truncatePath(long, 48);
        expect(result).toBe('aaaaaaaa…/file.txt');
        expect(result.endsWith('/file.txt')).toBe(true);
    });

    it('truncates the end of the file name when the name alone exceeds the limit', () => {
        const result = truncatePath('dir/' + 'a'.repeat(50), 48);
        expect(result).toBe('a'.repeat(47) + '…');
        expect(result.length).toBe(48);
        expect(result).not.toContain('dir');
    });

    it('truncates the end of a file-name-only path', () => {
        const result = truncatePath('b'.repeat(60), 48);
        expect(result).toBe('b'.repeat(47) + '…');
    });
});

describe('buildSideBySideRows', () => {
    function row(variant: 'context' | 'added' | 'removed', text: string, ln = 1) {
        return { lineNumber: ln, variant, text };
    }

    it('duplicates context lines to both columns', () => {
        const rows = [row('context', 'same line', 5)];
        const result = buildSideBySideRows(rows);
        expect(result).toHaveLength(1);
        expect(result[0].leftText).toBe('same line');
        expect(result[0].rightText).toBe('same line');
        expect(result[0].leftVariant).toBe('context');
        expect(result[0].rightVariant).toBe('context');
    });

    it('pairs removed and added lines side by side', () => {
        const rows = [row('removed', 'old', 5), row('added', 'new', 5)];
        const result = buildSideBySideRows(rows);
        expect(result).toHaveLength(1);
        expect(result[0].leftText).toBe('old');
        expect(result[0].leftVariant).toBe('removed');
        expect(result[0].rightText).toBe('new');
        expect(result[0].rightVariant).toBe('added');
    });

    it('shows unpaired removed lines with empty right side', () => {
        const rows = [row('removed', 'old1', 5), row('removed', 'old2', 6)];
        const result = buildSideBySideRows(rows);
        expect(result).toHaveLength(2);
        expect(result[0].rightText).toBe('');
        expect(result[0].rightLineNum).toBeNull();
    });

    it('shows unpaired added lines with empty left side', () => {
        const rows = [row('added', 'new1', 5)];
        const result = buildSideBySideRows(rows);
        expect(result).toHaveLength(1);
        expect(result[0].leftText).toBe('');
        expect(result[0].leftLineNum).toBeNull();
    });

    it('handles mixed paired and unpaired blocks', () => {
        const rows = [
            row('removed', 'old1', 5),
            row('removed', 'old2', 6),
            row('added', 'new1', 5),
            row('added', 'new2', 6),
            row('added', 'new3', 7),
        ];
        const result = buildSideBySideRows(rows);
        expect(result).toHaveLength(3);
        expect(result[0].leftText).toBe('old1');
        expect(result[0].rightText).toBe('new1');
        expect(result[1].leftText).toBe('old2');
        expect(result[1].rightText).toBe('new2');
        expect(result[2].leftText).toBe('');
        expect(result[2].rightText).toBe('new3');
    });
});

describe('getStatusBadge', () => {
    // The badge is read off the parsed file, in one precedence: a rename, then a deletion, then a
    // creation, then modified. The row the reader trusts is the one that says a file with only
    // removals is still there — that is `M`, not `D`, and only `isDeleted` says otherwise.
    it.each([
        ['a rename, however else it reads', makeDiffFile({ origPath: 'old.ts' }), 'R', 'var(--color-git-renamed)'],
        ['a rename that also deleted the old path', makeDiffFile({ origPath: 'old.ts', isDeleted: true, added: 5, removed: 5 }), 'R', 'var(--color-git-renamed)'],
        ['a deletion', makeDiffFile({ isDeleted: true, removed: 5, added: 0 }), 'D', 'var(--color-git-deleted)'],
        ['a deletion of a file that also reads new', makeDiffFile({ isDeleted: true, isNewFile: true, added: 0, removed: 5 }), 'D', 'var(--color-git-deleted)'],
        ['a creation', makeDiffFile({ isNewFile: true, added: 10, removed: 0 }), 'A', 'var(--color-git-added)'],
        ['a file with only additions and no new-file flag', makeDiffFile({ added: 10, removed: 0 }), 'M', 'var(--color-git-modified)'],
        ['a file with only removals and no deleted flag', makeDiffFile({ removed: 5, added: 0 }), 'M', 'var(--color-git-modified)'],
        ['an ordinary modification', makeDiffFile({ added: 3, removed: 2 }), 'M', 'var(--color-git-modified)'],
    ])('badges %s', (_name, file, letter, color) => {
        expect(getStatusBadge(file)).toEqual({ letter, color });
    });
});
