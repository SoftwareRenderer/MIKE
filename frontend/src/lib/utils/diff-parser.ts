import { diffWordsWithSpace } from 'diff';
import type { ApiDiffRow, DiffFile, DiffRow, InlineDiffSegment } from '$lib/types/diff';
import { gitStatusColor } from '$lib/utils/git-status';

/**
 * Compute a VS Code-style status badge for a diff file.
 * Uses explicit flags (isDeleted, isNewFile, origPath) instead of heuristics
 * to avoid misclassifying files with only removals as "deleted".
 *
 * Priority: Renamed (R) > Deleted (D) > Added (A) > Modified (M)
 */
export function getStatusBadge(file: DiffFile): { letter: string; color: string } {
    const letter = file.origPath ? 'R' : file.isDeleted ? 'D' : file.isNewFile ? 'A' : 'M';
    return { letter, color: gitStatusColor(letter) };
}

/**
 * A line at least this long is not word-diffed. `diffWordsWithSpace` costs far more than its input's
 * length — a pair of 2 000-char lines is ~26 ms, 20 000 is 1.6 s and 170 MB of RSS — and it runs on the
 * main thread as the diff renders. `git` bounds a diff by change-line *count* (`defaultMaxChangeLines`),
 * never by line length, so a minified bundle or a one-line JSON file arrives whole.
 */
const MAX_WORD_DIFF_CHARS = 2000;

/**
 * Build inline word-level diff segments for a pair of removed/added lines.
 * Uses diffWordsWithSpace to find the exact changed portions within the line.
 *
 * `null` when the pair is too large to diff inline; the caller then draws both rows as whole lines.
 */
export function buildModifiedSegments(oldText: string, newText: string): {
    oldSegments: InlineDiffSegment[];
    newSegments: InlineDiffSegment[];
} | null {
    if (oldText.length > MAX_WORD_DIFF_CHARS || newText.length > MAX_WORD_DIFF_CHARS) return null;
    const oldSegments: InlineDiffSegment[] = [];
    const newSegments: InlineDiffSegment[] = [];
    const parts = diffWordsWithSpace(oldText, newText);

    for (let i = 0; i < parts.length; i++) {
        const part = parts[i];
        if (!part) continue;
        if (part.removed) {
            oldSegments.push({ key: `o-${i}`, text: part.value, tone: 'removed' });
        } else if (part.added) {
            newSegments.push({ key: `n-${i}`, text: part.value, tone: 'added' });
        } else {
            oldSegments.push({ key: `oc-${i}`, text: part.value, tone: 'context' });
            newSegments.push({ key: `nc-${i}`, text: part.value, tone: 'context' });
        }
    }
    return { oldSegments, newSegments };
}

/**
 * Post-process rows to add word-level inline diff segments for adjacent
 * removed/added blocks (e.g. rows parsed from a git patch which lack them).
 */
export function enrichRowsWithInlineSegments(rows: ApiDiffRow[]): DiffRow[] {
    const result: DiffRow[] = [];
    let index = 0;

    while (index < rows.length) {
        const row = rows[index];
        if (!row || row.variant !== 'removed') {
            result.push(row as DiffRow);
            index++;
            continue;
        }

        // Collect contiguous removed rows
        const removedStart = index;
        while (index < rows.length && rows[index]?.variant === 'removed') {
            index++;
        }
        const removedBlock = rows.slice(removedStart, index);

        // Collect contiguous added rows immediately following
        const addedStart = index;
        while (index < rows.length && rows[index]?.variant === 'added') {
            index++;
        }
        const addedBlock = rows.slice(addedStart, index);

        if (addedBlock.length === 0) {
            // Pure deletion — no pairing possible
            result.push(...removedBlock as DiffRow[]);
            continue;
        }

        // Pair positionally and compute inline segments
        const pairCount = Math.min(removedBlock.length, addedBlock.length);
        for (let pi = 0; pi < pairCount; pi++) {
            const removedRow = removedBlock[pi];
            const addedRow = addedBlock[pi];
            if (removedRow && addedRow && !removedRow.segments && !addedRow.segments) {
                const segments = buildModifiedSegments(removedRow.text ?? '', addedRow.text ?? '');
                if (segments) {
                    removedBlock[pi] = { ...removedRow, segments: segments.oldSegments };
                    addedBlock[pi] = { ...addedRow, segments: segments.newSegments };
                }
            }
        }

        result.push(...removedBlock as DiffRow[], ...addedBlock as DiffRow[]);
    }

    return result;
}

