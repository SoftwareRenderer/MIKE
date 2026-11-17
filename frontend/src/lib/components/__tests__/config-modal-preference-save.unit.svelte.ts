import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, fireEvent, cleanup, waitFor } from '@testing-library/svelte';
import ConfigModal from '$lib/components/ConfigModal.svelte';
import type { Config } from '$lib/types';
import { updateConfig } from '$lib/api';
import { saveAppSetting } from '$lib/storage';
import { uiStore } from '$lib/stores';

/**
 * One Settings preference, one row, one Save. `PUT /api/config` merges at the top level but replaces
 * what it is given, so a row the person never touched must still arrive with the answer the switch
 * shows — an omitted field keeps whatever was stored, which for a switch that changed under the
 * person's hand is the old answer. A preference that is "on" when the server omitted it has to keep
 * that reading through the round trip too: a stored `false` is never "unset, therefore on".
 *
 * The last block is the opposite shape: a choice that belongs to this browser only, which Save writes
 * to IndexedDB and never mentions to the server.
 *
 * What the row itself does — its section, its wording, reporting dirty — is the tab's own suite
 * (`preferences-tab.unit.svelte.ts`, `task-defaults.unit.svelte.ts`). This is the payload.
 */

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
    } as Config;
}

interface Preference {
    /** The `types.Config` field the switch writes. */
    key: keyof Config;
    /** The tab that holds the row, named the way its tab button says it. */
    tab: 'Preferences' | 'Task';
    /** The switch, found by the name it is announced by. */
    toggle: string;
    /** What the switch shows when the server answered without the field. */
    whenOmitted: boolean;
}

const PREFERENCES: Preference[] = [
    { key: 'auto_maximize_chat_mobile', tab: 'Preferences', toggle: 'button[aria-label="Auto maximize chat visibility on mobile"]', whenOmitted: true },
    { key: 'sort_oldest_first', tab: 'Preferences', toggle: 'button[aria-label="Oldest Task on top"]', whenOmitted: false },
    { key: 'run_siblings_parallel', tab: 'Task', toggle: 'button[aria-label="Run subtasks in parallel"]', whenOmitted: false },
    { key: 'reclaim_idle_sandbox', tab: 'Task', toggle: 'button[aria-label="Release a waiting task sandbox container"]', whenOmitted: false },
    { key: 'loop_breaker', tab: 'Task', toggle: 'button[aria-label="Recover from failed turns automatically"]', whenOmitted: false },
];

/** Open the modal on `config`, walk to the tab holding the switch, and wait for the switch. */
async function openTab(tab: string, toggle: string, config: Config) {
    const utils = render(ConfigModal, { props: { config, onClose: vi.fn(), onSaved: vi.fn() } });
    await waitFor(() => expect(utils.container.querySelector('button.tab-btn')).toBeTruthy(), { timeout: 1000 });
    const button = [...utils.container.querySelectorAll('button.tab-btn')]
        .find((b) => b.textContent?.trim() === tab);
    fireEvent.click(button!);
    await waitFor(() => expect(utils.container.querySelector(toggle)).toBeTruthy(), { timeout: 1000 });
    return utils;
}

const openPreference = (preference: Preference, config: Config) =>
    openTab(preference.tab, preference.toggle, config);

function toggleOf(container: HTMLElement, preference: Preference): HTMLElement {
    const el = container.querySelector(preference.toggle);
    if (!el) throw new Error(`no switch for ${String(preference.key)}`);
    return el as HTMLElement;
}

/** Save and hand back what the one PUT carried. */
async function save(container: HTMLElement): Promise<Record<string, unknown>> {
    fireEvent.click(container.querySelector(SAVE)!);
    await waitFor(() => expect(updateConfig).toHaveBeenCalledTimes(1), { timeout: 1000 });
    return vi.mocked(updateConfig).mock.calls[0][0] as unknown as Record<string, unknown>;
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

describe.each(PREFERENCES)('ConfigModal $key save payload', ({ key, tab, toggle, whenOmitted }) => {
    const preference: Preference = { key, tab, toggle, whenOmitted };

    it('shows and sends the answer the server omitted it with', async () => {
        vi.mocked(updateConfig).mockResolvedValue(makeConfig());
        const { container } = await openPreference(preference, makeConfig());

        expect(toggleOf(container, preference).getAttribute('aria-checked')).toBe(String(whenOmitted));
        expect(await save(container)).toMatchObject({ [key]: whenOmitted });
    });

    it('keeps the stored answer stored, rather than re-asking the server', async () => {
        const stored = !whenOmitted;
        vi.mocked(updateConfig).mockResolvedValue(makeConfig({ [key]: stored } as Partial<Config>));
        const { container } = await openPreference(preference, makeConfig({ [key]: stored } as Partial<Config>));

        expect(toggleOf(container, preference).getAttribute('aria-checked')).toBe(String(stored));
        expect(await save(container)).toMatchObject({ [key]: stored });
    });

    it('sends the flipped answer', async () => {
        vi.mocked(updateConfig).mockResolvedValue(makeConfig());
        const { container } = await openPreference(preference, makeConfig());

        fireEvent.click(toggleOf(container, preference));
        expect(await save(container)).toMatchObject({ [key]: !whenOmitted });
    });
});

// The one Settings choice that is not the server's to keep.
const CODE_VIEWER = 'button[aria-label="Auto maximize code viewer"]';
const CODE_VIEWER_KEY = 'auto-maximize-code-viewer';

describe('a preference this browser keeps to itself', () => {
    it('opens on what this browser stored, not on what the server says', async () => {
        uiStore.autoMaximizeCodeViewer = false;
        vi.mocked(updateConfig).mockResolvedValue(makeConfig());

        const { container } = await openTab('Preferences', CODE_VIEWER, makeConfig());

        expect(container.querySelector(CODE_VIEWER)?.getAttribute('aria-checked')).toBe('false');
    });

    it('writes the flipped choice to this browser and tells the server nothing of it', async () => {
        vi.mocked(updateConfig).mockResolvedValue(makeConfig());
        const { container } = await openTab('Preferences', CODE_VIEWER, makeConfig());
        expect(container.querySelector(CODE_VIEWER)?.getAttribute('aria-checked')).toBe('true');

        fireEvent.click(container.querySelector(CODE_VIEWER)!);
        const body = await save(container);

        await waitFor(() => expect(saveAppSetting).toHaveBeenCalledWith(CODE_VIEWER_KEY, false), { timeout: 1000 });
        expect(uiStore.autoMaximizeCodeViewer).toBe(false);
        expect(Object.keys(body).filter((k) => k.includes('code_viewer'))).toEqual([]);
    });

    it('keeps an untouched choice stored on an unrelated save', async () => {
        uiStore.autoMaximizeCodeViewer = false;
        vi.mocked(updateConfig).mockResolvedValue(makeConfig());

        const { container } = await openTab('Preferences', CODE_VIEWER, makeConfig());
        await save(container);

        await waitFor(() => expect(saveAppSetting).toHaveBeenCalledWith(CODE_VIEWER_KEY, false), { timeout: 1000 });
    });
});
