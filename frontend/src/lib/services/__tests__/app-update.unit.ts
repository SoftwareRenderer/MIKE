import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';

// Which authority decides, when the check runs at all, and that nothing in it reloads on a guess.

const listeners = new Map<string, (event: any) => void>();
const mockRegister = vi.fn().mockResolvedValue(undefined);
const mockMessageSkipWaiting = vi.fn();

class MockWorkbox {
    addEventListener = (type: string, handler: (event: any) => void) => listeners.set(type, handler);
    register = mockRegister;
    messageSkipWaiting = mockMessageSkipWaiting;
}

vi.mock('workbox-window', () => ({ Workbox: MockWorkbox }));

// Stateful, so the gate is tested against the stamp the module writes, not a copy of its formula.
const persisted = new Map<string, unknown>();
const mockLoadAppSetting = vi.fn(async (key: string, fallback: unknown) =>
    persisted.has(key) ? persisted.get(key) : fallback,
);
const mockSaveAppSetting = vi.fn(async (key: string, value: unknown) => {
    persisted.set(key, value);
});

vi.mock('$lib/storage', () => ({
    loadAppSetting: mockLoadAppSetting,
    saveAppSetting: mockSaveAppSetting,
}));

const mockApiFetch = vi.fn();
vi.mock('$lib/api/helpers', () => ({ apiFetch: mockApiFetch }));

type FakeWorker = {
    state: string;
    addEventListener: (type: string, handler: () => void) => void;
    removeEventListener: (type: string, handler: () => void) => void;
};

type FakeRegistration = {
    active: object | null;
    waiting: object | null;
    installing: FakeWorker | null;
    updates: number;
    update: () => Promise<void>;
    /**
     * End the install the browser started, the way a browser does it: the slot moves first, then the
     * state change fires. The test decides when, because a real install finishes whenever the precache
     * does — including after `update()` has already answered.
     */
    finishInstall: (as: Installs) => void;
};

/** `waiting` = the worker stops at installed behind the one in control; `activated` = it took over. */
type Installs = 'waiting' | 'activated';

function fakeRegistration({
    waiting = false,
    appearsOnUpdate = false,
    onUpdate = false,
    alreadyInstalling = false,
}: {
    waiting?: boolean;
    appearsOnUpdate?: boolean;
    /** `update()` finds a new build and starts installing it. */
    onUpdate?: boolean;
    /** The browser's own check found it before anyone asked this registration. */
    alreadyInstalling?: boolean;
} = {}): FakeRegistration {
    const handlers = new Set<() => void>();
    const worker: FakeWorker = {
        state: 'installing',
        addEventListener: (_type, handler) => handlers.add(handler),
        removeEventListener: (_type, handler) => handlers.delete(handler),
    };
    const reg: FakeRegistration = {
        active: {},
        waiting: waiting ? {} : null,
        installing: alreadyInstalling ? worker : null,
        updates: 0,
        update: async () => {
            reg.updates++;
            if (appearsOnUpdate) reg.waiting = {};
            if (onUpdate) reg.installing = worker;
        },
        finishInstall: (as) => {
            reg.installing = null;
            if (as === 'activated') {
                worker.state = 'activated';
            } else {
                worker.state = 'installed';
                reg.waiting = worker;
            }
            handlers.forEach((handler) => handler());
        },
    };
    return reg;
}

/** A browser that supports a worker, with `registration` as the one already in this scope. */
function setServiceWorker(registration: FakeRegistration | null) {
    // Deleted, not set to undefined: `'serviceWorker' in navigator` answers yes to a property holding
    // undefined, which conflates "no registration" with "no service worker".
    delete (navigator as any).serviceWorker;
    Object.defineProperty(navigator, 'serviceWorker', {
        value: { getRegistration: async () => registration },
        configurable: true,
        writable: true,
    });
}

/** A browser that will not register a worker at all, which is where the identity takes over. */
function withoutServiceWorker() {
    delete (navigator as any).serviceWorker;
}

