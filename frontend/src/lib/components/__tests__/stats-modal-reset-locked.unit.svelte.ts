// @vitest-environment jsdom
// Under MIKE_RESTRICT_STAT_RESET the modal renders no reset column and says who disabled it. A button
// left on screen would be a click that comes back refused.
import { describe, it, expect, vi, beforeAll, afterEach } from 'vitest';
import { render, cleanup, waitFor } from '@testing-library/svelte';
import StatsModal from '$lib/components/StatsModal.svelte';
import { appStore } from '$lib/stores';
import type { Config } from '$lib/types';
import { STATS } from './stats-fixture';

const api = vi.hoisted(() => ({ getProjectStats: vi.fn(), resetProjectStats: vi.fn() }));
vi.mock('$lib/api', async (importOriginal) => ({
    ...(await importOriginal<typeof import('$lib/api')>()),
    getProjectStats: (...args: unknown[]) => api.getProjectStats(...args),
    resetProjectStats: (...args: unknown[]) => api.resetProjectStats(...args),
}));

let baseConfig: Config;

beforeAll(() => {
    // Read after the imports, so the store the test later restores is the one the mock built.
    baseConfig = appStore.config as Config;
});

afterEach(() => {
    cleanup();
    appStore.config = baseConfig;
    vi.clearAllMocks();
});

async function renderStats() {
    api.getProjectStats.mockResolvedValue(STATS);
    return render(StatsModal, { props: { projectId: 'p1', onClose: () => {} } });
}

describe('StatsModal under MIKE_RESTRICT_STAT_RESET', () => {
    it('renders no reset for any model and names the administrator instead', async () => {
        appStore.config = { ...baseConfig, stats_reset_restricted: true };
        const { container } = await renderStats();

        await waitFor(() => expect(container.querySelector('.model-table')).toBeTruthy());
        expect(container.querySelector('.btn-reset')).toBeNull();
        expect(container.querySelector('th.col-action')).toBeNull();
        const note = container.querySelector('[data-testid="stats-reset-locked"]');
        expect(note?.textContent?.trim()).toBe('Resetting token usage has been disabled by your administrator.');
        expect(api.resetProjectStats).not.toHaveBeenCalled();
    });

    // The unlocked button is stats-modal-reset-confirm's subject; only the note's absence is this one's.
    it('shows no administrator note for a server booted without the switch', async () => {
        appStore.config = { ...baseConfig, stats_reset_restricted: false };
        const { container } = await renderStats();

        await waitFor(() => expect(container.querySelector('.model-table')).toBeTruthy());
        expect(container.querySelector('[data-testid="stats-reset-locked"]')).toBeNull();
    });
});
