import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { render, fireEvent, cleanup, waitFor } from '@testing-library/svelte';
import type { UpdateStatus } from '$lib/types';

// An install that takes its own releases gets the buttons; one whose release is managed elsewhere gets the
// sentence naming who manages it, and nothing else. The service is real; the network and IndexedDB are not.

const api = vi.hoisted(() => ({
    fetchUpdateStatus: vi.fn(),
    checkForUpdates: vi.fn(),
    prepareUpdate: vi.fn(),
    applyUpdate: vi.fn(),
    restartToUpdate: vi.fn(),
}));

vi.mock('$lib/storage', () => ({
    loadAppSetting: vi.fn(async (_key: string, dflt: unknown) => dflt),
    saveAppSetting: vi.fn(async () => {}),
}));
vi.mock('$lib/api/updates', () => ({
    fetchUpdateStatus: () => api.fetchUpdateStatus(),
    checkForUpdates: () => api.checkForUpdates(),
    prepareUpdate: () => api.prepareUpdate(),
    applyUpdate: () => api.applyUpdate(),
    restartToUpdate: () => api.restartToUpdate(),
}));
vi.mock('$lib/api/config', () => ({ setUpdateCheckConsent: vi.fn(async () => {}) }));

const { updateCheck } = await import('$lib/services/update-check.svelte');
const UpdatesSection = (await import('$lib/components/ConfigModal/UpdatesSection.svelte')).default;

function status(over: Partial<UpdateStatus> = {}): UpdateStatus {
    return {
        current_version: '1.0.0',
        enabled: true,
        answered: true,
        available: false,
        managed: false,
        install: { kind: 'packaged', dir: '/opt/mike', can_stage: true },
        manifest_url: 'https://getmike.dev/download/version.json',
        ...over,
    };
}

const PREPARED = { version: '1.4.2', bytes: 4096, sha256: 'a'.repeat(64), files: 12 };
const APPLIED = {
    version: '1.4.2', bytes: 4096, sha256: 'a'.repeat(64),
    backup_path: '/opt/mike/.update-backup', replaced: ['mike-server'], restarting: true,
};

function open(over: Partial<UpdateStatus> = {}, enabled: boolean | null = true) {
    updateCheck.status = status(over);
    const onDirty = vi.fn();
    const utils = render(UpdatesSection, { props: { enabled, onDirty } });
    return { ...utils, onDirty };
}

beforeEach(() => {
    vi.clearAllMocks();
    updateCheck.phase = 'idle';
    updateCheck.fault = '';
    updateCheck.lastPrepared = null;
    updateCheck.lastApplied = null;
    updateCheck.lastRestart = null;
    api.fetchUpdateStatus.mockResolvedValue(status());
    api.prepareUpdate.mockResolvedValue(PREPARED);
    api.applyUpdate.mockResolvedValue(APPLIED);
    api.restartToUpdate.mockResolvedValue({ restarting: true, applies: '1.4.2' });
});

afterEach(() => {
    cleanup();
    updateCheck.status = null;
});

describe('a packaged install', () => {
    it('offers the download when a release is available', async () => {
        const { container } = open({ available: true, latest: '1.4.2' });
        const download = container.querySelector<HTMLButtonElement>('[data-testid="update-download"]');
        expect(download).toBeTruthy();
        expect(download?.disabled).toBe(false);
        expect(container.textContent).toContain('Release 1.4.2 is available');
    });

    it('refuses to start a second download while the server is preparing one', () => {
        const { container } = open({ available: true, latest: '1.4.2', working: 'prepare' });
        expect(container.querySelector<HTMLButtonElement>('[data-testid="update-download"]')?.disabled).toBe(true);
    });

    it('says what it cannot tell rather than claiming the install is current', () => {
        const { container } = open({ note: 'release 1.4.2 names no archive for linux/arm64' });
        const verdict = container.querySelector('[data-testid="update-verdict"]');
        expect(verdict?.textContent).toContain('names no archive');
        expect(verdict?.textContent).not.toContain('is the latest release');
    });

    it('says the row has no verdict until a check has produced a version', () => {
        const { container } = open({});
        expect(container.querySelector('[data-testid="update-verdict"]')).toBeNull();
    });

    it('names the host the switch is asking', () => {
        const { container } = open({});
        expect(container.textContent).toContain('getmike.dev');
    });
});

describe('a release that is downloaded and waiting', () => {
    // The counts come from the server's row, not from what this browser happens to remember: the row is
    // what a reload, a second tab and a restart all read.
    it('says what is on disk and what installing it would touch', () => {
        const { container } = open({ prepared_version: '1.4.2', prepared_bytes: 4096, prepared_files: 12 });
        const card = container.querySelector('[data-testid="update-prepared"]');
        expect(card?.textContent).toContain('1.4.2 is downloaded and verified');
        expect(card?.textContent).toContain('4.0 KB');
        expect(card?.textContent).toContain('12');
        expect(card?.textContent).not.toContain('0 files');
    });

    it('offers Install as the only way the files change', async () => {
        const { container } = open({ prepared_version: '1.4.2' });
        const install = container.querySelector<HTMLButtonElement>('[data-testid="update-install"]');
        expect(install?.textContent).toContain('1.4.2');
        expect(install?.disabled).toBe(false);
        fireEvent.click(install!);
        await waitFor(() => expect(api.applyUpdate).toHaveBeenCalledTimes(1));
        expect(api.restartToUpdate).not.toHaveBeenCalled();
    });

    it('does not offer a second download over one that is waiting', () => {
        const { container } = open({ available: true, latest: '1.4.2', prepared_version: '1.4.2' });
        expect(container.querySelector<HTMLButtonElement>('[data-testid="update-download"]')?.disabled).toBe(true);
    });

    it('will not start an install while another action holds the server', () => {
        const { container } = open({ prepared_version: '1.4.2', working: 'apply' });
        expect(container.querySelector<HTMLButtonElement>('[data-testid="update-install"]')?.disabled).toBe(true);
    });

    it('shows what the server noticed rather than swallowing it', () => {
        updateCheck.lastPrepared = { ...PREPARED, notes: ['the release carries no archive bridge, so it was left alone'] };
        const { container } = open({ prepared_version: '1.4.2' });
        expect(container.textContent).toContain('left alone');
    });
});

