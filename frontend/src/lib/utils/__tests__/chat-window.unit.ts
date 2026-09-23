import { describe, it, expect } from 'vitest';
import {
    prependRows,
    resolveAnchor,
    anchorScrollDelta,
    readerScrolledUp,
    atTopOfList,
    mergeCachedRows,
    noticeForWindowRead,
    CACHED_ROWS_FLOOR,
    type MeasuredEntry,
} from '../chat-window';

// The decisions that make a conversation growing at the top invisible.

const rows = (...ids: string[]) => ids.map(id => ({ id }));

describe('prependRows', () => {
    it('puts a page of older rows below the rows held', () => {
        expect(prependRows(rows('c', 'd'), rows('a', 'b')).map(r => r.id)).toEqual(['a', 'b', 'c', 'd']);
    });

    it('drops the rows a page repeats, wherever it repeats them', () => {
        // A cursor is a position the caller already has, so the row it names arrives twice.
        expect(prependRows(rows('c', 'd'), rows('a', 'b', 'c')).map(r => r.id)).toEqual(['a', 'b', 'c', 'd']);
        expect(prependRows(rows('c', 'd'), rows('c', 'd')).map(r => r.id)).toEqual(['c', 'd']);
    });

    it('keeps the held array when the page adds nothing', () => {
        const held = rows('a', 'b');
        expect(prependRows(held, [])).toBe(held);
        expect(prependRows(held, rows('a'))).toBe(held);
    });

    it('walks a whole conversation exactly once', () => {
        // Pages arrive newest-first, chronological within themselves.
        const pages = [rows('j', 'k', 'l'), rows('g', 'h', 'i'), rows('d', 'e', 'f'), rows('a', 'b', 'c')];
        let held = pages[0];
        for (const page of pages.slice(1)) held = prependRows(held, page);
        expect(held.map(r => r.id)).toEqual(['a', 'b', 'c', 'd', 'e', 'f', 'g', 'h', 'i', 'j', 'k', 'l']);
    });
});

/** Entry boxes at the offsets a scroller reports, given a scrollTop and a row height. */
function measured(ids: string[], scrollTop: number, rowHeight = 100, viewportTop = 0): MeasuredEntry[] {
    return ids.map((id, i) => ({
        id,
        top: i * rowHeight - scrollTop - viewportTop,
        bottom: (i + 1) * rowHeight - scrollTop - viewportTop,
    }));
}

describe('resolveAnchor', () => {
    it('takes the first entry with pixels below the top edge', () => {
        const entries = measured(['a', 'b', 'c', 'd'], 250);
        const anchor = resolveAnchor(entries);
        // 'a' and 'b' are scrolled past; 'c' is the row the reader is on.
        expect(anchor).toEqual({ id: 'c', top: -50 });
    });

    it('takes the topmost entry when nothing is scrolled past', () => {
        expect(resolveAnchor(measured(['a', 'b'], 0))?.id).toBe('a');
    });

    it('answers null when the list is scrolled entirely past', () => {
        expect(resolveAnchor(measured(['a', 'b'], 500))).toBeNull();
    });

    it('skips a zero-height entry above the viewport', () => {
        const entries: MeasuredEntry[] = [
            { id: 'divider', top: -20, bottom: 0 },
            { id: 'a', top: 0, bottom: 90 },
        ];
        expect(resolveAnchor(entries, 0)?.id).toBe('a');
    });
});

describe('anchorScrollDelta', () => {
    it('moves down by the height of the page that appeared above', () => {
        const anchor = { id: 'c', top: -50 };
        // After the prepend the same entry sits 300px lower.
        expect(anchorScrollDelta(anchor, 250)).toBe(300);
    });

    it('moves nothing when the anchor did not move', () => {
        expect(anchorScrollDelta({ id: 'c', top: 12 }, 12)).toBe(0);
    });
});

