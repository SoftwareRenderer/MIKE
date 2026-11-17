import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, fireEvent, cleanup, waitFor } from '@testing-library/svelte';
import ConfigModal from '$lib/components/ConfigModal.svelte';
import type { Config } from '$lib/types';

/**
 * Settings → About → System Info: the rows that report what the process knows about the machine it
 * started on. Each is a pure function of the config the server answered with, and a cached config
 * written by a server that predates the field reads as the field's absence: the row goes, the section
 * stays.
 */

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
const CA_LINK = 'a[href="/api/tls/ca"]';

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
            { id: 1, url: 'http://localhost:9999/v1', models: [{ name: 'model-a' }], concurrency: 1, max_requests_per_second: 0, stream_idle_seconds: 0 },
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

describe('the CA download row', () => {
    it('offers the CA as a downloadable file when the server uses its own CA', async () => {
        const { container } = await openAbout(makeConfig({ tls_ca_available: true }));

        const link = container.querySelector<HTMLAnchorElement>(CA_LINK);
        expect(link).not.toBeNull();
        // A real download, not a navigation away from the app.
        expect(link!.getAttribute('download')).toBe('mike-ca.crt');
        expect(link!.textContent).toContain('CA');
    });

    it('offers nothing when the server has no CA of its own', async () => {
        const { container } = await openAbout(makeConfig({ tls_ca_available: false }));

        expect(container.querySelector(CA_LINK)).toBeNull();
        // The rest of System Info is untouched — the row is absent, not the section.
        expect(container.querySelector(SYSTEM_INFO_ROW)).toBeTruthy();
    });
});

describe('the maximum sandbox memory row', () => {
    // The ceiling is MIKE_MEMORY_BUDGET_PCT of installed RAM, worked out once at startup.
    const BUDGET = { memory_budget_mb: 13624, memory_total_mb: 15138, memory_budget_pct: 90 };

    it('states the ceiling the server started with', async () => {
        const { container } = await openAbout(makeConfig(BUDGET));

        // Grouped, not the bare figure: the figure is read against a machine's own memory figures.
        expect(rowValue(container, 'Maximum Sandbox Memory')).toBe('13,624 MB');
    });

    it('points at the variable an administrator sets, and prints no figures of this machine', async () => {
        const { container } = await openAbout(makeConfig(BUDGET));

        const help = row(container, 'Maximum Sandbox Memory')?.querySelector('.field-help')?.textContent ?? '';
        expect(help).toContain('MIKE_MEMORY_BUDGET_PCT');
        expect(help).not.toContain('90%');
        expect(help).not.toContain('15,138');
    });

    it('shows nothing when the gate is disabled', async () => {
        const { container } = await openAbout(makeConfig({
            memory_budget_mb: 0, memory_total_mb: 0, memory_budget_pct: 0,
        }));

        expect(rowValue(container, 'Maximum Sandbox Memory')).toBeNull();
        // The row is absent, not the section.
        expect(container.querySelector(SYSTEM_INFO_ROW)).toBeTruthy();
    });
});
