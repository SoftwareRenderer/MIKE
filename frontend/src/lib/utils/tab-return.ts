/**
 * Decides whether a "the user came back" signal is worth a fetch. Pure, so the thresholds are
 * reachable from a test; `use-tab-return` owns the listeners.
 */

/** How long a stream must have said nothing for silence to stand in for "this window went unread". */
export const WAKE_FRAME_GAP_MS = 30_000;

/** The browser fires several signals for one gesture. */
export const RETURN_DEDUPE_MS = 2_000;

/** `visible` is a tab that was hidden and `resume` a page that was stopped; the others never set
 * `document.hidden`. */
export type ReturnSource = 'visible' | 'focus' | 'pageshow' | 'resume';

export interface ReturnSignal {
    hidden: boolean;
    source: ReturnSource;
    /** Null when there is no stream to ask. */
    msSinceFrame: number | null;
    msSinceReconcile: number;
    /** Null when no content has arrived while this window was unfocused. */
    msSinceUnpainted: number | null;
    /** True when the screen may be showing something the server has moved past. */
    mayBeBehind: boolean;
    /** True when a missed turn leaves nothing on this screen that could report it, so "this window went
     * unread" is evidence enough by itself — `docs/reconnection.md → The tab that comes back`. */
    unreadIsEvidence: boolean;
}

export function needsReturnReconcile(s: ReturnSignal): boolean {
    if (s.hidden) return false;
    if (s.msSinceReconcile < RETURN_DEDUPE_MS) return false;
    // A page that was stopped ran nothing while it was stopped, so it counts as unread for the view that
    // cannot see its own gap. The other two keep their own evidence, because a reload costs them state.
    if (s.source === 'resume') return s.unreadIsEvidence;
    if (s.source === 'visible') return s.unreadIsEvidence || s.mayBeBehind;
    return s.mayBeBehind && (contentWentUnread(s) || (s.msSinceFrame !== null && s.msSinceFrame >= WAKE_FRAME_GAP_MS));
}

/** Content arrived while this window was unfocused, after this view last reconciled — see
 * `docs/reconnection.md → The tab that comes back`. */
function contentWentUnread(s: ReturnSignal): boolean {
    return s.msSinceUnpainted !== null && s.msSinceUnpainted < s.msSinceReconcile;
}