describe('readerScrolledUp', () => {
    it('reads an upward move the loop did not write as the reader taking over', () => {
        // A kinetic scroll fires no wheel, touchstart or keydown after the finger lifts: the position is
        // the only signal, so the takeover cannot be left to a gesture listener.
        expect(readerScrolledUp(1000, 954)).toBe(true);
    });

    it('is not moved by the position it wrote, nor by the clamp at the bottom of the list', () => {
        expect(readerScrolledUp(1000, 1000)).toBe(false);
        // Read back after the write: a clamped write is this loop's own doing.
        expect(readerScrolledUp(1000, 1002)).toBe(false);
    });

    it('is not moved by scroll anchoring, which raises scrollTop to hold a row when rows land above', () => {
        expect(readerScrolledUp(1000, 2210)).toBe(false);
    });

    it('absorbs the fractional layout a zoomed or scaled page leaves behind', () => {
        expect(readerScrolledUp(1000, 999)).toBe(false);
        expect(readerScrolledUp(1000, 998)).toBe(false);
        expect(readerScrolledUp(1000, 997)).toBe(true);
    });
});

describe('atTopOfList', () => {
    it('is true within a viewport and a half of the top edge and false below it', () => {
        expect(atTopOfList(0, 600)).toBe(true);
        // The boundaries are literals, so a rule that fetches at another distance fails here.
        expect(atTopOfList(900, 600)).toBe(true);
        expect(atTopOfList(901, 600)).toBe(false);
    });

    it('scales with the viewport, so a short screen prefetches a short distance', () => {
        expect(atTopOfList(600, 400)).toBe(true);
        expect(atTopOfList(601, 400)).toBe(false);
        expect(atTopOfList(1350, 900)).toBe(true);
        expect(atTopOfList(1351, 900)).toBe(false);
    });
});

describe('mergeCachedRows', () => {
    it('keeps the rows the mirror already held above the window just read', () => {
        // An offline reload must not open on less than this browser has already shown.
        const held = rows('a', 'b', 'c', 'd', 'e');
        const fresh = rows('d', 'e', 'f');
        expect(mergeCachedRows(fresh, held).map(r => r.id)).toEqual(['a', 'b', 'c', 'd', 'e', 'f']);
    });

    it('takes the newest of the rows above, bounded by the floor', () => {
        const held = Array.from({ length: 200 }, (_, i) => ({ id: `h${i}` }));
        const fresh = Array.from({ length: 3 }, (_, i) => ({ id: `f${i}` }));
        const merged = mergeCachedRows(fresh, held);
        expect(merged).toHaveLength(CACHED_ROWS_FLOOR);
        expect(merged[0].id).toBe(`h${200 - (CACHED_ROWS_FLOOR - 3)}`);
        expect(merged[merged.length - 1].id).toBe('f2');
    });

    it('leaves a window bigger than the floor alone', () => {
        const fresh = Array.from({ length: CACHED_ROWS_FLOOR + 5 }, (_, i) => ({ id: `f${i}` }));
        const held = rows('a', 'b');
        expect(mergeCachedRows(fresh, held)).toBe(fresh);
    });

    it('is the window itself when the mirror holds nothing new', () => {
        const fresh = rows('d', 'e');
        expect(mergeCachedRows(fresh, [])).toBe(fresh);
        expect(mergeCachedRows(fresh, rows('d', 'e'))).toBe(fresh);
    });
});

describe('noticeForWindowRead', () => {
    const holding = { holdsTaskKey: true, taskInFlight: true };

    it('says nothing when a read answered', () => {
        expect(noticeForWindowRead('answered', holding)).toBeNull();
    });

    it('names the mirror when the read came from it', () => {
        // A transcript drawn from this device is stale, not owed.
        expect(noticeForWindowRead('cached', holding)).toBe('cached');
    });

    it('says the server owes the transcript when a read failed on a running task', () => {
        expect(noticeForWindowRead('failed', holding)).toBe('owing');
    });

    it('says so plainly when nothing can arrive', () => {
        // No key held means no stream to wait on; an idle task means no turn left to arrive.
        expect(noticeForWindowRead('failed', { holdsTaskKey: false, taskInFlight: true })).toBe('failed');
        expect(noticeForWindowRead('failed', { holdsTaskKey: true, taskInFlight: false })).toBe('failed');
    });
});
