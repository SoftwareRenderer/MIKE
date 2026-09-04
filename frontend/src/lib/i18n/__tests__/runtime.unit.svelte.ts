import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { I18nRuntime, t } from '$lib/i18n/runtime.svelte';
import { en, DEFAULT_LOCALE } from '$lib/i18n/en';
import type { Catalog } from '$lib/i18n/catalog';

// The runtime is what a component reads, so these tests are about what the
// screen does: which text a key resolves to, what happens per-key when a pack is
// incomplete, and that documentElement.lang follows the active pack.

describe('i18n runtime', () => {
    let runtime: I18nRuntime;
    let warn: ReturnType<typeof vi.spyOn>;
    const seededEn: string[] = [];

    beforeEach(() => {
        runtime = new I18nRuntime();
        warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
        document.documentElement.lang = DEFAULT_LOCALE;
    });

    afterEach(() => {
        warn.mockRestore();
        // `en` is the module-level catalog of record; leave it as found.
        for (const k of seededEn) delete en[k];
        seededEn.length = 0;
    });

    function useEnglish(key: string, text: string) {
        en[key] = text;
        seededEn.push(key);
    }

    it('starts in the fallback locale with no catalog', () => {
        expect(runtime.locale).toBe(DEFAULT_LOCALE);
        expect(runtime.catalog).toBe(null);
    });

    it('renders the active pack', () => {
        runtime.setCatalog('zh-CN', { 'nav.board': '看板' });
        expect(runtime.t('nav.board')).toBe('看板');
        expect(runtime.locale).toBe('zh-CN');
    });

    it('exposes one runtime to the app: the exported t/activeLocale read it', async () => {
        // Components import `t` and `activeLocale`, so those must be wired to the
        // same instance the loader calls setCatalog on — a second hidden copy
        // would render English forever and still pass every test above.
        const { i18n, t: appT, activeLocale } = await import('$lib/i18n/runtime.svelte');
        i18n.setCatalog('de', { 'nav.board': 'Tafel' });
        expect(appT('nav.board')).toBe('Tafel');
        expect(activeLocale()).toBe('de');
        i18n.setCatalog('de', null);
        expect(activeLocale()).toBe(DEFAULT_LOCALE);
    });

    it('falls back per key to English when the pack lacks the key, and warns', () => {
        useEnglish('common.save', 'Save');
        runtime.setCatalog('zh-CN', { 'nav.board': '看板' });

        expect(runtime.t('nav.board')).toBe('看板');
        // The pack answers one key and not the other — a partially translated
        // pack must degrade per key, not wholesale.
        expect(runtime.t('common.save')).toBe('Save');
        expect(warn).toHaveBeenCalledTimes(1);
        expect(warn.mock.calls[0][0]).toContain('common.save');
        expect(String(warn.mock.calls[0][0])).toContain('English');
    });

    it('warns once per key per catalog, not once per render', () => {
        runtime.setCatalog('fr', {});
        runtime.t('nope.one');
        runtime.t('nope.one');
        runtime.t('nope.two');
        expect(warn).toHaveBeenCalledTimes(2);
    });

    it('re-warns after a new catalog arrives — the pack may have been fixed', () => {
        runtime.setCatalog('fr', {});
        runtime.t('nav.board');
        runtime.setCatalog('fr', { 'nav.board': 'Tableau' });
        expect(runtime.t('nav.board')).toBe('Tableau');
        expect(warn).toHaveBeenCalledTimes(1);
    });

    it('falls back to the message id when neither pack nor English answers, and says so', () => {
        runtime.setCatalog('fr', {});
        expect(runtime.t('never.heard.of.it')).toBe('never.heard.of.it');
        expect(String(warn.mock.calls[0][0])).toContain('message id');
    });

    it('does not warn while no pack is active (English is the baseline, not a miss)', () => {
        // `en` is deliberately empty until the UI-string conversion task; an id
        // with no entry here is that task's to-do list, not a runtime fault.
        expect(t('some.key')).toBe('some.key');
        expect(warn).not.toHaveBeenCalled();
    });

    it('returns to English and drops the pack on setCatalog(locale, null)', () => {
        runtime.setCatalog('zh-CN', { 'nav.board': '看板' });
        runtime.setCatalog('zh-CN', null);
        expect(runtime.locale).toBe(DEFAULT_LOCALE);
        expect(runtime.catalog).toBe(null);
        expect(runtime.t('nav.board')).toBe('nav.board');
    });

    it('refuses to adopt the fallback locale as a pack', () => {
        // A pack declaring "en" must not become a catalog that shadows English:
        // the fallback map is the record, not something a pack overrides.
        runtime.setCatalog(DEFAULT_LOCALE, { 'nav.board': 'override' });
        expect(runtime.catalog).toBe(null);
        expect(runtime.locale).toBe(DEFAULT_LOCALE);
    });

    it('owns documentElement.lang in both directions', () => {
        runtime.setCatalog('ar', { a: 'x' });
        expect(document.documentElement.lang).toBe('ar');
        runtime.setCatalog('ar', null);
        expect(document.documentElement.lang).toBe(DEFAULT_LOCALE);
    });

    it('interpolates {name} tokens and leaves unknown ones visible', () => {
        runtime.setCatalog('zh-CN', { 'task.count': '{n} 个任务 · {other}' });
        expect(runtime.t('task.count', { n: 3 })).toBe('3 个任务 · {other}');
    });

    it('has() answers only for the active pack', () => {
        useEnglish('only.english', 'x');
        expect(runtime.has('only.english')).toBe(false);
        runtime.setCatalog('ja', { 'nav.board': 'ボード' });
        expect(runtime.has('nav.board')).toBe(true);
        expect(runtime.has('only.english')).toBe(false);
    });

    it('applies a snapshot — a catalog is never half-installed', () => {
        const catalog: Catalog = { a: 'A' };
        runtime.setCatalog('zh-CN', catalog);
        // The loader mutating its working object after the handover must not
        // change what is on screen: that is the half-applied pack again.
        catalog.b = 'B';
        expect(runtime.catalog).toEqual({ a: 'A' });
        expect(runtime.t('b')).toBe('b');
    });
});
