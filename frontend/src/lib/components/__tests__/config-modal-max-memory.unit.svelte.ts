import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, fireEvent, cleanup, waitFor } from '@testing-library/svelte';
import ConfigModal from '$lib/components/ConfigModal.svelte';
import type { Config } from '$lib/types';

// The Maximum Sandbox Memory row is a pure function of the budget the server was started with
// (MIKE_MEMORY_BUDGET_PCT of installed RAM). A server whose gate is off reports 0, and so does a cached
// config written by one that predates the field; neither has a row to show.

vi.mock('$lib/api', async (importOriginal) => {
    const actual = await importOriginal<typeof import('$lib/api')>();
    return {
        ...actual,
        updateConfig: vi.fn(),
        getConfig: vi.fn(),
        fetchModels: vi.fn(),
        testConnection: vi.fn(),
        listSessions: vi.fn(async () => []),
        revokeSession: vi.fn(),
        revokeOtherSessions: vi.fn(),
    };
});

const SYSTEM_INFO_ROW = '.readonly-value';
const ROW_LABEL = '.field-label-inline';

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

/** Open the modal, switch to About, and wait for System Info to be rendered. */
async function openAbout(config: Config) {
    const utils = render(ConfigModal, { props: { config, onClose: vi.fn(), onSaved: vi.fn() } });
    await waitFor(() => expect(utils.container.querySelector('button.tab-btn')).toBeTruthy(), { timeout: 1000 });
    const tabs = Array.from(utils.container.querySelectorAll('button.tab-btn'));
    fireEvent.click(tabs.find((b) => b.textContent?.trim() === 'About')!);
    await waitFor(() => expect(utils.container.querySelector(SYSTEM_INFO_ROW)).toBeTruthy(), { timeout: 1000 });
    return utils;
}

/** The System Info row labelled `label`, or null when no such row is rendered. */
function row(container: HTMLElement, label: string): HTMLElement | null {
    const labelEl = Array.from(container.querySelectorAll(ROW_LABEL))
        .find((el) => el.textContent?.trim() === label);
    return labelEl?.parentElement ?? null;
}

/** The value of the System Info row labelled `label`, or null when no such row is rendered. */
function rowValue(container: HTMLElement, label: string): string | null {
    return row(container, label)?.querySelector(SYSTEM_INFO_ROW)?.textContent?.trim() ?? null;
}

describe('Settings → About → System Info maximum sandbox memory', () => {
    it('states the ceiling the server started with', async () => {
        const { container } = await openAbout(makeConfig({
            memory_budget_mb: 13624, memory_total_mb: 15138, memory_budget_pct: 90,
        }));

        // Grouped, not the bare figure: an operator compares it against installed RAM.
        expect(rowValue(container, 'Maximum Sandbox Memory')).toBe('13,624 MB');
    });

    it('names the percentage, the installed RAM it came from and the variable that sets it', async () => {
        const { container } = await openAbout(makeConfig({
            memory_budget_mb: 13624, memory_total_mb: 15138, memory_budget_pct: 90,
        }));

        const help = row(container, 'Maximum Sandbox Memory')?.querySelector('.field-help')?.textContent ?? '';
        expect(help).toContain('90%');
        expect(help).toContain('15,138 MB');
        expect(help).toContain('MIKE_MEMORY_BUDGET_PCT');
    });

    it('shows nothing when the gate is disabled', async () => {
        const { container } = await openAbout(makeConfig({
            memory_budget_mb: 0, memory_total_mb: 0, memory_budget_pct: 0,
        }));

        expect(rowValue(container, 'Maximum Sandbox Memory')).toBeNull();
        // The row is absent, not the section.
        expect(container.querySelector(SYSTEM_INFO_ROW)).toBeTruthy();
    });

    it('shows nothing for a config that predates the field', async () => {
        const config = makeConfig();
        expect(config.memory_budget_mb).toBeUndefined();

        const { container } = await openAbout(config);
        expect(rowValue(container, 'Maximum Sandbox Memory')).toBeNull();
    });
});
