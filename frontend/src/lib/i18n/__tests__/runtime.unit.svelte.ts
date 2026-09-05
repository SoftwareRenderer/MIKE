import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { render, cleanup } from '@testing-library/svelte';
import { tick } from 'svelte';
import {
    activeLocale,
    documentLanguage,
    hasKey,
    i18n,
    I18nRuntime,
    locale,
    onLocaleChange,
    packText,
    registerCatalog,
    resetToEnglish,
    resolvedLocale,
    setCatalog,
    setLocale,
    t,
} from '$lib/i18n/runtime.svelte';
import { en, DEFAULT_LOCALE, type PluralTable } from '$lib/i18n/en';
import type { Catalog } from '$lib/i18n/catalog';
import LocaleProbe from './LocaleProbe.svelte';

// The runtime is what the app reads — both as a component (`t`, `activeLocale`) and
// as a plain module (`Intl` formatting, a canvas redraw) — so these pin the contract
// on both halves: which catalog answers, what happens when none does, who owns
// <html lang>, and that the language only ever comes from the active pack.
//
// The last two describes are the reactive half: a `$derived` reader and a real
// rendered template, both watched across a live catalog swap. That is the behaviour
// a conversion batch cannot see and would silently break — enabling a pack must
// re-render every reader with no reload.
//
// Vitest gives each file its own module instance, so the catalogs registered here
// are local to this file.

/** Keys seeded into the module-level `en` catalog, removed again afterwards. */
const seededEn: string[] = [];

/** Locale fixtures named for the plural categories `Intl` answers with for them. */
const TWO_FORMS = 'de';
const MANY_FORMS = 'ru';

/** A plural table whose every form names its category, so a wrong form is unmissable. */
const MANY_FORM_TABLE: PluralTable = {
    one: '[one:{count}]',
    few: '[few:{count}]',
    many: '[many:{count}]',
    other: '[other:{count}]',
};

beforeEach(() => {
    document.documentElement.lang = DEFAULT_LOCALE;
});

