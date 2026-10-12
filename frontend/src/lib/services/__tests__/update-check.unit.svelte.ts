import { describe, it, expect, beforeEach, vi } from 'vitest';
import type { UpdateStatus } from '$lib/types';

// The service touches IndexedDB, /api/updates/* and the dialog, so all three are faked: under test is
// when it asks, what it downloads on its own, what the dot claims, and what an answer costs.

const CONSENT_KEY = 'update-check-consent-asked';
const PREPARE_KEY = 'update-prepare-stamp';

const store = vi.hoisted(() => ({ db: new Map<string, unknown>() }));
const api = vi.hoisted(() => ({
    fetchUpdateStatus: vi.fn(),
    checkForUpdates: vi.fn(),
    prepareUpdate: vi.fn(),
    applyUpdate: vi.fn(),
    restartToUpdate: vi.fn(),
}));
const configApi = vi.hoisted(() => ({ setUpdateCheckConsent: vi.fn(async (_enabled: boolean) => {}) }));
type Asked = { title: string; message: string; confirmText: string; cancelText: string; variant?: string };
const dialogs = vi.hoisted(() => ({ showConfirm: vi.fn(async (_asked: Asked) => false) }));

vi.mock('$lib/storage', () => ({
    loadAppSetting: vi.fn(async (key: string, dflt: unknown) => (store.db.has(key) ? store.db.get(key) : dflt)),
    saveAppSetting: vi.fn(async (key: string, value: unknown) => void store.db.set(key, value)),
}));
vi.mock('$lib/api/updates', () => ({
    fetchUpdateStatus: () => api.fetchUpdateStatus(),
    checkForUpdates: (auto?: boolean) => api.checkForUpdates(auto),
    prepareUpdate: () => api.prepareUpdate(),
    applyUpdate: () => api.applyUpdate(),
    restartToUpdate: () => api.restartToUpdate(),
}));
vi.mock('$lib/api/config', () => ({
    setUpdateCheckConsent: (enabled: boolean) => configApi.setUpdateCheckConsent(enabled),
}));
vi.mock('$lib/stores', () => ({ dialogStore: dialogs }));

const { updateCheck } = await import('$lib/services/update-check.svelte');

function status(over: Partial<UpdateStatus> = {}): UpdateStatus {
    return {
        current_version: '1.0.0',
        enabled: true,
        answered: true,
        available: false,
        disabled: false,
        install: { kind: 'packaged', dir: '/opt/mike', can_stage: true },
        manifest_url: 'https://getmike.dev/version.json',
        ...over,
    };
}

const PREPARED = { version: '1.4.2', bytes: 2048, sha256: 'a'.repeat(64), files: 12 };
const APPLIED = {
    version: '1.4.2', bytes: 2048, sha256: 'a'.repeat(64),
    backup_path: '/opt/mike/.update-backup', replaced: ['mike-server'], restarting: true,
};

beforeEach(() => {
    store.db.clear();
    vi.clearAllMocks();
    updateCheck.reset();
    updateCheck.status = null;
    updateCheck.phase = 'idle';
    updateCheck.fault = '';
    updateCheck.lastPrepared = null;
    updateCheck.lastApplied = null;
    updateCheck.lastRestart = null;
    api.fetchUpdateStatus.mockResolvedValue(status());
    api.checkForUpdates.mockResolvedValue(status({ latest: '1.4.2', available: true }));
    api.prepareUpdate.mockResolvedValue(PREPARED);
    api.applyUpdate.mockResolvedValue(APPLIED);
    dialogs.showConfirm.mockResolvedValue(false);
    configApi.setUpdateCheckConsent.mockResolvedValue(undefined);
});

