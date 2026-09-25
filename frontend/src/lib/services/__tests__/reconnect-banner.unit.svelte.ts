/**
 * The reconnect banner's clocks and its one sentence per transition. What is under test is the timing the
 * browser owns — a countdown that follows the scheduled attempt, an age line that keeps moving while the
 * socket is down and stops when it is not, timers that do not outlive the page — plus the rule that a
 * reconnect re-reads only once the first load has finished (docs/reconnection.md → *The behaviour, in full*).
 *
 * `connectionStore` and the English catalog are the real ones: the flag this banner publishes is the same
 * object `TaskChat` reads, and the copy is the catalog's, not a fake. Only `/api`'s offline flag is faked,
 * because asserting on it is the whole point of "a dropped socket is what sets it".
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import type { AppStream } from '$lib/api';

const api = vi.hoisted(() => ({ setOfflineStatus: vi.fn() }));

vi.mock('$lib/api', () => ({ setOfflineStatus: api.setOfflineStatus }));
vi.mock('$lib/storage', () => ({
    loadAppSetting: vi.fn(async (_key: string, dflt: unknown) => dflt),
    saveAppSetting: vi.fn(async () => {}),
    deleteAppSetting: vi.fn(async () => {}),
}));

const { reconnectBanner } = await import('$lib/services/reconnect-banner.svelte');
const { connectionStore } = await import('$lib/stores/connection.svelte');

/** What the banner may re-read, and how many times it asked. */
function appUnderTest(stream: AppStream | null = null) {
    const refresh = vi.fn();
    reconnectBanner.configure({ stream: () => stream, refresh });
    return { refresh };
}

/** A stream whose only behaviour the banner can see is that it was asked to reconnect. */
function fakeStream() {
    const reconnectNow = vi.fn();
    return { stream: { reconnectNow } as unknown as AppStream, reconnectNow };
}

beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-09-25T12:00:00Z'));
    vi.clearAllMocks();
    reconnectBanner.reset();
    connectionStore.set(false);
});

afterEach(() => {
    vi.useRealTimers();
});

describe('the connection edge', () => {
    it('publishes the flag and raises it through the HTTP layer too', () => {
        appUnderTest();
        reconnectBanner.onConnected(false);
        expect(connectionStore.connected).toBe(false);
        expect(api.setOfflineStatus).toHaveBeenCalledWith(true);

        reconnectBanner.onConnected(true);
        expect(connectionStore.connected).toBe(true);
        expect(api.setOfflineStatus).toHaveBeenLastCalledWith(false);
    });

    it('re-reads only once the first load has finished', () => {
        const { refresh } = appUnderTest();
        // The first connect of a boot has nothing to catch up on.
        reconnectBanner.onConnected(true);
        expect(refresh).not.toHaveBeenCalled();

        reconnectBanner.initialLoadComplete = true;
        reconnectBanner.onConnected(false);
        reconnectBanner.onConnected(true);
        expect(refresh).toHaveBeenCalledTimes(1);
    });

    it('announces a transition, never the first wait', () => {
        appUnderTest();
        // A page that never had a socket has no transition to report.
        expect(reconnectBanner.notice).toBe('');

        reconnectBanner.onConnected(false);
        expect(reconnectBanner.notice).toBe('Reconnecting ...');
        reconnectBanner.onConnected(true);
        expect(reconnectBanner.notice).toBe('Connected again');
    });
});