afterEach(() => {
    // The singleton is shared by the whole file: leave it exactly as found, or a
    // later test reads a language an earlier test left switched on.
    i18n.setCatalog(DEFAULT_LOCALE, null);
    for (const k of seededEn) delete en[k];
    seededEn.length = 0;
    cleanup();
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

    it('selects a plural form through Intl.PluralRules, not count === 1', () => {
        runtime.setCatalog(MANY_FORMS, { 'i18n.test.files': MANY_FORM_TABLE });
        expect(runtime.t('i18n.test.files', { count: 1 })).toBe('[one:1]');
        expect(runtime.t('i18n.test.files', { count: 2 })).toBe('[few:2]');
        expect(runtime.t('i18n.test.files', { count: 5 })).toBe('[many:5]');
        expect(runtime.t('i18n.test.files', { count: 1.5 })).toBe('[other:1.5]');
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
        // (A core key, because `t()` is `MessageKey`-typed: that strictness is the
        // point, and it is what makes this assertion check the real call path.)
        i18n.setCatalog(TWO_FORMS, { 'explorer.shared.cancel': 'Abbrechen' });
        expect(t('explorer.shared.cancel')).toBe('Abbrechen');
        expect(activeLocale()).toBe(TWO_FORMS);
        i18n.setCatalog(TWO_FORMS, null);
        // No pack is not "English as a chosen locale": `undefined` is what every
        // `Intl.*` call site had before i18n existed, i.e. follow the browser.
        expect(activeLocale()).toBeUndefined();
    });

    it('warns for a key no catalog has even with no pack active', () => {
        // Silence here is the failure mode the ladder exists to prevent: an id in
        // neither `en` nor a pack is a bug (typed callers cannot produce one), and
        // a fallback nobody hears is a fallback that never gets fixed.
        const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
        expect(i18n.t('some.key.nobody.converted')).toBe('some.key.nobody.converted');
        expect(warn).toHaveBeenCalledTimes(1);
        expect(String(warn.mock.calls[0][0])).toContain('some.key.nobody.converted');
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
        // Reached through the pack-facing accessor: `t()` will not typecheck an id
        // `en.ts` does not have, which is the check that keeps 1100 keys honest.
        const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
        expect(i18n.t('time.does-not-exist')).toBe('time.does-not-exist');
        expect(i18n.t('')).toBe('');
        warn.mockRestore();
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

    it('renders a regional tag even though the server no longer installs one', () => {
        // A pack's declared locale is held to one bare subtag (`validateLocale` refuses
        // "zh-CN"), but a catalog that arrived before that rule — or a tag a test hands
        // the runtime — must still render. Refusing to parse a tag reads as "no pack",
        // which is a reader silently losing their language.
        i18n.setCatalog('zh-Hans-CN', { 'time.justNow': '刚刚' });
        expect(t('time.justNow')).toBe('刚刚');
        expect(activeLocale()).toBe('zh-Hans-CN');
        expect(documentLanguage()).toBe('zh-Hans-CN');
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

    it('adopts the tag the pre-paint script painted instead of stamping English back over it', async () => {
        // index.html sets <html lang> from the cached tag so the first frame is
        // not English. A module-scope sync of the fallback here would undo that
        // one frame later, so a reload would flash back through English between
        // two frames of the pack's own language.
        document.documentElement.lang = 'zh-Hans';
        try {
            vi.resetModules();
            const fresh = await import('$lib/i18n/runtime.svelte');
            expect(fresh.documentLanguage()).toBe('zh-Hans');
            expect(document.documentElement.lang).toBe('zh-Hans');
            // A tag with no catalog is not a translation: text stays English
            // until the loader supplies one, and the loader can still take the
            // tag back.
            expect(fresh.i18n.catalog).toBe(null);
            fresh.i18n.setCatalog(DEFAULT_LOCALE, null);
            expect(document.documentElement.lang).toBe('en');
        } finally {
            document.documentElement.lang = 'en';
            vi.resetModules();
        }
    });

    it('starts in English when the document carries the fallback tag', async () => {
        // The markup's own value is `en`, so a first run (or a cleared hint)
        // must not adopt anything.
        document.documentElement.lang = 'en';
        vi.resetModules();
        const fresh = await import('$lib/i18n/runtime.svelte');
        expect(fresh.activeLocale()).toBeUndefined();
        expect(fresh.documentLanguage()).toBe('en');
        vi.resetModules();
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

// Readers at module scope: each is a live view over the runtime's `$state`, read here
// outside any effect scope (lazily recomputed when its dependencies change). The
// rendered probe below is the stronger proof — this pair is the cheap one that names
// the mechanism when it breaks.
const derivedPlain = $derived(t('i18n.test.plain'));
const derivedFiles = $derived(t('i18n.test.files', { count: 3 }));

describe('a $derived reader', () => {
    let warn: ReturnType<typeof vi.spyOn>;

    beforeEach(() => {
        resetToEnglish();
        warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
    });
    afterEach(() => warn.mockRestore());

    it('changes when a pack is installed for the first time', () => {
        expect(derivedPlain).toBe('Plain message');
        setCatalog(TWO_FORMS, { 'i18n.test.plain': '[translated]' });
        expect(derivedPlain).toBe('[translated]');
    });

    it('changes when the active pack is replaced live — the loading task\'s swap', () => {
        setCatalog(TWO_FORMS, { 'i18n.test.plain': '[first]' });
        expect(derivedPlain).toBe('[first]');
        // Same locale, new messages: nothing uninstalls, nothing reloads.
        setCatalog(TWO_FORMS, { 'i18n.test.plain': '[second]' });
        expect(derivedPlain).toBe('[second]');
    });

    it('switches plural forms with the locale, not with the catalog it read last', () => {
        expect(derivedFiles).toBe('3 files');
        setCatalog(MANY_FORMS, { 'i18n.test.files': MANY_FORM_TABLE });
        expect(derivedFiles).toBe('[few:3]');
        // The next pack declares only `other`: the same reader degrades, it does not blank.
        setCatalog(TWO_FORMS, { 'i18n.test.files': { other: '[other:{count}]' } });
        expect(derivedFiles).toBe('[other:3]');
    });

    it('goes back to English when a pack is dropped, including for keys it never had', () => {
        setCatalog(TWO_FORMS, { 'i18n.test.plain': '[translated]' });
        expect(derivedPlain).toBe('[translated]');
        expect(derivedFiles).toBe('3 files');
        resetToEnglish();
        expect(derivedPlain).toBe('Plain message');
        expect(derivedFiles).toBe('3 files');
        expect(locale.code).toBe(DEFAULT_LOCALE);
    });
});

describe('a rendered template', () => {
    it('re-renders on a live catalog swap with no re-render and no reload', async () => {
        const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
        const { container } = render(LocaleProbe, { count: 2 });
        const probe = () => container.querySelector('.probe')?.textContent;

        expect(probe()).toBe('2 files');

        setCatalog(MANY_FORMS, { 'i18n.test.files': MANY_FORM_TABLE });
        await tick();
        expect(probe()).toBe('[few:2]');

        // A replacement pack for the same locale — the live swap again, same component tree.
        setCatalog(MANY_FORMS, { 'i18n.test.files': { other: '[swapped:{count}]' } });
        await tick();
        expect(probe()).toBe('[swapped:2]');

        resetToEnglish();
        await tick();
        expect(probe()).toBe('2 files');
        warn.mockRestore();
    });

    it('keeps showing English for a key the installed pack lacks', async () => {
        const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
        const { container } = render(LocaleProbe, { count: 2 });

        setCatalog(TWO_FORMS, { 'i18n.test.plain': '[not the key this probe reads]' });
        await tick();

        expect(container.querySelector('.probe')?.textContent).toBe('2 files');
        expect(document.documentElement.lang).toBe(TWO_FORMS);
        warn.mockRestore();
    });
});
