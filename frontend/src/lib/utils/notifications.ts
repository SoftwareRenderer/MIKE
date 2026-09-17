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

function api(): typeof Notification | null {
    return typeof Notification === 'undefined' ? null : Notification;
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
 * True on the service-worker path is optimistic: Safari refuses the constructor, and the generated
 * worker owns no click handler, so that notification cannot open a task.
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
        if (url) {
            shown.onclick = () => {
                shown.close();
                window.focus();
                if (location.href !== url) location.href = url;
            };
        }
        return true;
    } catch {
        const registration = navigator.serviceWorker?.getRegistration?.();
        if (!registration) return false;
        registration.then((r) => { void r?.showNotification(spec.title, options); }).catch(() => {});
        return true;
    }
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