describe('the countdown', () => {
    it('counts a scheduled attempt down', () => {
        appUnderTest();
        reconnectBanner.handlers.onReconnectScheduled(Date.now() + 5000);
        expect(reconnectBanner.countdownSeconds).toBe(5);
        expect(reconnectBanner.status).toBe('Reconnecting in 5s ...');

        vi.advanceTimersByTime(1000);
        expect(reconnectBanner.countdownSeconds).toBe(4);
        expect(reconnectBanner.status).toBe('Reconnecting in 4s ...');
    });

    it('stops when an attempt starts', () => {
        appUnderTest();
        reconnectBanner.handlers.onReconnectScheduled(Date.now() + 5000);
        reconnectBanner.handlers.onConnectAttempt();
        expect(reconnectBanner.countdownSeconds).toBe(0);
        // The word without a number: an attempt is running, so nothing is being counted down.
        expect(reconnectBanner.status).toBe('Reconnecting ...');

        vi.advanceTimersByTime(3000);
        expect(reconnectBanner.countdownSeconds).toBe(0);
    });

    it('is re-armed by a later schedule rather than stacked', () => {
        appUnderTest();
        reconnectBanner.handlers.onReconnectScheduled(Date.now() + 30000);
        reconnectBanner.handlers.onReconnectScheduled(Date.now() + 2000);
        vi.advanceTimersByTime(1000);
        expect(reconnectBanner.countdownSeconds).toBe(1);
    });
});

describe('the age line', () => {
    it('says nothing when nothing has landed', () => {
        appUnderTest();
        reconnectBanner.onConnected(false);
        expect(reconnectBanner.staleTimeText).toBe('');

        vi.advanceTimersByTime(5000);
        expect(reconnectBanner.staleTimeText).toBe('');
    });

    it('counts from when the data landed, and keeps counting while disconnected', () => {
        appUnderTest();
        reconnectBanner.lastSyncedAt = Date.now() - 60_000;
        reconnectBanner.onConnected(false);
        const first = reconnectBanner.staleTimeText;
        expect(first).not.toBe('');

        vi.advanceTimersByTime(60_000);
        expect(reconnectBanner.staleTimeText).not.toBe(first);
    });

    it('stops the moment the socket is back', () => {
        appUnderTest();
        reconnectBanner.lastSyncedAt = Date.now() - 60_000;
        reconnectBanner.onConnected(false);
        reconnectBanner.onConnected(true);
        const held = reconnectBanner.staleTimeText;

        vi.advanceTimersByTime(120_000);
        expect(reconnectBanner.staleTimeText).toBe(held);
    });
});

describe('the buttons', () => {
    it('skips the backoff on the stream this page holds', () => {
        const { stream, reconnectNow } = fakeStream();
        reconnectBanner.configure({ stream: () => stream, refresh: vi.fn() });
        reconnectBanner.handlers.onReconnectScheduled(Date.now() + 9000);

        reconnectBanner.reconnectNow();
        expect(reconnectNow).toHaveBeenCalledTimes(1);
        expect(reconnectBanner.nextReconnectAt).toBe(0);
    });

    it('does nothing without a stream, and never claims it reconnected', () => {
        const { refresh } = appUnderTest(null);
        reconnectBanner.reconnectNow();
        expect(refresh).not.toHaveBeenCalled();
    });

    it('re-reads live data when the cache banner is clicked', () => {
        const { stream, reconnectNow } = fakeStream();
        const refresh = vi.fn();
        reconnectBanner.configure({ stream: () => stream, refresh });

        reconnectBanner.retryLive();
        expect(reconnectNow).toHaveBeenCalledTimes(1);
        expect(refresh).toHaveBeenCalledTimes(1);
    });
});

describe('destroy', () => {
    it('leaves neither clock running', () => {
        appUnderTest();
        reconnectBanner.lastSyncedAt = Date.now() - 60_000;
        reconnectBanner.handlers.onReconnectScheduled(Date.now() + 5000);
        reconnectBanner.onConnected(false);

        reconnectBanner.destroy();
        const frozen = { countdown: reconnectBanner.countdownSeconds, age: reconnectBanner.staleTimeText };
        vi.advanceTimersByTime(120_000);
        expect(reconnectBanner.countdownSeconds).toBe(frozen.countdown);
        expect(reconnectBanner.staleTimeText).toBe(frozen.age);
    });
});
