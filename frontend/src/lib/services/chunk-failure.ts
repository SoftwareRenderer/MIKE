import { dialogStore } from '$lib/stores';
import { t } from '$lib/i18n';
import { checkNow } from '$lib/services/app-update';

// A document left open across an update imports chunk URLs the new build deleted
// (`docs/frontend-updates.md`). The answer is to load the document the server has.

/** What the engines say about a chunk they could not fetch. A plain "Failed to fetch" is not one of
 * them: that is a data request, and reloading is the wrong answer for it. */
const CHUNK_FAILURE = /dynamically imported module|Unable to preload|Importing a module script failed/;

/**
 * One reload per tab. It is `sessionStorage` and not `$lib/storage` because the write must land before
 * the reload it guards, and a reload that cannot record itself is a loop on every load.
 */
const RELOAD_GUARD_KEY = 'kanban-chunk-reload';

export function isChunkLoadFailure(err: unknown): boolean {
    const message = err instanceof Error ? err.message : String(err ?? '');
    return CHUNK_FAILURE.test(message);
}

export function handleChunkFailure(err: unknown): void {
    console.error('[lazy-load] could not load part of the app:', err);
    if (!isChunkLoadFailure(err)) return;
    // Offline is not a build problem, and reloading a navigation no worker has precached loses the app.
    if (navigator.onLine === false) return;
    if (!spendReload()) return;
    dialogStore.showToast(t('app.chunkUpdating'), 'success');
    void applyUpdate();
}

function spendReload(): boolean {
    try {
        if (sessionStorage.getItem(RELOAD_GUARD_KEY)) return false;
        sessionStorage.setItem(RELOAD_GUARD_KEY, '1');
        return true;
    } catch {
        return false;
    }
}

async function applyUpdate(): Promise<void> {
    // The update goes first: while an older worker still controls the tab, a plain reload is answered
    // by the same stale document, and `applying` means its `controlling` listener is what reloads.
    let reloading = true;
    try {
        reloading = (await checkNow()) !== 'applying';
    } catch {
        reloading = true;
    }
    if (reloading) window.location.reload();
}
