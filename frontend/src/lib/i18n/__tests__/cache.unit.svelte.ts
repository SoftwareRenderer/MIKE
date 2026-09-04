import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { applyCachedCatalog } from '$lib/i18n/boot';
import { readCachedCatalog, LOCALE_HINT_KEY } from '$lib/i18n/cache';
import { i18n } from '$lib/i18n/runtime.svelte';
import { DEFAULT_LOCALE } from '$lib/i18n/en';
import { loadI18NFromExtensions } from '$lib/i18n/load-pack';
import type { Extension } from '$lib/types';

// The pre-paint cache exists to avoid one frame of English — nothing more. Every
// test here is really about the same property from a different angle: the cache
// may anticipate the live extension list, never outrank it.
//
// `$lib/storage` is mocked to a map so the assertions are about the entries the
// app actually writes, not about IndexedDB in jsdom (which it does not have).

const store = vi.hoisted(() => ({ db: new Map<string, unknown>() }));

vi.mock('$lib/storage', () => ({
    loadAppSetting: vi.fn(async (key: string, dflt: unknown) =>
        store.db.has(key) ? store.db.get(key) : dflt,
    ),
    saveAppSetting: vi.fn(async (key: string, value: unknown) => void store.db.set(key, value)),
    deleteAppSetting: vi.fn(async (key: string) => void store.db.delete(key)),
}));

const CACHE_KEY = 'i18n-catalog';

function seed(entry: unknown) {
    store.db.set(CACHE_KEY, entry);
}

function cached(): { locale?: string; source?: string } | null {
    return (store.db.get(CACHE_KEY) as { locale?: string; source?: string } | null) ?? null;
}

