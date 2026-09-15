/**
 * Which requests the generated service worker takes over, and in what order.
 *
 * `vite-plugin-pwa` hands these functions to workbox-build, which **stringifies**
 * them into `sw.js`: the worker runs the text of the body, so a matcher may
 * reference nothing but its arguments and worker globals (`self`). An imported
 * constant or a module-level helper is `undefined` there, and the failure is
 * silent — every request the matcher was meant to take stops being taken.
 *
 * workbox-routing answers with the *first* registered route, so together the two
 * have to partition a URL rather than overlap: what one claims, the other must
 * refuse.
 *
 * The extension tree is `/extensions/` and not `/api/extensions/`: an API answer
 * changes whenever the server's own state does, and this strategy serves a stored
 * body whenever the network is slow.
 */

/** A request for a file the extension file server serves: `/extensions/<author>/<name>/…`. */
export function isExtensionAsset({ url }: { url: URL }): boolean {
    return url.origin === self.location.origin && url.pathname.startsWith('/extensions/');
}

/** The app's own build — everything same-origin that neither the API nor the extension tree owns. */
export function isAppShellAsset({ url }: { url: URL }): boolean {
    if (url.origin !== self.location.origin) return false;
    // Never intercepted, so the offline queue in `queue.ts` is the one that sees the failure.
    if (url.pathname.startsWith('/api/')) return false;
    if (url.pathname.startsWith('/extensions/')) return false;
    return true;
}
