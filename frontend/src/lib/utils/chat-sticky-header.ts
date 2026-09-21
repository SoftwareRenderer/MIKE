/**
 * The sticky header names the user message to jump to next: the latest one above the sticky line,
 * excluding whichever message already rests on it. Positions are in content space — offsets from the
 * top of the scroll content, whatever the container's place on screen.
 */

/** A rendered user message and its measured position in content space. */
export interface StickyUserMessage {
    entryIdx: number;
    firstLine: string;
    top: number;
    bottom: number;
}

export interface StickyHeaderState {
    firstLine: string;
    /** The entry the header refers to, -1 when none. */
    entryIdx: number;
    show: boolean;
    /**
     * The next step back is a row this window does not hold, so older rows should be fetched. One
     * agent turn is routinely a hundred rows of a 120-row window, so the previous user message is
     * often not in the document at all.
     */
    needsEarlier: boolean;
}

/**
 * A message resting on the line is the one at the top of the chat, so the header names the message
 * before it. The slack covers the browser nudging a landing — entries above realize their height late
 * and scroll anchoring moves `scrollTop` by a measured 8px — and equals a message's own top padding.
 */
const TOP_MESSAGE_TOLERANCE = 12;

/**
 * Pick the header's message at a scroll position. `topInset` is the measured distance from the
 * container's top edge to the sticky line — never an estimate, because the choice below turns on it.
 *
 * `tappedIdx`: the message the header last jumped to. `olderRowsBeyond`: rows older than the rendered
 * ones exist and have not been fetched.
 */
export function computeStickyHeader(
    userMessages: StickyUserMessage[],
    scrollTop: number,
    topInset = 0,
    tappedIdx = -1,
    olderRowsBeyond = false,
): StickyHeaderState {
    if (userMessages.length === 0) {
        return { firstLine: '', entryIdx: -1, show: false, needsEarlier: false };
    }
    const line = scrollTop + topInset;
    let above: StickyUserMessage | null = null;
    for (const m of userMessages) {
        if (m.top < line - TOP_MESSAGE_TOLERANCE) above = m;
    }
    let latest = above;
    if (above && above.entryIdx === tappedIdx) {
        const at = userMessages.indexOf(above);
        latest = at > 0 ? userMessages[at - 1] : null;
    }
    // The gate is about the conversation's first message, and a page edge is not that.
    const show = latest !== null && (olderRowsBeyond || scrollTop >= userMessages[0].bottom);
    return {
        firstLine: latest ? latest.firstLine : '',
        entryIdx: latest ? latest.entryIdx : -1,
        show,
        needsEarlier: olderRowsBeyond && above === userMessages[0],
    };
}