function setRunningBuildId(id: string | null) {
    document.head.querySelector('meta[name="app-build"]')?.remove();
    if (id !== null) {
        const meta = document.createElement('meta');
        meta.setAttribute('name', 'app-build');
        meta.setAttribute('content', id);
        document.head.appendChild(meta);
    }
}

function servedBuildId(assets: unknown) {
    mockApiFetch.mockResolvedValue({ ok: true, json: async () => ({ version: 'v1', assets }) });
}

const reload = vi.fn();

async function loadModule() {
    vi.resetModules();
    return import('$lib/services/app-update');
}

describe('app-update', () => {
    beforeEach(() => {
        vi.resetModules();
        mockRegister.mockClear();
        mockMessageSkipWaiting.mockClear();
        mockLoadAppSetting.mockClear();
        mockSaveAppSetting.mockClear();
        mockApiFetch.mockReset();
        listeners.clear();
        persisted.clear();
        // The automatic path declines under `vite dev`, and vitest is a dev build.
        vi.stubEnv('DEV', false);
        Object.defineProperty(window, 'location', {
            value: { reload },
            configurable: true,
            writable: true,
        });
        reload.mockClear();
        setServiceWorker(null);
        setRunningBuildId('aaaaaaaaaaaa');
    });

    afterEach(() => {
        vi.unstubAllEnvs();
    });

    describe('registering the worker', () => {
        it('registers once, however many times the app is opened', async () => {
            const mod = await loadModule();
            mod.startAppUpdate();
            mod.startAppUpdate();

            expect(mockRegister).toHaveBeenCalledTimes(1);
        });

        it('reloads when a new worker takes over as an update', async () => {
            const mod = await loadModule();
            mod.startAppUpdate();

            listeners.get('controlling')!({ isUpdate: true });
            expect(reload).toHaveBeenCalledTimes(1);
        });

        // The worker already there at page load is not an update; reloading for it loops on open.
        it('does not reload for the worker that was in control already', async () => {
            const mod = await loadModule();
            mod.startAppUpdate();

            listeners.get('controlling')!({ isUpdate: false });
            expect(reload).not.toHaveBeenCalled();
        });
    });

    describe('the automatic check, with a worker registered', () => {
        it('asks the worker, and asks the server nothing when no worker is waiting', async () => {
            const reg = fakeRegistration();
            setServiceWorker(reg);
            const mod = await loadModule();
            mod.startAppUpdate();
            await new Promise((r) => setTimeout(r, 0));

            expect(reg.updates).toBe(1);
            expect(mockApiFetch).not.toHaveBeenCalled();
            expect(mockMessageSkipWaiting).not.toHaveBeenCalled();
            expect(reload).not.toHaveBeenCalled();
        });

        it('releases a worker that is waiting and lets it reload the page', async () => {
            setServiceWorker(fakeRegistration({ waiting: true }));
            const mod = await loadModule();
            mod.startAppUpdate();
            await new Promise((r) => setTimeout(r, 0));

            expect(mockMessageSkipWaiting).toHaveBeenCalledTimes(1);
            // No direct reload: `controlling` is what says the new build is in charge.
            expect(reload).not.toHaveBeenCalled();
        });

        it('releases a worker that only appeared once the check asked', async () => {
            setServiceWorker(fakeRegistration({ appearsOnUpdate: true }));
            const mod = await loadModule();
            mod.startAppUpdate();
            await new Promise((r) => setTimeout(r, 0));

            expect(mockMessageSkipWaiting).toHaveBeenCalledTimes(1);
        });
    });

    // `update()` answering is not the new build being ready: its worker still has every asset to
    // precache. These are the clicks that used to need a retry.
    describe('the install the check waits on', () => {
        /**
         * Open the app against a registration with nothing new, so the manual check under test runs on
         * the Workbox instance the registration created but is the only one asked anything.
         */
        async function openApp() {
            setServiceWorker(fakeRegistration());
            const mod = await loadModule();
            mod.startAppUpdate();
            await new Promise((r) => setTimeout(r, 0));
            mockMessageSkipWaiting.mockClear();
            return mod;
        }

        it('releases a worker that finishes installing after the check asked', async () => {
            const mod = await openApp();
            const reg = fakeRegistration({ onUpdate: true });
            setServiceWorker(reg);

            const checking = mod.checkNow();
            await new Promise((r) => setTimeout(r, 0));
            expect(reg.updates).toBe(1);
            expect(reg.installing).not.toBeNull();
            expect(mockMessageSkipWaiting).not.toHaveBeenCalled();

            reg.finishInstall('waiting');

            expect(await checking).toBe('applying');
            expect(mockMessageSkipWaiting).toHaveBeenCalledTimes(1);
        });

        it('asks nothing of a browser already installing a new worker', async () => {
            // `update()` over an in-flight install is a rejected call, and a rejected call was an answer
            // of "could not determine" on the click that was about to succeed.
            const mod = await openApp();
            const reg = fakeRegistration({ alreadyInstalling: true });
            setServiceWorker(reg);

            const checking = mod.checkNow();
            await new Promise((r) => setTimeout(r, 0));
            expect(reg.updates).toBe(0);

            reg.finishInstall('waiting');

            expect(await checking).toBe('applying');
            expect(mockMessageSkipWaiting).toHaveBeenCalledTimes(1);
        });

        it('answers current when the installing worker takes over instead of waiting', async () => {
            const mod = await openApp();
            const reg = fakeRegistration({ onUpdate: true });
            setServiceWorker(reg);

            const checking = mod.checkNow();
            await new Promise((r) => setTimeout(r, 0));
            reg.finishInstall('activated');

            expect(await checking).toBe('current');
            expect(mockMessageSkipWaiting).not.toHaveBeenCalled();
            expect(reload).not.toHaveBeenCalled();
        });

        it('answers current without waiting when nothing new was found', async () => {
            const mod = await openApp();
            const reg = fakeRegistration();
            setServiceWorker(reg);

            expect(await mod.checkNow()).toBe('current');
            expect(reg.updates).toBe(1);
            expect(mockMessageSkipWaiting).not.toHaveBeenCalled();
            expect(reload).not.toHaveBeenCalled();
        });
    });

    describe('the day gate', () => {
        it('runs once per local day, and the second open asks nobody', async () => {
            const reg = fakeRegistration();
            setServiceWorker(reg);
            const first = await loadModule();
            first.startAppUpdate();
            await new Promise((r) => setTimeout(r, 0));

            const second = await loadModule();
            second.startAppUpdate();
            await new Promise((r) => setTimeout(r, 0));

            expect(mockSaveAppSetting).toHaveBeenCalledTimes(1);
            expect(reg.updates).toBe(1);
        });

        it('compares nothing when the identity of the running document is unknown', async () => {
            setRunningBuildId(null);
            servedBuildId('bbbbbbbbbbbb');
            const mod = await loadModule();

            expect(await mod.checkNow()).toBe('unknown');
            expect(mockApiFetch).not.toHaveBeenCalled();
            expect(reload).not.toHaveBeenCalled();
        });

        it('does not check when the stamp cannot be written', async () => {
            // With no record that this ran, a reload could repeat on every load.
            mockSaveAppSetting.mockRejectedValueOnce(new Error('quota'));
            const reg = fakeRegistration();
            setServiceWorker(reg);
            const mod = await loadModule();
            mod.startAppUpdate();
            await new Promise((r) => setTimeout(r, 0));

            expect(reg.updates).toBe(0);
            expect(reload).not.toHaveBeenCalled();
        });

        it('does not check at all in a dev build', async () => {
            vi.stubEnv('DEV', true);
            setServiceWorker(fakeRegistration());
            const mod = await loadModule();
            mod.startAppUpdate();
            await new Promise((r) => setTimeout(r, 0));

            expect(mockSaveAppSetting).not.toHaveBeenCalled();
        });

        // The one thing a reload cannot recover: a password typed but not submitted.
        it('will not reload over text somebody is typing', async () => {
            setServiceWorker(fakeRegistration({ waiting: true }));
            const field = document.createElement('input');
            field.value = 'hunter2';
            document.body.appendChild(field);
            field.focus();
            try {
                const mod = await loadModule();
                mod.startAppUpdate();
                await new Promise((r) => setTimeout(r, 0));
            } finally {
                field.remove();
            }

            expect(mockMessageSkipWaiting).not.toHaveBeenCalled();
            expect(reload).not.toHaveBeenCalled();
        });

        // And not a refusal to ever update: a focused field holding nothing is nobody typing.
        it('still updates when the focused field holds nothing', async () => {
            setServiceWorker(fakeRegistration({ waiting: true }));
            const field = document.createElement('input');
            document.body.appendChild(field);
            field.focus();
            try {
                const mod = await loadModule();
                mod.startAppUpdate();
                await new Promise((r) => setTimeout(r, 0));
            } finally {
                field.remove();
            }

            expect(mockMessageSkipWaiting).toHaveBeenCalledTimes(1);
        });
    });

    describe('the manual check', () => {
        it('answers current when the served build is the running one', async () => {
            servedBuildId('aaaaaaaaaaaa');
            const mod = await loadModule();

            expect(await mod.checkNow()).toBe('current');
            expect(reload).not.toHaveBeenCalled();
        });

        it('reloads when the served build is a different one', async () => {
            servedBuildId('bbbbbbbbbbbb');
            const mod = await loadModule();

            expect(await mod.checkNow()).toBe('applying');
            expect(reload).toHaveBeenCalledTimes(1);
        });

        it('runs even the day the automatic check already ran', async () => {
            servedBuildId('aaaaaaaaaaaa');
            const mod = await loadModule();
            mod.startAppUpdate();
            await new Promise((r) => setTimeout(r, 0));
            const afterOpen = mockApiFetch.mock.calls.length;

            expect(await mod.checkNow()).toBe('current');
            expect(mockApiFetch.mock.calls.length).toBe(afterOpen + 1);
        });

        it('answers unknown when the server names no build', async () => {
            servedBuildId('');
            const mod = await loadModule();

            expect(await mod.checkNow()).toBe('unknown');
            expect(reload).not.toHaveBeenCalled();
        });

        it('answers unknown when the request fails, and says nothing by reloading', async () => {
            mockApiFetch.mockRejectedValue(new Error('connection refused'));
            const mod = await loadModule();

            expect(await mod.checkNow()).toBe('unknown');
            expect(reload).not.toHaveBeenCalled();
        });

        it('uses the identity when the platform has no service worker', async () => {
            withoutServiceWorker();
            servedBuildId('bbbbbbbbbbbb');
            const mod = await loadModule();

            expect(await mod.checkNow()).toBe('applying');
            expect(reload).toHaveBeenCalledTimes(1);
        });

        it('answers unknown when the answer is an error status', async () => {
            mockApiFetch.mockResolvedValue({ ok: false, json: async () => ({}) });
            const mod = await loadModule();

            expect(await mod.checkNow()).toBe('unknown');
            expect(reload).not.toHaveBeenCalled();
        });

        it('releases a waiting worker rather than reloading itself', async () => {
            setServiceWorker(fakeRegistration({ waiting: true }));
            const mod = await loadModule();
            mod.startAppUpdate();
            await new Promise((r) => setTimeout(r, 0));
            mockMessageSkipWaiting.mockClear();

            expect(await mod.checkNow()).toBe('applying');
            expect(mockMessageSkipWaiting).toHaveBeenCalledTimes(1);
            expect(reload).not.toHaveBeenCalled();
        });
    });
});
