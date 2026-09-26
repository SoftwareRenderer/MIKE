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

/** What a newest-window read leaves on screen, and which paging edge describes it. */
export interface WindowReadRows<T> {
    rows: T[];
    /** True when the older pages the reader paged into still stand in front of the window, which means the
     * cursor naming the oldest row held still names the oldest row of `rows`. */
    keepsEarlier: boolean;
}

/**
 * Join a newest-window read to the rows on screen.
 *
 * The window is the newest answers of the conversation, so every row it does not name is older than every
 * row it does: the pages the reader paged into stay in front of it, and the window replaces the rows it
 * overlaps. Drawing the window alone would strand those pages behind it — `mergeLoadedHistory` appends the
 * entries the read does not name, so the newest answers would land above the older ones and the bottom of
 * the transcript would stop being the newest content.
 *
 * A window that opens nowhere inside the rows held cannot be joined: a thread switch reads a different
 * conversation, and a truncation can cut the overlap away. The window then stands alone.
 */
export function applyWindowRead<T extends PagedRow>(held: T[], fresh: T[]): WindowReadRows<T> {
    const oldest = fresh[0];
    const joinsAt = oldest ? held.findIndex(r => r.id === oldest.id) : -1;
    if (joinsAt > 0) return { rows: [...held.slice(0, joinsAt), ...fresh], keepsEarlier: true };
    return { rows: fresh, keepsEarlier: false };
}

/** How a window read ended, as far as the transcript on screen is concerned. */
export type WindowRead = 'answered' | 'cached' | 'failed';

/** Why what is on screen may not be the whole conversation, and whether anything can still arrive. */
export type WindowReadNotice = 'cached' | 'failed' | 'owing' | null;

/**
 * What to tell the reader about the transcript on screen. `'owing'` means a read failed on a running task
 * this browser holds a key for: the turn still arriving on the stream is all that will come.
 */
export function noticeForWindowRead(read: WindowRead, seen: { holdsTaskKey: boolean; taskInFlight: boolean }): WindowReadNotice {
    if (read === 'answered') return null;
    if (read === 'cached') return 'cached';
    return seen.holdsTaskKey && seen.taskInFlight ? 'owing' : 'failed';
}

/** Whether a window read may redraw the transcript. A mirror copy is this browser's last answered read,
 * so it is older than what the screen has been sent: it paints only a screen holding no rows. */
export function windowIsReading(read: WindowRead, hasRows: boolean): boolean {
    if (read === 'answered') return true;
    return read === 'cached' && !hasRows;
}

/** The fewest rows one task keeps in the cache. A window is five answers, which can be fewer rows than an
 * earlier open read, so an offline reload does not open on less than what was already shown. */
export const CACHED_ROWS_FLOOR = 60;

/** The rows to leave in the cache after a read: the window just answered, with the rows the cache held
 * above it, up to the floor. */
export function mergeCachedRows<T extends PagedRow>(fresh: T[], held: T[]): T[] {
    if (held.length === 0) return fresh;
    const seen = new Set(fresh.map(r => r.id));
    const room = Math.max(CACHED_ROWS_FLOOR, fresh.length) - fresh.length;
    if (room <= 0) return fresh;
    const above = held.filter(r => !seen.has(r.id));
    if (above.length === 0) return fresh;
    return [...above.slice(-room), ...fresh];
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

/**
 * Whether the reader moved the viewport up since the anchoring loop wrote it.
 *
 * A kinetic scroll fires no `wheel`, `touchstart` or `keydown` once the finger leaves the screen, so the
 * position is the only evidence that someone is driving. A move the other way is not a takeover: scroll
 * anchoring raises `scrollTop` to hold a row in place when rows land above it, which is the same
 * direction this loop writes in.
 */
export function readerScrolledUp(writtenTop: number, topNow: number, slack = 2): boolean {
    return writtenTop - topNow > slack;
}

/** True when a newest-window response matches the revision already on screen. */
export function newestWindowUnchanged(revision: string, windowRevision?: string, unchanged?: boolean): boolean {
    return unchanged === true || (!!revision && !!windowRevision && windowRevision === revision);
}

/** True when a read belongs to an older request or to a screen that no longer exists. */
export function isStaleHistoryRead(destroyed: boolean, seq: number, currentSeq: number): boolean {
    return destroyed || seq !== currentSeq;
}

/** How far above the top edge older rows are fetched, as a multiple of the viewport, so a page lands while
 * the reader is still heading toward the top. */
const EARLIER_EDGE_VIEWPORTS = 1.5;

/** Whether the reader is inside the distance where older rows are wanted. */
export function atTopOfList(scrollTop: number, viewportHeight: number): boolean {
    return scrollTop <= Math.round(EARLIER_EDGE_VIEWPORTS * viewportHeight);
}

/** A prompt's position, as the prompt read answered it. */
export interface TurnCursor {
    id: string;
    cursor?: string;
}

/** The floor a jump to one prompt names, or '' when no prompt read carried it. Then the caller walks, the
 * only way to fetch the rows in between. */
export function jumpFloor(turns: TurnCursor[], entryId: string): string {
    return turns.find(turn => turn.id === entryId)?.cursor ?? '';
}

/**
 * Pages older until the row a jump named is on screen. The floor rides every page, and a page that answers
 * nothing ends the walk — a nearby row is not the row the caller named.
 */
export async function pageUntilRowLands(
    entryId: string,
    rowOnScreen: (id: string) => boolean,
    loadPage: (floor: string) => Promise<boolean>,
    floor: string,
): Promise<boolean> {
    for (;;) {
        if (rowOnScreen(entryId)) return true;
        if (!(await loadPage(floor))) return false;
    }
}
