/**
 * The sticky header names the user message to jump to next: the latest one above the sticky line,
 * excluding whichever message already rests on it. Positions are in content space — offsets from the
 * top of the scroll content, whatever the container's place on screen.
 *
 * A turn the window does not hold is still a turn the header can name. One agent turn is routinely more
 * answers than a window of answers, so the prompt that opened it is older than every row a task opens on,
 * and the prompt read (`GET …/turns`) is what names it. Such a marker has no geometry: it is older than
 * everything rendered, and jumping to it means fetching before landing.
 */

/** A prompt the header can name. `entryIdx` is -1 when the row is not on screen, and then so is its box. */
export interface StickyTurn {
    /** The message's row id — how a jump finds it once it is rendered. */
    entryId: string;
    firstLine: string;
    entryIdx: number;
    top: number;
    bottom: number;
}

export interface StickyHeaderState {
    firstLine: string;
    /** The named prompt's row id, '' when the header names nothing. */
    entryId: string;
    show: boolean;
    /** The named prompt is older than the rows on screen: a jump fetches before it can land. */
    needsRows: boolean;
}

/**
 * A message resting on the line is the one at the top of the chat, so the header names the message
 * before it. The slack covers the browser nudging a landing — entries above realize their height late
 * and scroll anchoring moves `scrollTop` by a measured 8px — and equals a message's own top padding.
 */
const TOP_MESSAGE_TOLERANCE = 12;

/** The line a prompt's header shows: its first line, whatever came from the server or from the DOM. */
export function turnLabel(text: string): string {
    return text.split('\n')[0];
}

/**
 * Pick the header's message at a scroll position. `turns` is oldest first — the prompts older than the
 * rendered rows, then the rendered ones in document order, which is the order they were written.
 *
 * `topInset` is the measured distance from the container's top edge to the sticky line — never an
 * estimate, because the choice below turns on it. `tappedId` is the prompt the header last jumped to, and
 * `olderTurnsBeyond` says the prompt read has an older page this list does not hold.
 */
export function computeStickyHeader(
    turns: StickyTurn[],
    scrollTop: number,
    topInset = 0,
    tappedId = '',
    olderTurnsBeyond = false,
): StickyHeaderState {
    const hidden = { firstLine: '', entryId: '', show: false, needsRows: false };
    if (turns.length === 0) return hidden;

    const line = scrollTop + topInset;
    // From the newest prompt down, stopping at the first answer: a rendered prompt above the line wins,
    // and the unrendered ones — the older end of the list — are met only when nothing rendered has crossed
    // the line, so the first of them met is the newest of them.
    let above: StickyTurn | null = null;
    for (let i = turns.length - 1; i >= 0 && above === null; i--) {
        const turn = turns[i];
        if (turn.entryIdx < 0 || turn.top < line - TOP_MESSAGE_TOLERANCE) above = turn;
    }

    let latest = above;
    if (above && above.entryId === tappedId) {
        const at = turns.indexOf(above);
        latest = at > 0 ? turns[at - 1] : null;
    }
    if (!latest) return hidden;
    // The gate is about the conversation's first message, and a page edge is not that. A prompt that is
    // not on screen is by definition out of it.
    const first = turns[0];
    const firstIsOut = first.entryIdx < 0 || scrollTop >= first.bottom;
    return {
        firstLine: latest.firstLine,
        entryId: latest.entryId,
        show: olderTurnsBeyond || firstIsOut,
        needsRows: latest.entryIdx < 0,
    };
}
