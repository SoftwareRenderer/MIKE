/**
 * Pointer-vs-element geometry, shared by everything that has to agree on what
 * "the pointer is inside this tooltip" means. The margin is deliberately a
 * single value: when the hover card and the scroll handler disagree about it,
 * one of them dismisses a tooltip the user is still pointing at.
 */

export interface Box {
    left: number;
    right: number;
    top: number;
    bottom: number;
}

/**
 * Tolerance, in px, around a box before a pointer counts as outside it.
 *
 * @knipignore The tolerance is measured one pixel inside it and one pixel past it.
 */
export const POINTER_MARGIN = 4;

/** Point-in-box test with a margin, so a 1px edge does not dismiss a tooltip. */
export function inBox(box: Box, x: number, y: number, margin = POINTER_MARGIN): boolean {
    return x >= box.left - margin && x <= box.right + margin && y >= box.top - margin && y <= box.bottom + margin;
}
