/**
 * Shared auto-scroll-to-bottom composable.
 * Tracks whether the scroll container is near the bottom and provides a scroll function.
 * Uses double rAF to correct for DOM shrink (e.g., streaming cursor disappearing).
 *
 * Usage:
 *   // With a getter (scrollEl may be null initially):
 *   const { atBottom, scroll } = useAutoScroll(() => scrollEl);
 *
 *   // Then attach the onScroll handler to the scroll container:
 *   <div onScroll={onScroll}>...</div>
 */
import { SCROLL_BOTTOM_THRESHOLD } from '$lib/constants';

export function useAutoScroll(getScrollEl: () => HTMLElement | null): {
    atBottom: boolean;
    scroll: (force?: boolean) => void;
    onScroll: () => void;
} {
    let atBottom = $state(true);
    let rafPending = $state(false);

    const checkAtBottom = () => {
        const el = getScrollEl();
        if (!el) return;
        const gap = el.scrollHeight - el.scrollTop - el.clientHeight;
        atBottom = gap < SCROLL_BOTTOM_THRESHOLD;
    };

    const scroll = (force = false) => {
        const el = getScrollEl();
        if (!el) return;
        if (!force && !atBottom) return;
        requestAnimationFrame(() => {
            const el2 = getScrollEl();
            if (!el2) return;
            el2.scrollTop = el2.scrollHeight;
            // Second frame to correct for DOM shrink (e.g., streaming cursor disappearing)
            requestAnimationFrame(() => {
                const el3 = getScrollEl();
                if (el3) {
                    el3.scrollTop = el3.scrollHeight;
                }
            });
        });
    };

    // rAF-throttled scroll handler — at most once per frame.
    const onScroll = () => {
        if (rafPending) return;
        rafPending = true;
        requestAnimationFrame(() => {
            rafPending = false;
            checkAtBottom();
        });
    };

    return { atBottom, scroll, onScroll };
}
