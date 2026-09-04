import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import type { Extension } from '$lib/types';
import { resolveI18NPack, loadI18NFromExtensions } from '$lib/i18n/load-pack';
import { i18n } from '$lib/i18n/runtime.svelte';
import { DEFAULT_LOCALE, en } from '$lib/i18n/en';
import { loadAppSetting, saveAppSetting, deleteAppSetting } from '$lib/storage';

// The loader is the only new behavior in T6: resolve the enabled i18n extension,
// fetch its catalog from the pack's own served directory, and hand it to the
// runtime — or stay in English and say why. `/extensions/...` is mocked here; the
// real served tree is covered by the manual gate.
//
// The IndexedDB-backed cache is mocked to a map so "the cache never outranks the
// live extension list" is an assertion about the same key the app writes.

const store = vi.hoisted(() => ({ db: new Map<string, unknown>() }));

vi.mock('$lib/storage', () => ({
    loadAppSetting: vi.fn(async (key: string, dflt: unknown) =>
        store.db.has(key) ? store.db.get(key) : dflt,
    ),
    saveAppSetting: vi.fn(async (key: string, value: unknown) => void store.db.set(key, value)),
    deleteAppSetting: vi.fn(async (key: string) => void store.db.delete(key)),
}));

const CACHE_KEY = 'i18n-catalog';

function makePack(opts: {
    name: string;
    locale: string;
    author?: string;
    enabled?: boolean;
    priority?: number;
    messagesUrl?: string;
}): Extension {
    return {
        id: opts.name,
        name: opts.name,
        version: '1.0.0',
        type: 'i18n',
        enabled: opts.enabled ?? true,
        manifest: {
            display_name: opts.name,
            description: 'test pack',
            author: opts.author ?? 'tester',
            version: '1.0.0',
            priority: opts.priority ?? 0,
            capabilities: {
                i18n: {
                    locale: opts.locale,
                    messages_url: opts.messagesUrl ?? 'messages.json',
                },
            },
        },
        config: {},
        container_config: {},
        sandbox_config: { network_isolation: false, memory_limit: 0 },
        running: false,
        installed_at: '',
        built_at: '',
        install_source: 'local',
    };
}

/** Serve `body` for a catalog URL, failing anything else. */
function mockCatalogFetch(body: string, ok = true, status = 200) {
    const fetchMock = vi.fn(async (url: string) => ({
        ok,
        status,
        url,
        text: async () => body,
    }));
    vi.stubGlobal('fetch', fetchMock);
    return fetchMock;
}

const zh = { 'nav.board': '看板', 'task.create': '新建任务' };

