import { describe, it, expect, vi, afterEach } from 'vitest';
import { notificationPermission, requestNotificationPermission, showBrowserNotification } from '$lib/utils/notifications';

afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
});

interface Posted {
    title: string;
    options: NotificationOptions;
    onclick: (() => void) | null;
    close: ReturnType<typeof vi.fn>;
}

/** jsdom has no Notification, so each case states the browser it wants — including none at all. */
function stubNotification(permission: 'default' | 'granted' | 'denied', behaviour: { throws?: boolean; ask?: 'granted' | 'denied' } = {}) {
    const posted: Posted[] = [];
    const requestPermission = vi.fn().mockResolvedValue(behaviour.ask ?? (permission === 'default' ? 'granted' : permission));
    class Fake {
        static permission = permission;
        static requestPermission = requestPermission;
        onclick: (() => void) | null = null;
        close = vi.fn();
        constructor(public title: string, public options: NotificationOptions = {}) {
            if (behaviour.throws) throw new TypeError('only a service worker may notify here');
            posted.push(this);
        }
    }
    vi.stubGlobal('Notification', Fake);
    return { posted, requestPermission };
}

function stubServiceWorker(showNotification: ReturnType<typeof vi.fn> | null) {
    Object.defineProperty(navigator, 'serviceWorker', {
        configurable: true,
        value: showNotification ? { getRegistration: () => Promise.resolve({ showNotification }) } : undefined,
    });
}

describe('notificationPermission', () => {
    it('reports a browser with no Notification API as unsupported', () => {
        expect(notificationPermission()).toBe('unsupported');
    });

    it('reads the answer the browser already gives', () => {
        stubNotification('denied');
        expect(notificationPermission()).toBe('denied');
    });
});

describe('requestNotificationPermission', () => {
    it('asks nothing of a browser that cannot answer', async () => {
        await expect(requestNotificationPermission()).resolves.toBe('unsupported');
    });

    it('asks once when the browser has no answer yet', async () => {
        const { requestPermission } = stubNotification('default');
        await expect(requestNotificationPermission()).resolves.toBe('granted');
        expect(requestPermission).toHaveBeenCalledTimes(1);
    });

    it('does not re-ask an answered browser', async () => {
        const { requestPermission } = stubNotification('denied');
        await expect(requestNotificationPermission()).resolves.toBe('denied');
        expect(requestPermission).not.toHaveBeenCalled();
    });

    it('reports a refusal below the API as denied rather than as permission', async () => {
        const { requestPermission } = stubNotification('default');
        requestPermission.mockRejectedValue(new TypeError('not allowed in an insecure context'));
        await expect(requestNotificationPermission()).resolves.toBe('denied');
    });
});

describe('showBrowserNotification', () => {
    it('posts nothing and stays quiet where there is no API', () => {
        expect(() => showBrowserNotification({ title: 'T', body: 'B' })).not.toThrow();
        expect(showBrowserNotification({ title: 'T', body: 'B' })).toBe(false);
    });

    it('posts nothing while permission is missing', () => {
        const { posted } = stubNotification('default');
        expect(showBrowserNotification({ title: 'T', body: 'B' })).toBe(false);
        expect(posted).toHaveLength(0);
    });

    it('posts once with the body and the tag that keeps a task to one notification', () => {
        const { posted } = stubNotification('granted');
        expect(showBrowserNotification({ title: 'Fix the diff', body: 'proj · waiting in Review', tag: 'mike-task:t1' })).toBe(true);
        expect(posted).toHaveLength(1);
        expect(posted[0].title).toBe('Fix the diff');
        expect(posted[0].options.body).toBe('proj · waiting in Review');
        expect(posted[0].options.tag).toBe('mike-task:t1');
    });

    it('leaves the tag out when the caller has no task to replace', () => {
        const { posted } = stubNotification('granted');
        showBrowserNotification({ title: 'T', body: 'B' });
        expect(posted[0].options.tag).toBeUndefined();
    });

    it('brings the app back when the notification is clicked', () => {
        const { posted } = stubNotification('granted');
        const focus = vi.spyOn(window, 'focus').mockImplementation(() => {});
        // The url is where this tab already is, so the click may not navigate anywhere.
        showBrowserNotification({ title: 'T', body: 'B', url: location.href });
        posted[0].onclick?.();
        expect(focus).toHaveBeenCalledTimes(1);
        expect(posted[0].close).toHaveBeenCalledTimes(1);
    });

    it('falls back to the service worker where the constructor is refused', async () => {
        const showNotification = vi.fn();
        stubServiceWorker(showNotification);
        stubNotification('granted', { throws: true });
        expect(showBrowserNotification({ title: 'T', body: 'B' })).toBe(true);
        await Promise.resolve();
        expect(showNotification).toHaveBeenCalledWith('T', { body: 'B' });
    });

    it('gives up when a refusing browser also has no worker', () => {
        stubServiceWorker(null);
        stubNotification('granted', { throws: true });
        expect(showBrowserNotification({ title: 'T', body: 'B' })).toBe(false);
    });
});
