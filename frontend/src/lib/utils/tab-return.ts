/**
 * Decides whether a "the user came back" signal is worth a fetch. Pure, so the thresholds are
 * reachable from a test; `use-tab-return` owns the listeners.
 */

/** Silence is the only evidence a window that was merely unfocused has slept. */
export const WAKE_FRAME_GAP_MS = 30_000;

/** The browser fires several signals for one gesture. */
export const RETURN_DEDUPE_MS = 2_000;

/** `visible` is a tab that was hidden; the others never set `document.hidden`. */
export type ReturnSource = 'visible' | 'focus' | 'pageshow';

export interface ReturnSignal {
    hidden: boolean;
    source: ReturnSource;
    /** Null when there is no stream to ask. */
    msSinceFrame: number | null;
    msSinceReconcile: number;
    /** True when the screen may be showing something the server has moved past. */
    mayBeBehind: boolean;
    /** True when a missed frame leaves nothing on this screen that could report it, so the only evidence is
     * that the page was hidden — `docs/reconnection.md → The tab that comes back`. */
    hiddenIsEvidence: boolean;
}

export function needsReturnReconcile(s: ReturnSignal): boolean {
    if (s.hidden) return false;
    if (s.msSinceReconcile < RETURN_DEDUPE_MS) return false;
    if (s.source === 'visible') return s.hiddenIsEvidence || s.mayBeBehind;
    return s.mayBeBehind && s.msSinceFrame !== null && s.msSinceFrame >= WAKE_FRAME_GAP_MS;
}
