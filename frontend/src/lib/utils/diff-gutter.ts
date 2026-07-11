import { diffLines } from 'diff';

/** Per-line diff marker type. */
export type DiffMarker =
    | { type: 'context' }
    | { type: 'added' }
    | { type: 'removed'; text: string }
    | { type: 'modified'; removedText: string };

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

        const lines = part.value.split('\n');
        // If the value ends with '\n', the split produces a trailing empty string — remove it
        if (part.value.endsWith('\n') && lines[lines.length - 1] === '') {
            lines.pop();
        }

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
