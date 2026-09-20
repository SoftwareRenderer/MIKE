import { describe, it, expect } from 'vitest';
import {
    prependRows,
    resolveAnchor,
    anchorScrollDelta,
    atTopOfList,
    EARLIER_EDGE_PX,
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

describe('atTopOfList', () => {
    it('is true within the edge distance and false below it', () => {
        expect(atTopOfList(0)).toBe(true);
        expect(atTopOfList(EARLIER_EDGE_PX)).toBe(true);
        expect(atTopOfList(EARLIER_EDGE_PX + 1)).toBe(false);
    });
});
