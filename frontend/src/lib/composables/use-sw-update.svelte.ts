import { Workbox } from 'workbox-window';

/**
 * Detects when a new service worker (frontend build) has taken control
 * and prompts the user to reload.
 *
 * The SW's `skipWaiting()` + `clientsClaim()` means the transition from
 * "new SW installed" to "new SW controlling" is immediate. We listen for
 * the `controlling` event with `isUpdate: true` to know a reload is needed.
 */
export class SwUpdateDetector {
    private wb: Workbox | null = null;
    private _needsReload = $state(false);
    private _online = $state(true);

    /** True when a new SW has taken control and the page should be reloaded. */
    get needsReload(): boolean {
        // Only show the banner when online — can't update from cache anyway.
        return this._needsReload && this._online;
    }

    constructor() {
        if (typeof navigator === 'undefined' || !('serviceWorker' in navigator)) {
            return;
        }

        // Track online/offline state
        this._online = navigator.onLine;
        window.addEventListener('online', () => { this._online = true; });
        window.addEventListener('offline', () => { this._online = false; });

        this.wb = new Workbox('/sw.js');

        this.wb.addEventListener('controlling', (event) => {
            // A new SW version has taken control of this page.
            // The page is still running old JS — reload to pick up new assets.
            if (event.isUpdate) {
                this._needsReload = true;
            }
        });

        // Register after the window has loaded (workbox-window default).
        this.wb.register().catch((err) => {
            console.warn('[sw-update] failed to register service worker:', err);
        });
    }

    /** Reload the page to activate the new frontend build. */
    reload(): void {
        window.location.reload();
    }
}
