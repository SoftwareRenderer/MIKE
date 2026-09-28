/**
 * Browser notifications. Every entry point is safe where `Notification` does not exist — a plain-http
 * origin, a webview, Safari's page-context ban — since a browser that cannot notify still stores the flag.
 */

/** `tag` replaces an earlier notification for the same task instead of stacking. */
export interface BrowserNotification {
    title: string;
    body: string;
    tag?: string;
    /** Opened when the notification is clicked. */
    url?: string;
}

export type PermissionAnswer = 'unsupported' | 'default' | 'granted' | 'denied';

/**
 * What the worker posts when a notification it showed is clicked while a window is open. Named here and
 * in `public/sw-notifications.js`, which is a classic worker script and cannot import this module.
 */
const WORKER_CLICK_MESSAGE = 'mike:notification-click';

let watchingClicks = false;

function api(): typeof Notification | null {
    return typeof Notification === 'undefined' ? null : Notification;
}

function parse(url: string): URL | null {
    try {
        return new URL(url, location.href);
    } catch {
        return null;
    }
}

/**
 * What a notification wants opened, as an absolute url of this origin: `null` when it carries no target
 * or one belonging to another origin. Whether this tab has to move to reach it is the caller's question.
 *
 * @knipignore The same-origin rule is asserted on two urls; the posting paths need a Notification API.
 */
export function notificationTarget(current: string, url: string | undefined): string | null {
    if (!url) return null;
    const target = parse(url);
    const here = parse(current);
    if (!target || !here || target.origin !== here.origin) return null;
    return target.href;
}

/** Bring the app to what the notification spoke for. The worker's click path and the page's agree here. */
function openNotifiedTarget(url: string | undefined): void {
    const target = notificationTarget(location.href, url);
    if (!target) return;
    window.focus();
    if (target !== location.href) location.href = target;
}

/**
 * Constructor-path notifications by tag, so one can be closed later: `getNotifications` answers only
 * for what `showNotification` showed, never for these. Entries leave on the `close` event, which the
 * browser fires for its own timeout and the user's own swipe as well as for ours.
 */
const held = new Map<string, Set<Notification>>();

function hold(tag: string, notification: Notification): void {
    let byTag = held.get(tag);
    if (!byTag) {
        byTag = new Set();
        held.set(tag, byTag);
    }
    byTag.add(notification);
    notification.onclose = () => {
        const still = held.get(tag);
        if (!still) return;
        still.delete(notification);
        if (still.size === 0) held.delete(tag);
    };
}

export function notificationPermission(): PermissionAnswer {
    const n = api();
    return n ? n.permission : 'unsupported';
}

export async function requestNotificationPermission(): Promise<PermissionAnswer> {
    const n = api();
    if (!n) return 'unsupported';
    if (n.permission !== 'default') return n.permission;
    try {
        return await n.requestPermission();
    } catch {
        // Refused below the API (insecure context, webview): report what the API still answers.
        return notificationPermission() === 'granted' ? 'granted' : 'denied';
    }
}

/**
 * True on the service-worker path is optimistic: the notice is handed to the registration, and whether
 * the browser puts it up is not answered back.
 */
export function showBrowserNotification(spec: BrowserNotification): boolean {
    const n = api();
    if (!n || n.permission !== 'granted') return false;
    const options: NotificationOptions = { body: spec.body };
    if (spec.tag) options.tag = spec.tag;
    try {
        const shown = new n(spec.title, options);
        if (spec.tag) hold(spec.tag, shown);
        const url = spec.url;
        if (url) shown.onclick = () => {
            shown.close();
            openNotifiedTarget(url);
        };
        return true;
    } catch {
        // The worker shows it here, so the worker is also the one that gets clicked: the target travels
        // in the notification's own data.
        const target = notificationTarget(location.href, spec.url ?? undefined);
        if (target) options.data = { url: target };
        const registration = navigator.serviceWorker?.getRegistration?.();
        if (!registration) return false;
        registration.then((r) => { void r?.showNotification(spec.title, options); }).catch(() => {});
        return true;
    }
}

/**
 * Hear the clicks the worker cannot answer on its own: `WindowClient.navigate` is not available
 * everywhere, so it focuses this window and posts it the target. Called at boot rather than at the first
 * show, because a notification can outlive the reload of the page that wants to open it.
 */
export function watchNotificationClicks(): void {
    if (watchingClicks) return;
    const channel = navigator.serviceWorker;
    if (!channel) return;
    watchingClicks = true;
    channel.addEventListener('message', (event: MessageEvent) => {
        const frame = event.data as { type?: unknown; url?: unknown } | null;
        if (!frame || frame.type !== WORKER_CLICK_MESSAGE || typeof frame.url !== 'string') return;
        openNotifiedTarget(frame.url);
    });
}

/**
 * Take back a notification this app showed under `tag`: the objects this page still holds, plus
 * whatever a live registration reports for that tag — which is how one that outlived a reload goes.
 * Quiet everywhere the API is absent.
 */
export function dismissBrowserNotification(tag: string): void {
    const ours = held.get(tag);
    if (ours) {
        for (const notification of ours) notification.close();
        held.delete(tag);
    }
    const registration = navigator.serviceWorker?.getRegistration?.();
    if (!registration) return;
    registration
        .then((r) => r?.getNotifications?.({ tag }))
        .then((found) => { for (const notification of found || []) notification.close(); })
        .catch(() => {});
}
