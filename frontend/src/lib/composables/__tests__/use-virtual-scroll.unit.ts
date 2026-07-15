import { describe, it, expect } from 'vitest';
import { computeVirtualScrollRange } from '$lib/composables/use-virtual-scroll.svelte';

describe('computeVirtualScrollRange', () => {
    it('returns correct state for empty list', () => {
        const result = computeVirtualScrollRange(0, () => 50, 0, 400, 3);
        expect(result.startIdx).toBe(0);
        expect(result.endIdx).toBe(0);
        expect(result.totalHeight).toBe(0);
        expect(result.paddingTop).toBe(0);
        expect(result.paddingBottom).toBe(0);
    });

    it('computes total height from uniform estimated heights', () => {
        const result = computeVirtualScrollRange(10, () => 50, 0, 400, 3);
        expect(result.totalHeight).toBe(500);
    });

    it('computes total height from variable estimated heights', () => {
        const heights = [60, 80, 40, 100, 50];
        const result = computeVirtualScrollRange(5, (i) => heights[i], 0, 400, 3);
        expect(result.totalHeight).toBe(330);
    });

    it('computes visible range for items at the top', () => {
        // Container: 400px tall, scroll at 0
        // Items: 100 items × 50px = 5000px total
        const result = computeVirtualScrollRange(100, () => 50, 0, 400, 3);
        expect(result.totalHeight).toBe(5000);
        // Visible: 0-400px = items 0-7 (8 items)
        // With overscan 3: startIdx=0, endIdx=11
        expect(result.startIdx).toBe(0);
        expect(result.endIdx).toBe(11);
    });

    it('computes visible range for items in the middle', () => {
        // Scroll to middle: scrollTop = 2000
        const result = computeVirtualScrollRange(100, () => 50, 2000, 400, 3);
        // Viewport: 2000-2400px
        // Item at index 40: offset 2000, bottom 2050
        // Item at index 47: offset 2350, bottom 2400
        // Visible: items 40-47 (8 items)
        // With overscan 3: startIdx=37, endIdx=51
        expect(result.startIdx).toBe(37);
        expect(result.endIdx).toBe(51);
    });

    it('computes visible range for items at the bottom', () => {
        // Scroll to bottom: scrollTop = 4600 (5000 - 400)
        const result = computeVirtualScrollRange(100, () => 50, 4600, 400, 3);
        // Viewport: 4600-5000px
        // Item at index 92: offset 4600, bottom 4650
        // Item at index 99: offset 4950, bottom 5000
        // Visible: items 92-99 (8 items)
        // With overscan 3: startIdx=89, endIdx=100 (capped)
        expect(result.startIdx).toBe(89);
        expect(result.endIdx).toBe(100);
    });

    it('computes correct item offsets', () => {
        const heights = [60, 80, 40, 100, 50];
        const result = computeVirtualScrollRange(5, (i) => heights[i], 0, 400, 3);
        expect(result.offsets[0]).toBe(0);
        expect(result.offsets[1]).toBe(60);
        expect(result.offsets[2]).toBe(140);
        expect(result.offsets[3]).toBe(180);
        expect(result.offsets[4]).toBe(280);
        expect(result.offsets[5]).toBe(330);
    });

    it('handles measured heights (simulated via getHeight)', () => {
        // Start with estimated heights
        const estimated = [60, 80, 40, 100, 50];
        // After measurement, item 1 is actually 120px instead of 80px
        const measured = [60, 120, 40, 100, 50];
        const result = computeVirtualScrollRange(5, (i) => measured[i], 0, 400, 3);
        expect(result.totalHeight).toBe(370);
        expect(result.offsets[0]).toBe(0);
        expect(result.offsets[1]).toBe(60);
        expect(result.offsets[2]).toBe(180); // 60 + 120
        expect(result.offsets[3]).toBe(220); // 180 + 40
        expect(result.offsets[4]).toBe(320); // 220 + 100
    });

    it('handles out-of-bounds getHeight gracefully', () => {
        // getHeight should only be called for valid indices
        const heights = [60, 80, 40];
        const result = computeVirtualScrollRange(3, (i) => heights[i], 0, 400, 3);
        expect(result.totalHeight).toBe(180);
        expect(result.startIdx).toBe(0);
        expect(result.endIdx).toBe(3);
    });

    it('handles zero-height items', () => {
        const heights = [60, 0, 40, 100, 50];
        const result = computeVirtualScrollRange(5, (i) => heights[i], 0, 400, 3);
        expect(result.totalHeight).toBe(250);
        expect(result.offsets[2]).toBe(60); // item 1 has 0 height
    });

    it('handles single item that fills the viewport', () => {
        const result = computeVirtualScrollRange(1, () => 400, 0, 400, 3);
        expect(result.totalHeight).toBe(400);
        expect(result.startIdx).toBe(0);
        expect(result.endIdx).toBe(1);
    });

    it('handles single item larger than viewport', () => {
        const result = computeVirtualScrollRange(1, () => 800, 0, 400, 3);
        expect(result.totalHeight).toBe(800);
        expect(result.startIdx).toBe(0);
        expect(result.endIdx).toBe(1);
    });

    it('handles single item smaller than viewport', () => {
        const result = computeVirtualScrollRange(1, () => 100, 0, 400, 3);
        expect(result.totalHeight).toBe(100);
        expect(result.startIdx).toBe(0);
        expect(result.endIdx).toBe(1);
    });

    it('clamps startIdx to 0 when scrolling near top', () => {
        const result = computeVirtualScrollRange(100, () => 50, 10, 400, 3);
        expect(result.startIdx).toBe(0);
    });

    it('clamps endIdx to itemCount when scrolling near bottom', () => {
        const result = computeVirtualScrollRange(100, () => 50, 4950, 400, 3);
        expect(result.endIdx).toBe(100);
    });

    it('computes padding correctly', () => {
        const result = computeVirtualScrollRange(100, () => 50, 2000, 400, 3);
        // startIdx=37, so paddingTop = 37 * 50 = 1850
        expect(result.paddingTop).toBe(1850);
        // endIdx=51, so paddingBottom = 5000 - 51*50 = 2450
        expect(result.paddingBottom).toBe(2450);
    });

    it('handles large lists efficiently', () => {
        // 10000 items × 50px = 500000px total
        const result = computeVirtualScrollRange(10000, () => 50, 250000, 400, 3);
        expect(result.totalHeight).toBe(500000);
        // Viewport: 250000-250400
        // Item 5000: offset 250000
        // Item 5007: offset 250350
        // Visible: ~5000-5007
        expect(result.startIdx).toBeGreaterThanOrEqual(4995);
        expect(result.endIdx).toBeLessThanOrEqual(5015);
    });

    it('handles variable heights with correct binary search', () => {
        // Items with varying heights: [100, 200, 50, 300, 75, 150]
        const heights = [100, 200, 50, 300, 75, 150];
        // Total: 875px
        // Scroll to 400px (between items 2 and 3)
        const result = computeVirtualScrollRange(6, (i) => heights[i], 400, 200, 2);
        // Viewport: 400-600
        // Item 3 starts at offset 350, bottom at 650 → visible
        // Item 4 starts at offset 650 → not visible
        expect(result.totalHeight).toBe(875);
        expect(result.offsets[3]).toBe(350); // 100+200+50
        expect(result.offsets[4]).toBe(650); // 350+300
    });

    it('handles zero overscan', () => {
        const result = computeVirtualScrollRange(100, () => 50, 2000, 400, 0);
        // Without overscan, exactly the visible items
        expect(result.startIdx).toBe(40);
        expect(result.endIdx).toBe(48);
    });
});
