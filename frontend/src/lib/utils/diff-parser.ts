import { diffWordsWithSpace } from 'diff';
import type { DiffFile, DiffRow, InlineDiffSegment } from '$lib/types/diff';

/**
 * Parse a raw unified diff (git diff output) into structured DiffFile[] data.
 *
 * Handles the standard unified diff format:
 *   diff --git a/path b/path
 *   --- a/path
 *   +++ b/path
 *   @@ -old_start,old_count +new_start,new_count @@
 *   context line (prefix: space)
 *   -removed line
 *   +added line
 */
export function parseUnifiedDiff(patch: string): DiffFile[] {
    if (!patch || !patch.trim()) {
        return [];
    }

    const lines = patch.split('\n');
    // Remove trailing empty line from git diff output
    if (lines.length > 0 && lines[lines.length - 1] === '') {
        lines.pop();
    }

    const files: DiffFile[] = [];
    let currentFile: DiffFile | null = null;
    let oldLine = 0;
    let newLine = 0;
    let inHunk = false;

    for (let i = 0; i < lines.length; i++) {
        const line = lines[i]!;

        // File header: "diff --git a/path b/path"
        if (line.startsWith('diff --git ')) {
            const match = line.match(/^diff --git a\/(.+?) b\/(.+)$/);
            const path = match?.[1] ?? line.slice('diff --git '.length).split(' b/')[1] ?? 'unknown';
            currentFile = {
                path,
                rows: [],
                added: 0,
                removed: 0,
            };
            files.push(currentFile);
            inHunk = false;
            oldLine = 0;
            newLine = 0;
            continue;
        }

        // Skip index line
        if (line.startsWith('index ')) {
            continue;
        }

        // Old file header
        if (line.startsWith('--- ')) {
            continue;
        }

        // New file header
        if (line.startsWith('+++ ')) {
            continue;
        }

        // Binary file notice
        if (line.startsWith('Binary files ') || line.includes('Binary file')) {
            if (currentFile) {
                currentFile.rows.push({
                    lineNumber: null,
                    variant: 'context',
                    text: line,
                });
            }
            continue;
        }

        // Large/binary file size annotation (injected by backend)
        // Backend uses "# binary-file: N" and "# large-file: N" format
        if (line.startsWith('# binary-file: ') || line.startsWith('# large-file: ')) {
            const isBinary = line.startsWith('# binary-file: ');
            const prefix = isBinary ? '# binary-file: ' : '# large-file: ';
            const sizeStr = line.slice(prefix.length).trim();
            if (currentFile) {
                const size = Number.parseInt(sizeStr, 10);
                if (!isNaN(size)) {
                    currentFile.fileSize = size;
                    if (isBinary) {
                        currentFile.isBinary = true;
                    }
                }
            }
            continue;
        }
        // Legacy format (kept for backward compatibility)
        if (line.startsWith('# binary or large file: ')) {
            const match = line.match(/^# binary or large file: (\d+) bytes/);
            if (currentFile && match) {
                currentFile.fileSize = Number.parseInt(match[1], 10);
            }
            continue;
        }

        // Hunk header: "@@ -old_start,old_count +new_start,new_count @@"
        if (line.startsWith('@@')) {
            inHunk = true;
            const match = line.match(/@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? +@@/);
            if (match) {
                oldLine = Number.parseInt(match[1] ?? '0', 10);
                newLine = Number.parseInt(match[2] ?? '0', 10);
            }
            continue;
        }

        if (!inHunk || !currentFile) {
            continue;
        }

        // Parse diff content lines
        if (line.startsWith('+')) {
            currentFile.rows.push({
                lineNumber: newLine,
                variant: 'added',
                text: line.slice(1),
            });
            currentFile.added++;
            newLine++;
        } else if (line.startsWith('-')) {
            currentFile.rows.push({
                lineNumber: oldLine,
                variant: 'removed',
                text: line.slice(1),
            });
            currentFile.removed++;
            oldLine++;
        } else if (line.startsWith(' ')) {
            currentFile.rows.push({
                lineNumber: newLine,
                variant: 'context',
                text: line.slice(1),
            });
            oldLine++;
            newLine++;
        } else if (line === '\\ No newline at end of file') {
            // Marker for files without trailing newline — skip
            continue;
        }
    }

    // Filter out files with no actual changes (keep binary/large files with metadata)
    // Enrich rows with inline word-level diff segments
    return files.filter(f => f.rows.length > 0 || f.fileSize).map(f => ({
        ...f,
        rows: enrichRowsWithInlineSegments(f.rows || []),
    }));
}

/**
 * Build inline word-level diff segments for a pair of removed/added lines.
 * Uses diffWordsWithSpace to find the exact changed portions within the line.
 */
function buildModifiedSegments(oldText: string, newText: string): {
    oldSegments: InlineDiffSegment[];
    newSegments: InlineDiffSegment[];
} {
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
export function enrichRowsWithInlineSegments(rows: DiffRow[]): DiffRow[] {
    const result: DiffRow[] = [];
    let index = 0;

    while (index < rows.length) {
        const row = rows[index];
        if (!row || row.variant !== 'removed') {
            result.push(row);
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
            result.push(...removedBlock);
            continue;
        }

        // Pair positionally and compute inline segments
        const pairCount = Math.min(removedBlock.length, addedBlock.length);
        for (let pi = 0; pi < pairCount; pi++) {
            const removedRow = removedBlock[pi];
            const addedRow = addedBlock[pi];
            if (removedRow && addedRow && !removedRow.segments && !addedRow.segments) {
                const { oldSegments, newSegments } = buildModifiedSegments(removedRow.text, addedRow.text);
                removedBlock[pi] = { ...removedRow, segments: oldSegments };
                addedBlock[pi] = { ...addedRow, segments: newSegments };
            }
        }

        result.push(...removedBlock, ...addedBlock);
    }

    return result;
}

/**
 * Group consecutive context lines into collapsible blocks.
 * Returns an array of display items: either a single row or a collapsible block.
 */
export interface CollapsedBlock {
    id: string;
    count: number;
    rows: DiffRow[];
}

export type DiffDisplayItem =
    | { type: 'row'; row: DiffRow }
    | { type: 'collapsed'; block: CollapsedBlock };

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
                items.push({ type: 'row', row });
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
            for (const row of blockRows) {
                items.push({ type: 'row', row });
            }
            continue;
        }

        items.push({
            type: 'collapsed',
            block: {
                id: `ctx-${start}-${i - 1}`,
                count: blockRows.length,
                rows: blockRows,
            },
        });
    }

    return items;
}

/** Truncate long file paths for display, keeping the beginning and end visible. */
export function truncatePath(path: string, maxLength = 48): string {
    if (path.length <= maxLength) return path;
    const head = Math.ceil((maxLength - 3) / 2);
    const tail = Math.floor((maxLength - 3) / 2);
    return `${path.slice(0, head)}…${path.slice(path.length - tail)}`;
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
                leftText: row.text,
                leftVariant: 'context',
                rightLineNum: row.lineNumber,
                rightText: row.text,
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
                    leftText: r.text,
                    leftVariant: 'removed',
                    rightLineNum: a.lineNumber,
                    rightText: a.text,
                    rightVariant: 'added',
                });
            }
            // Unpaired removed lines
            for (let p = pairCount; p < removedBlock.length; p++) {
                const r = removedBlock[p]!;
                result.push({
                    leftLineNum: r.lineNumber,
                    leftText: r.text,
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
                    rightText: a.text,
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
                rightText: row.text,
                rightVariant: 'added',
            });
            i++;
        } else {
            i++;
        }
    }

    return result;
}


