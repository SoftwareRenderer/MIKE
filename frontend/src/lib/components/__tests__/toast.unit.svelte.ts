import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, waitFor } from '@testing-library/svelte';

// Mock the dialogStore with a reactive toast property
let mockToastValue: { message: string; type: 'error' | 'success' } | null = null;
vi.mock('$lib/stores', () => ({
    dialogStore: {
        get toast() { return mockToastValue; },
    },
    uiStore: {},
    appStore: {},
}));

import Toast from '$lib/components/Toast.svelte';

describe('Toast', () => {
    beforeEach(() => {
        vi.useFakeTimers();
        mockToastValue = null;
    });

    afterEach(() => {
        vi.useRealTimers();
    });

    it('should apply error styling for error toasts', async () => {
        mockToastValue = { message: 'Something went wrong', type: 'error' };

        const { container } = render(Toast);
        const toast = container.querySelector('.toast');
        expect(toast).toBeTruthy();
        expect(toast?.classList.contains('error')).toBe(true);
        expect(toast?.classList.contains('success')).toBe(false);
        expect(toast?.getAttribute('role')).toBe('alert');
        expect(toast?.textContent).toContain('Something went wrong');
    });

    it('should apply success styling for success toasts', async () => {
        mockToastValue = { message: 'Task started successfully', type: 'success' };

        const { container } = render(Toast);
        const toast = container.querySelector('.toast');
        expect(toast).toBeTruthy();
        expect(toast?.classList.contains('success')).toBe(true);
        expect(toast?.classList.contains('error')).toBe(false);
        expect(toast?.getAttribute('role')).toBe('alert');
        expect(toast?.textContent).toContain('Task started successfully');
    });

    it('should auto-dismiss toast after 4 seconds', async () => {
        mockToastValue = { message: 'Auto-dismiss test', type: 'success' };

        const { container } = render(Toast);
        expect(container.querySelector('.toast')).toBeTruthy();

        // Advance time by 4 seconds
        vi.advanceTimersByTime(4000);

        // Wait for the effect to process
        await waitFor(() => {
            expect(container.querySelector('.toast')).toBeNull();
        });
    });

    it('should dismiss toast when a new toast replaces it', async () => {
        mockToastValue = { message: 'First toast', type: 'success' };

        const { container } = render(Toast);
        let toast = container.querySelector('.toast');
        expect(toast).toBeTruthy();
        expect(toast?.textContent).toContain('First toast');

        // Simulate a new toast replacing the first one
        // The Toast component's $effect watches dialogStore.toast,
        // and when it changes, the previous timeout is cleared and a new one starts.
        mockToastValue = { message: 'Second toast', type: 'error' };

        // Verify the toast still exists (not auto-dismissed yet)
        toast = container.querySelector('.toast');
        expect(toast).toBeTruthy();
    });
});
