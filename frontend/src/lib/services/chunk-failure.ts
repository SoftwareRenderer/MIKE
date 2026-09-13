import { dialogStore } from '$lib/stores';
import { TOAST_DURATION_MS } from '$lib/constants';
import { t } from '$lib/i18n';

// A document left open across an update imports chunk URLs the new build deleted
// (`docs/frontend-updates.md`). The condition never resolves on its own, so one report is enough
// and the caller must leave its "loading" flag clear for the next click.

/** The phrasings the engines give a chunk that could not be fetched. A plain "Failed to fetch" is
 * not one of them: that is a data request, and reloading is the wrong instruction for it. */
const CHUNK_FAILURE = /dynamically imported module|Unable to preload|Importing a module script failed/;

const STALE_BUILD_NOTICE_MS = TOAST_DURATION_MS * 3;

let reported = false;

export function isChunkLoadFailure(err: unknown): boolean {
    const message = err instanceof Error ? err.message : String(err ?? '');
    return CHUNK_FAILURE.test(message);
}

export function reportChunkFailure(err: unknown): void {
    console.error('[lazy-load] could not load part of the app:', err);
    if (reported) return;
    reported = true;
    dialogStore.showToast(t('app.chunkStale'), 'error', STALE_BUILD_NOTICE_MS);
}

/** Test seam: forget that the notice was shown. */
export function resetChunkFailureReport(): void {
    reported = false;
}