describe('the daily check', () => {
    it('asks the manifest once a boot, after reading what is known', async () => {
        api.checkForUpdates.mockResolvedValue(status());
        await updateCheck.start();
        expect(api.fetchUpdateStatus).toHaveBeenCalledTimes(1);
        expect(api.checkForUpdates).toHaveBeenCalledTimes(1);
    });

    it('does not ask when the operator has not turned checks on', async () => {
        api.fetchUpdateStatus.mockResolvedValue(status({ enabled: false }));
        await updateCheck.start();
        expect(api.checkForUpdates).not.toHaveBeenCalled();
    });

    it('asks as the install when it boots, and as a person when the button is clicked', async () => {
        api.checkForUpdates.mockResolvedValue(status());

        await updateCheck.start();
        expect(api.checkForUpdates).toHaveBeenLastCalledWith(true);

        await updateCheck.checkNow();
        expect(api.checkForUpdates).toHaveBeenLastCalledWith(false);
    });

    it('keeps no day of its own, because one browser is not one install', async () => {
        api.checkForUpdates.mockResolvedValue(status());

        await updateCheck.start();
        updateCheck.reset();
        await updateCheck.start();

        // Both boots asked, and the install's day is what decided whether the manifest got dialled.
        expect(api.checkForUpdates).toHaveBeenCalledTimes(2);
        expect([...store.db.keys()].filter(key => key.includes('day'))).toEqual([]);
    });

    it('asks again when the button asked for it', async () => {
        api.checkForUpdates.mockResolvedValue(status());
        await updateCheck.checkNow();
        await updateCheck.checkNow();
        expect(api.checkForUpdates).toHaveBeenCalledTimes(2);
    });

    it('keeps the last good answer and names the failure when a check fails', async () => {
        api.fetchUpdateStatus.mockResolvedValue(status({ latest: '1.4.2', available: true, last_error: 'no answer' }));
        api.checkForUpdates.mockRejectedValue(new Error('no answer'));

        await updateCheck.checkNow();

        expect(updateCheck.fault).toBe('no answer');
        expect(updateCheck.status?.available).toBe(true);
        // A check that never answered names no release, so nothing is fetched off the older answer.
        expect(api.prepareUpdate).not.toHaveBeenCalled();
    });

    it('says nothing about a status the server could not be asked for', async () => {
        api.fetchUpdateStatus.mockRejectedValue(new Error('offline'));
        await updateCheck.start();
        expect(updateCheck.status).toBeNull();
        expect(updateCheck.dot).toBe('none');
    });
});

describe('the automatic download', () => {
    it('fetches the newer release the check found, without being asked for it', async () => {
        await updateCheck.start();
        expect(api.prepareUpdate).toHaveBeenCalledTimes(1);
    });

    it('fetches nothing for an install that cannot replace its own files', async () => {
        api.checkForUpdates.mockResolvedValue(
            status({ latest: '1.4.2', available: true, install: { kind: 'container', can_stage: false } })
        );
        await updateCheck.start();
        expect(api.prepareUpdate).not.toHaveBeenCalled();
    });

    it('never fetches over a release that is staged or already waiting', async () => {
        for (const owed of [{ staged_version: '1.4.1' }, { prepared_version: '1.4.1' }]) {
            store.db.clear();
            api.prepareUpdate.mockClear();
            api.checkForUpdates.mockResolvedValue(status({ latest: '1.4.2', available: true, ...owed }));
            await updateCheck.start();
            expect(api.prepareUpdate).not.toHaveBeenCalled();
        }
    });

    it('does not fetch when checks are off, whatever the last answer said', async () => {
        api.fetchUpdateStatus.mockResolvedValue(status({ enabled: false, available: true, latest: '1.4.2' }));
        await updateCheck.start();
        expect(api.prepareUpdate).not.toHaveBeenCalled();
    });

    it('tries one release once a day, not once a reload', async () => {
        api.fetchUpdateStatus.mockResolvedValue(status({ latest: '1.4.2', available: true }));
        await updateCheck.start();
        expect(store.db.get(PREPARE_KEY)).toBeTypeOf('string');

        updateCheck.reset();
        await updateCheck.start();
        expect(api.prepareUpdate).toHaveBeenCalledTimes(1);
    });

    it('tries a newer release the day it appears', async () => {
        api.fetchUpdateStatus.mockResolvedValue(status({ latest: '1.4.2', available: true }));
        await updateCheck.start();
        // The install knows the newer release by then, so both reads of it answer 1.5.0.
        api.fetchUpdateStatus.mockResolvedValue(status({ latest: '1.5.0', available: true }));
        api.checkForUpdates.mockResolvedValue(status({ latest: '1.5.0', available: true }));

        updateCheck.reset();
        await updateCheck.start();
        expect(api.prepareUpdate).toHaveBeenCalledTimes(2);
    });

    it('keeps a download that failed as tried for the day, and carries the reason', async () => {
        api.fetchUpdateStatus.mockResolvedValue(status({ latest: '1.4.2', available: true }));
        api.prepareUpdate.mockRejectedValue(new Error('no space left on device'));
        await updateCheck.start();
        expect(updateCheck.fault).toContain('no space');

        updateCheck.reset();
        await updateCheck.start();
        expect(api.prepareUpdate).toHaveBeenCalledTimes(1);
    });

    it('does not start a second download while the server is preparing', async () => {
        api.checkForUpdates.mockResolvedValue(status({ latest: '1.4.2', available: true, working: 'prepare' }));
        await updateCheck.start();
        expect(api.prepareUpdate).not.toHaveBeenCalled();
    });
});

