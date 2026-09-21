import { describe, it, expect } from 'vitest';
import { computeStickyHeader } from '$lib/utils/chat-sticky-header';

// Ground truth in content space (px offsets from the top of the scroll
// content): user A [0, 120), user B [400, 520), user C [900, 1020).
const A = { entryIdx: 0, firstLine: 'First message', top: 0, bottom: 120 };
const B = { entryIdx: 2, firstLine: 'Second message', top: 400, bottom: 520 };
const C = { entryIdx: 4, firstLine: 'Third message', top: 900, bottom: 1020 };

describe('computeStickyHeader', () => {
    it('hides the header when there are no user messages', () => {
        expect(computeStickyHeader([], 500)).toEqual({ firstLine: '', entryIdx: -1, show: false, needsEarlier: false });
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
        expect(r.entryIdx).toBe(0);
    });

    it('follows the newest user message as it crosses the viewport top', () => {
        expect(computeStickyHeader([A, B, C], 450).firstLine).toBe('Second message');
        expect(computeStickyHeader([A, B, C], 450).entryIdx).toBe(2);
        // A message resting exactly on the line is the one AT the top, so the header
        // already holds the next one back instead of naming it again.
        expect(computeStickyHeader([A, B, C], 900).entryIdx).toBe(2);
        // Past it, it becomes the message the header offers to jump back to.
        expect(computeStickyHeader([A, B, C], 950).firstLine).toBe('Third message');
    });

    it('targets the message above the viewport, not a later duplicate first line', () => {
        const dup1 = { entryIdx: 2, firstLine: 'Fix the bug', top: 100, bottom: 200 };
        const dup2 = { entryIdx: 5, firstLine: 'Fix the bug', top: 300, bottom: 400 };
        // At scrollTop 250 the message in the header is dup1 — even though a
        // later user message repeats the same first line.
        const r = computeStickyHeader([A, dup1, dup2], 250);
        expect(r.firstLine).toBe('Fix the bug');
        expect(r.entryIdx).toBe(2);
    });

    it('keeps the header hidden until the first user message is fully out of view', () => {
        const tallA = { entryIdx: 0, firstLine: 'Tall message', top: 0, bottom: 10000 };
        const b = { entryIdx: 2, firstLine: 'After tall', top: 10100, bottom: 10200 };
        expect(computeStickyHeader([tallA, b], 5000).show).toBe(false);
    });

    it('shows the first message alone when it is the only user message', () => {
        const r = computeStickyHeader([A], 121);
        expect(r.show).toBe(true);
        expect(r.firstLine).toBe('First message');
        expect(r.entryIdx).toBe(0);
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
        expect(r.entryIdx).toBe(0);
        expect(r.firstLine).toBe('First message');
    });

    it('still names a message the user has scrolled past, to jump back to it', () => {
        // B's top is 50px above the line, so its own first line is off screen.
        expect(computeStickyHeader([A, B, C], 450, INSET).entryIdx).toBe(2);
    });

    it('treats a landing the browser nudged as resting on the line', () => {
        // 8px is the measured scroll-anchoring shift after the settle loop returns;
        // naming B here would make the next tap land B again.
        expect(computeStickyHeader([A, B, C], landedAt(B.top) + 8, INSET).entryIdx).toBe(0);
        expect(computeStickyHeader([A, B, C], landedAt(B.top) + 20, INSET).entryIdx).toBe(2);
    });

    it('never names a message still below the line', () => {
        // B (top 400) is on screen at both positions, so the header holds A.
        expect(computeStickyHeader([A, B, C], 350, INSET).entryIdx).toBe(0);
        expect(computeStickyHeader([A, B, C], 300, INSET).entryIdx).toBe(0);
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
            expect(computeStickyHeader([A, B, C], landedAt(B.top), INSET, B.entryIdx).entryIdx).toBe(0);
            expect(computeStickyHeader([A, B, C], overshot, INSET, B.entryIdx).entryIdx).toBe(0);
            expect(computeStickyHeader([A, B, C], overshot, INSET, B.entryIdx).firstLine).toBe('First message');
        });

        it('keeps offering the earlier message while the tapped one is the one above the line', () => {
            for (const scrollTop of [354, 370, overshot]) {
                expect(computeStickyHeader([A, B, C], scrollTop, INSET, B.entryIdx).entryIdx).toBe(0);
            }
        });

        it('hides the header when the message it jumped to has nothing before it', () => {
            // A is the first message, so there is no earlier turn to offer.
            const r = computeStickyHeader([A, B, C], 130, INSET, A.entryIdx);
            expect(r.show).toBe(false);
            expect(r.entryIdx).toBe(-1);
        });

        it('names the tapped message again once a later one is above the line', () => {
            // C is the message above the line now, so the tap on B no longer applies.
            expect(computeStickyHeader([A, B, C], landedAt(C.top) + 30, INSET, B.entryIdx).entryIdx).toBe(4);
        });
    });

    // Why a page with one user message matters: see `StickyHeaderState.needsEarlier`.
    describe('beyond the loaded window', () => {
        const alpha = { entryIdx: 0, firstLine: 'Alpha request', top: 300, bottom: 900 };
        const beta = { entryIdx: 2, firstLine: 'Bravo request', top: 1000, bottom: 1120 };

        it('hides at the start of the conversation, which a page edge is not', () => {
            // Alpha has not scrolled out, so the header would overlap it.
            expect(computeStickyHeader([alpha], 500, INSET).show).toBe(false);
            // Older rows are unfetched, so this is only where the fetch happened to start.
            const r = computeStickyHeader([alpha], 500, INSET, -1, true);
            expect(r.show).toBe(true);
            expect(r.entryIdx).toBe(0);
        });

        it('asks for older rows when the oldest loaded one is the message above the line', () => {
            expect(computeStickyHeader([alpha, beta], 500, INSET, -1, true).needsEarlier).toBe(true);
        });

        it('asks for nothing while the oldest loaded message is still below the line', () => {
            const r = computeStickyHeader([alpha, beta], 100, INSET, -1, true);
            expect(r.show).toBe(false);
            expect(r.needsEarlier).toBe(false);
        });

        it('asks for nothing when the window holds the whole conversation', () => {
            expect(computeStickyHeader([alpha, beta], 500, INSET).needsEarlier).toBe(false);
        });

        it('asks for older rows when the tap landed the oldest loaded message', () => {
            // Alpha rests on the line and nothing older is rendered, so the header is empty and the
            // next step back is a row nobody has fetched.
            const r = computeStickyHeader([alpha, beta], landedAt(alpha.top) + 30, INSET, alpha.entryIdx, true);
            expect(r.show).toBe(false);
            expect(r.needsEarlier).toBe(true);
        });
    });
});
