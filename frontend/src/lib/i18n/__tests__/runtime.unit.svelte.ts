import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import {
    activeLocale,
    documentLanguage,
    hasKey,
    i18n,
    I18nRuntime,
    onLocaleChange,
    packText,
    registerCatalog,
    resolvedLocale,
    setLocale,
    t,
} from '$lib/i18n/runtime.svelte';
import { en, DEFAULT_LOCALE } from '$lib/i18n/en';
import type { Catalog } from '$lib/i18n/catalog';

// The runtime is what the app reads — both as a component (`t`, `activeLocale`) and
// as a plain module (`Intl` formatting, a canvas redraw) — so these pin the contract
// on both halves: which catalog answers, what happens when none does, who owns
// <html lang>, and that the language only ever comes from the active pack.
//
// Vitest gives each file its own module instance, so the catalogs registered here
// are local to this file.

/** Keys seeded into the module-level `en` catalog, removed again afterwards. */
const seededEn: string[] = [];

beforeEach(() => {
    document.documentElement.lang = DEFAULT_LOCALE;
});

afterEach(() => {
    // The singleton is shared by the whole file: leave it exactly as found, or a
    // later test reads a language an earlier test left switched on.
    i18n.setCatalog(DEFAULT_LOCALE, null);
    for (const k of seededEn) delete en[k];
    seededEn.length = 0;
});

function useEnglish(key: string, text: string): void {
    en[key] = text;
    seededEn.push(key);
}