describe('the dot', () => {
    it('lights green on a newer release with an archive for this machine', async () => {
        api.fetchUpdateStatus.mockResolvedValue(status({ available: true, latest: '1.4.2' }));
        await updateCheck.refresh();
        expect(updateCheck.dot).toBe('update');
    });

    it('reads ready when a release is downloaded and waiting to be installed', async () => {
        api.fetchUpdateStatus.mockResolvedValue(status({ available: true, latest: '1.4.2', prepared_version: '1.4.2' }));
        await updateCheck.refresh();
        expect(updateCheck.dot).toBe('ready');
    });

    it('asks a question while the opt-in answer is owed', async () => {
        api.fetchUpdateStatus.mockResolvedValue(status({ answered: false }));
        await updateCheck.refresh();
        expect(updateCheck.dot).toBe('question');
    });

    it('stays dark when the organization forbids updates', async () => {
        api.fetchUpdateStatus.mockResolvedValue(status({ answered: false, disabled: true }));
        await updateCheck.refresh();
        expect(updateCheck.dot).toBe('none');
    });

    it('is dark on a build whose version cannot be compared', async () => {
        api.fetchUpdateStatus.mockResolvedValue(status({ available: false, note: 'this build reports its version as "dev"' }));
        await updateCheck.refresh();
        expect(updateCheck.dot).toBe('none');
    });
});

describe('the opt-in question', () => {
    it('asks while the answer is owed, and the written answer is what ends it', async () => {
        api.fetchUpdateStatus.mockResolvedValue(status({ answered: false }));
        api.checkForUpdates.mockResolvedValue(status());
        dialogs.showConfirm.mockResolvedValue(true);

        await updateCheck.start();

        expect(configApi.setUpdateCheckConsent).toHaveBeenCalledWith(true);

        // The answer did not land — a tab closed mid-question, a write that failed — so the install still
        // owes it and the question comes again. Only the preference ends it, never a dismissal.
        updateCheck.status = status({ answered: false });
        await updateCheck.askConsent();
        expect(dialogs.showConfirm).toHaveBeenCalledTimes(2);
        // Nothing is kept in the browser about having asked.
        expect([...store.db.keys()]).not.toContain(CONSENT_KEY);
    });

    it('takes a decline as an answer too', async () => {
        api.fetchUpdateStatus.mockResolvedValue(status({ answered: false }));
        api.checkForUpdates.mockResolvedValue(status());
        await updateCheck.start();
        expect(configApi.setUpdateCheckConsent).toHaveBeenCalledWith(false);
    });

    it('never asks an install that already answered', async () => {
        api.checkForUpdates.mockResolvedValue(status());
        await updateCheck.start();
        expect(dialogs.showConfirm).not.toHaveBeenCalled();
        expect(configApi.setUpdateCheckConsent).not.toHaveBeenCalled();
    });

    it('asks for the decision in plain words, with No and Yes', async () => {
        api.fetchUpdateStatus.mockResolvedValue(status({ answered: false }));
        api.checkForUpdates.mockResolvedValue(status());
        await updateCheck.start();
        const [asked] = dialogs.showConfirm.mock.calls[0];
        expect(asked.title).toBe('Enable automatic updates?');
        expect(asked.message).toBe('Would you like MIKE to automatically check for updates once per day?');
        expect(asked.cancelText).toBe('No');
        expect(asked.confirmText).toBe('Yes');
        // A question, not a thing being undone, so it is not dressed in the destructive red.
        expect(asked.variant).toBe('default');
    });
});

