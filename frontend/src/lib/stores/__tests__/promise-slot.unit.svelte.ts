import { describe, it, expect } from 'vitest';
import { DialogStore } from '../dialogs.svelte';

/**
 * The slot behind every promise-backed dialog: one pending request, the promise its caller waits on,
 * and no way to leave a caller waiting. The dialog half of the pattern is `use-promise-dialog`'s own
 * suite; the button copy defaults are pinned in `app-shell-copy`.
 */
describe('DialogStore promise slots', () => {
    it('holds the request the dialog arms from and answers through its resolve handle', async () => {
        const store = new DialogStore();
        const asked = store.showConfirm({ title: 'Drop it', message: 'Three cards go' });

        const pending = store.confirmationSlot.pending;
        expect(pending?.title).toBe('Drop it');
        pending?._resolve?.(true);
        await expect(asked).resolves.toBe(true);

        store.confirmationSlot.clear();
        expect(store.confirmationSlot.pending).toBeNull();
    });

    it('answers the confirmation a notice replaces, and the notice keeps one button', async () => {
        const store = new DialogStore();
        const asked = store.showConfirm({ title: 'Overwritten', message: 'first' });
        const noticed = store.showNotice({ title: 'Read this', message: 'second' });

        await expect(asked).resolves.toBe(false);
        const pending = store.confirmationSlot.pending;
        expect(pending?.title).toBe('Read this');
        expect(pending?.okText).toBe('OK');
        expect(pending?.confirmText).toBeUndefined();

        pending?._resolve?.(true);
        await expect(noticed).resolves.toBeUndefined();
    });

    it('answers a replaced commit request as a cancellation', async () => {
        const store = new DialogStore();
        const first = store.showCommitDialog({ message: 'first' });
        const second = store.showCommitDialog({ message: 'second' });

        await expect(first).resolves.toBeNull();
        expect(store.commitDialogSlot.pending?.message).toBe('second');

        store.commitDialogSlot.pending?._resolve?.('shipped');
        await expect(second).resolves.toBe('shipped');
    });
});
