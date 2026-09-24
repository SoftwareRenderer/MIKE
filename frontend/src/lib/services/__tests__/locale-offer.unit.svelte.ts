import { describe, it, expect, beforeEach, vi } from 'vitest';
import type { Extension, RegistryEntry } from '$lib/types';
import type { BuildProgressEvent } from '$lib/api/extensions';
import { LOCALE_OFFER_SETTING_KEY, type LocaleOfferRecord } from '$lib/utils/locale-offer';

// The service is the part that touches IndexedDB, the registry and the install
// stream, so all three are faked here: what is under test is which of the four
// outcomes the user sees and what is remembered, not what the server does.
//
// The resolver's own rules are unit-tested in `utils/__tests__/locale-offer`; this
// file covers what only this layer can: that a registry failure is silence rather
// than an error, that the answer is persisted, and that one boot asks once.

const store = vi.hoisted(() => ({ db: new Map<string, unknown>() }));
const api = vi.hoisted(() => ({
    getRegistryIndex: vi.fn(),
    installExtension: vi.fn(),
}));
const extManager = vi.hoisted(() => ({
    state: { extensions: [] as Extension[] },
    loadExtensions: vi.fn(async () => {}),
}));
const dialogs = vi.hoisted(() => ({ showToast: vi.fn() }));

vi.mock('$lib/storage', () => ({
    loadAppSetting: vi.fn(async (key: string, dflt: unknown) => (store.db.has(key) ? store.db.get(key) : dflt)),
    saveAppSetting: vi.fn(async (key: string, value: unknown) => void store.db.set(key, value)),
    deleteAppSetting: vi.fn(async (key: string) => void store.db.delete(key)),
}));

vi.mock('$lib/api', () => ({
    getRegistryIndex: api.getRegistryIndex,
    installExtension: api.installExtension,
}));

vi.mock('$lib/services/extension-manager.svelte', () => ({ extensionManager: extManager }));
vi.mock('$lib/stores', () => ({ dialogStore: dialogs }));

// Imported after the mocks are declared (vi.mock is hoisted anyway) so the module
// under test is built on the fakes above.
const { localeOffer } = await import('$lib/services/locale-offer.svelte');

const PACK_ENTRY: RegistryEntry = {
    name: 'locale-ja',
    version: '1.0.0',
    type: 'i18n',
    display_name: '日本語',
    description: 'test pack',
    author: 'Tester',
    repo: 'https://example.test/extensions',
    commit: '0'.repeat(40),
    subdir: 'i18n/locale-ja',
    capabilities: { i18n: { locale: 'ja', messages_url: 'messages.json' } },
};

function sse(events: BuildProgressEvent[]): AsyncIterable<BuildProgressEvent> {
    return {
        [Symbol.asyncIterator]: async function* () {
            for (const e of events) yield e;
        },
    };
}

function doneEvent(error?: string): BuildProgressEvent {
    return error
        ? { extension: 'locale-ja', progress: 100, message: 'Install failed', done: true, error }
        : { extension: 'locale-ja', progress: 100, message: 'Installed', done: true };
}

function storedRecord(): LocaleOfferRecord | undefined {
    return store.db.get(LOCALE_OFFER_SETTING_KEY) as LocaleOfferRecord | undefined;
}

beforeEach(() => {
    store.db.clear();
    vi.clearAllMocks();
    localeOffer.reset();
    extManager.state.extensions = [];
    api.getRegistryIndex.mockResolvedValue([PACK_ENTRY]);
    api.installExtension.mockReturnValue(sse([
        { extension: 'locale-ja', progress: 10, message: 'Fetching extension...', done: false },
        doneEvent(),
    ]));
});

