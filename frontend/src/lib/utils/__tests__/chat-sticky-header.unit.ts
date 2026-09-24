import { describe, it, expect } from 'vitest';
import { computeStickyHeader, turnLabel, type StickyTurn } from '$lib/utils/chat-sticky-header';

// Ground truth in content space (px offsets from the top of the scroll
// content): user A [0, 120), user B [400, 520), user C [900, 1020).
const A = { entryId: 'u-a', entryIdx: 0, firstLine: 'First message', top: 0, bottom: 120 };
const B = { entryId: 'u-b', entryIdx: 2, firstLine: 'Second message', top: 400, bottom: 520 };
const C = { entryId: 'u-c', entryIdx: 4, firstLine: 'Third message', top: 900, bottom: 1020 };

/** A prompt older than every rendered row: named by the turn read, not by the document. */
function older(entryId: string, firstLine: string): StickyTurn {
    return { entryId, firstLine, entryIdx: -1, top: 0, bottom: 0 };
}

describe('computeStickyHeader', () => {
    it('hides the header when there are no user messages', () => {
        expect(computeStickyHeader([], 500)).toEqual({ firstLine: '', entryId: '', show: false, needsRows: false });
    });

    it('hides the header while the first user message is still in view', () => {
        const r = computeStickyHeader([A, B, C], 0);
        expect(r.show).toBe(false);
    });

    it('shows the latest user message above the viewport once the first has left it', () => {
        // scrollTop 200: A (bottom 120) is fully out; B (top 400) has not scrolled above yet.
        const r = computeStickyHeader([A, B, C], 200);
        expect(r.show).toBe(true);
        expect(r.firstLine).toBe('First message');
        expect(r.entryId).toBe('u-a');
    });

    it('follows the newest user message as it crosses the viewport top', () => {
        expect(computeStickyHeader([A, B, C], 450).firstLine).toBe('Second message');
        expect(computeStickyHeader([A, B, C], 450).entryId).toBe('u-b');
        // A message resting exactly on the line is the one AT the top, so the header
        // already holds the next one back instead of naming it again.
        expect(computeStickyHeader([A, B, C], 900).entryId).toBe('u-b');
        // Past it, it becomes the message the header offers to jump back to.
        expect(computeStickyHeader([A, B, C], 950).firstLine).toBe('Third message');
    });

    it('targets the message above the viewport, not a later duplicate first line', () => {
        const dup1 = { entryId: 'u-dup1', entryIdx: 2, firstLine: 'Fix the bug', top: 100, bottom: 200 };
        const dup2 = { entryId: 'u-dup2', entryIdx: 5, firstLine: 'Fix the bug', top: 300, bottom: 400 };
        // At scrollTop 250 the message in the header is dup1 — even though a
        // later user message repeats the same first line.
        const r = computeStickyHeader([A, dup1, dup2], 250);
        expect(r.firstLine).toBe('Fix the bug');
        expect(r.entryId).toBe('u-dup1');
    });

    it('keeps the header hidden until the first user message is fully out of view', () => {
        const tallA = { entryId: 'u-a', entryIdx: 0, firstLine: 'Tall message', top: 0, bottom: 10000 };
        const b = { entryId: 'u-b', entryIdx: 2, firstLine: 'After tall', top: 10100, bottom: 10200 };
        expect(computeStickyHeader([tallA, b], 5000).show).toBe(false);
    });

    it('shows the first message alone when it is the only user message', () => {
        const r = computeStickyHeader([A], 121);
        expect(r.show).toBe(true);
        expect(r.firstLine).toBe('First message');
        expect(r.entryId).toBe('u-a');
    });

    // The sticky line is the overlay's bottom edge (46px here): a tapped message
    // lands on it and the header selects against it.
    const INSET = 46;
    /** scrollTop that rests a message with the given content top on the line. */
    const landedAt = (top: number) => top - INSET;

    it('hands the header to the message before the one it just landed', () => {
        // B is on the line, so B is the message at the top and the header shows A.
        const r = computeStickyHeader([A, B, C], landedAt(B.top), INSET);
        expect(r.show).toBe(true);
        expect(r.entryId).toBe('u-a');
        expect(r.firstLine).toBe('First message');
    });

    it('still names a message the user has scrolled past, to jump back to it', () => {
        // B's top is 50px above the line, so its own first line is off screen.
        expect(computeStickyHeader([A, B, C], 450, INSET).entryId).toBe('u-b');
    });

    it('treats a landing the browser nudged as resting on the line', () => {
        // 8px is the measured scroll-anchoring shift after the settle loop returns; naming B here would
        // make the next tap land B again.
        expect(computeStickyHeader([A, B, C], landedAt(B.top) + 8, INSET).entryId).toBe('u-a');
        expect(computeStickyHeader([A, B, C], landedAt(B.top) + 20, INSET).entryId).toBe('u-b');
    });

    it('never names a message still below the line', () => {
        // B (top 400) is on screen at both positions, so the header holds A.
        expect(computeStickyHeader([A, B, C], 350, INSET).entryId).toBe('u-a');
        expect(computeStickyHeader([A, B, C], 300, INSET).entryId).toBe('u-a');
    });

    it('keeps the header hidden at the top of the conversation even with a line', () => {
        // Jumping to the first message clamps to scrollTop 0.
        const r = computeStickyHeader([A, B, C], 0, INSET);
        expect(r.show).toBe(false);
    });

    // A message can end up more than 12px above the line, past the tolerance, when the
    // content above it changes height as it is drawn.
    describe('after a tap jumped to a message', () => {
        const overshot = landedAt(B.top) + 30;

        it('names the message before the one it jumped to, past the tolerance', () => {
            expect(computeStickyHeader([A, B, C], landedAt(B.top), INSET, B.entryId).entryId).toBe('u-a');
            expect(computeStickyHeader([A, B, C], overshot, INSET, B.entryId).entryId).toBe('u-a');
            expect(computeStickyHeader([A, B, C], overshot, INSET, B.entryId).firstLine).toBe('First message');
        });

        it('keeps offering the earlier message while the tapped one is the one above the line', () => {
            for (const scrollTop of [354, 370, overshot]) {
                expect(computeStickyHeader([A, B, C], scrollTop, INSET, B.entryId).entryId).toBe('u-a');
            }
        });

        it('hides the header when the message it jumped to has nothing before it', () => {
            // A is the first message, so there is no earlier turn to offer.
            const r = computeStickyHeader([A, B, C], 130, INSET, A.entryId);
            expect(r.show).toBe(false);
            expect(r.entryId).toBe('');
        });

        it('names the tapped message again once a later one is above the line', () => {
            // C is the message above the line now, so the tap on B no longer applies.
            expect(computeStickyHeader([A, B, C], landedAt(C.top) + 30, INSET, B.entryId).entryId).toBe('u-c');
        });
    });

    // Why a page with no prompt in it matters: a window counted in answers routinely holds none, and the
    // prompt read names the one that opened the turn the window is inside.
    describe('the prompt the window left out', () => {
        const opened = older('u-open', 'work through the tree');

        it('names it and says the jump has to fetch first', () => {
            // Nothing rendered is above the line, so the newest unrendered prompt is the one above.
            const r = computeStickyHeader([opened], 5000, INSET);
            expect(r.show).toBe(true);
            expect(r.firstLine).toBe('work through the tree');
            expect(r.entryId).toBe('u-open');
            expect(r.needsRows).toBe(true);
        });

        it('names it while the reader follows the answer under it', () => {
            // The header is the question the visible answers are answering, so it shows at scrollTop 0
            // too: the whole turn is on screen and its prompt is not.
            const r = computeStickyHeader([opened], 0, INSET);
            expect(r.show).toBe(true);
            expect(r.entryId).toBe('u-open');
        });

        it('prefers a rendered message once one has crossed the line', () => {
            const rendered = { ...A, entryId: 'u-later' };
            const r = computeStickyHeader([opened, rendered], 500, INSET);
            expect(r.entryId).toBe('u-later');
            expect(r.needsRows).toBe(false);
        });

        it('offers the older prompt again once a tap has landed it', () => {
            const r = computeStickyHeader([older('u-first', 'the first prompt'), opened], 5000, INSET, opened.entryId);
            expect(r.entryId).toBe('u-first');
            expect(r.needsRows).toBe(true);
        });

        it('names nothing when the turn the reader is inside is the conversation itself', () => {
            // Every prompt of this conversation is on screen and none has crossed the line.
            expect(computeStickyHeader([A, B], 60, INSET).show).toBe(false);
        });
    });

    describe('turnLabel', () => {
        it('takes the first line of a prompt however it arrived', () => {
            expect(turnLabel('Fix the login bug\nwith details')).toBe('Fix the login bug');
            expect(turnLabel('one line')).toBe('one line');
            expect(turnLabel('')).toBe('');
        });
    });
});
