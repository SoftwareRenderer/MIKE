/**
 * How wide the Task View diff column has to be for the inline file editor to be
 * worth showing inside it. Extracted from CardDetail.svelte for unit tests.
 */

/** Below this rendered diff-column width, opening a file from a diff line maximizes the editor. */
export const DIFF_EDITOR_FULLSCREEN_MAX_WIDTH = 1200;

/**
 * Should a diff line click maximize the editor it opens?
 *
 * `widthPx` is the rendered diff column, so the Context/Chat resize handles
 * count, not just the window. Mobile never maximizes: its tabs already give the
 * panel the whole window. A hidden column measures 0 — width unknown, so leave
 * the editor inline.
 */
export function shouldAutoFullscreenDiffEditor(widthPx: number, isMobile: boolean): boolean {
    if (isMobile) return false;
    if (!(widthPx > 0)) return false;
    return widthPx < DIFF_EDITOR_FULLSCREEN_MAX_WIDTH;
}
