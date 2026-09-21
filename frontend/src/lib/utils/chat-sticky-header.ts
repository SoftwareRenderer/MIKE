/**
 * Sticky-header computation for the Task Chat.
 *
 * The header names the user message to jump to next: the latest one above the line
 * it sits over, excluding whichever message already rests on that line. Tapping it
 * lands that message there, so the header always holds the next step backwards.
 *
 * All positions are in "content space": offsets from the top of the scroll content,
 * independent of where the container sits on screen.
 */

/** A rendered user message and its measured position in content space. */
export interface StickyUserMessage {
    /** Index of the message in the full entries list. */
    entryIdx: number;
    /** First line of the message — what the header displays. */
    firstLine: string;
    /** Offset of the message's top edge from the content top. */
    top: number;
    /** Offset of the message's bottom edge from the content top. */
    bottom: number;
}

export interface StickyHeaderState {
    /** First line to display. */
    firstLine: string;
    /** Index of the entry the header refers to, -1 when none. */
    entryIdx: number;
    /** Whether the sticky header should be visible. */
    show: boolean;
}

/**
 * Distance to the sticky line when the overlay cannot be measured (unmounted, or no
 * layout at all): ~46px of overlay plus an 8px gap. `stickyLineOffset` measures the
 * real height whenever the header is on screen, so this is rarely used.
 */
export const STICKY_LINE_FALLBACK = 54;

/**
 * A message resting on the line is the one at the top of the chat, so the header
 * names the message before it — the next tap at the same spot goes back a turn.
 *
 * The exclusion needs slack because the browser nudges a landing: entries above
 * realize their height late and scroll anchoring shifts `scrollTop` to hold the view
 * steady (measured 8px). A message left a few pixels above the line would still be
 * named, and the next tap would land it again instead of going back. 12px absorbs
 * that and equals a message's own top padding, so no line of text is skipped.
 */
const TOP_MESSAGE_TOLERANCE = 12;

/**
 * Pick the message for the sticky header at a given scroll position. `topInset` is
 * the distance from the container's top edge to the sticky line (0 for the raw edge).
 *
 * The header stays hidden until the first user message has scrolled out, so it never
 * overlaps that message. The returned entryIdx identifies the exact entry, which
 * keeps the jump target right when first lines repeat.
 *
 * `tappedIdx` is the message the header last jumped to. If that message is still the
 * one above the line, the header names the message before it, so a tap always goes
 * back one turn.
 */
export function computeStickyHeader(
    userMessages: StickyUserMessage[],
    scrollTop: number,
    topInset = 0,
    tappedIdx = -1,
): StickyHeaderState {
    if (userMessages.length === 0) {
        return { firstLine: '', entryIdx: -1, show: false };
    }
    const line = scrollTop + topInset;
    let latest: StickyUserMessage | null = null;
    for (const m of userMessages) {
        if (m.top < line - TOP_MESSAGE_TOLERANCE) latest = m;
    }
    if (latest && latest.entryIdx === tappedIdx) {
        const at = userMessages.indexOf(latest);
        latest = at > 0 ? userMessages[at - 1] : null;
    }
    const show = latest !== null && scrollTop >= userMessages[0].bottom;
    return {
        firstLine: latest ? latest.firstLine : '',
        entryIdx: latest ? latest.entryIdx : -1,
        show,
    };
}
