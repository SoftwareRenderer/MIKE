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
    dialogStore.confirmation = null;
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

        expect(dialogStore.confirmation).toBeNull();
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

        expect(dialogStore.confirmation).toBeNull();
        await expect(answered).resolves.toBe(true);
    });

    it('re-labels the default confirm button when the locale changes while it is open', async () => {
        const { container } = render(ConfirmationDialog);
        // The store fills both labels in, so the no-label caller is written straight into the slot.
        let answer = false;
        dialogStore.confirmation = {
            title: 'Drop it',
            message: 'Three cards go',
            _resolve: (value) => { answer = value; },
        };
        await flush();

        expect(footerButton(container, 'Confirm')).toBeTruthy();

        i18n.setCatalog('fr', { 'app.confirm.confirm': 'Confirmer', 'app.cancel': 'Annuler' });
        await flush();

        expect(footerButton(container, 'Confirmer')).toBeTruthy();
        expect(footerButton(container, 'Annuler')).toBeTruthy();
        expect(dialogStore.confirmation).not.toBeNull();

        await fireEvent.click(footerButton(container, 'Confirmer'));
        expect(answer).toBe(true);
    });
});
