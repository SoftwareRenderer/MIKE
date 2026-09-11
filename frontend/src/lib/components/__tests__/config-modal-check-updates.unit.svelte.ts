import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, fireEvent, cleanup, waitFor } from '@testing-library/svelte';
import ConfigModal from '$lib/components/ConfigModal.svelte';
import type { Config } from '$lib/types';
import type { UpdateOutcome } from '$lib/services/app-update';

// The manual check is the only place the outcome is spoken, so it must answer in every case.

const checkNow = vi.fn();
vi.mock('$lib/services/app-update', () => ({
    checkNow: () => checkNow(),
}));

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

const CHECK_BUTTON = 'button.btn-check-updates';
const OUTCOME = '.about-update-outcome';

function makeConfig(): Config {
    return {
        workspace_dir: '/tmp/wt',
        database_path: '/tmp/db',
        version: '1.0.0',
        providers: [],
        worker_provider: '1',
        worker_model: 'model-a',
        utility_provider: '1',
        utility_model: 'model-a',
    };
}

async function openAbout() {
    const utils = render(ConfigModal, {
        props: { config: makeConfig(), onClose: vi.fn(), onSaved: vi.fn() },
    });
    await waitFor(() => expect(utils.container.querySelector('button.tab-btn')).toBeTruthy(), { timeout: 1000 });
    const tabs = Array.from(utils.container.querySelectorAll('button.tab-btn'));
    fireEvent.click(tabs.find((b) => b.textContent?.trim() === 'About')!);
    await waitFor(() => expect(utils.container.querySelector(CHECK_BUTTON)).toBeTruthy(), { timeout: 1000 });
    return utils;
}

function resolveWith(outcome: UpdateOutcome) {
    checkNow.mockResolvedValue(outcome);
}

describe('Settings → About → check for updates', () => {
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
        checkNow.mockReset();
        document.head.querySelector('meta[name="app-build"]')?.remove();
    });

    afterEach(() => {
        cleanup();
        vi.restoreAllMocks();
    });

    it('answers up to date when this is the build on disk', async () => {
        resolveWith('current');
        const { container } = await openAbout();

        await fireEvent.click(container.querySelector(CHECK_BUTTON)!);

        await waitFor(() => expect(container.querySelector(OUTCOME)?.textContent).toContain('Up to date'));
    });

    it('says a reload is coming when it is', async () => {
        resolveWith('applying');
        const { container } = await openAbout();

        await fireEvent.click(container.querySelector(CHECK_BUTTON)!);

        await waitFor(() => expect(container.querySelector(OUTCOME)?.textContent).toContain('reloading'));
    });

    it('says it could not determine, rather than claiming the build is current', async () => {
        resolveWith('unknown');
        const { container } = await openAbout();

        await fireEvent.click(container.querySelector(CHECK_BUTTON)!);

        const outcome = await waitFor(() => {
            const node = container.querySelector(OUTCOME);
            expect(node?.textContent).toContain('Could not determine');
            return node!;
        });
        expect(outcome.textContent).not.toContain('Up to date');
    });

    it('will not start a second check while one is running', async () => {
        let settle: ((outcome: UpdateOutcome) => void) | null = null;
        checkNow.mockImplementation(() => new Promise<UpdateOutcome>((resolve) => { settle = resolve; }));
        const { container } = await openAbout();

        const button = container.querySelector<HTMLButtonElement>(CHECK_BUTTON)!;
        await fireEvent.click(button);
        await waitFor(() => expect(button.disabled).toBe(true));
        expect(button.textContent).toContain('Checking');

        await fireEvent.click(button);
        expect(checkNow).toHaveBeenCalledTimes(1);

        settle!('current');
        await waitFor(() => expect(button.disabled).toBe(false));
        expect(checkNow).toHaveBeenCalledTimes(1);
    });
});
