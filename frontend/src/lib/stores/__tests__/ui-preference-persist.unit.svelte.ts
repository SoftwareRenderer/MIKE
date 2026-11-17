/**
 * The three preferences that belong to a browser rather than to an account
 * (`docs/adding-a-preference.md → When the preference belongs to a browser`).
 *
 * One rule with three rows: the choice is read from its own `app-settings` key with its
 * own default, a read that is refused leaves the field at that default, and a stored
 * value that is not the shape the field means is decided per preference — not by
 * truthiness. `notifyOnReview` and `autoMaximizeCodeViewer` accept exactly a boolean
 * (so a stored `'false'` is not a yes dressed as a string), while `sidebarCollapsed`
 * still honours the `'true'` an older build wrote. Each row also writes its own key on
 * persist, because Settings' Save flow is what calls it.
 */
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { UiStore } from '$lib/stores/ui.svelte';
import { AppStore } from '$lib/stores/app.svelte';
import { loadAppSetting, saveAppSetting } from '$lib/storage';

vi.mock('$lib/storage', () => ({
    loadAppSetting: vi.fn(),
    saveAppSetting: vi.fn(),
}));

/** The stored read is a promise, so a field settles a tick after construction. */
const settled = async (): Promise<void> => new Promise((resolve) => setTimeout(resolve, 10));

interface Preference {
    key: string;
    make: () => object;
    /** The field the read lands in, and the persist call that writes it back. */
    field: string;
    persist: (store: any) => void;
    /** What the field holds when storage has nothing to say. */
    defaultValue: boolean;
    /** `[stored value, field]` — including the values that are not the shape the field means. */
    stored: Array<[unknown, boolean]>;
}

const PREFERENCES: Preference[] = [
    {
        key: 'notify-on-review',
        make: () => new UiStore(),
        field: 'notifyOnReview',
        persist: (store) => store.persistNotifyOnReview(),
        defaultValue: false,
        stored: [[true, true], [false, false], ['true', false], ['false', false], [undefined, false], [1, false]],
    },
    {
        key: 'auto-maximize-code-viewer',
        make: () => new UiStore(),
        field: 'autoMaximizeCodeViewer',
        persist: (store) => store.persistAutoMaximizeCodeViewer(),
        defaultValue: true,
        stored: [[true, true], [false, false], ['true', true], ['false', true], [undefined, true], [0, true]],
    },
    {
        key: 'kanban-sidebar-collapsed',
        make: () => new AppStore(),
        field: 'sidebarCollapsed',
        persist: (store) => store.persistSidebarCollapsed(),
        defaultValue: false,
        // The one legacy spelling: a string written by an older build still counts.
        stored: [[true, true], [false, false], ['true', true], ['false', false], [undefined, false], [1, false]],
    },
];

async function open(preference: Preference): Promise<any> {
    const store = preference.make();
    await settled();
    return store;
}

describe.each(PREFERENCES)('the browser preference $key', (preference) => {
    beforeEach(() => {
        vi.clearAllMocks();
    });

    it('asks for this browser\'s choice under its own key, with its own default', async () => {
        vi.mocked(loadAppSetting).mockResolvedValue(preference.defaultValue);
        const store = await open(preference);

        expect(loadAppSetting).toHaveBeenCalledWith(preference.key, preference.defaultValue);
        expect(store[preference.field]).toBe(preference.defaultValue);
    });

    it.each(preference.stored)('reads the stored %j as %j', async (stored, expected) => {
        vi.mocked(loadAppSetting).mockResolvedValue(stored);
        const store = await open(preference);

        expect(store[preference.field]).toBe(expected);
    });

    it('keeps the default when the load is refused', async () => {
        vi.mocked(loadAppSetting).mockRejectedValue(new Error('storage.unavailable'));
        const store = await open(preference);

        expect(store[preference.field]).toBe(preference.defaultValue);
    });

    it('writes the current choice back to its own key', async () => {
        vi.mocked(loadAppSetting).mockResolvedValue(preference.defaultValue);
        vi.mocked(saveAppSetting).mockResolvedValue(undefined);
        const store = await open(preference);

        store[preference.field] = !preference.defaultValue;
        preference.persist(store);

        expect(saveAppSetting).toHaveBeenCalledWith(preference.key, !preference.defaultValue);
    });
});
