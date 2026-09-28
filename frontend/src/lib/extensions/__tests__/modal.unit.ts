/**
 * `ui.modal` budget — the dialog is blocking and user-dismissed, so a granted
 * `ui.modal` must not be able to stack dialogs until the app is unusable.
 */
import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import { showExtensionModal, MAX_OPEN_MODALS } from '../modal';

/** The open dialogs, which `showExtensionModal` appends and a dismiss removes. */
function openModals() {
    return document.querySelectorAll('[data-ext-modal]');
}

function dismissTop() {
    const overlays = openModals();
    const top = overlays[overlays.length - 1] as HTMLElement | undefined;
    (top?.querySelector('button') as HTMLButtonElement | null)?.click();
}

describe('extension dialog budget', () => {
    beforeEach(() => {
        document.body.innerHTML = '';
    });

    afterEach(() => {
        // Close (not just remove) so the module-level open-dialog bookkeeping
        // is back to zero for the next test.
        let guard = 0;
        while (openModals().length > 0 && guard++ < 20) dismissTop();
    });

    it('renders dialogs up to the cap', async () => {
        for (let i = 0; i < MAX_OPEN_MODALS; i++) void showExtensionModal({ message: `m${i}` });
        expect(openModals()).toHaveLength(MAX_OPEN_MODALS);
    });

    it('rejects beyond the cap without rendering another dialog', async () => {
        for (let i = 0; i < MAX_OPEN_MODALS; i++) void showExtensionModal({ message: `m${i}` });
        await expect(showExtensionModal({ message: 'one too many' }))
            .rejects.toThrow(/too many open extension dialogs/);
        expect(openModals()).toHaveLength(MAX_OPEN_MODALS);
        expect(document.body.textContent).not.toContain('one too many');
    });

    it('frees a slot when a dialog is dismissed', async () => {
        for (let i = 0; i < MAX_OPEN_MODALS; i++) void showExtensionModal({ message: `m${i}` });
        dismissTop();
        expect(openModals()).toHaveLength(MAX_OPEN_MODALS - 1);
        void showExtensionModal({ message: 'fits again' });
        expect(openModals()).toHaveLength(MAX_OPEN_MODALS);
        expect(document.body.textContent).toContain('fits again');
    });
});
