/**
 * The one place core reaches into the service worker's Cache Storage.
 *
 * `vite.config.ts` caches extension assets under this name (`NetworkFirst`, so an
 * update reaches the browser immediately and the cache is only the offline
 * fallback). That fallback is the reason this file exists: a body served from it
 * was written when the extension was last fetched, so after an update or a delete
 * it is old code — or code for an extension this install no longer has — being run
 * in a frame the host armed and granted. Anything else that wants a cache entry
 * gone belongs here too, never at a call site with a string literal.
 */

/** Must equal `workbox.runtimeCaching[0].options.cacheName` in `vite.config.ts`. */
export const EXTENSION_ASSET_CACHE = 'kanban-extensions';

/**
 * Drop every cached extension asset. Best-effort and never throws: no cached
 * body is worth a failed extension load, and the next fetch repopulates it.
 * A browser with no Cache Storage (or no service worker registered, which is the
 * case in tests and in `--no-tls` development before the worker takes control)
 * has nothing to drop and says so by doing nothing.
 */
export async function dropExtensionAssetCache(): Promise<void> {
    if (typeof caches === 'undefined') return;
    try {
        await caches.delete(EXTENSION_ASSET_CACHE);
    } catch (e) {
        console.warn(`[sw-cache] could not drop the ${EXTENSION_ASSET_CACHE} cache:`, e);
    }
}

/**
 * What the installed extension set looks like to the browser. Two lists with the
 * same fingerprint serve the same bytes from the same URLs; any difference is one
 * where a cached extension asset may be the wrong answer: a new entry, a removed
 * one, a re-enabled one, or the same path holding a different commit.
 */
export function extensionSetFingerprint(extensions: ReadonlyArray<{
    id: string;
    enabled: boolean;
    version?: string;
    commit?: string;
    served_url?: string;
}>): string {
    return extensions
        .map((e) => `${e.id}\u0000${e.served_url ?? ''}\u0000${e.enabled ? 1 : 0}\u0000${e.version ?? ''}\u0000${e.commit ?? ''}`)
        .sort()
        .join('\u0001');
}
