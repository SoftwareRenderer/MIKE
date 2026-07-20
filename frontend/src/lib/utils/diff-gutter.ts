import { type Change, diffLines } from 'diff';

/** Per-line diff marker type. */
export type DiffMarker =
    | { type: 'context' }
    | { type: 'added' }
    | { type: 'removed'; text: string }
    | { type: 'modified'; removedText: string };

/** Merge server-provided markers (diff vs HEAD) with working-tree delta markers (diff vs baseline).
 *
 * The server markers reflect the diff between HEAD and the file on disk at load time.
 * The delta markers reflect changes the user made in the editor since loading.
 * This function shifts server markers to account for line insertions/deletions,
 * then merges them with the new delta markers.
 *
 * @param serverMarkers - Pre-computed markers from server (diff vs HEAD), keyed by baseline line number (1-based)
 * @param baselineContent - File content as read from disk (before user edits)
 * @param currentContent - Current editor content (after user edits)
 * @return Map of current-document line number → marker types
 */
export function mergeMarkers(
    serverMarkers: Map<number, string[]> | null,
    baselineContent: string,
    currentContent: string,
): Map<number, string[]> {
    if (baselineContent === currentContent) {
        // No user edits — server markers are already for the current document
        return serverMarkers ?? new Map();
    }

    // Build a mapping from baseline line number → current line number
    // by walking the diff and tracking cumulative offset.
    const baselineToCurrent = new Map<number, number>();
    const diffs = diffLines(baselineContent, currentContent);
    let baselinePos = 0; // 0-based index into baseline lines
    let currentLine = 1; // 1-based current document line
    let addedCount = 0;  // total added lines so far
    let removedCount = 0; // total removed lines so far

    for (const part of diffs) {
        if (part.value === '') continue;
        const lines = splitLines(part.value);

        if (part.removed && !part.added) {
            removedCount += lines.length;
            baselinePos += lines.length;
        } else if (part.added && !part.removed) {
            addedCount += lines.length;
            for (let i = 0; i < lines.length; i++) {
                currentLine++;
            }
        } else {
            // Context (unchanged) — maps baseline to current
            const offset = addedCount - removedCount;
            for (let i = 0; i < lines.length; i++) {
                baselineToCurrent.set(baselinePos + 1, currentLine);
                baselinePos++;
                currentLine++;
            }
        }
    }

    // Also map lines that were added (they appear only in current, not in baseline)
    // These get delta markers but no server markers — handled below.

    // Shift server markers using the mapping
    const shifted = new Map<number, string[]>();
    if (serverMarkers) {
        for (const [baselineLn, types] of serverMarkers) {
            const currentLn = baselineToCurrent.get(baselineLn);
            if (currentLn !== undefined) {
                shifted.set(currentLn, types);
            }
            // If not in mapping, the baseline line was removed — no marker in current doc
        }
    }

    // Compute delta markers (user edits vs baseline)
    const deltaMarkers = computeDiffMarkers(baselineContent, currentContent);

    // Merge: combine shifted server markers + delta markers
    const merged = new Map<number, string[]>();

    // Add shifted server markers
    for (const [ln, types] of shifted) {
        const existing = merged.get(ln);
        if (existing) {
            merged.set(ln, [...existing, ...types]);
        } else {
            merged.set(ln, [...types]);
        }
    }

    // Add delta markers (skip 'context')
    for (let i = 1; i < deltaMarkers.length; i++) {
        const markers = deltaMarkers[i];
        if (!markers) continue;
        const types: string[] = [];
        for (const m of markers) {
            if (m && m.type !== 'context') {
                types.push(m.type);
            }
        }
        if (types.length > 0) {
            const existing = merged.get(i);
            if (existing) {
                merged.set(i, [...existing, ...types]);
            } else {
                merged.set(i, types);
            }
        }
    }

    return merged;
}

/** Split a diff value into lines, removing trailing empty string from final newline. */
function splitLines(value: string): string[] {
    const lines = value.split('\n');
    if (value.endsWith('\n') && lines.length > 0 && lines[lines.length - 1] === '') {
        lines.pop();
    }
    return lines;
}

/** Compute per-line diff markers comparing original (git HEAD) content against current (editor) content.
 *
 * Returns an array indexed by current document line number (1-based).
 * Each entry is an array of markers (a line can have both 'removed' and another marker).
 * - 'context': unchanged line
 * - 'added': new line not in original
 * - 'removed': line was deleted (appears on the line before the gap)
 * - 'modified': line was changed (consecutive removed+added pair); includes the original text for display
 */
export function computeDiffMarkers(
    originalContent: string,
    currentContent: string,
): DiffMarker[][] {
    const diffs = diffLines(originalContent, currentContent);

    // Build markers indexed by current document line number (1-based).
    // Each entry is an array of markers.
    const markers: DiffMarker[][] = [];
    let currentLine = 1;

    function getMarkers(line: number): DiffMarker[] {
        if (!markers[line]) markers[line] = [];
        return markers[line];
    }

    // Track pending removed lines to detect "modified" (removed+added pair)
    let pendingRemovals: string[] = [];

    function flushRemovals(): void {
        pendingRemovals = [];
    }

    for (const part of diffs) {
        if (part.value === '') continue;
        const lines = splitLines(part.value);

        if (part.removed && !part.added) {
            // Removed lines — store for potential pairing with next added block
            for (const line of lines) {
                pendingRemovals.push(line);
            }
            continue;
        }

        if (part.added && !part.removed) {
            // Added lines
            let startIdx = 0;
            if (pendingRemovals.length > 0) {
                // Pair removed+added as "modified" — one-to-one pairing
                const paired = Math.min(pendingRemovals.length, lines.length);
                for (let i = 0; i < paired; i++) {
                    getMarkers(currentLine).push({ type: 'modified', removedText: pendingRemovals[i] });
                    currentLine++;
                }
                // Excess removals (more removed than added) — mark the line BEFORE the gap
                if (pendingRemovals.length > paired && currentLine > 1) {
                    getMarkers(currentLine - 1).push({ type: 'removed', text: pendingRemovals[paired] });
                }
                // Mark where unpaired added lines start
                startIdx = paired;
                pendingRemovals = [];
            }
            // Unpaired added lines
            for (let i = startIdx; i < lines.length; i++) {
                getMarkers(currentLine).push({ type: 'added' });
                currentLine++;
            }
            continue;
        }

        // Context (unchanged) lines
        if (pendingRemovals.length > 0 && lines.length > 0) {
            // Mark the line BEFORE the gap (last line before the removal)
            getMarkers(currentLine - 1).push({ type: 'removed', text: pendingRemovals[0] });
        }
        flushRemovals();
        for (const _line of lines) {
            getMarkers(currentLine).push({ type: 'context' });
            currentLine++;
        }
    }

    // Flush any remaining removals at end of document
    // These are lines removed from the end — mark the last line
    if (pendingRemovals.length > 0 && currentLine > 1) {
        getMarkers(currentLine - 1).push({ type: 'removed', text: pendingRemovals[0] });
    }

    return markers;
}