/**
 * Group consecutive context lines into collapsible blocks.
 * Returns an array of display items: either a single row or a collapsible block.
 */
interface CollapsedBlock {
    id: string;
    /** Where the block starts in the file's rows, so an expanded row knows its own position. */
    startIndex: number;
    rows: DiffRow[];
}

export type DiffDisplayItem =
    | { type: 'row'; row: DiffRow; rowIndex: number }
    | { type: 'collapsed'; block: CollapsedBlock };

/** The line a click on each row opens, indexed by row position. A removed row has no line in the file the
 *  editor opens, so it opens the new-side line its deletion sits against — `null` when there is no new side. */
export function jumpTargets(rows: DiffRow[]): (number | null)[] {
    const newSide = rows.map(r => (r.variant === 'removed' ? null : r.lineNumber));

    // Nearest numbered new-side row ahead of, and behind, each index. An unnumbered row passes the number
    // through rather than resetting the walk.
    const ahead: (number | null)[] = new Array(rows.length).fill(null);
    let carry: number | null = null;
    for (let i = rows.length - 1; i >= 0; i--) {
        ahead[i] = carry;
        carry = newSide[i] ?? carry;
    }
    const behind: (number | null)[] = new Array(rows.length).fill(null);
    carry = null;
    for (let i = 0; i < rows.length; i++) {
        behind[i] = carry;
        carry = newSide[i] ?? carry;
    }

    return rows.map((row, i) =>
        row.variant === 'removed' ? ahead[i] ?? behind[i] : newSide[i]);
}

/** Minimum number of consecutive context lines to offer collapsing. */
const MIN_COLLAPSE_LINES = 8;
/** Number of context lines to keep visible around changes. */
const CONTEXT_RADIUS = 3;

export function buildDisplayItems(rows: DiffRow[]): DiffDisplayItem[] {
    // Find indices of changed lines
    const changedIndices: number[] = [];
    for (let i = 0; i < rows.length; i++) {
        if (rows[i]?.variant !== 'context') {
            changedIndices.push(i);
        }
    }

    // Build set of context lines near changes (within CONTEXT_RADIUS)
    const nearbyContext = new Set<number>();
    for (const ci of changedIndices) {
        const start = Math.max(0, ci - CONTEXT_RADIUS);
        const end = Math.min(rows.length - 1, ci + CONTEXT_RADIUS);
        for (let i = start; i <= end; i++) {
            nearbyContext.add(i);
        }
    }

    const shouldHide = (i: number): boolean => {
        const row = rows[i];
        if (!row || row.variant !== 'context') return false;
        if (changedIndices.length === 0) {
            return rows.length >= MIN_COLLAPSE_LINES;
        }
        return !nearbyContext.has(i);
    };

    const items: DiffDisplayItem[] = [];
    let i = 0;
    while (i < rows.length) {
        if (!shouldHide(i)) {
            const row = rows[i];
            if (row) {
                items.push({ type: 'row', row, rowIndex: i });
            }
            i++;
            continue;
        }

        // Collect consecutive hideable lines
        const start = i;
        while (i < rows.length && shouldHide(i)) {
            i++;
        }
        const blockRows = rows.slice(start, i);

        if (blockRows.length < MIN_COLLAPSE_LINES) {
            // Too small to collapse — show all
            for (let ri = start; ri < i; ri++) {
                const row = rows[ri];
                if (row) {
                    items.push({ type: 'row', row, rowIndex: ri });
                }
            }
            continue;
        }

        items.push({
            type: 'collapsed',
            block: {
                id: `ctx-${start}-${i - 1}`,
                startIndex: start,
                rows: blockRows,
            },
        });
    }

    return items;
}

/** Truncate a path for display, keeping the file name (last segment) whole — the ellipsis
 *  lands in the directory part. Only a name longer than the limit is end-truncated. */
