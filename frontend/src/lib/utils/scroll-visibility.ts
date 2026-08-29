/** A vertical span in viewport coordinates — a DOMRect, or any {top, bottom}. */
export interface Box {
    top: number;
    bottom: number;
}

/**
 * Fractional layout (browser zoom, scaled rows) can leave a box that is visually
 * flush with the container's edge reporting an edge a hair past it. Without slack
 * that reads as clipped and scrolls the card when nothing needs moving.
 */
const VISIBILITY_SLACK = 0.5;

/**
 * Whether `target` sits entirely within `container`. Both must be in the same
 * coordinate space — pass two `getBoundingClientRect()` results.
 */
export function isFullyVisibleIn(container: Box, target: Box, slack = VISIBILITY_SLACK): boolean {
    return target.top >= container.top - slack && target.bottom <= container.bottom + slack;
}

/**
 * The nearest ancestor that scrolls vertically — what a `scrollIntoView()` on
 * `el` would actually move.
 *
 * Returns null when nothing scrolls it or when heights can't be measured (no
 * layout, e.g. jsdom). Callers should read null as "unknown" and scroll anyway.
 */
export function verticalScrollParent(el: HTMLElement): HTMLElement | null {
    for (let node = el.parentElement; node; node = node.parentElement) {
        const { overflowY } = getComputedStyle(node);
        if (overflowY !== 'auto' && overflowY !== 'scroll') continue;
        if (node.scrollHeight > node.clientHeight) return node;
    }
    return null;
}
