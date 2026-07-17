import { describe, it, expect } from 'vitest';
import { parseUnifiedDiff, enrichRowsWithInlineSegments, buildDisplayItems, truncatePath, buildSideBySideRows, buildModifiedSegments } from '$lib/utils/diff-parser';

describe('parseUnifiedDiff', () => {
    it('returns empty array for empty input', () => {
        expect(parseUnifiedDiff('')).toEqual([]);
        expect(parseUnifiedDiff('   ')).toEqual([]);
        expect(parseUnifiedDiff(null as any)).toEqual([]);
    });

    it('parses a simple single-file diff', () => {
        const patch = `diff --git a/src/main.ts b/src/main.ts
index abc1234..def5678 100644
--- a/src/main.ts
+++ b/src/main.ts
@@ -1,3 +1,4 @@
 const x = 1;
-const y = 2;
+const y = 3;
+const z = 4;
 const w = 5;`;

        const files = parseUnifiedDiff(patch);
        expect(files).toHaveLength(1);
        expect(files[0].path).toBe('src/main.ts');
        expect(files[0].added).toBe(2);
        expect(files[0].removed).toBe(1);
        // 5 rows: context(x), removed(y=2), added(y=3), added(z=4), context(w)
        expect(files[0].rows).toHaveLength(5);
        expect(files[0].rows[0].variant).toBe('context');
        expect(files[0].rows[1].variant).toBe('removed');
        expect(files[0].rows[2].variant).toBe('added');
        expect(files[0].rows[3].variant).toBe('added');
        expect(files[0].rows[4].variant).toBe('context');
    });

    it('parses multi-file diffs', () => {
        const patch = `diff --git a/file1.ts b/file1.ts
index 111..222 100644
--- a/file1.ts
+++ b/file1.ts
@@ -1,2 +1,3 @@
 line1
+added1
 line2
diff --git a/file2.ts b/file2.ts
index 333..444 100644
--- a/file2.ts
+++ b/file2.ts
@@ -1,2 +1,2 @@
-old
+new`;

        const files = parseUnifiedDiff(patch);
        expect(files).toHaveLength(2);
        expect(files[0].path).toBe('file1.ts');
        expect(files[1].path).toBe('file2.ts');
        expect(files[0].added).toBe(1);
        expect(files[1].removed).toBe(1);
    });

    it('detects new files', () => {
        const patch = `diff --git a/newfile.ts b/newfile.ts
new file mode 100644
index 0000000..abc1234
--- /dev/null
+++ a/newfile.ts
@@ -0,0 +1,2 @@
+line1
+line2`;

        const files = parseUnifiedDiff(patch);
        expect(files[0].isNewFile).toBe(true);
        expect(files[0].added).toBe(2);
    });

    it('detects deleted files', () => {
        const patch = `diff --git a/deleted.ts b/deleted.ts
deleted file mode 100644
index abc1234..0000000
--- a/deleted.ts
+++ /dev/null
@@ -1,2 +0,0 @@
-line1
-line2`;

        const files = parseUnifiedDiff(patch);
        expect(files[0].isDeleted).toBe(true);
        expect(files[0].removed).toBe(2);
    });

    it('handles binary file markers', () => {
        const patch = `diff --git a/image.png b/image.png
Binary files a/image.png and b/image.png differ`;

        const files = parseUnifiedDiff(patch);
        expect(files).toHaveLength(1);
        expect(files[0].path).toBe('image.png');
        expect(files[0].rows).toHaveLength(1);
        expect(files[0].rows[0].text).toBe('Binary files a/image.png and b/image.png differ');
    });

    it('handles backend binary-file annotation', () => {
        const patch = `diff --git a/large.bin b/large.bin
# binary-file: 5242880
Binary files a/large.bin and b/large.bin differ`;

        const files = parseUnifiedDiff(patch);
        expect(files[0].fileSize).toBe(5242880);
        expect(files[0].isBinary).toBe(true);
    });

    it('handles backend large-file annotation', () => {
        const patch = `diff --git a/big.txt b/big.txt
# large-file: 1048576`;

        const files = parseUnifiedDiff(patch);
        expect(files).toHaveLength(1);
        expect(files[0].fileSize).toBe(1048576);
        expect(files[0].isBinary).toBeUndefined();
    });

    it('skips "No newline at end of file" markers', () => {
        const patch = `diff --git a/file.txt b/file.txt
--- a/file.txt
+++ b/file.txt
@@ -1 +1 @@
-old line
\\ No newline at end of file
+new line
\\ No newline at end of file`;

        const files = parseUnifiedDiff(patch);
        expect(files[0].rows).toHaveLength(2);
        expect(files[0].rows[0].variant).toBe('removed');
        expect(files[0].rows[1].variant).toBe('added');
    });

    it('line numbers track correctly through hunks', () => {
        const patch = `diff --git a/file.ts b/file.ts
--- a/file.ts
+++ b/file.ts
@@ -5,3 +5,4 @@
 context1
-removed1
+added1
+added2
 context2`;

        const files = parseUnifiedDiff(patch);
        const rows = files[0].rows;
        expect(rows[0].lineNumber).toBe(5); // context1 at new line 5
        expect(rows[1].lineNumber).toBe(6); // removed1 at old line 6
        expect(rows[2].lineNumber).toBe(6); // added1 at new line 6
        expect(rows[3].lineNumber).toBe(7); // added2 at new line 7
        expect(rows[4].lineNumber).toBe(8); // context2 at new line 8
    });

    it('keeps files with only context lines (rows.length > 0)', () => {
        const patch = `diff --git a/nochange.ts b/nochange.ts
index abc..abc 100644
--- a/nochange.ts
+++ b/nochange.ts
@@ -1 +1 @@
 same line`;

        // Files with rows (even just context) are kept by the filter
        const files = parseUnifiedDiff(patch);
        expect(files).toHaveLength(1);
        expect(files[0].path).toBe('nochange.ts');
        expect(files[0].rows).toHaveLength(1);
        expect(files[0].added).toBe(0);
        expect(files[0].removed).toBe(0);
    });
});

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
    function row(variant: 'context' | 'added' | 'removed', text = 'line'): ReturnType<typeof parseUnifiedDiff>[number]['rows'][number] {
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

    it('truncates long paths with ellipsis', () => {
        const long = 'src/very/long/nested/directory/structure/that/exceeds/limit/file.ts';
        const result = truncatePath(long, 48);
        // truncatePath uses ceil((48-3)/2) + 1 (ellipsis) + floor((48-3)/2) = 23 + 1 + 22 = 46
        // (the ellipsis character '…' counts as 1 char in JS string length)
        expect(result.length).toBeLessThanOrEqual(48);
        expect(result).toContain('…');
        expect(result.startsWith('src/')).toBe(true);
        expect(result.endsWith('file.ts')).toBe(true);
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
