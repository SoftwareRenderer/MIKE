import { describe, it, expect, vi, beforeAll, afterEach } from 'vitest';
import {
    notificationPermission,
    requestNotificationPermission,
    notificationTarget,
    showBrowserNotification,
    dismissBrowserNotification,
    watchNotificationClicks,
} from '$lib/utils/notifications';

afterEach(() => {
    restoreLocation?.();
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

function stubServiceWorker(showNotification: ReturnType<typeof vi.fn> | null, reported: WorkerNotice[] = []) {
    Object.defineProperty(navigator, 'serviceWorker', {
        configurable: true,
        value: showNotification ? {
            getRegistration: () => Promise.resolve({
                showNotification,
                getNotifications: (options?: { tag?: string }) => Promise.resolve(
                    reported.filter(n => n.tag === options?.tag) as unknown as Notification[]),
            }),
        } : undefined,
    });
}

/** A notification a live worker would answer for `getNotifications({ tag })`. */
interface WorkerNotice {
    tag: string;
    close: ReturnType<typeof vi.fn>;
}

function workerNotice(tag: string): WorkerNotice {
    return { tag, close: vi.fn() };
}

/** jsdom navigates nowhere, so a case that cares where a click lands swaps in a location it can read. */
let restoreLocation: (() => void) | undefined;

function stubLocation(href: string): () => string {
    const original = Object.getOwnPropertyDescriptor(window, 'location');
    const stub = { href };
    Object.defineProperty(window, 'location', { configurable: true, writable: true, value: stub });
    restoreLocation = () => {
        if (original) Object.defineProperty(window, 'location', original);
        restoreLocation = undefined;
    };
    return () => stub.href;
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

    it('brings the app back to the task the notification spoke for', () => {
        const { posted } = stubNotification('granted');
        const here = stubLocation('http://localhost:3000/');
        const focus = vi.spyOn(window, 'focus').mockImplementation(() => {});
        showBrowserNotification({ title: 'T', body: 'B', url: '/?project=p1&task=t1' });
        posted[0].onclick?.();
        expect(focus).toHaveBeenCalledTimes(1);
        expect(posted[0].close).toHaveBeenCalledTimes(1);
        expect(here()).toBe(new URL('/?project=p1&task=t1', 'http://localhost:3000/').href);
    });

    it('leaves the tab alone when the notification already names what it is showing', () => {
        const { posted } = stubNotification('granted');
        const here = stubLocation('http://localhost:3000/?project=p1&task=t1');
        const focus = vi.spyOn(window, 'focus').mockImplementation(() => {});
        showBrowserNotification({ title: 'T', body: 'B', url: here() });
        posted[0].onclick?.();
        expect(focus).toHaveBeenCalledTimes(1);
        expect(here()).toBe('http://localhost:3000/?project=p1&task=t1');
    });

    it('falls back to the service worker where the constructor is refused', async () => {
        const showNotification = vi.fn();
        stubServiceWorker(showNotification);
        stubNotification('granted', { throws: true });
        expect(showBrowserNotification({ title: 'T', body: 'B' })).toBe(true);
        await Promise.resolve();
        expect(showNotification).toHaveBeenCalledWith('T', { body: 'B' });
    });

    it('hands the worker the target too, since the worker is the one that will be clicked', async () => {
        const showNotification = vi.fn();
        stubServiceWorker(showNotification);
        stubNotification('granted', { throws: true });
        stubLocation('http://localhost:3000/');
        expect(showBrowserNotification({ title: 'T', body: 'B', tag: 'mike-review-batch', url: '/?project=p1' })).toBe(true);
        await Promise.resolve();
        expect(showNotification).toHaveBeenCalledWith('T', {
            body: 'B',
            tag: 'mike-review-batch',
            data: { url: new URL('/?project=p1', 'http://localhost:3000/').href },
        });
    });

    it('gives up when a refusing browser also has no worker', () => {
        stubServiceWorker(null);
        stubNotification('granted', { throws: true });
        expect(showBrowserNotification({ title: 'T', body: 'B' })).toBe(false);
    });
});

describe('dismissBrowserNotification', () => {
    it('closes what this page put up under that tag', () => {
        const { posted } = stubNotification('granted');
        showBrowserNotification({ title: 'T', body: 'B', tag: 'mike-task-t1' });
        dismissBrowserNotification('mike-task-t1');
        expect(posted[0].close).toHaveBeenCalledTimes(1);
    });

    it('leaves alone what went up under another tag', () => {
        const { posted } = stubNotification('granted');
        showBrowserNotification({ title: 'T', body: 'B', tag: 'mike-task-t1' });
        dismissBrowserNotification('mike-task-somebody-else');
        expect(posted[0].close).not.toHaveBeenCalled();
    });

    it('closes what the worker reports for that tag, and only that tag', async () => {
        const arrived = workerNotice('mike-review-batch');
        const elsewhere = workerNotice('mike-task-t7');
        stubServiceWorker(vi.fn(), [arrived, elsewhere]);
        stubNotification('granted', { throws: true });
        showBrowserNotification({ title: 'T', body: 'B', tag: 'mike-review-batch' });
        dismissBrowserNotification('mike-review-batch');
        await vi.waitFor(() => expect(arrived.close).toHaveBeenCalledTimes(1));
        expect(elsewhere.close).not.toHaveBeenCalled();
    });

    it('closes the same tag twice without complaint', () => {
        const { posted } = stubNotification('granted');
        showBrowserNotification({ title: 'T', body: 'B', tag: 'mike-task-t2' });
        dismissBrowserNotification('mike-task-t2');
        expect(() => dismissBrowserNotification('mike-task-t2')).not.toThrow();
        expect(posted[0].close).toHaveBeenCalledTimes(1);
    });

    it('is quiet where the Notification API does not exist', () => {
        expect(() => dismissBrowserNotification('mike-task-t3')).not.toThrow();
    });
});

describe('notificationTarget', () => {
    const here = 'http://localhost:3000/';

    it('resolves the app link a notification carries against this tab', () => {
        expect(notificationTarget(here, '/?project=p1&task=t1'))
            .toBe(new URL('/?project=p1&task=t1', here).href);
    });

    it('answers nothing for a notification that speaks for no target', () => {
        expect(notificationTarget(here, undefined)).toBeNull();
        expect(notificationTarget(here, '')).toBeNull();
    });

    it('answers the same address when this tab is already where the click would go', () => {
        const current = 'http://localhost:3000/?project=p1&task=t1';
        expect(notificationTarget(current, current)).toBe(current);
        expect(notificationTarget(current, '/?project=p1&task=t1')).toBe(current);
    });

    it('answers nothing for a target on another origin', () => {
        expect(notificationTarget(here, 'https://elsewhere.test/?project=p1')).toBeNull();
    });

    it('answers nothing for bytes that are no url', () => {
        expect(notificationTarget(here, 'http://')).toBeNull();
    });
});

describe('watchNotificationClicks', () => {
    /**
     * The page holds one listener for the worker's clicks however often boot calls for it, so the cases
     * share the handler the first registration captured.
     */
    const handlers: Array<(event: MessageEvent) => void> = [];
    let send: (data: unknown) => void = () => {};

    beforeAll(() => {
        Object.defineProperty(navigator, 'serviceWorker', {
            configurable: true,
            value: {
                addEventListener: (type: string, handler: (event: MessageEvent) => void) => {
                    if (type === 'message') handlers.push(handler);
                },
            },
        });
        watchNotificationClicks();
        // A second boot must not put a second listener on the same channel.
        watchNotificationClicks();
        send = (data: unknown) => {
            for (const handler of handlers) handler({ data } as MessageEvent);
        };
    });

    it('registers once, however often the app boots', () => {
        expect(handlers).toHaveLength(1);
    });

    it('opens the task the worker was clicked on', () => {
        const here = stubLocation('http://localhost:3000/');
        const focus = vi.spyOn(window, 'focus').mockImplementation(() => {});
        send({ type: 'mike:notification-click', url: 'http://localhost:3000/?project=p1&task=t1' });
        expect(focus).toHaveBeenCalledTimes(1);
        expect(here()).toBe('http://localhost:3000/?project=p1&task=t1');
    });

    it('ignores a frame that is not a notification click', () => {
        const here = stubLocation('http://localhost:3000/');
        const focus = vi.spyOn(window, 'focus').mockImplementation(() => {});
        send({ type: 'somebody-elses-message', url: 'http://localhost:3000/?project=p1' });
        send({ type: 'mike:notification-click' });
        send({ type: 'mike:notification-click', url: 'https://elsewhere.test/?project=p1' });
        send('not even an object');
        expect(focus).not.toHaveBeenCalled();
        expect(here()).toBe('http://localhost:3000/');
    });
});
