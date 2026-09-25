// @vitest-environment jsdom
import { describe, it, expect, afterEach } from 'vitest';
import { render, fireEvent, cleanup } from '@testing-library/svelte';
import ConfirmationDialog from '$lib/components/ConfirmationDialog.svelte';
import { dialogStore } from '$lib/stores';
import { i18n } from '$lib/i18n/runtime.svelte';
import { DEFAULT_LOCALE } from '$lib/i18n/en';

// Svelte 5 effects flush on the microtask queue, so give them a few ticks after
// a store write or a catalog swap.
async function flush() {
    for (let i = 0; i < 6; i++) await Promise.resolve();
}

// [role="dialog"] is not a unique locator, and neither is a footer: find the button by its label.
function footerButton(container: HTMLElement, label: string): HTMLElement {
    const found = Array.from(container.querySelectorAll('.modal-footer button'))
        .find((b) => (b.textContent ?? '').trim() === label);
    if (!found) throw new Error(`no footer button labelled ${label}`);
    return found as HTMLElement;
}

afterEach(() => {
    cleanup();
    dialogStore.confirmationSlot.clear();
    i18n.setCatalog(DEFAULT_LOCALE, null);
});

describe('ConfirmationDialog', () => {
    it('empties the slot and resolves false when cancelled', async () => {
        const { container } = render(ConfirmationDialog);
        const answered = dialogStore.showConfirm({ title: 'Drop it', message: 'Three cards go' });
        await flush();

        expect(container.textContent).toContain('Drop it');
        expect(container.textContent).toContain('Three cards go');

        await fireEvent.click(footerButton(container, 'Cancel'));
        await flush();

        expect(dialogStore.confirmationSlot.pending).toBeNull();
        await expect(answered).resolves.toBe(false);
    });

    it('resolves true, and the answer reaches the awaiting caller once', async () => {
        const { container } = render(ConfirmationDialog);
        const answered = dialogStore.showConfirm({
            title: 'Ship',
            message: 'one',
            items: ['branch a', 'branch b'],
            confirmText: 'Ship it',
        });
        await flush();

        expect(container.textContent).toContain('branch a');
        expect(container.textContent).toContain('branch b');

        await fireEvent.click(footerButton(container, 'Ship it'));
        await flush();

        expect(dialogStore.confirmationSlot.pending).toBeNull();
        await expect(answered).resolves.toBe(true);
    });

    it('re-labels the default confirm button when the locale changes while it is open', async () => {
        const { container } = render(ConfirmationDialog);
        // Opened on the slot rather than through showConfirm, so no label is filled in and the
        // dialog's own defaults are what the pack translates.
        const answered = dialogStore.confirmationSlot.open({ title: 'Drop it', message: 'Three cards go' });
        await flush();

        expect(footerButton(container, 'Confirm')).toBeTruthy();

        i18n.setCatalog('fr', { 'app.confirm.confirm': 'Confirmer', 'app.cancel': 'Annuler' });
        await flush();

        expect(footerButton(container, 'Confirmer')).toBeTruthy();
        expect(footerButton(container, 'Annuler')).toBeTruthy();
        expect(dialogStore.confirmationSlot.pending).not.toBeNull();

        await fireEvent.click(footerButton(container, 'Confirmer'));
        await expect(answered).resolves.toBe(true);
    });

    it('dresses the ordinary answer apart from the destructive one', async () => {
        const { container } = render(ConfirmationDialog);
        const asked = dialogStore.showConfirm({
            title: 'Look for releases?', message: 'Once a day', confirmText: 'Yes', cancelText: 'No',
            variant: 'default',
        });
        await flush();

        const yes = footerButton(container, 'Yes');
        expect(yes.className).not.toContain('destructive');
        await fireEvent.click(yes);
        expect(await asked).toBe(true);

        cleanup();
        const second = render(ConfirmationDialog);
        const dropped = dialogStore.showConfirm({ title: 'Drop it', message: 'Three cards go' });
        await flush();

        // Naming nothing is still a destruction: that is what the default is for.
        const confirm = footerButton(second.container, 'Confirm');
        expect(confirm.className).toContain('destructive');
        await fireEvent.click(confirm);
        expect(await dropped).toBe(true);
    });
});
