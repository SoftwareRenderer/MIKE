import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, cleanup, waitFor } from '@testing-library/svelte';

// A real DialogStore behind the `$lib/stores` name Toast imports: the component renders
// `dialogStore.toast` as derived state, so only genuinely reactive store can drive it.
vi.mock('$lib/stores', async () => {
    const { DialogStore } = await import('$lib/stores/dialogs.svelte');
    return { dialogStore: new DialogStore(), uiStore: {}, appStore: {} };
});

import Toast from '$lib/components/Toast.svelte';
import { dialogStore } from '$lib/stores';
import { TOAST_DURATION_MS } from '$lib/constants';

describe('Toast', () => {
    beforeEach(() => {
        vi.useFakeTimers();
    });

    afterEach(() => {
        cleanup();
        vi.clearAllTimers();
        vi.useRealTimers();
    });

    it('renders nothing while the store holds no toast', () => {
        const { container } = render(Toast);
        expect(container.querySelector('.toast')).toBeNull();
    });

    it('should apply error styling for error toasts', async () => {
        const { container } = render(Toast);
        dialogStore.showToast('Something went wrong', 'error');

        const toast = await waitFor(() => {
            const el = container.querySelector('.toast');
            expect(el).toBeTruthy();
            return el;
        });
        expect(toast?.classList.contains('error')).toBe(true);
        expect(toast?.classList.contains('success')).toBe(false);
        expect(toast?.getAttribute('role')).toBe('alert');
        expect(toast?.textContent).toContain('Something went wrong');
    });

    it('should apply success styling for success toasts', async () => {
        const { container } = render(Toast);
        dialogStore.showToast('Task started successfully', 'success');

        const toast = await waitFor(() => {
            const el = container.querySelector('.toast');
            expect(el).toBeTruthy();
            return el;
        });
        expect(toast?.classList.contains('success')).toBe(true);
        expect(toast?.classList.contains('error')).toBe(false);
        expect(toast?.getAttribute('role')).toBe('alert');
        expect(toast?.textContent).toContain('Task started successfully');
    });

    it('dismisses when the store releases the toast', async () => {
        const { container } = render(Toast);
        dialogStore.showToast('Auto-dismiss test', 'success');
        await waitFor(() => expect(container.querySelector('.toast')).toBeTruthy());

        vi.advanceTimersByTime(TOAST_DURATION_MS);

        await waitFor(() => {
            expect(container.querySelector('.toast')).toBeNull();
        });
    });

    it('shows the newest toast when one replaces a pending toast', async () => {
        const { container } = render(Toast);
        dialogStore.showToast('First toast', 'success');
        await waitFor(() => expect(container.querySelector('.toast')?.textContent).toContain('First toast'));

        dialogStore.showToast('Second toast', 'error');

        await waitFor(() => {
            const toast = container.querySelector('.toast');
            expect(toast).toBeTruthy();
            expect(toast?.textContent).toContain('Second toast');
        });
    });
});
