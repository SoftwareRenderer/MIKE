/**
 * Sticky-header computation for the Task Chat.
 *
 * The chat shows a sticky header with the first line of the latest user
 * message whose top has scrolled above the viewport. All positions are in
 * "content space": offsets from the top of the scroll content, so they are
 * independent of where the container sits on screen.
 */

/** A rendered user message and its measured position in content space. */
export interface StickyUserMessage {
    /** Index of the message in the full entries list. */
    entryIdx: number;
    /** First line of the message (what the sticky bubble displays). */
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
 * Pick the user message for the sticky header at a given scroll position.
 *
 * A message counts as "above the viewport" when its top is at or above the
 * current scroll offset. The header only appears once the first user message
 * has fully scrolled out of view, so it never overlaps that first message.
 * The returned entryIdx identifies the exact entry, which keeps the
 * click-to-scroll target correct when first lines repeat.
 */
export function computeStickyHeader(userMessages: StickyUserMessage[], scrollTop: number): StickyHeaderState {
    if (userMessages.length === 0) {
        return { firstLine: '', entryIdx: -1, show: false };
    }
    let latest: StickyUserMessage | null = null;
    for (const m of userMessages) {
        if (m.top <= scrollTop) latest = m;
    }
    const show = latest !== null && scrollTop >= userMessages[0].bottom;
    return {
        firstLine: latest ? latest.firstLine : '',
        entryIdx: latest ? latest.entryIdx : -1,
        show,
    };
}
