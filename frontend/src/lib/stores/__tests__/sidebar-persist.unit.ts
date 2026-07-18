import { describe, it, expect, vi, beforeEach } from 'vitest';
import { AppStore } from '$lib/stores/app.svelte';
import { loadAppSetting, saveAppSetting } from '$lib/storage';

vi.mock('$lib/storage', () => ({
    loadAppSetting: vi.fn(),
    saveAppSetting: vi.fn(),
}));

describe('sidebar persistence', () => {
    beforeEach(() => {
        vi.clearAllMocks();
    });

    it('loads sidebar state from IndexedDB on init', async () => {
        vi.mocked(loadAppSetting).mockResolvedValue(true);
        const store = new AppStore();
        expect(loadAppSetting).toHaveBeenCalledWith('kanban-sidebar-collapsed', false);
        // Allow async load to complete
        await new Promise(resolve => setTimeout(resolve, 10));
        expect(store.sidebarCollapsed).toBe(true);
    });

    it('persists sidebar state via persistSidebarCollapsed', async () => {
        vi.mocked(loadAppSetting).mockResolvedValue(false);
        vi.mocked(saveAppSetting).mockResolvedValue(undefined);
        const store = new AppStore();
        await new Promise(resolve => setTimeout(resolve, 10));

        store.sidebarCollapsed = true;
        store.persistSidebarCollapsed();

        expect(saveAppSetting).toHaveBeenCalledWith('kanban-sidebar-collapsed', true);
    });

    it('handles default value when no stored state exists', async () => {
        vi.mocked(loadAppSetting).mockResolvedValue(false);
        const store = new AppStore();
        await new Promise(resolve => setTimeout(resolve, 10));
        expect(store.sidebarCollapsed).toBe(false);
    });

    it('handles stored string "true"', async () => {
        vi.mocked(loadAppSetting).mockResolvedValue('true');
        const store = new AppStore();
        await new Promise(resolve => setTimeout(resolve, 10));
        expect(store.sidebarCollapsed).toBe(true);
    });
});
