// @vitest-environment jsdom
// The Reset row of the usage table, rendered beside the dialog App renders it next to: the click asks on
// `dialogStore`'s confirmation slot, and only the dialog's answer performs the DELETE. The regression this
// pins is the earlier shape — the browser's own `confirm()`, which is unstyled, untranslated and blocks
// the thread — so the test names the browser function and asserts it is never reached.
import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';
import StatsModal from '$lib/components/StatsModal.svelte';
import ConfirmationDialog from '$lib/components/ConfirmationDialog.svelte';
import { dialogStore } from '$lib/stores';
import { STATS } from './stats-fixture';

const api = vi.hoisted(() => ({ getProjectStats: vi.fn(), resetProjectStats: vi.fn() }));
vi.mock('$lib/api', async (importOriginal) => ({
    ...(await importOriginal<typeof import('$lib/api')>()),
    getProjectStats: (...args: unknown[]) => api.getProjectStats(...args),
    resetProjectStats: (...args: unknown[]) => api.resetProjectStats(...args),
}));

function footerButton(container: HTMLElement, label: string): HTMLElement {
    const found = Array.from(container.querySelectorAll('.modal-footer button'))
        .find((b) => (b.textContent ?? '').trim() === label);
    if (!found) throw new Error(`no footer button labelled ${label}`);
    return found as HTMLElement;
}

async function renderStats() {
    const stats = render(StatsModal, { props: { projectId: 'p1', onClose: () => {} } });
    // The dialog is App's sibling, not the modal's child: the request leaves one modal and is answered
    // by the other, which is what `BaseModal`'s focus trap lets a second dialog do.
    const dialog = render(ConfirmationDialog);
    await waitFor(() => expect(stats.container.querySelector('.btn-reset')).toBeTruthy());
    return { ...stats, dialog };
}

afterEach(() => {
    cleanup();
    dialogStore.confirmationSlot.clear();
    vi.clearAllMocks();
    vi.restoreAllMocks();
});

describe('StatsModal model reset', () => {
    it('asks on the app confirmation dialog, naming the model, before it resets anything', async () => {
        api.getProjectStats.mockResolvedValue(STATS);
        const browserConfirm = vi.spyOn(window, 'confirm');
        const { container, dialog } = await renderStats();

        await fireEvent.click(container.querySelector('.btn-reset') as HTMLElement);
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
        api.getProjectStats.mockResolvedValue(STATS);
        const { container, dialog } = await renderStats();

        await fireEvent.click(container.querySelector('.btn-reset') as HTMLElement);
        await waitFor(() => expect(dialog.container.querySelector('.btn-cancel')).toBeTruthy());
        await fireEvent.click(footerButton(dialog.container, 'Cancel'));

        await waitFor(() => expect(dialogStore.confirmationSlot.pending).toBeNull());
        expect(api.resetProjectStats).not.toHaveBeenCalled();
    });
});
