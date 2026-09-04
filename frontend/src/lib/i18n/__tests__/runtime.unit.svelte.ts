import { describe, it, expect, afterEach, vi } from 'vitest';
import {
    activeLocale,
    documentLanguage,
    hasKey,
    initLocale,
    LOCALE_STORAGE_KEY,
    onLocaleChange,
    packText,
    registerCatalog,
    resolvedLocale,
    setLocale,
    t,
} from '$lib/i18n/runtime.svelte';

// The runtime is the seam every localized thing in the app reads, so what these pin
// is the contract: which catalog answers, what happens when none does, and who owns
// <html lang>. Vitest gives each file its own module instance, so the catalogs
// registered here are local to this file.

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
    afterEach(() => setLocale(null));

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
});

describe('catalog lookup', () => {
    afterEach(() => setLocale(null));

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
    afterEach(() => {
        setLocale(null);
        globalThis.localStorage?.removeItem(LOCALE_STORAGE_KEY);
    });

    it('is written by this runtime and nobody else', () => {
        setLocale('zh-Hant');
        expect(document.documentElement.lang).toBe('zh-Hant');
        setLocale(null);
        expect(document.documentElement.lang).toBe('en');
    });

    it('survives a reload through the stored override, which initLocale() re-applies', () => {
        // This is the whole flow the CJK probe exercises: set → reload → boot applies.
        setLocale('ja');
        expect(globalThis.localStorage?.getItem(LOCALE_STORAGE_KEY)).toBe('ja');
        document.documentElement.lang = 'en'; // simulate a fresh document
        initLocale();
        expect(document.documentElement.lang).toBe('ja');
        expect(activeLocale()).toBe('ja');
    });

    it('ignores a stored value that is not a tag', () => {
        globalThis.localStorage?.setItem(LOCALE_STORAGE_KEY, '<img src=x>');
        initLocale();
        expect(activeLocale()).toBeUndefined();
        expect(document.documentElement.lang).toBe('en');
    });

    it('can be chosen without persisting, for a preview that should not survive a reload', () => {
        setLocale('ko', { persist: false });
        expect(activeLocale()).toBe('ko');
        expect(globalThis.localStorage?.getItem(LOCALE_STORAGE_KEY)).toBeNull();
    });
});

describe('onLocaleChange()', () => {
    afterEach(() => setLocale(null));

    it('fires with the new locale, only on an actual change, until unsubscribed', () => {
        const seen: Array<string | undefined> = [];
        const off = onLocaleChange(locale => seen.push(locale));
        setLocale('ja');
        setLocale('ja');   // no change, no event
        setLocale(null);
        off();
        setLocale('ko');   // after unsubscribe
        expect(seen).toEqual(['ja', undefined]);
    });

    it('keeps firing for the remaining listeners when one unsubscribes', () => {
        const error = vi.spyOn(console, 'error').mockImplementation(() => {});
        const kept: string[] = [];
        const off = onLocaleChange(() => { throw new Error('a listener must not break the others'); });
        const offKept = onLocaleChange(l => kept.push(l ?? 'none'));
        setLocale('de');
        off();
        offKept();
        setLocale('fr');
        expect(kept).toEqual(['de']);   // the second change reached nobody — both unsubscribed
        expect(error).toHaveBeenCalledTimes(1);
        error.mockRestore();
    });
});
