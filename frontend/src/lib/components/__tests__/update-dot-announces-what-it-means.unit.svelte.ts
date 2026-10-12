import { it, expect, afterEach, beforeEach, vi } from 'vitest';
import { render, cleanup } from '@testing-library/svelte';
import type { UpdateStatus } from '$lib/types';

// The dot is the only thing the board says about releases, so its reason has to be in the accessible name
// and visible without color: three states, three sentences, three classes.

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
vi.mock('$lib/api/updates', () => api);
vi.mock('$lib/api/config', () => ({ setUpdateCheckConsent: vi.fn(async () => {}) }));

const { updateCheck } = await import('$lib/services/update-check.svelte');
const UpdateDot = (await import('$lib/components/UpdateDot.svelte')).default;

function status(over: Partial<UpdateStatus> = {}): UpdateStatus {
    return {
        current_version: '1.0.0',
        enabled: true,
        answered: true,
        available: false,
        disabled: false,
        install: { kind: 'packaged', dir: '/opt/mike', can_stage: true },
        manifest_url: 'https://getmike.dev/download/version.json',
        ...over,
    };
}

function renderDot(over: Partial<UpdateStatus>) {
    updateCheck.status = status(over);
    const { container } = render(UpdateDot);
    return container.querySelector('[data-update-dot]');
}

beforeEach(() => {
    vi.clearAllMocks();
    updateCheck.status = null;
});

afterEach(() => {
    cleanup();
    updateCheck.status = null;
});

it('shows nothing when there is nothing to say', () => {
    expect(renderDot({})).toBeNull();
});

it('names the newer release on a plain green dot', () => {
    const dot = renderDot({ available: true, latest: '1.4.2' });
    expect(dot?.getAttribute('data-update-dot')).toBe('update');
    expect(dot?.className).toContain('update');
    expect(dot?.getAttribute('aria-label')).toBe('A new release is available');
});

it('looks different and says something different once the release is on disk', () => {
    const dot = renderDot({ available: true, latest: '1.4.2', prepared_version: '1.4.2' });
    expect(dot?.getAttribute('data-update-dot')).toBe('ready');
    expect(dot?.className).toContain('ready');
    expect(dot?.getAttribute('aria-label')).toBe('A release is downloaded and waiting to be installed');
});

it('reads as a question while the opt-in answer is owed', () => {
    const dot = renderDot({ answered: false });
    expect(dot?.getAttribute('data-update-dot')).toBe('question');
    expect(dot?.className).toContain('question');
    expect(dot?.getAttribute('aria-label')).toContain('asking');
});