describe('I18nRuntime (one instance per runtime)', () => {
    let runtime: I18nRuntime;
    let warn: ReturnType<typeof vi.spyOn>;

    beforeEach(() => {
        runtime = new I18nRuntime();
        warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
    });

    afterEach(() => {
        warn.mockRestore();
    });

    it('starts in the fallback locale with no catalog', () => {
        expect(runtime.locale).toBe(DEFAULT_LOCALE);
        expect(runtime.catalog).toBe(null);
    });

    it('renders the active pack', () => {
        runtime.setCatalog('zh-CN', { 'nav.board': '看板' });
        expect(runtime.t('nav.board')).toBe('看板');
        expect(runtime.locale).toBe('zh-CN');
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
        expect(runtime.registerCatalog(DEFAULT_LOCALE, { a: 'b' })).toBe(false);
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

describe('the app-wide runtime', () => {
    it('is the one the exported accessors read', () => {
        // Components import `t`/`activeLocale`, so those must be wired to the same
        // instance the loader calls setCatalog on — a second hidden copy would
        // render English forever and still pass every instance test above.
        i18n.setCatalog('de', { 'nav.board': 'Tafel' });
        expect(t('nav.board')).toBe('Tafel');
        expect(activeLocale()).toBe('de');
        i18n.setCatalog('de', null);
        // No pack is not "English as a chosen locale": `undefined` is what every
        // `Intl.*` call site had before i18n existed, i.e. follow the browser.
        expect(activeLocale()).toBeUndefined();
    });

    it('does not warn while no pack is active (English is the baseline, not a miss)', () => {
        const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
        // `en` holds only the keys the UI renders today; an id with no entry here
        // is the conversion task's to-do list, not a runtime fault.
        expect(t('some.key.nobody.converted')).toBe('some.key.nobody.converted');
        expect(warn).not.toHaveBeenCalled();
        warn.mockRestore();
    });
});

describe('t()', () => {
    it('answers from the English catalog of record', () => {
        expect(t('time.justNow')).toBe('just now');
        expect(t('time.yesterdayAt', { time: '3:45 PM' })).toBe('Yesterday at 3:45 PM');
    });

    it('interpolates {token} and leaves an unknown token alone', () => {
        expect(t('time.built', { when: '5m ago' })).toBe('Built 5m ago');
        expect(t('time.built')).toBe('Built {when}');
        expect(t('time.built', { time: '3:45 PM' })).toBe('Built {when}');
    });

    it('degrades to the key rather than to blank, and never throws', () => {
        expect(t('time.does-not-exist')).toBe('time.does-not-exist');
        expect(t('')).toBe('');
    });
});

describe('active locale', () => {
    it('is undefined until a locale is chosen — "use the browser", the pre-i18n behaviour', () => {
        expect(activeLocale()).toBeUndefined();
        setLocale('zh-Hans');
        expect(activeLocale()).toBe('zh-Hans');
        setLocale(null);
        expect(activeLocale()).toBeUndefined();
    });

    it('rejects a malformed tag instead of silently dropping the user out of their language', () => {
        setLocale('ja');
        setLocale('not a tag!');
        expect(activeLocale()).toBe('ja');
        setLocale('');
        expect(activeLocale()).toBe('ja');
    });

    it('accepts exactly the tag shape the server validated at install time', () => {
        // `bcp47ish` (backend/extensions/manifest_validate.go) is the rule that let a pack
        // in, and `setCatalog` reads a tag it cannot parse as "no pack" — a stricter rule
        // here would keep a perfectly valid pack silently in English. Single-character
        // subtags are legal BCP 47, private-use tags included.
        setLocale('sr-Latn-RS');
        expect(activeLocale()).toBe('sr-Latn-RS');
        setLocale('de-x-y');
        expect(activeLocale()).toBe('de-x-y');
    });

    it('separates "what Intl should use" from "what the strings are"', () => {
        // resolvedLocale() is the taste (a browser default counts); documentLanguage()
        // is the language of the rendered text, and stays `en` while no pack is on —
        // otherwise :lang() typography would restyle an English UI in Japanese.
        expect(documentLanguage()).toBe('en');
        expect(resolvedLocale().length).toBeGreaterThan(1);
        setLocale('ko');
        expect(documentLanguage()).toBe('ko');
        expect(resolvedLocale()).toBe('ko');
    });

    it('is held in memory only — the persisted language is the enabled pack', () => {
        // There is deliberately no stored locale: it would have to be reconciled
        // with the extension list forever and the two would disagree. The only
        // locale in storage is the pre-paint hint, written by `cache.ts` beside the
        // cached catalog and deleted with it.
        setLocale('ko');
        expect(activeLocale()).toBe('ko');
        for (const key of ['kanban-locale', 'kanban-i18n-locale']) {
            expect(globalThis.localStorage?.getItem(key) ?? null).toBeNull();
        }
    });
});

describe('catalog lookup', () => {
    it('answers from the active pack when it ships the key', () => {
        registerCatalog('pt-BR', { 'time.justNow': 'agora mesmo' });
        setLocale('pt-BR');
        expect(t('time.justNow')).toBe('agora mesmo');
    });

    it('narrows the tag by subtag, and borrows from nothing else', () => {
        registerCatalog('de', { 'time.justNow': 'gerade eben' });
        setLocale('de-AT');
        expect(t('time.justNow')).toBe('gerade eben');
        // …but a sibling region with nothing to inherit from has no catalog at all.
        registerCatalog('nb', { 'time.justNow': 'nettopp' });
        setLocale('nb');
        expect(t('time.justNow')).toBe('nettopp');
        setLocale('sv');
        expect(t('time.justNow')).toBe('just now');
    });

    it('packText() does NOT fall back to English — that is what lets Intl answer', () => {
        registerCatalog('nl', { 'time.justNow': 'zojuist' });
        setLocale('nl');
        expect(packText('time.justNow')).toBe('zojuist');
        expect(packText('time.minuteAgo')).toBeUndefined();
        // …while t() keeps its English fallback, because a caller with no better
        // answer must still render something readable.
        expect(t('time.minuteAgo', { n: 5 })).toBe('5m ago');
    });

    it('answers from the English catalog when no locale is chosen at all', () => {
        expect(packText('time.justNow')).toBe('just now');
        expect(hasKey('time.justNow')).toBe(true);
        expect(hasKey('time.nope')).toBe(false);
    });

    it('refuses to install a catalog under a tag nothing could ever select', () => {
        expect(registerCatalog('not a tag!', { 'time.justNow': 'x' })).toBe(false);
        expect(registerCatalog('pt-PT', { 'time.justNow': 'agora mesmo' })).toBe(true);
        setLocale('pt-PT');
        expect(t('time.justNow')).toBe('agora mesmo');
    });
});

describe('<html lang>', () => {
    it('follows the pack the loader installed, in both directions', () => {
        // This is the production path — `load-pack.ts` calling setCatalog when the
        // enabled extension list arrives. It is the only writer of the attribute
        // after index.html's initial value, and it is what the first-run offer's
        // e2e asserts on without a reload.
        i18n.setCatalog('zh-Hans', { 'time.justNow': '刚刚' });
        expect(document.documentElement.lang).toBe('zh-Hans');
        expect(documentLanguage()).toBe('zh-Hans');
        i18n.setCatalog('zh-Hans', null);
        expect(document.documentElement.lang).toBe('en');
        expect(documentLanguage()).toBe('en');
    });

    it('is re-applied, not re-read: nothing here restores a locale from storage', () => {
        // A stored value is `cache.ts`'s pre-paint hint, and the runtime never
        // consults it — the server's extension list is the only answer.
        globalThis.localStorage?.setItem('kanban-i18n-locale', '<img src=x>');
        expect(activeLocale()).toBeUndefined();
        expect(documentLanguage()).toBe('en');
        globalThis.localStorage?.removeItem('kanban-i18n-locale');
    });
});

describe('onLocaleChange()', () => {
    it('fires with the new locale, only on an actual change, until unsubscribed', () => {
        const seen: Array<string | undefined> = [];
        const off = onLocaleChange(locale => seen.push(locale));
        setLocale('ja');
        setLocale('ja'); // no change, no event
        setLocale(null);
        off();
        setLocale('ko'); // after unsubscribe
        expect(seen).toEqual(['ja', undefined]);
    });

    it('fires when a pack arrives or goes away — that is the live switch', () => {
        const seen: Array<string | undefined> = [];
        const off = onLocaleChange(locale => seen.push(locale));
        i18n.setCatalog('ja', { 'time.justNow': '今' });
        i18n.setCatalog('ja', { 'time.justNow': '今' }); // same tag: a refresh, not a switch
        i18n.setCatalog('ja', null);
        off();
        expect(seen).toEqual(['ja', undefined]);
    });

    it('keeps firing for the remaining listeners when one throws or unsubscribes', () => {
        const error = vi.spyOn(console, 'error').mockImplementation(() => {});
        const kept: string[] = [];
        const off = onLocaleChange(() => {
            throw new Error('a listener must not break the others');
        });
        const offKept = onLocaleChange(l => kept.push(l ?? 'none'));
        setLocale('de');
        off();
        offKept();
        setLocale('fr');
        expect(kept).toEqual(['de']); // the second change reached nobody — both unsubscribed
        expect(error).toHaveBeenCalledTimes(1);
        error.mockRestore();
    });
});
