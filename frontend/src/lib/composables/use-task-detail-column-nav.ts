/**
 * Task View (CardDetail) column navigation. Left/Right cycle focus between
 * [Board][Chat][Diff]; Up/Down dispatches on the focused column. Pure
 * resolvers only — the keydown wiring lives in CardDetail.svelte (same
 * pattern as use-board-arrow-nav.ts).
 */

export type DetailColumnId = 'board' | 'chat' | 'diff';

/** Cyclic Left/Right over the columns. `current` null (or not in `order`) is
 *  the neutral anchor: Right -> first, Left -> last. */
export function resolveDetailColumn(
    order: DetailColumnId[],
    current: DetailColumnId | null,
    key: 'ArrowLeft' | 'ArrowRight',
): DetailColumnId | null {
    if (!order.length) return null;
    const dir = key === 'ArrowRight' ? 1 : -1;
    const idx = current === null ? -1 : order.indexOf(current);
    if (idx === -1) return dir > 0 ? order[0] : order[order.length - 1];
    return order[(idx + dir + order.length) % order.length];
}

/** Up/Down between diff file headers (wraps at both ends). `sections` are
 *  `.diff-file-section` elements in DOM order, `headers` their focusable
 *  buttons. Focus inside a section anchors to it; focus outside all sections
 *  is neutral (Down -> first, Up -> last). */
export function resolveDiffFileTarget(opts: {
    sections: HTMLElement[];
    headers: HTMLElement[];
    activeElement: Element | null;
    key: 'ArrowDown' | 'ArrowUp';
}): HTMLElement | null {
    const { sections, headers, activeElement, key } = opts;
    if (!headers.length) return null;
    const dir = key === 'ArrowDown' ? 1 : -1;
    let idx = -1;
    if (activeElement instanceof Element) {
        idx = sections.findIndex((s) => s.contains(activeElement));
    }
    if (idx === -1) return dir > 0 ? headers[0] : headers[headers.length - 1];
    return headers[(idx + dir + headers.length) % headers.length];
}
