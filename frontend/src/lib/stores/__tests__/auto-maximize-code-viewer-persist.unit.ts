import { describe, it, expect, vi, beforeEach } from 'vitest';
import { UiStore } from '$lib/stores/ui.svelte';
import { loadAppSetting, saveAppSetting } from '$lib/storage';

vi.mock('$lib/storage', () => ({
    loadAppSetting: vi.fn(),
    saveAppSetting: vi.fn(),
}));

const KEY = 'auto-maximize-code-viewer';

/** The stored read is a promise, so the field settles a tick after construction. */
async function settled(store: UiStore): Promise<UiStore> {
    await new Promise(resolve => setTimeout(resolve, 10));
    return store;
}

describe('autoMaximizeCodeViewer persistence', () => {
    beforeEach(() => {
        vi.clearAllMocks();
    });

    it('asks for this browser\'s choice with on as the default', async () => {
        vi.mocked(loadAppSetting).mockResolvedValue(true);
        const store = await settled(new UiStore());
        expect(loadAppSetting).toHaveBeenCalledWith(KEY, true);
        expect(store.autoMaximizeCodeViewer).toBe(true);
    });

    it('is on when nothing is stored for it', async () => {
        vi.mocked(loadAppSetting).mockResolvedValue(undefined);
        const store = await settled(new UiStore());
        expect(store.autoMaximizeCodeViewer).toBe(true);
    });

    it('stays off once this browser turned it off', async () => {
        vi.mocked(loadAppSetting).mockResolvedValue(false);
        const store = await settled(new UiStore());
        expect(store.autoMaximizeCodeViewer).toBe(false);
    });

    it('falls back to on for a stored value that is not a boolean', async () => {
        vi.mocked(loadAppSetting).mockResolvedValue('false');
        const store = await settled(new UiStore());
        expect(store.autoMaximizeCodeViewer).toBe(true);
    });

    it('stays on when the load is refused', async () => {
        vi.mocked(loadAppSetting).mockRejectedValue(new Error('storage.unavailable'));
        const store = await settled(new UiStore());
        expect(store.autoMaximizeCodeViewer).toBe(true);
    });

    it('persists the current choice', async () => {
        vi.mocked(loadAppSetting).mockResolvedValue(true);
        vi.mocked(saveAppSetting).mockResolvedValue(undefined);
        const store = await settled(new UiStore());

        store.autoMaximizeCodeViewer = false;
        store.persistAutoMaximizeCodeViewer();

        expect(saveAppSetting).toHaveBeenCalledWith(KEY, false);
    });
});
