import type { DiffFile, DiffRow } from '$lib/types/diff';

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
                    key: `binary-${currentFile.path}`,
                    lineNumber: null,
                    variant: 'context',
                    text: line,
                });
            }
            continue;
        }

        // Large/binary file size annotation (injected by backend)
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
                key: `n-${newLine}-${i}`,
                lineNumber: newLine,
                variant: 'added',
                text: line.slice(1),
            });
            currentFile.added++;
            newLine++;
        } else if (line.startsWith('-')) {
            currentFile.rows.push({
                key: `o-${oldLine}-${i}`,
                lineNumber: oldLine,
                variant: 'removed',
                text: line.slice(1),
            });
            currentFile.removed++;
            oldLine++;
        } else if (line.startsWith(' ')) {
            currentFile.rows.push({
                key: `c-${newLine}-${i}`,
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

    // Filter out files with no actual changes
    return files.filter(f => f.rows.length > 0);
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


