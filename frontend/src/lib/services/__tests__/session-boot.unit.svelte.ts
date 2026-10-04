/**
 * The boot's decisions: which of the three screens the browser gets, and when. Under test is the timing
 * and the order, because both are the product — the mirror is *offered* at 2 s and only if a mirror exists,
 * the answer that arrives later takes the offer back, and every boot path closes the previous stream before
 * it opens the next one (docs/reconnection.md → *The boot offer and one stream per page*).
 *
 * The app's side is a recorder: the boot may open a stream and ask for the board, and this file asserts the
 * sequence those two calls arrive in. `reconnectBanner` is the real one, because the mirrored board's age
 * is the fact the banner shows.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import type { SessionAnswer } from '$lib/api';
import type { Project } from '$lib/types';

const api = vi.hoisted(() => ({
    checkSession: vi.fn(),
    logout: vi.fn(async () => {
        store.seq.push('logout');
    }),
    clearQueue: vi.fn(() => {
        store.seq.push('clearQueue');
    }),
    setOfflineStatus: vi.fn(),
}));
const store = vi.hoisted(() => ({
    projects: [] as Project[],
    /** What the `projects` mirror reports as its own arrival, or null when there is no mirror at all. */
    mirrorCachedAt: null as number | null,
    /** Every faked call that has an order the boot is accountable for. */
    seq: [] as string[],
}));
const packs = vi.hoisted(() => ({ loadExtensions: vi.fn(async () => {}), detect: vi.fn(async () => {}) }));

vi.mock('$lib/api', () => ({
    checkSession: api.checkSession,
    logout: api.logout,
    clearQueue: api.clearQueue,
    setOfflineStatus: api.setOfflineStatus,
}));
vi.mock('$lib/storage', () => ({
    getBoardCache: vi.fn(async () => store.projects),
    getBoardCacheEntry: vi.fn(async () =>
        store.projects.length ? { data: store.projects, cachedAt: store.mirrorCachedAt } : null),
    clearAll: vi.fn(async () => {
        store.seq.push('clearAll');
        store.projects = [];
    }),
    loadAppSetting: vi.fn(async (_key: string, dflt: unknown) => dflt),
    saveAppSetting: vi.fn(async () => {}),
    deleteAppSetting: vi.fn(async () => {}),
}));
vi.mock('$lib/services/extension-manager.svelte', () => ({ extensionManager: packs }));
vi.mock('$lib/services/locale-offer.svelte', () => ({ localeOffer: packs }));

const { sessionBoot } = await import('$lib/services/session-boot.svelte');
const { reconnectBanner } = await import('$lib/services/reconnect-banner.svelte');

/** The app's side of a boot, in the order it was asked for it. */
function recordingApp() {
    const calls: string[] = [];
    sessionBoot.configure({
        closeStream: () => void calls.push('close'),
        openStream: (mode) => void calls.push(`open:${mode}`),
        loadAppData: async () => {
            calls.push('load');
        },
    });
    return calls;
}

/** A session check that answers only when the test says so. */
function deferredSession() {
    let resolve!: (session: SessionAnswer) => void;
    const promise = new Promise<SessionAnswer>((res) => {
        resolve = res;
    });
    api.checkSession.mockReturnValue(promise);
    return { promise, resolve };
}

/**
 * The two bodies `/api/auth/session` answers with. `has_passkey` is the login form's branch, so the boot
 * hands it on whatever the answer said — the tests that do not care about it still have to answer it.
 */
const SIGNED_IN: SessionAnswer = { authenticated: true, hasPasskey: true };
const SIGNED_OUT: SessionAnswer = { authenticated: false, hasPasskey: true };

function cachedProject() {
    store.projects = [{ id: 'p1', name: 'repo', path: '/repo' } as Project];
}

afterEach(() => {
    vi.useRealTimers();
});

beforeEach(() => {
    vi.useFakeTimers();
    vi.clearAllMocks();
    // The boot reports a server that did not answer; two of these tests are about exactly that.
    vi.spyOn(console, 'error').mockImplementation(() => {});
    store.projects = [];
    store.mirrorCachedAt = null;
    store.seq.length = 0;
    sessionBoot.reset();
    reconnectBanner.reset();
});

