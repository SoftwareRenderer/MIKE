/**
 * Browser notifications. Every entry point is safe where `Notification` does not exist — a plain-http
 * origin, a webview, Safari's page-context ban — since a browser that cannot notify still stores the flag.
 */

/** `tag` replaces an earlier notification for the same task instead of stacking. */
interface BrowserNotification {
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