export function truncatePath(path: string, maxLength = 48): string {
    if (path.length <= maxLength) return path;
    const lastSlash = path.lastIndexOf('/');
    const filename = lastSlash < 0 ? path : path.slice(lastSlash + 1);
    const slash = lastSlash < 0 ? '' : '/';

    // Name + '/' + ellipsis doesn't fit — drop the directory, end-truncate the name.
    if (filename.length + slash.length + 1 > maxLength) {
        return filename.slice(0, maxLength - 1) + '…';
    }

    const head = Math.min(8, maxLength - 1 - (slash.length + filename.length));
    const tailBudget = maxLength - 1 - head;

    // Extend the tail leftward across whole directory segments ('/' boundaries).
    let tailStart = lastSlash;
    for (;;) {
        const prev = path.lastIndexOf('/', tailStart - 1);
        const nextStart = prev < 0 ? 0 : prev;
        if (path.length - nextStart > tailBudget) break;
        tailStart = nextStart;
        if (tailStart === 0) break;
    }

    return path.slice(0, head) + '…' + path.slice(tailStart);
}

/** A single row in a side-by-side diff view. */
export interface SideBySideRow {
    leftLineNum: number | null;
    leftText: string;
    leftVariant: 'context' | 'removed' | '';
    rightLineNum: number | null;
    rightText: string;
    rightVariant: 'context' | 'added' | '';
}

/**
 * Convert unified diff rows into side-by-side pairs.
 * Paired removed/added lines appear on the same row (left/right).
 * Context lines appear in both columns.
 * Unpaired removed lines have an empty right side.
 * Unpaired added lines have an empty left side.
 */
export function buildSideBySideRows(rows: DiffRow[]): SideBySideRow[] {
    const result: SideBySideRow[] = [];
    let i = 0;

    while (i < rows.length) {
        const row = rows[i];
        if (!row) { i++; continue; }

        if (row.variant === 'context') {
            // Context line appears in both columns
            result.push({
                leftLineNum: row.lineNumber,
                leftText: row.text ?? '',
                leftVariant: 'context',
                rightLineNum: row.lineNumber,
                rightText: row.text ?? '',
                rightVariant: 'context',
            });
            i++;
        } else if (row.variant === 'removed') {
            // Collect contiguous removed block
            const removedStart = i;
            while (i < rows.length && rows[i]?.variant === 'removed') i++;
            const removedBlock = rows.slice(removedStart, i);

            // Collect contiguous added block immediately following
            const addedStart = i;
            while (i < rows.length && rows[i]?.variant === 'added') i++;
            const addedBlock = rows.slice(addedStart, i);

            // Pair positionally
            const pairCount = Math.min(removedBlock.length, addedBlock.length);
            for (let p = 0; p < pairCount; p++) {
                const r = removedBlock[p]!;
                const a = addedBlock[p]!;
                result.push({
                    leftLineNum: r.lineNumber,
                    leftText: r.text ?? '',
                    leftVariant: 'removed',
                    rightLineNum: a.lineNumber,
                    rightText: a.text ?? '',
                    rightVariant: 'added',
                });
            }
            // Unpaired removed lines
            for (let p = pairCount; p < removedBlock.length; p++) {
                const r = removedBlock[p]!;
                result.push({
                    leftLineNum: r.lineNumber,
                    leftText: r.text ?? '',
                    leftVariant: 'removed',
                    rightLineNum: null,
                    rightText: '',
                    rightVariant: '',
                });
            }
            // Unpaired added lines
            for (let p = pairCount; p < addedBlock.length; p++) {
                const a = addedBlock[p]!;
                result.push({
                    leftLineNum: null,
                    leftText: '',
                    leftVariant: '',
                    rightLineNum: a.lineNumber,
                    rightText: a.text ?? '',
                    rightVariant: 'added',
                });
            }
        } else if (row.variant === 'added') {
            // Standalone added line (no preceding removed block)
            result.push({
                leftLineNum: null,
                leftText: '',
                leftVariant: '',
                rightLineNum: row.lineNumber,
                rightText: row.text ?? '',
                rightVariant: 'added',
            });
            i++;
        } else {
            i++;
        }
    }

    return result;
}