describe('a deployment that forbids updates', () => {
    const forbidden = { disabled: true, answered: false, available: true, latest: '1.4.2', prepared_version: '1.4.2' };

    it('asks, checks, downloads and prompts nothing', async () => {
        api.fetchUpdateStatus.mockResolvedValue(status(forbidden));
        await updateCheck.start();
        expect(api.checkForUpdates).not.toHaveBeenCalled();
        expect(api.prepareUpdate).not.toHaveBeenCalled();
        expect(dialogs.showConfirm).not.toHaveBeenCalled();
        expect(configApi.setUpdateCheckConsent).not.toHaveBeenCalled();
        expect(updateCheck.dot).toBe('none');
    });

    // The switch outranks an answer that is still stored, and a release still sitting on disk.
    it('is not answered by the release it already downloaded', async () => {
        api.fetchUpdateStatus.mockResolvedValue(status(forbidden));
        await updateCheck.refresh();
        expect(updateCheck.dot).toBe('none');
        expect(updateCheck.consentOwed).toBe(false);
    });
});

describe('the install prompt', () => {
    it('offers the install once a download lands, and installs on the yes', async () => {
        dialogs.showConfirm.mockResolvedValue(true);
        await updateCheck.start();

        expect(dialogs.showConfirm).toHaveBeenCalledTimes(1);
        const [asked] = dialogs.showConfirm.mock.calls[0];
        expect(asked.title).toContain('1.4.2');
        // The destructive red is honest only because the label says what the click does: the files are
        // replaced and the server restarts onto them.
        expect(asked.confirmText).toBe('Install and Restart');
        expect(asked.cancelText).toBe('Later');
        expect(api.applyUpdate).toHaveBeenCalledTimes(1);
    });

    it('takes Later as an answer and leaves the release waiting', async () => {
        dialogs.showConfirm.mockResolvedValue(false);
        await updateCheck.start();
        expect(api.applyUpdate).not.toHaveBeenCalled();
        expect(updateCheck.lastPrepared?.version).toBe('1.4.2');
    });

    it('asks nothing when the download is refused', async () => {
        api.prepareUpdate.mockRejectedValue(new Error('no space left on device'));
        await updateCheck.start();
        expect(dialogs.showConfirm).not.toHaveBeenCalled();
    });
});

describe('installing and restarting', () => {
    it('reports the refusal a busy server answers with', async () => {
        api.applyUpdate.mockRejectedValue(new Error('these tasks are running: Ship it — an update is not applied while work is in flight'));
        await updateCheck.apply();
        expect(updateCheck.fault).toContain('Ship it');
        expect(updateCheck.phase).toBe('idle');
    });

    it('re-reads the status when the swap landed but the process will not restart itself', async () => {
        api.applyUpdate.mockResolvedValue({ ...APPLIED, restarting: false, restart_note: 'this process has no restart path' });
        await updateCheck.apply();
        expect(updateCheck.lastApplied?.restart_note).toContain('no restart path');
        expect(updateCheck.phase).toBe('idle');
        expect(api.fetchUpdateStatus).toHaveBeenCalled();
    });

    it('holds the phase once the process is going down', async () => {
        await updateCheck.apply();
        expect(updateCheck.lastApplied?.restarting).toBe(true);
        expect(updateCheck.phase).toBe('applying');
        expect(api.fetchUpdateStatus).not.toHaveBeenCalled();
    });

    it('holds the restart answer once the process is going down', async () => {
        api.restartToUpdate.mockResolvedValue({ restarting: true, applies: '1.4.2' });
        await updateCheck.restart();
        expect(updateCheck.lastRestart?.restarting).toBe(true);
        expect(updateCheck.phase).toBe('restarting');
    });

    it('will not run two actions at once', async () => {
        let releasePrepare: (value: typeof PREPARED) => void = () => {};
        api.prepareUpdate.mockReturnValue(new Promise((resolve) => {
            releasePrepare = resolve;
        }));
        const preparing = updateCheck.prepare();
        expect(updateCheck.busy).toBe(true);

        await updateCheck.restart();
        await updateCheck.apply();
        expect(api.restartToUpdate).not.toHaveBeenCalled();
        expect(api.applyUpdate).not.toHaveBeenCalled();

        releasePrepare(PREPARED);
        await preparing;
        expect(updateCheck.busy).toBe(false);
    });
});