describe('loadI18NFromExtensions', () => {
    let err: ReturnType<typeof vi.spyOn>;
    let info: ReturnType<typeof vi.spyOn>;

    beforeEach(async () => {
        err = vi.spyOn(console, 'error').mockImplementation(() => {});
        info = vi.spyOn(console, 'info').mockImplementation(() => {});
        vi.spyOn(console, 'warn').mockImplementation(() => {});
        store.db.clear();
        // Baseline: no pack enabled. This is also the reset path for the
        // loader's module state between tests.
        await loadI18NFromExtensions([]);
        vi.stubGlobal('fetch', vi.fn());
    });

    afterEach(() => {
        vi.unstubAllGlobals();
        vi.restoreAllMocks();
    });

    it('applies the enabled pack, resolved against its own served directory', async () => {
        const fetchMock = mockCatalogFetch(JSON.stringify(zh));
        const res = await loadI18NFromExtensions([makePack({ name: 'pack-zh', locale: 'zh-CN' })]);

        expect(res).toEqual({ locale: 'zh-CN', source: '/extensions/tester/pack-zh' });
        // The URL is resolveAssetURL's answer — the pack's own directory, no new route.
        expect(fetchMock).toHaveBeenCalledWith('/extensions/tester/pack-zh/messages.json', expect.anything());
        expect(i18n.locale).toBe('zh-CN');
        expect(i18n.t('nav.board')).toBe('看板');
        expect(document.documentElement.lang).toBe('zh-CN');
    });

    it('stores the applied catalog in the app-settings cache', async () => {
        mockCatalogFetch(JSON.stringify(zh));
        await loadI18NFromExtensions([makePack({ name: 'pack-zh', locale: 'zh-CN' })]);
        expect(store.db.get(CACHE_KEY)).toEqual({
            source: '/extensions/tester/pack-zh',
            locale: 'zh-CN',
            messages: zh,
        });
    });

    it('stays in English on malformed JSON and reports it', async () => {
        mockCatalogFetch('{"nav.board": ');
        const res = await loadI18NFromExtensions([makePack({ name: 'pack-zh', locale: 'zh-CN' })]);

        expect(res.locale).toBe(DEFAULT_LOCALE);
        expect(i18n.catalog).toBe(null);
        expect(document.documentElement.lang).toBe(DEFAULT_LOCALE);
        expect(res.reason).toMatch(/not valid JSON/);
        expect(err).toHaveBeenCalled();
        expect(String(err.mock.calls[0][0])).toContain('Remaining in English');
        // Nothing half-loaded is left to be found on the next boot either.
        expect(store.db.has(CACHE_KEY)).toBe(false);
    });

    it('stays in English when the catalog is not an object of strings', async () => {
        mockCatalogFetch(JSON.stringify({ nav: { board: '看板' } }));
        const res = await loadI18NFromExtensions([makePack({ name: 'pack-zh', locale: 'zh-CN' })]);
        expect(res.locale).toBe(DEFAULT_LOCALE);
        expect(i18n.catalog).toBe(null);
    });

    it('stays in English when the pack answers 404', async () => {
        const fetchMock = mockCatalogFetch('', false, 404);
        const res = await loadI18NFromExtensions([makePack({ name: 'pack-gone', locale: 'fr' })]);
        expect(res.locale).toBe(DEFAULT_LOCALE);
        expect(res.reason).toMatch(/answered 404/);
        expect(err).toHaveBeenCalled();
        expect(fetchMock).toHaveBeenCalledTimes(1);
    });

    it('falls back per key to English for a key the pack lacks, warning about that key', async () => {
        const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
        en['common.save'] = 'Save';
        try {
            mockCatalogFetch(JSON.stringify({ 'nav.board': '看板' }));
            await loadI18NFromExtensions([makePack({ name: 'pack-zh', locale: 'zh-CN' })]);

            expect(i18n.t('nav.board')).toBe('看板');
            expect(i18n.t('common.save')).toBe('Save'); // per-key, not wholesale
            expect(warn).toHaveBeenCalledTimes(1);
            expect(String(warn.mock.calls[0][0])).toContain('common.save');
        } finally {
            delete en['common.save'];
        }
    });

    it('ignores a disabled pack entirely — no fetch, no text', async () => {
        const fetchMock = mockCatalogFetch(JSON.stringify(zh));
        const res = await loadI18NFromExtensions([
            makePack({ name: 'pack-zh', locale: 'zh-CN', enabled: false }),
        ]);

        expect(res).toEqual({ locale: DEFAULT_LOCALE, source: null });
        expect(fetchMock).not.toHaveBeenCalled();
        expect(i18n.t('nav.board')).toBe('nav.board');
    });

    it('drops back to English when the pack goes away (uninstalled, or disabled after applying)', async () => {
        mockCatalogFetch(JSON.stringify(zh));
        const pack = makePack({ name: 'pack-zh', locale: 'zh-CN' });
        await loadI18NFromExtensions([pack]);
        expect(i18n.locale).toBe('zh-CN');

        // Uninstalled: the row is gone from the list the server just sent.
        const res = await loadI18NFromExtensions([]);
        expect(res.locale).toBe(DEFAULT_LOCALE);
        expect(i18n.catalog).toBe(null);
        expect(document.documentElement.lang).toBe(DEFAULT_LOCALE);
        // And the cached text is deleted with it: no language outlives its pack.
        expect(store.db.has(CACHE_KEY)).toBe(false);
    });

    it('refuses an external messages_url and fetches nothing', async () => {
        const fetchMock = mockCatalogFetch(JSON.stringify(zh));
        const res = await loadI18NFromExtensions([
            makePack({ name: 'pack-zh', locale: 'zh-CN', messagesUrl: 'https://evil.test/zh.json' }),
        ]);

        expect(res.locale).toBe(DEFAULT_LOCALE);
        expect(fetchMock).not.toHaveBeenCalled();
        expect(res.reason).toMatch(/not a file in the extension's own directory/);
    });

    it('slugifies the author into the catalog URL, like a css pack', async () => {
        const fetchMock = mockCatalogFetch(JSON.stringify(zh));
        await loadI18NFromExtensions([makePack({ name: 'pack-zh', locale: 'zh-CN', author: 'Test Vendor' })]);
        expect(fetchMock.mock.calls[0][0]).toBe('/extensions/test-vendor/pack-zh/messages.json');
    });

    // The same three packs, same priorities, same winner as TestActiveI18N in
    // backend/extensions/i18n_test.go. That test and this one are the acceptance
    // check that the browser and the server pick the same active pack from the
    // same list: the UI can never render a language different from the one the
    // prompt locale derivation (T14) will use, because both apply this rule and
    // nothing stores the result.
    describe('two enabled packs', () => {
        const packZh = makePack({ name: 'pack-zh', locale: 'zh-CN', priority: 1 });
        const packJa = makePack({ name: 'pack-ja', locale: 'ja', priority: 1 });
        const packFr = makePack({ name: 'pack-fr', locale: 'fr', priority: 10 });

        it('picks the same winner from any input order', () => {
            const orders = [
                [packFr, packJa, packZh],
                [packZh, packFr, packJa],
                [packJa, packZh, packFr],
            ];
            const winners = orders.map((list) => resolveI18NPack(list)?.name);
            expect(new Set(winners)).toEqual(new Set(['pack-ja'])); // priority tie → first name
        });

        it('applies that winner and logs which packs were ignored', async () => {
            mockCatalogFetch(JSON.stringify({ nav: 'ボード' }));
            const res = await loadI18NFromExtensions([packFr, packJa, packZh]);

            expect(res.source).toBe('/extensions/tester/pack-ja');
            expect(i18n.locale).toBe('ja');
            const logged = info.mock.calls.map((c: unknown[]) => String(c[0])).join('\n');
            expect(logged).toContain('pack-ja → ja (active)');
            expect(logged).toContain('pack-fr');
            expect(logged).toContain('pack-zh');
        });

        it('skips a disabled winner and takes the next enabled pack', async () => {
            mockCatalogFetch(JSON.stringify({}));
            const res = await loadI18NFromExtensions([
                makePack({ name: 'pack-ja', locale: 'ja', priority: 1, enabled: false }),
                packFr,
            ]);
            expect(res.source).toBe('/extensions/tester/pack-fr');
            expect(i18n.locale).toBe('fr');
        });
    });

    describe('failure policy', () => {
        it('keeps the working catalog when the same pack merely fails to refresh', async () => {
            mockCatalogFetch(JSON.stringify(zh));
            await loadI18NFromExtensions([makePack({ name: 'pack-zh', locale: 'zh-CN' })]);
            err.mockClear();

            mockCatalogFetch('nope', false, 500);
            const res = await loadI18NFromExtensions([makePack({ name: 'pack-zh', locale: 'zh-CN' })]);

            // Still Chinese — a transient blip must not throw a working UI back
            // to English — and the failure is still reported.
            expect(res).toMatchObject({ locale: 'zh-CN', kept: true });
            expect(i18n.t('nav.board')).toBe('看板');
            expect(err).toHaveBeenCalled();
            expect(String(err.mock.calls[0][0])).toContain('keeping the catalog already in use');
        });

        it('falls to English when a *different* winner fails to load', async () => {
            mockCatalogFetch(JSON.stringify(zh));
            await loadI18NFromExtensions([makePack({ name: 'pack-zh', locale: 'zh-CN' })]);

            mockCatalogFetch('nope', false, 500);
            const res = await loadI18NFromExtensions([makePack({ name: 'pack-fr', locale: 'fr' })]);

            expect(res.locale).toBe(DEFAULT_LOCALE);
            expect(i18n.catalog).toBe(null);
            expect(store.db.has(CACHE_KEY)).toBe(false);
        });
    });

    it('does not let an older list land after a newer one', async () => {
        let release!: () => void;
        const gate = new Promise<void>((r) => (release = r));
        vi.stubGlobal(
            'fetch',
            vi.fn(async (url: string) => {
                if (url.includes('pack-slow')) await gate;
                return {
                    ok: true,
                    status: 200,
                    text: async () => JSON.stringify({ 'nav.board': url.includes('slow') ? '慢' : '速' }),
                };
            }),
        );

        const slow = loadI18NFromExtensions([makePack({ name: 'pack-slow', locale: 'zh-CN' })]);
        const fast = loadI18NFromExtensions([makePack({ name: 'pack-fast', locale: 'ja' })]);
        await fast;
        release();
        await slow;

        // The second list (pack-fast) is the truth; the slow fetch must not
        // overwrite it when it finally arrives.
        expect(i18n.locale).toBe('ja');
        expect(i18n.t('nav.board')).toBe('速');
    });

    describe('cache vs reality', () => {
        it('a cached pack that is no longer in the list leaves no cache behind', async () => {
            // Simulate a previous session's cache for a pack that is gone.
            store.db.set(CACHE_KEY, { source: '/extensions/tester/pack-zh', locale: 'zh-CN', messages: zh });

            await loadI18NFromExtensions([]); // the live list: no packs

            expect(store.db.has(CACHE_KEY)).toBe(false);
            expect(i18n.locale).toBe(DEFAULT_LOCALE);
        });

        it('rewrites the cache when the winner changes packs', async () => {
            mockCatalogFetch(JSON.stringify(zh));
            await loadI18NFromExtensions([makePack({ name: 'pack-zh', locale: 'zh-CN' })]);
            mockCatalogFetch(JSON.stringify({ 'nav.board': 'ボード' }));
            await loadI18NFromExtensions([makePack({ name: 'pack-ja', locale: 'ja' })]);

            const cached = store.db.get(CACHE_KEY) as { source: string; locale: string };
            expect(cached.source).toBe('/extensions/tester/pack-ja');
            expect(cached.locale).toBe('ja');
        });

        it('uses the mocked storage helpers rather than IndexedDB directly', async () => {
            mockCatalogFetch(JSON.stringify(zh));
            await loadI18NFromExtensions([makePack({ name: 'pack-zh', locale: 'zh-CN' })]);
            expect(saveAppSetting).toHaveBeenCalledWith(CACHE_KEY, expect.objectContaining({ locale: 'zh-CN' }));

            await loadI18NFromExtensions([]);
            expect(deleteAppSetting).toHaveBeenCalledWith(CACHE_KEY);
            expect(loadAppSetting).toBeDefined();
        });
    });
});