describe('the boot offer', () => {
    it('waits the whole 2 s before it says the wait is slow', async () => {
        const { resolve } = deferredSession();
        recordingApp();
        const booting = sessionBoot.boot();

        await vi.advanceTimersByTimeAsync(1999);
        expect(sessionBoot.bootSlow).toBe(false);
        expect(sessionBoot.isLoadingAuth).toBe(true);

        resolve(SIGNED_IN);
        await booting;
    });

    it('offers the mirror only when a mirror exists', async () => {
        const emptyMirror = deferredSession();
        const calls = recordingApp();
        const booting = sessionBoot.boot();
        await vi.advanceTimersByTimeAsync(2000);
        expect(sessionBoot.hasLocalCache).toBe(false);
        expect(sessionBoot.isLoadingAuth).toBe(true);
        emptyMirror.resolve(SIGNED_IN);
        await booting;
        expect(calls).toEqual(['close', 'open:live', 'load']);
    });

    it('says the server is slow, not gone, and takes the offer back when the answer lands', async () => {
        cachedProject();
        const { resolve } = deferredSession();
        recordingApp();
        const booting = sessionBoot.boot();

        await vi.advanceTimersByTimeAsync(2000);
        expect(sessionBoot.bootSlow).toBe(true);
        expect(sessionBoot.hasLocalCache).toBe(true);
        // The mirror screen is on screen, so the loading screen has stepped aside.
        expect(sessionBoot.isLoadingAuth).toBe(false);

        resolve(SIGNED_IN);
        await booting;
        expect(sessionBoot.hasLocalCache).toBe(false);
        expect(sessionBoot.bootSlow).toBe(false);
        expect(sessionBoot.isAuthenticated).toBe(true);
        expect(sessionBoot.isLoadingAuth).toBe(false);
    });

    it('hands the login form the pairing answer it opens on', async () => {
        // A fresh install has no credential to ask for, and the form decides which of its two bodies to
        // paint from this one flag. Reading a body without it is the api layer's job.
        api.checkSession.mockResolvedValue({ authenticated: false, hasPasskey: false });
        recordingApp();
        await sessionBoot.boot();
        expect(sessionBoot.hasPasskey).toBe(false);
    });

    it('treats a check that will not answer as offline, and reads the mirror', async () => {
        cachedProject();
        api.checkSession.mockRejectedValue(new TypeError('Failed to fetch'));
        recordingApp();

        await sessionBoot.boot();
        expect(api.setOfflineStatus).toHaveBeenCalledWith(true);
        expect(sessionBoot.hasLocalCache).toBe(true);
        expect(sessionBoot.isLoadingAuth).toBe(false);
    });
});

describe('the two boot paths', () => {
    it('closes the stream it found before opening the next one', async () => {
        api.checkSession.mockResolvedValue(SIGNED_OUT);
        const calls = recordingApp();
        await sessionBoot.boot();
        expect(calls).toEqual([]); // signed out: no stream, just the login form

        await sessionBoot.startAfterLogin();
        expect(calls).toEqual(['close', 'open:live', 'load']);
        expect(sessionBoot.isAuthenticated).toBe(true);
        // A session exists only behind a passkey, so the form after a sign-out asks for one.
        expect(sessionBoot.hasPasskey).toBe(true);
    });

    it('reads the mirror behind the offline button, and counts the age from its own arrival', async () => {
        cachedProject();
        store.mirrorCachedAt = Date.UTC(2026, 8, 24, 9, 0, 0);
        api.checkSession.mockRejectedValue(new TypeError('Failed to fetch'));
        const calls = recordingApp();
        await sessionBoot.boot();
        calls.length = 0;

        await sessionBoot.useLocalCache();
        expect(calls).toEqual(['close', 'open:mirror', 'load']);
        expect(reconnectBanner.lastSyncedAt).toBe(store.mirrorCachedAt);
        expect(sessionBoot.isLoadingAuth).toBe(false);
        expect(sessionBoot.hasLocalCache).toBe(false);
    });

    it('asks for a translation pack only after the extension list resolved', async () => {
        api.checkSession.mockResolvedValue(SIGNED_IN);
        recordingApp();
        await sessionBoot.boot();
        await vi.runOnlyPendingTimersAsync();

        expect(packs.loadExtensions).toHaveBeenCalledTimes(1);
        expect(packs.detect).toHaveBeenCalledTimes(1);
    });
});

describe('signing out', () => {
    it('empties this browser: server first, then the queue, then the database', async () => {
        cachedProject();
        sessionBoot.isAuthenticated = true;

        await sessionBoot.signOut();
        // The queue goes before the database: a queue left in memory is written back into the emptied
        // store by the next enqueue.
        expect(store.seq).toEqual(['logout', 'clearQueue', 'clearAll']);
        expect(store.projects).toEqual([]);
        expect(sessionBoot.isAuthenticated).toBe(false);
    });
});