function pack(name: string, locale: string, enabled = true): Extension {
    return {
        id: name,
        name,
        version: '1.0.0',
        type: 'i18n',
        enabled,
        manifest: {
            display_name: name,
            description: 'test pack',
            author: 'tester',
            version: '1.0.0',
            capabilities: { i18n: { locale, messages_url: 'messages.json' } },
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

describe('pre-paint catalog cache', () => {
    let warn: ReturnType<typeof vi.spyOn>;

    beforeEach(() => {
        warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
        vi.spyOn(console, 'info').mockImplementation(() => {});
        store.db.clear();
        window.localStorage.clear();
        i18n.setCatalog(DEFAULT_LOCALE, null);
        document.documentElement.lang = DEFAULT_LOCALE;
    });

    afterEach(() => {
        vi.restoreAllMocks();
        vi.useRealTimers();
    });

    it('applies the cached catalog before mount, lang included', async () => {
        seed({ source: '/extensions/tester/pack-zh', locale: 'zh-CN', messages: { 'nav.board': '看板' } });

        expect(await applyCachedCatalog()).toBe(true);
        expect(i18n.locale).toBe('zh-CN');
        expect(i18n.t('nav.board')).toBe('看板');
        expect(document.documentElement.lang).toBe('zh-CN');
    });

    it('does nothing when there is no cache — the ordinary English start', async () => {
        expect(await applyCachedCatalog()).toBe(false);
        expect(i18n.locale).toBe(DEFAULT_LOCALE);
    });

    it('drops a corrupt entry rather than trusting what is on disk', async () => {
        seed({ source: '/extensions/tester/pack-zh', locale: 'zh-CN', messages: { nav: { board: 'x' } } });

        expect(await applyCachedCatalog()).toBe(false);
        expect(i18n.locale).toBe(DEFAULT_LOCALE);
        expect(store.db.has(CACHE_KEY)).toBe(false);
        expect(warn).toHaveBeenCalled();
    });

    it('drops an oversized entry', async () => {
        const big: Record<string, string> = {};
        for (let i = 0; i < 20_001; i++) big[`k${i}`] = 'v';
        seed({ source: '/extensions/tester/pack-x', locale: 'de', messages: big });

        expect(await applyCachedCatalog()).toBe(false);
        expect(i18n.locale).toBe(DEFAULT_LOCALE);
    });

    it('drops an entry with no source or locale', async () => {
        seed({ locale: 'zh-CN', messages: { a: 'A' } });
        expect(await applyCachedCatalog()).toBe(false);
        seed({ source: '/extensions/tester/pack-zh', messages: { a: 'A' } });
        expect(await applyCachedCatalog()).toBe(false);
    });

    it('drops a stale entry naming the fallback locale — that is not a pack', async () => {
        seed({ source: '/extensions/tester/pack-en', locale: DEFAULT_LOCALE, messages: { a: 'A' } });
        expect(await applyCachedCatalog()).toBe(false);
        expect(store.db.has(CACHE_KEY)).toBe(false);
        expect(i18n.catalog).toBe(null);
    });

    it('gives up rather than blocking the mount when storage will not answer', async () => {
        vi.useFakeTimers();
        // A database that never opens: another tab holding the lock, a browser
        // refusing the store. The app must still boot.
        store.db.set(CACHE_KEY, { source: '/extensions/tester/pack-zh', locale: 'zh-CN', messages: { a: 'A' } });
        const storage = await import('$lib/storage');
        vi.mocked(storage.loadAppSetting).mockImplementationOnce(() => new Promise(() => {}));

        const pending = applyCachedCatalog();
        let settled = false;
        void pending.then(() => (settled = true));
        await vi.advanceTimersByTimeAsync(300);

        await expect(pending).resolves.toBe(false);
        expect(settled).toBe(true);
        expect(i18n.locale).toBe(DEFAULT_LOCALE);
    });

    it('mirrors the cached locale into the synchronous hint for index.html', async () => {
        seed({ source: '/extensions/tester/pack-zh', locale: 'zh-CN', messages: { a: 'A' } });
        await applyCachedCatalog();
        expect(window.localStorage.getItem(LOCALE_HINT_KEY)).toBe('zh-CN');
    });

    it('is corrected by the live list — a cached pack that is gone goes back to English and takes the cache with it', async () => {
        // Exactly the uninstalled-while-the-server-was-down case: boot reads a
        // cache for a pack the live list no longer contains.
        seed({ source: '/extensions/tester/pack-zh', locale: 'zh-CN', messages: { 'nav.board': '看板' } });
        await applyCachedCatalog();
        expect(i18n.locale).toBe('zh-CN');

        await loadI18NFromExtensions([]); // reality: no packs installed

        expect(i18n.locale).toBe(DEFAULT_LOCALE);
        expect(i18n.catalog).toBe(null);
        expect(store.db.has(CACHE_KEY)).toBe(false);
        expect(window.localStorage.getItem(LOCALE_HINT_KEY)).toBe(null);
        expect(document.documentElement.lang).toBe(DEFAULT_LOCALE);
    });

    it('is corrected by the live list — a cached pack that is merely disabled also goes', async () => {
        seed({ source: '/extensions/tester/pack-zh', locale: 'zh-CN', messages: { 'nav.board': '看板' } });
        await applyCachedCatalog();

        await loadI18NFromExtensions([pack('pack-zh', 'zh-CN', false)]);

        expect(i18n.locale).toBe(DEFAULT_LOCALE);
        expect(cached()).toBe(null);
    });

    it('is replaced, not merged, when a different pack wins', async () => {
        seed({ source: '/extensions/tester/pack-zh', locale: 'zh-CN', messages: { 'nav.board': '看板' } });
        await applyCachedCatalog();

        vi.stubGlobal(
            'fetch',
            vi.fn(async () => ({ ok: true, status: 200, text: async () => JSON.stringify({ 'nav.board': 'ボード' }) })),
        );
        try {
            await loadI18NFromExtensions([pack('pack-ja', 'ja')]);
        } finally {
            vi.unstubAllGlobals();
        }

        expect(i18n.t('nav.board')).toBe('ボード'); // not zh leaking through
        expect(cached()).toMatchObject({ locale: 'ja', source: '/extensions/tester/pack-ja' });
    });

    it('readCachedCatalog re-validates on the way out', async () => {
        seed({ source: '/extensions/tester/pack-zh', locale: 'zh-CN', messages: { a: 1 } });
        expect(await readCachedCatalog()).toBe(null);
    });
});
