import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, fireEvent, cleanup, waitFor } from '@testing-library/svelte';
import ConfigModal from '$lib/components/ConfigModal.svelte';
import type { Config } from '$lib/types';
import { updateConfig } from '$lib/api';
import { saveAppSetting } from '$lib/storage';
import { uiStore } from '$lib/stores';

// The choice belongs to the browser: the form opens on what this browser stored, and Save writes
// it to IndexedDB without telling the server.

vi.mock('$lib/api', async (importOriginal) => {
    const actual = await importOriginal<typeof import('$lib/api')>();
    return { ...actual, updateConfig: vi.fn() };
});

vi.mock('$lib/storage', async (importOriginal) => {
    const actual = await importOriginal<typeof import('$lib/storage')>();
    // Some stores read their settings at import, so every read must answer with a promise.
    return {
        ...actual,
        loadAppSetting: vi.fn(async (_key: string, defaultValue: unknown) => defaultValue),
        saveAppSetting: vi.fn(async () => {}),
    };
});

const KEY = 'auto-maximize-code-viewer';
const TOGGLE = 'button[aria-label="Auto maximize code viewer"]';
const SAVE = 'button[title="Save settings"]';

function makeConfig(overrides: Partial<Config> = {}): Config {
    return {
        workspace_dir: '/tmp/wt',
        database_path: '/tmp/db',
        version: '1.0.0',
        providers: [
            { id: 1, url: 'http://localhost:9999/v1', models: [{ name: 'model-a' }], concurrency: 1, max_requests_per_second: 0, stream_idle_seconds: 0 },
        ],
        worker_provider: '1',
        worker_model: 'model-a',
        utility_provider: '1',
        utility_model: 'model-a',
        ...overrides,
    };
}

async function openPreferences(config: Config) {
    const utils = render(ConfigModal, { props: { config, onClose: vi.fn(), onSaved: vi.fn() } });
    await waitFor(() => expect(utils.container.querySelector('button.tab-btn')).toBeTruthy(), { timeout: 1000 });
    const tabs = Array.from(utils.container.querySelectorAll('button.tab-btn'));
    fireEvent.click(tabs.find((b) => b.textContent?.trim() === 'Preferences')!);
    await waitFor(() => expect(utils.container.querySelector(TOGGLE)).toBeTruthy(), { timeout: 1000 });
    return utils;
}

beforeEach(() => {
    vi.stubGlobal('matchMedia', (query: string) => ({
        matches: false,
        media: query,
        onchange: null,
        addEventListener: () => {},
        removeEventListener: () => {},
        addListener: () => {},
        removeListener: () => {},
        dispatchEvent: () => false,
    }));
    vi.mocked(updateConfig).mockReset();
    // Cleared, not reset: a reset drops the async implementation, and the store calls
    // `.catch()` on the result.
    vi.mocked(saveAppSetting).mockClear();
});

afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    uiStore.autoMaximizeCodeViewer = true;
});

describe('ConfigModal auto-maximize-code-viewer save', () => {
    it('opens on this browser\'s stored off', async () => {
        uiStore.autoMaximizeCodeViewer = false;
        vi.mocked(updateConfig).mockResolvedValue(makeConfig());

        const { container } = await openPreferences(makeConfig());
        expect(container.querySelector(TOGGLE)?.getAttribute('aria-checked')).toBe('false');
    });

    it('writes the flipped-off choice to this browser and tells the server nothing of it', async () => {
        vi.mocked(updateConfig).mockResolvedValue(makeConfig());

        const { container } = await openPreferences(makeConfig());
        expect(container.querySelector(TOGGLE)?.getAttribute('aria-checked')).toBe('true');

        await fireEvent.click(container.querySelector(TOGGLE)!);
        fireEvent.click(container.querySelector(SAVE)!);
        await waitFor(() => expect(updateConfig).toHaveBeenCalledTimes(1), { timeout: 1000 });

        await waitFor(() => expect(saveAppSetting).toHaveBeenCalledWith(KEY, false), { timeout: 1000 });
        expect(uiStore.autoMaximizeCodeViewer).toBe(false);
        const body = vi.mocked(updateConfig).mock.calls[0][0] as unknown as Record<string, unknown>;
        expect(Object.keys(body).filter((k) => k.includes('code_viewer'))).toEqual([]);
    });

    it('keeps an untouched choice stored on an unrelated save', async () => {
        uiStore.autoMaximizeCodeViewer = false;
        vi.mocked(updateConfig).mockResolvedValue(makeConfig());

        const { container } = await openPreferences(makeConfig());
        fireEvent.click(container.querySelector(SAVE)!);
        await waitFor(() => expect(updateConfig).toHaveBeenCalledTimes(1), { timeout: 1000 });

        await waitFor(() => expect(saveAppSetting).toHaveBeenCalledWith(KEY, false), { timeout: 1000 });
    });
});
