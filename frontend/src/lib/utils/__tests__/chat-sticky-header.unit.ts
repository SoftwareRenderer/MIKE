import { describe, it, expect } from 'vitest';
import { computeStickyHeader } from '$lib/utils/chat-sticky-header';

// Ground truth in content space (px offsets from the top of the scroll
// content): user A [0, 120), user B [400, 520), user C [900, 1020).
const A = { entryIdx: 0, firstLine: 'First message', top: 0, bottom: 120 };
const B = { entryIdx: 2, firstLine: 'Second message', top: 400, bottom: 520 };
const C = { entryIdx: 4, firstLine: 'Third message', top: 900, bottom: 1020 };

describe('computeStickyHeader', () => {
    it('hides the header when there are no user messages', () => {
        expect(computeStickyHeader([], 500)).toEqual({ firstLine: '', entryIdx: -1, show: false });
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
        // top exactly at scrollTop counts as scrolled above the viewport
        expect(computeStickyHeader([A, B, C], 900).entryIdx).toBe(4);
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
});
