/**
 * Virtual scrolling composable for efficiently rendering long lists.
 *
 * Computes which items are visible in a scroll container based on scroll
 * position and item heights. Supports dynamic item heights via measurement
 * cache (estimated heights on first render, measured heights after).
 *
 * Usage:
 *   const vs = useVirtualScroll(
 *     () => scrollEl,                    // container ref getter
 *     entries.length,                    // total item count
 *     (i) => getEstimatedHeight(entries[i], i),  // estimated height per index
 *     3,                                 // overscan buffer count
 *   );
 *
 *   // In template:
 *   // - Render a spacer div with height = vs.totalHeight
 *   // - Render items from vs.startIdx to vs.endIdx with translateY offsets
 *   // - Call vs.measureItem(index, height) when an item is measured
 */
export interface VirtualScrollState {
    /** First visible item index (exclusive of overscan) */
    startIdx: number;
    /** Last visible item index + 1 (exclusive of overscan) */
    endIdx: number;
    /** CSS padding-top to push visible items down */
    paddingTop: number;
    /** CSS padding-bottom to maintain scroll height below visible items */
    paddingBottom: number;
    /** Total virtual height of all items */
    totalHeight: number;
    /**
     * Record the measured height of an item.
     * Future height lookups for this index will use the measured value.
     */
    measureItem: (index: number, height: number) => void;
    /** Get the cumulative Y offset of an item (sum of heights of all preceding items). */
    getItemOffset: (index: number) => number;
}

/**
 * Pure computation: calculate visible range and offsets for a virtual scroll list.
 * This function is framework-agnostic and can be tested independently.
 *
 * @param itemCount       - Total number of items
 * @param getHeight       - Function to get height for an item by index
 * @param scrollTop       - Current scroll position
 * @param clientHeight    - Visible container height
 * @param overscan        - Number of extra items to render above/below
 */
export function computeVirtualScrollRange(
    itemCount: number,
    getHeight: (index: number) => number,
    scrollTop: number,
    clientHeight: number,
    overscan: number,
): {
    startIdx: number;
    endIdx: number;
    paddingTop: number;
    paddingBottom: number;
    totalHeight: number;
    offsets: number[];
} {
    // Compute cumulative offsets
    const offsets = new Array(itemCount + 1);
    offsets[0] = 0;
    for (let i = 0; i < itemCount; i++) {
        offsets[i + 1] = offsets[i] + getHeight(i);
    }
    const totalHeight = offsets[itemCount];

    if (itemCount === 0) {
        return { startIdx: 0, endIdx: 0, paddingTop: 0, paddingBottom: 0, totalHeight: 0, offsets };
    }

    const viewTop = scrollTop;
    const viewBottom = scrollTop + clientHeight;

    // Binary search for the first item whose bottom is > viewTop
    let lo = 0, hi = itemCount;
    while (lo < hi) {
        const mid = (lo + hi) >> 1;
        if (offsets[mid] + getHeight(mid) <= viewTop) {
            lo = mid + 1;
        } else {
            hi = mid;
        }
    }
    const firstVisible = lo;

    // Binary search for the last item whose top is < viewBottom
    lo = firstVisible;
    hi = itemCount;
    while (lo < hi) {
        const mid = (lo + hi) >> 1;
        if (offsets[mid] < viewBottom) {
            lo = mid + 1;
        } else {
            hi = mid;
        }
    }
    const lastVisible = lo;

    // Apply overscan
    const startIdx = Math.max(0, firstVisible - overscan);
    const endIdx = Math.min(itemCount, lastVisible + overscan);

    return {
        startIdx,
        endIdx,
        paddingTop: offsets[startIdx],
        paddingBottom: totalHeight - offsets[endIdx],
        totalHeight,
        offsets,
    };
}

/**
 * Create a virtual scroll controller.
 *
 * @param containerRef  - Getter for the scroll container element
 * @param itemCount     - Total number of items in the list (or getter)
 * @param getEstimatedHeight - Function to get the estimated height for an item by index
 * @param overscan      - Number of extra items to render above/below the visible area
 */
