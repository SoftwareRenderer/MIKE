/**
 * The state machine behind a file save: whether one may start, and what to do
 * with a request that arrives while another save is running. No Svelte and no
 * fetch here, so it can be tested without mounting an editor.
 */

import { SAVE_RETRY_BASE_DELAY_MS, SAVE_RETRY_MAX_DELAY_MS } from '$lib/constants';

/** Fired by an editor when it is no longer saving `detail.path`. */
export const EDITOR_SAVE_SETTLED = 'editor-save-settled';

/**
 * How long an explorer waits for EDITOR_SAVE_SETTLED before freeing its Save
 * button anyway. Only reached if nothing answers (an editor unmounting
 * mid-save), so it is a deadlock guard, not something a save runs into.
 */
export const SAVE_SETTLE_BACKSTOP_MS = 10_000;

export class SaveController {
    private inFlight = false;
    private queued = false;

    get busy(): boolean {
        return this.inFlight;
    }

    /**
     * May a save start? A request during a save is refused but remembered:
     * the buffer may have changed since that save began.
     */
    begin(dirty: boolean): boolean {
        if (this.inFlight) {
            this.queued = true;
            return false;
        }
        if (!dirty) return false;
        this.inFlight = true;
        return true;
    }

    /**
     * The save is over, ok or not. True means save again: someone asked during
     * that save and the buffer is still dirty — which a failed write also
     * leaves it, so a dropped request does not cost the user their edits.
     */
    settle(dirtyAfter: boolean): boolean {
        this.inFlight = false;
        const again = this.queued && dirtyAfter;
        this.queued = false;
        return again;
    }

    /** Forget a queued request (the editor is going away). */
    reset(): void {
        this.inFlight = false;
        this.queued = false;
    }
}

/**
 * How long a save that got no answer waits before it is written again. The wait grows to a ceiling and never
 * ends: a buffer that still differs from the file is not given up on.
 */
export function saveRetryDelayMs(attempt: number): number {
    const rung = Number.isFinite(attempt) ? Math.max(0, Math.floor(attempt)) : 0;
    return Math.min(SAVE_RETRY_BASE_DELAY_MS * 2 ** rung, SAVE_RETRY_MAX_DELAY_MS);
}

/**
 * The explorer's side: one save request may be outstanding at a time. The
 * component mirrors `busy` into `$state` for the UI.
 */
export class SaveRequestGuard {
    busy = false;
    private pendingPath = '';

    /** Record a request for `path`. False when one is already outstanding. */
    request(path: string): boolean {
        if (this.busy || !path) return false;
        this.busy = true;
        this.pendingPath = path;
        return true;
    }

    /**
     * True when this answers the request this explorer made. Both explorers
     * listen on the same window event, so an answer for another file is not ours.
     */
    settled(path: string | undefined): boolean {
        if (this.pendingPath && path !== this.pendingPath) return false;
        this.busy = false;
        this.pendingPath = '';
        return true;
    }

    /** The backstop fired: nothing answered, so free the button anyway. */
    expire(): void {
        this.busy = false;
        this.pendingPath = '';
    }
}
