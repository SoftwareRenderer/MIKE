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
            if (pendingRemovals.length > 0) {
                // Pair removed+added as "modified" — one-to-one pairing
                const paired = Math.min(pendingRemovals.length, lines.length);
                for (let i = 0; i < paired; i++) {
                    markers[currentLine] = { type: 'modified', removedText: pendingRemovals[i] };
                    currentLine++;
                }
                // Remaining removals (more removed than added) — attach to current position
                for (let i = paired; i < pendingRemovals.length; i++) {
                    // These extra removals are attached as 'removed' markers at the current position
                    // The current line at this position was already an added line, so we keep it as added
                    // but store the removed text alongside. For simplicity, mark as 'removed'.
                    // Actually, the extra removed lines don't have a corresponding current line,
                    // so we insert them as standalone 'removed' markers.
                    markers[currentLine] = { type: 'removed', text: pendingRemovals[i] };
                    // Don't increment currentLine — the removed line doesn't exist in current doc
                }
                pendingRemovals = [];
            }
            // Remaining added lines (no removal pairing)
            for (const _line of lines) {
                markers[currentLine] = { type: 'added' };
                currentLine++;
            }
            continue;
        }

        // Context (unchanged) lines
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
