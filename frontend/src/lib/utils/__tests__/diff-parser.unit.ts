import { describe, it, expect } from 'vitest';
import { enrichRowsWithInlineSegments, buildDisplayItems, truncatePath, buildSideBySideRows, buildModifiedSegments, getStatusBadge } from '$lib/utils/diff-parser';
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

    it('adds inline segments for paired removed/added lines', () => {
        const rows = [row('removed', 'hello world'), row('added', 'hello there')];
        const result = enrichRowsWithInlineSegments(rows);
        expect(result[0].segments).toBeDefined();
        expect(result[1].segments).toBeDefined();
        // "hello " is context, "world" is removed, "there" is added
        expect(result[0].segments![0].tone).toBe('context');
        expect(result[0].segments![0].text).toBe('hello ');
        expect(result[0].segments![1].tone).toBe('removed');
        expect(result[0].segments![1].text).toBe('world');
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
});

describe('buildModifiedSegments', () => {
    it('identifies unchanged prefix, removed word, and added word', () => {
        const { oldSegments, newSegments } = buildModifiedSegments('hello world', 'hello there');
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
        const { oldSegments, newSegments } = buildModifiedSegments('abc', 'xyz');
        expect(oldSegments).toHaveLength(1);
        expect(oldSegments[0].tone).toBe('removed');
        expect(oldSegments[0].text).toBe('abc');
        expect(newSegments).toHaveLength(1);
        expect(newSegments[0].tone).toBe('added');
        expect(newSegments[0].text).toBe('xyz');
    });

    it('handles identical lines as all context', () => {
        const { oldSegments, newSegments } = buildModifiedSegments('same line', 'same line');
        expect(oldSegments).toHaveLength(1);
        expect(oldSegments[0].tone).toBe('context');
        expect(oldSegments[0].text).toBe('same line');
        expect(newSegments).toHaveLength(1);
        expect(newSegments[0].tone).toBe('context');
        expect(newSegments[0].text).toBe('same line');
    });

    it('identical lines produce all-context segments (used to detect unchanged paired lines)', () => {
        // When ToolCall.svelte pairs old/new lines positionally, identical lines
        // should be detected as unchanged and rendered as a single context line.
        // buildModifiedSegments returns all-context segments for identical input,
        // but the caller (formatEditDiff) should check oldLine === newLine first.
        const { oldSegments } = buildModifiedSegments('### 3. Verification', '### 3. Verification');
        const allContext = oldSegments.every(s => s.tone === 'context');
        expect(allContext).toBe(true);
    });

    it('handles empty strings', () => {
        const { oldSegments, newSegments } = buildModifiedSegments('', '');
        expect(oldSegments).toHaveLength(0);
        expect(newSegments).toHaveLength(0);
    });

    it('handles change in middle of line', () => {
        const { oldSegments, newSegments } = buildModifiedSegments('const x = 1', 'const x = 2');
        // "const x = " (context) + "1" (removed/added) + "" (context)
        const oldTones = oldSegments.map(s => s.tone);
        const newTones = newSegments.map(s => s.tone);
        expect(oldTones).toContain('context');
        expect(oldTones).toContain('removed');
        expect(newTones).toContain('context');
        expect(newTones).toContain('added');
    });

    it('handles multi-word changes', () => {
        const { oldSegments, newSegments } = buildModifiedSegments('foo bar baz', 'foo qux baz');
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

    it('collapses long context blocks', () => {
        const rows = [
            row('removed', '-'),
            ...Array.from({ length: 20 }, () => row('context')),
            row('added', '+'),
        ];
        const items = buildDisplayItems(rows);
        const collapsed = items.filter(i => i.type === 'collapsed');
        expect(collapsed.length).toBe(1);
        // Some context lines near changes should remain as rows (CONTEXT_RADIUS = 3)
        const rowItems = items.filter(i => i.type === 'row');
        expect(rowItems.length).toBeGreaterThan(2); // at least the changed lines
    });

    it('does not collapse short context blocks', () => {
        const rows = [
            row('removed', '-'),
            row('context'),
            row('context'),
            row('added', '+'),
        ];
        const items = buildDisplayItems(rows);
        const collapsed = items.filter(i => i.type === 'collapsed');
        expect(collapsed.length).toBe(0);
    });

    it('keeps context lines near changes visible', () => {
        // CONTEXT_RADIUS = 3, so 3 lines before and after changes should be visible
        const rows = [
            ...Array.from({ length: 3 }, () => row('context')),
            row('added', '+'),
            ...Array.from({ length: 3 }, () => row('context')),
        ];
        const items = buildDisplayItems(rows);
        const collapsed = items.filter(i => i.type === 'collapsed');
        expect(collapsed.length).toBe(0); // all within radius
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
    it('shows R (Renamed) when origPath is set', () => {
        const badge = getStatusBadge(makeDiffFile({ origPath: 'old.ts' }));
        expect(badge.letter).toBe('R');
        expect(badge.color).toBe('#d29922');
    });

    it('shows D (Deleted) when isDeleted is true', () => {
        const badge = getStatusBadge(makeDiffFile({ isDeleted: true, removed: 5, added: 0 }));
        expect(badge.letter).toBe('D');
        expect(badge.color).toBe('#f85149');
    });

    it('shows A (Added) when isNewFile is true', () => {
        const badge = getStatusBadge(makeDiffFile({ isNewFile: true, added: 10, removed: 0 }));
        expect(badge.letter).toBe('A');
        expect(badge.color).toBe('#3fb950');
    });

    it('shows M (Modified) for a file with only removals but NOT actually deleted', () => {
        // This is the key bug fix: a file with removed > 0 && added === 0
        // should NOT show "D" unless isDeleted is explicitly true
        const badge = getStatusBadge(makeDiffFile({ removed: 5, added: 0 }));
        expect(badge.letter).toBe('M');
        expect(badge.color).toBe('#569cd6');
    });

    it('shows M (Modified) for a normal modified file', () => {
        const badge = getStatusBadge(makeDiffFile({ added: 3, removed: 2 }));
        expect(badge.letter).toBe('M');
        expect(badge.color).toBe('#569cd6');
    });

    it('shows M (Modified) for binary files', () => {
        const badge = getStatusBadge(makeDiffFile({ isBinary: true, added: 0, removed: 0 }));
        expect(badge.letter).toBe('M');
        expect(badge.color).toBe('#569cd6');
    });

    it('shows M (Modified) for files with only additions but not marked as new', () => {
        const badge = getStatusBadge(makeDiffFile({ added: 10, removed: 0 }));
        expect(badge.letter).toBe('M');
        expect(badge.color).toBe('#569cd6');
    });

    it('priority: origPath (R) takes precedence over isDeleted', () => {
        // A renamed file that was deleted at old path and created at new path
        const badge = getStatusBadge(makeDiffFile({
            origPath: 'old.ts',
            isDeleted: true,
            added: 5,
            removed: 5,
        }));
        expect(badge.letter).toBe('R');
    });

    it('priority: isDeleted (D) takes precedence over isNewFile', () => {
        const badge = getStatusBadge(makeDiffFile({
            isDeleted: true,
            isNewFile: true,
            added: 0,
            removed: 5,
        }));
        expect(badge.letter).toBe('D');
    });
});
