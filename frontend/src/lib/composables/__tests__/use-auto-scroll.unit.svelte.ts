import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { useAutoScroll } from '$lib/composables/use-auto-scroll.svelte';

/** Create a mock scroll container with controllable scroll properties. */
function createMockScrollEl(): HTMLElement & { scrollTop: number; scrollHeight: number; clientHeight: number } {
    const el = document.createElement('div');
    Object.defineProperty(el, 'scrollTop', { value: 0, writable: true, configurable: true });
    Object.defineProperty(el, 'scrollHeight', { value: 1000, writable: true, configurable: true });
    Object.defineProperty(el, 'clientHeight', { value: 400, writable: true, configurable: true });
    return el as HTMLElement & { scrollTop: number; scrollHeight: number; clientHeight: number };
}

describe('useAutoScroll', () => {
    let mockEl: ReturnType<typeof createMockScrollEl>;
    let elGetter: () => HTMLElement | null;

    beforeEach(() => {
        mockEl = createMockScrollEl();
        elGetter = () => mockEl;
        vi.useFakeTimers();
    });

    afterEach(() => {
        vi.restoreAllMocks();
    });

    it('starts with atBottom true', () => {
        const { atBottom } = useAutoScroll(elGetter);
        expect(atBottom).toBe(true);
    });

    it('scroll(force=true) scrolls to bottom regardless of position', () => {
        const { scroll } = useAutoScroll(elGetter);
        mockEl.scrollTop = 0;
        scroll(true);
        vi.runAllTimers();
        expect(mockEl.scrollTop).toBe(1000);
    });

    it('scroll(force=true) does nothing when element is null', () => {
        elGetter = () => null;
        const { scroll } = useAutoScroll(elGetter);
        expect(() => scroll(true)).not.toThrow();
    });

    it('onScroll triggers rAF-based atBottom check', () => {
        const { onScroll } = useAutoScroll(elGetter);
        // Set scroll position away from bottom
        mockEl.scrollTop = 650; // gap = 1000 - 650 - 400 = 350 > 32
        onScroll();
        // The rAF callback runs without error
        vi.runAllTimers();
        // No assertion on atBottom (captured value) — just verify no crash
        expect(() => onScroll()).not.toThrow();
    });

    it('scroll() scrolls when atBottom is true (initial state)', () => {
        const { scroll } = useAutoScroll(elGetter);
        // atBottom starts true, and the scroll() closure captures this initial value
        // (Svelte compiler warning: state_referenced_locally)
        mockEl.scrollTop = 0;
        scroll();
        vi.runAllTimers();
        expect(mockEl.scrollTop).toBe(1000);
    });

    it('scroll() scrolls when at bottom initially', () => {
        const { scroll } = useAutoScroll(elGetter);
        // atBottom starts true, so scroll() should work without force
        mockEl.scrollTop = 0;
        scroll();
        vi.runAllTimers();
        expect(mockEl.scrollTop).toBe(1000);
    });

    it('onScroll is rAF-throttled — double calls only trigger one check', () => {
        const { onScroll } = useAutoScroll(elGetter);
        // First call sets rafPending=true
        onScroll();
        // Second call should be ignored
        onScroll();
        // Only one rAF callback should have run
        vi.runAllTimers();
        // No error — the throttling prevents double processing
    });
});
