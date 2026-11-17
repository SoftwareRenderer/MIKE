// @vitest-environment jsdom
/**
 * THE RESET COLUMN of the usage table.
 *
 * A reset destroys every row of this project's usage, so two things hold it back: the server decides
 * whether the column exists at all (`MIKE_RESTRICT_STAT_RESET` renders no button — one on screen would
 * be a click that comes back refused), and the click asks on `dialogStore`'s confirmation slot rather
 * than the browser's own `confirm()`, which is unstyled, untranslated and blocks the thread. The dialog
 * is App's sibling, not the modal's child: the request leaves one modal and is answered by the other,
 * which is what `BaseModal`'s focus trap lets a second dialog do.
 */
import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';
import StatsModal from '$lib/components/StatsModal.svelte';
import ConfirmationDialog from '$lib/components/ConfirmationDialog.svelte';
import { appStore, dialogStore } from '$lib/stores';
import type { Config, ProjectStats } from '$lib/types';

const api = vi.hoisted(() => ({ getProjectStats: vi.fn(), resetProjectStats: vi.fn() }));
vi.mock('$lib/api', async (importOriginal) => ({
    ...(await importOriginal<typeof import('$lib/api')>()),
    getProjectStats: (...args: unknown[]) => api.getProjectStats(...args),
    resetProjectStats: (...args: unknown[]) => api.resetProjectStats(...args),
}));

/** One model's worth of usage — the row the reset is asked for. */
const STATS: ProjectStats = {
    total_tasks: 1,
    total_prompt_tokens: 10,
    total_cache_tokens: 0,
    total_processed_tokens: 10,
    total_generated_tokens: 5,
    avg_prompt_tps: 0,
    avg_generation_tps: 0,
    total_power_kwh: 0,
    total_cost: 0,
    by_model: [{
        model_name: 'gpt-test', provider_name: 'prov-a',
        prompt_tokens: 10, cache_tokens: 0, processed_tokens: 10, generated_tokens: 5,
        avg_prompt_tps: 0, avg_generation_tps: 0, power_kwh: 0, task_count: 1, cost: 0,
    }],
};

const baseConfig = appStore.config as Config;

function footerButton(container: HTMLElement, label: string): HTMLElement {
    const found = Array.from(container.querySelectorAll('.modal-footer button'))
        .find((b) => (b.textContent ?? '').trim() === label);
    if (!found) throw new Error(`no footer button labelled ${label}`);
    return found as HTMLElement;
}

/** Open the table, optionally under the administrator's switch. */
async function openTable(restricted?: boolean) {
    if (restricted !== undefined) appStore.config = { ...baseConfig, stats_reset_restricted: restricted };
    api.getProjectStats.mockResolvedValue(STATS);
    const stats = render(StatsModal, { props: { projectId: 'p1', onClose: () => {} } });
    await waitFor(() => expect(stats.container.querySelector('.model-table')).toBeTruthy());
    return stats;
}

afterEach(() => {
    cleanup();
    appStore.config = baseConfig;
    dialogStore.confirmationSlot.clear();
    vi.clearAllMocks();
    vi.restoreAllMocks();
});

describe('StatsModal model reset', () => {
    it('asks on the app confirmation dialog, naming the model, before it resets anything', async () => {
        const stats = await openTable();
        await waitFor(() => expect(stats.container.querySelector('.btn-reset')).toBeTruthy());
        const browserConfirm = vi.spyOn(window, 'confirm');
        // The dialog App renders beside the modal, which is what answers the click.
        const dialog = render(ConfirmationDialog);

        await fireEvent.click(stats.container.querySelector('.btn-reset') as HTMLElement);
        await waitFor(() => expect(dialogStore.confirmationSlot.pending).not.toBeNull());

        const pending = dialogStore.confirmationSlot.pending;
        expect(pending?.title).toBe('Reset token stats');
        expect(pending?.message).toContain('gpt-test');
        expect(pending?.variant).toBe('destructive');
        expect(browserConfirm).not.toHaveBeenCalled();
        expect(api.resetProjectStats).not.toHaveBeenCalled();

        // The answer that destroys is dressed as the thing being undone.
        await waitFor(() => expect(dialog.container.querySelector('.btn-confirm')).toBeTruthy());
        expect(dialog.container.querySelector('.btn-confirm')?.className).toContain('btn-danger');

        await fireEvent.click(footerButton(dialog.container, 'Reset'));
        await waitFor(() => expect(api.resetProjectStats).toHaveBeenCalledWith('p1', 'gpt-test', 'prov-a'));
    });

    it('resets nothing when the dialog is cancelled', async () => {
        const stats = await openTable();
        await waitFor(() => expect(stats.container.querySelector('.btn-reset')).toBeTruthy());
        const dialog = render(ConfirmationDialog);

        await fireEvent.click(stats.container.querySelector('.btn-reset') as HTMLElement);
        await waitFor(() => expect(dialog.container.querySelector('.btn-cancel')).toBeTruthy());
        await fireEvent.click(footerButton(dialog.container, 'Cancel'));

        await waitFor(() => expect(dialogStore.confirmationSlot.pending).toBeNull());
        expect(api.resetProjectStats).not.toHaveBeenCalled();
    });
});

describe('StatsModal under MIKE_RESTRICT_STAT_RESET', () => {
    it.each([
        { restricted: true, switch: "on", note: 'Resetting token usage has been disabled by your administrator.' },
        { restricted: false, switch: "off", note: null },
    ])('judges the reset column by the administrator switch, here $switch', async ({ restricted, note }) => {
        const { container } = await openTable(restricted);

        expect(container.querySelector('[data-testid="stats-reset-locked"]')?.textContent?.trim() ?? null).toBe(note);
        if (!restricted) return;

        expect(container.querySelector('.btn-reset')).toBeNull();
        expect(container.querySelector('th.col-action')).toBeNull();
        expect(api.resetProjectStats).not.toHaveBeenCalled();
    });
});
