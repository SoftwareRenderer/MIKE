/**
 * Task View (CardDetail) column navigation. Left/Right cycle focus between
 * [Board][Chat][Diff]; Up/Down dispatches on the focused column. Pure
 * resolvers only — the keydown wiring lives in CardDetail.svelte (same
 * pattern as use-board-arrow-nav.ts).
 */

export type DetailColumnId = 'board' | 'chat' | 'diff';

/** Gate for the Left/Right column switch. Plain arrows only switch from
 *  non-editable focus (editables keep native text-cursor movement);
 *  Ctrl+Alt+arrows switch from any focus — the keyboard way out of the chat
 *  input (plain Ctrl+arrows stay native, e.g. word jump). Never switches
 *  while a file is being edited (code-editor word movement), a column is
 *  fullscreen, a modal is open, on mobile, or the event was already handled. */
export function canSwitchDetailColumn(opts: {
    ctrlKey: boolean;
    altKey: boolean;
    metaKey: boolean;
    shiftKey: boolean;
    defaultPrevented: boolean;
    isMobile: boolean;
    blocked: boolean;
    editingFile: boolean;
    diffFullscreen: boolean;
    chatFullscreen: boolean;
    isEditable: boolean;
}): boolean {
    const {
        ctrlKey, altKey, metaKey, shiftKey, defaultPrevented, isMobile,
        blocked, editingFile, diffFullscreen, chatFullscreen, isEditable,
    } = opts;
    const ctrlAlt = ctrlKey && altKey;
    if (metaKey || shiftKey) return false;
    if (altKey && !ctrlAlt) return false; // Alt without Ctrl
    if (ctrlKey && !ctrlAlt) return false; // plain Ctrl: no switch (native word jump)
    if (defaultPrevented) return false;
    if (isMobile) return false;
    if (blocked) return false;
    if (editingFile || diffFullscreen || chatFullscreen) return false;
    if (isEditable && !ctrlAlt) return false;
    return true;
}

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
