import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { DialogStore } from '../dialogs.svelte';
import { TOAST_DURATION_MS } from '$lib/constants';

/**
 * The store owns the toast's whole lifetime — Toast.svelte only renders what is in
 * `store.toast`. That is only true because the dismissal timer lives here, so the
 * budget is asserted here: a caller asking for a longer toast must get it.
 */
describe('DialogStore.showToast', () => {
    let store: DialogStore;

    beforeEach(() => {
        vi.useFakeTimers();
        store = new DialogStore();
    });

    afterEach(() => {
        vi.useRealTimers();
    });

    it('publishes the message and type', () => {
        store.showToast('Saved', 'success');
        expect(store.toast).toEqual({ message: 'Saved', type: 'success' });
    });

    it('releases the toast after the default duration', () => {
        store.showToast('Saved');

        vi.advanceTimersByTime(TOAST_DURATION_MS - 1);
        expect(store.toast).not.toBeNull();

        vi.advanceTimersByTime(1);
        expect(store.toast).toBeNull();
    });

    it('honours a caller-supplied duration', () => {
        store.showToast('Long thing', 'error', TOAST_DURATION_MS * 2);

        vi.advanceTimersByTime(TOAST_DURATION_MS);
        expect(store.toast).not.toBeNull();

        vi.advanceTimersByTime(TOAST_DURATION_MS);
        expect(store.toast).toBeNull();
    });

    it('replaces a pending toast without letting the old timer dismiss the new one', () => {
        store.showToast('First', 'error', 1000);
        vi.advanceTimersByTime(500);
        store.showToast('Second', 'success', 5000);

        // The first toast's timer would have fired right about now.
        vi.advanceTimersByTime(600);
        expect(store.toast).toEqual({ message: 'Second', type: 'success' });
    });
});
