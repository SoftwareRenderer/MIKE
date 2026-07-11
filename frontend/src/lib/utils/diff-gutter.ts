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
 * - 'context': unchanged line
 * - 'added': new line not in original
 * - 'removed': line was deleted (appears at the line position where the deletion occurred;
 *   the current line at that position is shown normally, but a removed-line marker is attached)
 * - 'modified': line was changed (consecutive removed+added pair); includes the original text for display
 */
export function computeDiffMarkers(originalContent: string, currentContent: string): DiffMarker[] {
    const diffs = diffLines(originalContent, currentContent);

    // Build markers indexed by current document line number (1-based).
    // We walk the diff output and track current line position.
    const markers: DiffMarker[] = [];
    let currentLine = 1;

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
                    markers[currentLine] = { type: 'modified', removedText: pendingRemovals[i] };
                    currentLine++;
                }
                // Excess removals (more removed than added) — mark the line BEFORE the gap
                if (pendingRemovals.length > paired && currentLine > 1) {
                    markers[currentLine - 1] = { type: 'removed', text: pendingRemovals[paired] };
                }
                // Mark where unpaired added lines start
                startIdx = paired;
                pendingRemovals = [];
            }
            // Unpaired added lines
            for (let i = startIdx; i < lines.length; i++) {
                markers[currentLine] = { type: 'added' };
                currentLine++;
            }
            continue;
        }

        // Context (unchanged) lines
        if (pendingRemovals.length > 0 && lines.length > 0) {
            // Mark the line BEFORE the gap (last line before the removal)
            markers[currentLine - 1] = { type: 'removed', text: pendingRemovals[0] };
        }
        flushRemovals();
        for (const _line of lines) {
            markers[currentLine] = { type: 'context' };
            currentLine++;
        }
    }

    // Flush any remaining removals at end of document
    // These are lines removed from the end — they don't map to a current line
    // We store them past the end so the gutter can optionally display them
    for (const line of pendingRemovals) {
        markers[currentLine] = { type: 'removed', text: line };
        currentLine++;
    }

    return markers;
}
