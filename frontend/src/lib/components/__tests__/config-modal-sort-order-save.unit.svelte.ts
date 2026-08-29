import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, fireEvent, cleanup, waitFor } from '@testing-library/svelte';
import ConfigModal from '$lib/components/ConfigModal.svelte';
import type { Config } from '$lib/types';
import { updateConfig } from '$lib/api';

// Board sort direction is a view preference, but it rides the shared Settings Save
// payload — and PUT /api/config replaces the whole object, so the field must always
// be sent (mirrors how run_siblings_parallel is asserted).

vi.mock('$lib/api', async (importOriginal) => {
    const actual = await importOriginal<typeof import('$lib/api')>();
    return { ...actual, updateConfig: vi.fn() };
});

const TOGGLE = 'button[aria-label="Oldest Task on top"]';
const SAVE = 'button[title="Save settings"]';

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
});

afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
});

function makeConfig(overrides: Partial<Config> = {}): Config {
    return {
        workspace_dir: '/tmp/wt',
        database_path: '/tmp/db',
        version: '1.0.0',
        providers: [
            { id: 1, url: 'http://localhost:9999/v1', models: [{ name: 'model-a' }], concurrency: 1, max_requests_per_second: 0 },
        ],
        worker_provider: '1',
        worker_model: 'model-a',
        utility_provider: '1',
        utility_model: 'model-a',
        ...overrides,
    };
}

/** Open the modal, switch to Preferences, and wait for the toggle to be rendered. */
async function openPreferences(config: Config) {
    const utils = render(ConfigModal, { props: { config, onClose: vi.fn(), onSaved: vi.fn() } });
    await waitFor(() => expect(utils.container.querySelector('button.tab-btn')).toBeTruthy(), { timeout: 1000 });
    const tabs = Array.from(utils.container.querySelectorAll('button.tab-btn'));
    fireEvent.click(tabs.find((b) => b.textContent?.trim() === 'Preferences')!);
    await waitFor(() => expect(utils.container.querySelector(TOGGLE)).toBeTruthy(), { timeout: 1000 });
    return utils;
}

describe('ConfigModal board sort direction save payload', () => {
    it('sends sort_oldest_first: true when the toggle is on', async () => {
        vi.mocked(updateConfig).mockResolvedValue(makeConfig());
        const { container } = await openPreferences(makeConfig());

        fireEvent.click(container.querySelector(TOGGLE)!);
        fireEvent.click(container.querySelector(SAVE)!);
        await waitFor(() => expect(updateConfig).toHaveBeenCalledTimes(1), { timeout: 1000 });

        expect(vi.mocked(updateConfig).mock.calls[0][0]).toMatchObject({ sort_oldest_first: true });
    });

    it('sends sort_oldest_first: false when the toggle is left off', async () => {
        vi.mocked(updateConfig).mockResolvedValue(makeConfig());
        const { container } = await openPreferences(makeConfig());

        fireEvent.click(container.querySelector(SAVE)!);
        await waitFor(() => expect(updateConfig).toHaveBeenCalledTimes(1), { timeout: 1000 });

        expect(vi.mocked(updateConfig).mock.calls[0][0]).toMatchObject({ sort_oldest_first: false });
    });

    it('hydrates the toggle from the stored preference and keeps it on save', async () => {
        vi.mocked(updateConfig).mockResolvedValue(makeConfig({ sort_oldest_first: true }));
        const { container } = await openPreferences(makeConfig({ sort_oldest_first: true }));

        expect(container.querySelector(TOGGLE)!.getAttribute('aria-checked')).toBe('true');

        fireEvent.click(container.querySelector(SAVE)!);
        await waitFor(() => expect(updateConfig).toHaveBeenCalledTimes(1), { timeout: 1000 });

        expect(vi.mocked(updateConfig).mock.calls[0][0]).toMatchObject({ sort_oldest_first: true });
    });
});