describe('localeOffer.detect', () => {
    it('offers the pack that matches the browser language', async () => {
        await localeOffer.detect('ja');
        expect(localeOffer.state).toMatchObject({ phase: 'offered', displayName: '日本語', locale: 'ja' });
        expect(api.installExtension).not.toHaveBeenCalled();
    });

    it('says nothing to an English browser', async () => {
        await localeOffer.detect('en-US');
        expect(localeOffer.state.phase).toBe('idle');
    });

    it('says nothing when the registry cannot be read — and does not throw', async () => {
        // MIKE_REGISTRY_URL is a local fixture path in tests and unreachable
        // offline: this is the normal case, not a failure to report.
        api.getRegistryIndex.mockRejectedValue(new Error('open file /nope/registry.json: no such file'));
        const info = vi.spyOn(console, 'info').mockImplementation(() => {});
        await expect(localeOffer.detect('ja')).resolves.toBeUndefined();
        expect(localeOffer.state.phase).toBe('idle');
        info.mockRestore();
    });

    it('does not even read the registry once the answer is on record', async () => {
        store.db.set(LOCALE_OFFER_SETTING_KEY, { pack: 'tester/locale-ja', outcome: 'dismissed' });
        await localeOffer.detect('ja');
        expect(localeOffer.state.phase).toBe('idle');
        expect(api.getRegistryIndex).not.toHaveBeenCalled();
    });

    it('asks once per boot, whatever happens to the extension list meanwhile', async () => {
        await localeOffer.detect('ja');
        expect(localeOffer.state.phase).toBe('offered');
        // A second boot path (or any later caller) must not pay for the registry
        // again, and after the user has answered, nothing re-raises the offer.
        await localeOffer.detect('de');
        expect(api.getRegistryIndex).toHaveBeenCalledTimes(1);
        await localeOffer.dismiss();
        await localeOffer.detect('ja');
        expect(localeOffer.state.phase).toBe('idle');
    });
});

describe('localeOffer.install', () => {
    it('reports progress while the operation streams', async () => {
        let seen: number[] = [];
        api.installExtension.mockImplementation(() => ({
            [Symbol.asyncIterator]: async function* () {
                for (const pct of [10, 40, 100]) {
                    seen.push(pct);
                    yield { extension: 'locale-ja', progress: pct, message: 'step', done: pct === 100 };
                }
            },
        }));
        await localeOffer.detect('ja');
        await localeOffer.install();
        expect(seen).toEqual([10, 40, 100]);
        expect(localeOffer.state.phase).toBe('idle');
    });

    it('remembers the install, reloads the extension list, and switches without a prompt', async () => {
        await localeOffer.detect('ja');
        await localeOffer.install();
        expect(storedRecord()).toEqual({ pack: 'tester/locale-ja', outcome: 'installed' });
        // The live switch: the language follows the enabled pack out of
        // loadExtensions(), which the extension_change broadcast also drives.
        expect(extManager.loadExtensions).toHaveBeenCalledTimes(1);
        expect(dialogs.showToast).not.toHaveBeenCalled();
        expect(localeOffer.state.phase).toBe('idle');
    });

    it('keeps the UI in English and remembers a failed install, so a broken registry cannot nag', async () => {
        api.installExtension.mockReturnValue(sse([doneEvent('failed to fetch extension (commit …): HTTP 404')]));
        await localeOffer.detect('ja');
        await localeOffer.install();

        expect(storedRecord()).toEqual({ pack: 'tester/locale-ja', outcome: 'failed' });
        expect(dialogs.showToast).toHaveBeenCalledTimes(1);
        expect(dialogs.showToast.mock.calls[0][0]).toContain('日本語');
        expect(extManager.loadExtensions).not.toHaveBeenCalled();
        expect(localeOffer.state.phase).toBe('idle');

        // Next boot: the failure is why nothing is offered.
        localeOffer.reset();
        await localeOffer.detect('ja');
        expect(localeOffer.state.phase).toBe('idle');
    });

    it('treats a stream that dies mid-way as the same failure', async () => {
        api.installExtension.mockImplementation(() => {
            throw new Error('network down');
        });
        await localeOffer.detect('ja');
        await localeOffer.install();
        expect(storedRecord()?.outcome).toBe('failed');
        expect(dialogs.showToast).toHaveBeenCalledTimes(1);
    });

    it('does nothing when there is nothing on offer', async () => {
        await localeOffer.install();
        expect(api.installExtension).not.toHaveBeenCalled();
    });
});

describe('localeOffer.dismiss', () => {
    it('"Don\'t ask again" hides the offer and persists the answer', async () => {
        await localeOffer.detect('ja');
        await localeOffer.dismiss();
        expect(localeOffer.state.phase).toBe('idle');
        expect(storedRecord()).toEqual({ pack: 'tester/locale-ja', outcome: 'dismissed' });
        expect(api.installExtension).not.toHaveBeenCalled();

        localeOffer.reset();
        await localeOffer.detect('ja');
        expect(localeOffer.state.phase).toBe('idle');
    });
});