describe('staged and waiting for a restart', () => {
    it('says what is on disk, where the previous release went, and how to roll back', () => {
        const { container } = open({ staged_version: '1.4.2', backup_path: '/opt/mike/.update-backup' });
        expect(container.textContent).toContain('1.4.2 is on disk');
        expect(container.textContent).toContain('/opt/mike/.update-backup');
        expect(container.textContent).toContain('Rolling back is manual');
    });

    it('applies it only when asked', async () => {
        const { container } = open({ staged_version: '1.4.2' });
        const restart = container.querySelector<HTMLButtonElement>('[data-testid="update-restart"]');
        expect(restart?.disabled).toBe(false);
        fireEvent.click(restart!);
        await waitFor(() => expect(api.restartToUpdate).toHaveBeenCalledTimes(1));
    });

    it('says the server is going down, and stops offering the button', async () => {
        updateCheck.lastRestart = { restarting: true, applies: '1.4.2' };
        const { container } = open({ staged_version: '1.4.2' });
        await waitFor(() => expect(container.textContent).toContain('The server is restarting'));
        expect(container.querySelector('[data-testid="update-restart"]')).toBeNull();
    });

    it('says how to finish by hand when the install landed on a server with no restart path', () => {
        updateCheck.lastApplied = { ...APPLIED, restarting: false, restart_note: 'this process has no restart path' };
        const { container } = open({ staged_version: '1.4.2' });
        expect(container.textContent).toContain('this process has no restart path');
        expect(container.querySelector('[data-testid="update-restart"]')).toBeTruthy();
    });

    it('shows the sentence of a server that has no restart path once the click is refused', async () => {
        api.restartToUpdate.mockRejectedValue(new Error('this process has no restart path; restart it the way it was started'));
        const { container } = open({ staged_version: '1.4.2' });
        fireEvent.click(container.querySelector('[data-testid="update-restart"]')!);
        await waitFor(() => expect(container.textContent).toContain('no restart path'));
    });
});

describe('who decided', () => {
    it('replaces every action with the sentence naming the administrator', () => {
        const { container } = open({ managed: true, available: true, latest: '1.4.2', prepared_version: '1.4.2' });
        const toggle = container.querySelector<HTMLButtonElement>('button[role="switch"]');
        expect(toggle?.disabled).toBe(true);
        expect(toggle?.getAttribute('aria-checked')).toBe('false');
        expect(container.querySelector('[data-testid="update-managed"]')?.textContent)
            .toContain('Updates are managed by your administrator');
        expect(container.querySelector('[data-testid="update-check-now"]')).toBeNull();
        expect(container.querySelector('[data-testid="update-download"]')).toBeNull();
        expect(container.querySelector('[data-testid="update-install"]')).toBeNull();
        expect(container.querySelector('[data-testid="update-verdict"]')).toBeNull();
        expect(container.querySelector('code')).toBeNull();
        expect(container.textContent).not.toContain('docker compose');
        // The switch's description is what tells a screen reader the control is inert for a reason.
        expect(toggle?.getAttribute('aria-describedby')).toBe('update-check-desc');
    });

    it('leaves an off preference with nothing but the switch', () => {
        const { container } = open({ enabled: false }, false);
        expect(container.querySelector('[data-testid="update-verdict"]')).toBeNull();
        expect(container.querySelector('[data-testid="update-check-now"]')).toBeNull();
    });

    it('routes a toggle through the shared Save flow', async () => {
        const { container, onDirty } = open({}, null);
        const toggle = container.querySelector<HTMLButtonElement>('button[role="switch"]')!;
        expect(toggle.getAttribute('aria-checked')).toBe('false');
        fireEvent.click(toggle);
        await waitFor(() => expect(onDirty).toHaveBeenCalledTimes(1));
        expect(toggle.getAttribute('aria-checked')).toBe('true');
    });
});

describe('an action that was refused', () => {
    it('shows the server\'s sentence beside the buttons rather than as a toast', async () => {
        api.applyUpdate.mockRejectedValue(new Error('these tasks are running: Ship it — an update is not applied while work is in flight'));
        const { container } = open({ prepared_version: '1.4.2' });
        fireEvent.click(container.querySelector('[data-testid="update-install"]')!);
        await waitFor(() => expect(container.textContent).toContain('an update is not applied while work is in flight'));
        expect(container.querySelector('[role="toast"]')).toBeNull();
    });
});
