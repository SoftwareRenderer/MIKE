/**
 * Paging a conversation that arrives a window at a time. The client's list grows at the top, so a row
 * that arrives on two pages must be drawn once, and the reader must stay on the row they were reading.
 */

/** The one field paging needs from a stored row. */
export interface PagedRow {
    id: string;
}

/** Older rows below the rows held. Pages overlap at most at their edges, so dropping held ids is the whole dedup. */
export function prependRows<T extends PagedRow>(held: T[], older: T[]): T[] {
    if (older.length === 0) return held;
    const seen = new Set(held.map(r => r.id));
    const fresh = older.filter(r => !seen.has(r.id));
    if (fresh.length === 0) return held;
    return [...fresh, ...held];
}

/** Where the reader is looking. */
export interface ScrollAnchor {
    id: string;
    /** The entry's top, measured from the top edge of the scroll viewport. */
    top: number;
}

/** One entry's box, in offsets from the top edge of the scroll viewport. */
export interface MeasuredEntry {
    id: string;
    top: number;
    bottom: number;
}

/** The entry to keep under the eye when a page lands above it: the first one with pixels below the top edge. */
export function resolveAnchor(entries: MeasuredEntry[], viewportTop = 0): ScrollAnchor | null {
    for (const e of entries) {
        if (e.bottom > viewportTop) return { id: e.id, top: e.top };
    }
    return null;
}

/** How far to move the viewport so the anchor sits where it did. Positive is down. */
export function anchorScrollDelta(anchor: ScrollAnchor, anchorTopNow: number): number {
    return anchorTopNow - anchor.top;
}

/** How close to the top of the list counts as "the reader wants what is above". */
export const EARLIER_EDGE_PX = 240;

/** Whether the viewport is at the top of the list, where older rows are fetched from. */
export function atTopOfList(scrollTop: number, edgePx: number = EARLIER_EDGE_PX): boolean {
    return scrollTop <= edgePx;
}
