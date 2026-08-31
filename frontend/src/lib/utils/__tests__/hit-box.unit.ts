import { describe, it, expect } from 'vitest';
import { inBox, POINTER_MARGIN } from '../hit-box';

/**
 * The one hit test shared by the hover card and the scroll handler: both must
 * agree on when a tooltip is still being pointed at, or one of them dismisses
 * it while the user is reaching for it.
 */
describe('inBox', () => {
    const box = { left: 100, right: 200, top: 50, bottom: 80 };

    it('accepts points inside', () => {
        expect(inBox(box, 150, 60)).toBe(true);
    });

    it('accepts points in the margin, so a 1px edge does not dismiss the tooltip', () => {
        expect(inBox(box, 97, 60, 4)).toBe(true);
        expect(inBox(box, 150, 47, 4)).toBe(true);
    });

    it('rejects points past the margin', () => {
        expect(inBox(box, 95, 60, 4)).toBe(false);
        expect(inBox(box, 205, 60, 4)).toBe(false);
        expect(inBox(box, 150, 85, 4)).toBe(false);
    });

    it('defaults to the shared margin', () => {
        expect(inBox(box, 100 - POINTER_MARGIN, 60)).toBe(true);
        expect(inBox(box, 100 - POINTER_MARGIN - 1, 60)).toBe(false);
    });
});
