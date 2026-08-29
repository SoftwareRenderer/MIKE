import { describe, it, expect, afterEach } from 'vitest';
import { isFullyVisibleIn, verticalScrollParent } from '../scroll-visibility';

describe('isFullyVisibleIn', () => {
    const container = { top: 100, bottom: 400 };

    it('is true for a target strictly inside the container', () => {
        expect(isFullyVisibleIn(container, { top: 120, bottom: 200 })).toBe(true);
    });

    it('is true when the target fills the container exactly', () => {
        expect(isFullyVisibleIn(container, { top: 100, bottom: 400 })).toBe(true);
    });

    it('allows sub-pixel overhang (browser zoom / fractional layout)', () => {
        expect(isFullyVisibleIn(container, { top: 99.9, bottom: 400.2 })).toBe(true);
    });

    it('is false when the top edge is clipped', () => {
        expect(isFullyVisibleIn(container, { top: 60, bottom: 200 })).toBe(false);
    });

    it('is false when the bottom edge is clipped', () => {
        // A card below the fold — this is the case the reveal must still scroll to.
        expect(isFullyVisibleIn(container, { top: 380, bottom: 460 })).toBe(false);
    });

    it('is false when the target is entirely outside', () => {
        expect(isFullyVisibleIn(container, { top: 500, bottom: 560 })).toBe(false);
        expect(isFullyVisibleIn(container, { top: 20, bottom: 80 })).toBe(false);
    });

    it('honours a custom slack', () => {
        expect(isFullyVisibleIn(container, { top: 95, bottom: 400 }, 6)).toBe(true);
        expect(isFullyVisibleIn(container, { top: 95, bottom: 400 })).toBe(false);
    });
});

describe('verticalScrollParent', () => {
    afterEach(() => {
        // The stubs below are own properties of HTMLElement.prototype; removing
        // them restores the inherited jsdom getters on Element.prototype.
        delete (HTMLElement.prototype as unknown as { scrollHeight?: number }).scrollHeight;
        delete (HTMLElement.prototype as unknown as { clientHeight?: number }).clientHeight;
    });

    /** A parent that scrolls and has real content beyond its viewport. */
    function buildTree(): { scroller: HTMLDivElement; card: HTMLDivElement } {
        const scroller = document.createElement('div');
        scroller.style.overflowY = 'auto';
        const inner = document.createElement('div');
        const card = document.createElement('div');
        card.className = 'card';
        inner.appendChild(card);
        scroller.appendChild(inner);
        document.body.appendChild(scroller);
        return { scroller, card };
    }

    function stubHeights(scrollHeight: number, clientHeight: number) {
        Object.defineProperty(HTMLElement.prototype, 'scrollHeight', { configurable: true, get: () => scrollHeight });
        Object.defineProperty(HTMLElement.prototype, 'clientHeight', { configurable: true, get: () => clientHeight });
    }

    it('finds the nearest ancestor that scrolls vertically', () => {
        const { scroller, card } = buildTree();
        stubHeights(1000, 300);
        expect(verticalScrollParent(card)).toBe(scroller);
        scroller.remove();
    });

    it('skips non-scrolling ancestors', () => {
        const { scroller, card } = buildTree();
        stubHeights(1000, 300);
        const inner = card.parentElement as HTMLDivElement;
        expect(verticalScrollParent(card)).not.toBe(inner);
        scroller.remove();
    });

    it('returns null when the ancestor has no scroll range', () => {
        const { scroller, card } = buildTree();
        stubHeights(300, 300); // fits: nothing to scroll
        expect(verticalScrollParent(card)).toBeNull();
        scroller.remove();
    });

    it('returns null with no scrollable ancestor at all (jsdom default)', () => {
        const card = document.createElement('div');
        const wrap = document.createElement('div');
        wrap.appendChild(card);
        document.body.appendChild(wrap);
        expect(verticalScrollParent(card)).toBeNull();
        wrap.remove();
    });
});
