/**
 * The click half of every notification this worker shows.
 *
 * Loaded by `importScripts` from the generated worker — `workbox.importScripts` in `vite.config.ts` — so
 * it is a classic worker script with nothing to import: `'mike:notification-click'` must keep agreeing
 * with `WORKER_CLICK_MESSAGE` in `src/lib/utils/notifications.ts`.
 *
 * The navigation belongs to the page, because `WindowClient.navigate` is not available everywhere: an
 * open window is focused and told where to go, and only a cold start hands the URL to the browser.
 */
(() => {
    'use strict';

    const CLICK = 'mike:notification-click';

    self.addEventListener('notificationclick', (event) => {
        const notification = event.notification;
        const url = notification.data && notification.data.url;
        notification.close();
        if (!url) return;
        event.waitUntil((async () => {
            const windows = await self.clients.matchAll({ type: 'window', includeUncontrolled: true });
            const target = windows.find((client) => 'focus' in client);
            if (!target) {
                await self.clients.openWindow(url);
                return;
            }
            await target.focus();
            target.postMessage({ type: CLICK, url: url });
        })().catch(() => {}));
    });
})();