export function useVirtualScroll(
    containerRef: () => HTMLElement | null,
    itemCount: number | (() => number),
    getEstimatedHeight: (index: number) => number,
    overscan: number = 3,
): VirtualScrollState {
    // Cache of measured heights, keyed by index.
    let measuredHeights = $state(new Map<number, number>());

    /** Get the current item count (supports both number and getter). */
    function getCount(): number {
        return typeof itemCount === 'function' ? itemCount() : itemCount;
    }

    /** Get the height for an item (measured if available, estimated otherwise). */
    function getHeight(index: number): number {
        const measured = measuredHeights.get(index);
        if (measured !== undefined) return measured;
        return getEstimatedHeight(index);
    }

    /** Cumulative offset cache — recomputed when heights change. */
    let offsets = $state(new Float64Array(getCount() + 1));
    let totalHeight = $state(0);
    let dirty = $state(true);

    /** Recompute cumulative offsets and total height. */
    function recomputeOffsets() {
        if (!dirty) return;
        const n = getCount();
        // Extend the offsets array if itemCount grew
        if (offsets.length < n + 1) {
            const newOffsets = new Float64Array(Math.max(n + 1, offsets.length * 2));
            newOffsets.set(offsets);
            offsets = newOffsets;
        }
        let sum = 0;
        offsets[0] = 0;
        for (let i = 0; i < n; i++) {
            sum += getHeight(i);
            offsets[i + 1] = sum;
        }
        totalHeight = sum;
        dirty = false;
    }

    // Reactive state
    let startIdx = $state(0);
    let endIdx = $state(0);
    let paddingTop = $state(0);
    let paddingBottom = $state(0);

    /** Recompute the visible range based on current scroll position. */
    function computeVisibleRange() {
        const el = containerRef();
        const n = getCount();
        if (!el || n === 0) {
            startIdx = 0;
            endIdx = 0;
            paddingTop = 0;
            paddingBottom = 0;
            return;
        }

        recomputeOffsets();

        const scrollTop = el.scrollTop;
        const clientHeight = el.clientHeight;
        const viewTop = scrollTop;
        const viewBottom = scrollTop + clientHeight;

        // Binary search for the first item whose bottom is > viewTop
        let lo = 0, hi = n;
        while (lo < hi) {
            const mid = (lo + hi) >> 1;
            if (offsets[mid] + getHeight(mid) <= viewTop) {
                lo = mid + 1;
            } else {
                hi = mid;
            }
        }
        const firstVisible = lo;

        // Binary search for the last item whose top is < viewBottom
        lo = firstVisible;
        hi = n;
        while (lo < hi) {
            const mid = (lo + hi) >> 1;
            if (offsets[mid] < viewBottom) {
                lo = mid + 1;
            } else {
                hi = mid;
            }
        }
        const lastVisible = lo;

        // Apply overscan
        startIdx = Math.max(0, firstVisible - overscan);
        endIdx = Math.min(n, lastVisible + overscan);

        // Padding: offset of the first rendered item
        paddingTop = offsets[startIdx];
        // Padding: remaining height after the last rendered item
        paddingBottom = totalHeight - offsets[endIdx];
    }

    // Scroll handler — rAF-throttled to avoid excessive recomputation
    let rafPending = false;
    const onScroll = () => {
        if (rafPending) return;
        rafPending = true;
        requestAnimationFrame(() => {
            rafPending = false;
            computeVisibleRange();
        });
    };

    // Attach scroll listener when container is available
    if (typeof window !== 'undefined') {
        $effect(() => {
            const el = containerRef();
            if (el) {
                el.addEventListener('scroll', onScroll, { passive: true });
                // Also listen for resize to recompute on container size changes
                const ro = new ResizeObserver(() => computeVisibleRange());
                ro.observe(el);
                // Initial computation
                computeVisibleRange();
                return () => {
                    el.removeEventListener('scroll', onScroll);
                    ro.disconnect();
                };
            }
        });
    }

    /**
     * Measure an item's actual height and trigger recomputation.
     * Call this from a ResizeObserver on each rendered item.
     */
    function measureItem(index: number, height: number) {
        const prev = measuredHeights.get(index);
        if (prev === height) return; // no change
        measuredHeights.set(index, height);
        dirty = true;
        computeVisibleRange();
    }

    /** Get the Y offset of an item (sum of all preceding item heights). */
    function getItemOffset(index: number): number {
        recomputeOffsets();
        if (index <= 0) return 0;
        if (index >= getCount()) return totalHeight;
        return offsets[index];
    }

    return {
        get startIdx() { return startIdx; },
        get endIdx() { return endIdx; },
        get paddingTop() { return paddingTop; },
        get paddingBottom() { return paddingBottom; },
        get totalHeight() { return totalHeight; },
        measureItem,
        getItemOffset,
    };
}
