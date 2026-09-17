import { describe, it, expect, vi, beforeEach } from 'vitest';
import { UiStore } from '$lib/stores/ui.svelte';
import { loadAppSetting, saveAppSetting } from '$lib/storage';

vi.mock('$lib/storage', () => ({
    loadAppSetting: vi.fn(),
    saveAppSetting: vi.fn(),
}));

const KEY = 'notify-on-review';

describe('notifyOnReview persistence', () => {
    beforeEach(() => {
        vi.clearAllMocks();
    });

    it('loads this browser\'s choice on init', async () => {
        vi.mocked(loadAppSetting).mockResolvedValue(true);
        const store = new UiStore();
        expect(loadAppSetting).toHaveBeenCalledWith(KEY, false);
        await new Promise(resolve => setTimeout(resolve, 10));
        expect(store.notifyOnReview).toBe(true);
    });

    it('stays off when nothing is stored', async () => {
        vi.mocked(loadAppSetting).mockResolvedValue(false);
        const store = new UiStore();
        await new Promise(resolve => setTimeout(resolve, 10));
        expect(store.notifyOnReview).toBe(false);
    });

    it('ignores a stored value that is not a boolean', async () => {
        vi.mocked(loadAppSetting).mockResolvedValue('true');
        const store = new UiStore();
        await new Promise(resolve => setTimeout(resolve, 10));
        expect(store.notifyOnReview).toBe(false);
    });

    it('refuses a rejected load', async () => {
        vi.mocked(loadAppSetting).mockRejectedValue(new Error('storage.unavailable'));
        const store = new UiStore();
        await new Promise(resolve => setTimeout(resolve, 10));
        expect(store.notifyOnReview).toBe(false);
    });

    it('persists the current choice', async () => {
        vi.mocked(loadAppSetting).mockResolvedValue(false);
        vi.mocked(saveAppSetting).mockResolvedValue(undefined);
        const store = new UiStore();
        await new Promise(resolve => setTimeout(resolve, 10));

        store.notifyOnReview = true;
        store.persistNotifyOnReview();

        expect(saveAppSetting).toHaveBeenCalledWith(KEY, true);
    });
});
