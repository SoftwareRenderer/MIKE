import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, fireEvent, cleanup, waitFor } from '@testing-library/svelte';
import ConfigModal from '$lib/components/ConfigModal.svelte';
import type { Config } from '$lib/types';

// The CA row is a pure function of config.tls_ca_available, which the server
// reports only while it runs its own CA-signed certificate. Also covers a cached
// config written by a server that had no such field.

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

const CA_LINK = 'a[href="/api/tls/ca"]';
const SYSTEM_INFO_ROW = '.readonly-value';

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

describe('Settings → About → System Info CA download', () => {
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

    it('offers nothing for a config that predates the field', async () => {
        const config = makeConfig();
        expect(config.tls_ca_available).toBeUndefined();

        const { container } = await openAbout(config);
        expect(container.querySelector(CA_LINK)).toBeNull();
    });
});
