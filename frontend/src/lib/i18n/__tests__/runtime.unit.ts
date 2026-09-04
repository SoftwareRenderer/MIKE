import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { t, locale, setCatalog, resetToEnglish } from '$lib/i18n';
import type { MessageKey, MessageValue, PluralCategory, PluralTable } from '$lib/i18n';

/**
 * The runtime's contract on its pure resolution surface — no compiler macros in this file, so
 * it stays a plain `.unit.ts` (importing the runes module is fine; using runes is not, and
 * `runtime.unit.svelte.ts` is where those live).
 *
 * Everything here is deterministic: no network, no timers, no component. The behaviours under
 * test are the ones a conversion batch cannot see and will therefore silently break — the
 * fallback ladder and its warnings, plural selection through `Intl`, and the `<html lang>`
 * attribute the runtime owns.
 */

/**
 * Locale fixtures. Plural rules can only be exercised by a locale that actually has them, so
 * these are fixtures named for the CATEGORIES they answer with, not features: `t()` never
 * branches on a locale code, it hands the code to `Intl` and believes the answer.
 * `SINGLE_FORM` is here because it answers `other` for every count — the shape the East Asian
 * languages have — and it is provably one on this ICU (checked before writing this file).
 */
const TWO_FORMS = 'de';        // one | other
const MANY_FORMS = 'ru';       // one | few | many | other
const SIX_FORMS = 'ar';        // zero | one | two | few | many | other
const SINGLE_FORM = 'id';      // other, always

/** A pack fixture, typed by key: a typo in a fixture key is a TS error, not a silent hole. */
function pack(entries: { [K in MessageKey]?: MessageValue }): Record<string, MessageValue> {
    const out: Record<string, MessageValue> = {};
    for (const [key, value] of Object.entries(entries)) {
        if (value !== undefined) out[key] = value;
    }
    return out;
}

/**
 * A plural table whose every form says which category it is (`[few:{count}]`), so a form
 * selected off-by-one is unmissable in the assertion — and the repository still contains no
 * translated string, which is the rule for `frontend/src/`.
 */
function forms(...categories: PluralCategory[]): PluralTable {
    const table: Partial<Record<PluralCategory, string>> = { other: '[other:{count}]' };
    for (const category of categories) table[category] = `[${category}:{count}]`;
    return table as PluralTable;
}

let warn: ReturnType<typeof vi.spyOn>;

beforeEach(() => {
    // resetToEnglish() also re-arms the dedupe, so each test starts from English and each
    // fallback is reportable again.
    resetToEnglish();
    warn = vi.spyOn(console, 'warn').mockImplementation(() => { });
});

afterEach(() => {
    warn.mockRestore();
    resetToEnglish();
});

describe('interpolation', () => {
    it('fills a named placeholder from the catalog value', () => {
        expect(t('explorer.shared.terminalTitle', { shortcut: 'Ctrl+`' }))
            .toBe('Open terminal (Ctrl+`)');
    });

    it('keeps word order in the catalog, not in the code', () => {
        // Three placeholders in one English sentence. The caller passes facts — it cannot
        // express "rename X to Y in Z" as fragments, which is the whole point of named params.
        expect(t('chat.args.lspRenameTo', { sym: 'parse', newName: 'parseFile', path: 'src/a.ts' }))
            .toBe('rename parse to parseFile in src/a.ts');

        // The same three facts in a different insertion order produce the same string: the
        // order comes from the message, so a pack is free to reorder them.
        expect(t('chat.args.lspRenameTo', { path: 'src/a.ts', sym: 'parse', newName: 'parseFile' }))
            .toBe('rename parse to parseFile in src/a.ts');
    });

    it('leaves a placeholder with no value visible instead of silently blanking it', () => {
        expect(t('chat.args.lspRenameTo', { sym: 'parse' }))
            .toBe('rename parse to {newName} in {path}');
    });

    it('substitutes once — a value cannot inject another placeholder', () => {
        expect(t('chat.args.lspRenameTo', { sym: '{newName}', newName: 'X', path: 'p' }))
            .toBe('rename {newName} to X in p');
    });

    it('renders numbers and zero as text, and a paramless message untouched', () => {
        expect(t('i18n.test.plain')).toBe('Plain message');
        expect(t('i18n.test.files', { count: 0 })).toBe('0 files');
    });
});

describe('plural selection', () => {
    it('English selects one/other through Intl.PluralRules, not count === 1', () => {
        expect(t('i18n.test.files', { count: 1 })).toBe('1 file');
        expect(t('i18n.test.files', { count: 2 })).toBe('2 files');
        // A fraction is `other` in English; `count === 1` would have made it singular too.
        expect(t('i18n.test.files', { count: 1.5 })).toBe('1.5 files');
    });

    it('a locale with one/few/many/other gets all of them', () => {
        setCatalog(MANY_FORMS, pack({ 'i18n.test.files': forms('one', 'few', 'many') }));
        expect(t('i18n.test.files', { count: 1 })).toBe('[one:1]');
        expect(t('i18n.test.files', { count: 2 })).toBe('[few:2]');
        expect(t('i18n.test.files', { count: 5 })).toBe('[many:5]');
        expect(t('i18n.test.files', { count: 21 })).toBe('[one:21]');
        expect(t('i18n.test.files', { count: 1.5 })).toBe('[other:1.5]');
    });

    it('a locale with six categories reaches zero and two as well', () => {
        setCatalog(SIX_FORMS, pack({
            'i18n.test.files': forms('zero', 'one', 'two', 'few', 'many'),
        }));
        expect(t('i18n.test.files', { count: 0 })).toBe('[zero:0]');
        expect(t('i18n.test.files', { count: 2 })).toBe('[two:2]');
        expect(t('i18n.test.files', { count: 3 })).toBe('[few:3]');
        expect(t('i18n.test.files', { count: 11 })).toBe('[many:11]');
        expect(t('i18n.test.files', { count: 100 })).toBe('[other:100]');
    });

    it('a single-form locale is served by a catalog that supplies only `other`', () => {
        setCatalog(SINGLE_FORM, pack({ 'i18n.test.files': { other: '[other:{count}]' } }));
        for (const count of [0, 1, 2, 7, 100, 1.5]) {
            expect(t('i18n.test.files', { count })).toBe(`[other:${count}]`);
        }
    });

    it('a plural table missing the selected category degrades to `other`, never to blank', () => {
        // Legal pack: it ships only `other`. Counts that the locale would call few/many must
        // still render something.
        setCatalog(MANY_FORMS, pack({ 'i18n.test.files': { other: '[other:{count}]' } }));
        for (const count of [1, 2, 5]) {
            expect(t('i18n.test.files', { count })).toBe(`[other:${count}]`);
        }
    });

    it('a plural message asked without a count renders `other`', () => {
        setCatalog(TWO_FORMS, pack({ 'i18n.test.files': forms('one') }));
        expect(t('i18n.test.files')).toBe('[other:{count}]');
    });
});

describe('the fallback ladder and its warnings', () => {
    it('a key the pack has not translated renders English and warns once', () => {
        setCatalog(TWO_FORMS, pack({}));

        expect(t('i18n.test.plain')).toBe('Plain message');
        expect(warn).toHaveBeenCalledTimes(1);

        const reported = String(warn.mock.calls[0][0]);
        expect(reported).toContain(TWO_FORMS);
        expect(reported).toContain('i18n.test.plain');
        expect(reported).toContain('English');
    });

    it('repeated lookups of the same hole warn once, so a hot render path stays readable', () => {
        setCatalog(TWO_FORMS, pack({}));

        t('i18n.test.plain');
        t('i18n.test.plain');
        t('i18n.test.plain');
        expect(warn).toHaveBeenCalledTimes(1);

        // A different hole is a different warning.
        t('i18n.test.files', { count: 1 });
        expect(warn).toHaveBeenCalledTimes(2);
    });

    it('resetToEnglish() re-arms the report, so a hole that comes back is reported again', () => {
        setCatalog(TWO_FORMS, pack({}));
        t('i18n.test.plain');
        expect(warn).toHaveBeenCalledTimes(1);

        resetToEnglish();
        setCatalog(TWO_FORMS, pack({}));
        t('i18n.test.plain');
        expect(warn).toHaveBeenCalledTimes(2);
    });

    it('a key no catalog has at all warns and answers with the key, never with empty', () => {
        // @ts-expect-error the key deliberately does not exist in en.ts
        const rendered = t('does.not.exist');

        expect(rendered).toBe('does.not.exist');
        expect(rendered.length).toBeGreaterThan(0);
        expect(warn).toHaveBeenCalledTimes(1);
        expect(String(warn.mock.calls[0][0])).toContain('does.not.exist');
    });

    it('no rung of the ladder can render an empty string', () => {
        const rendered = [
            t('i18n.test.plain'),                    // English
            (setCatalog(MANY_FORMS, pack({})), t('i18n.test.plain')),  // pack miss → English
            t('i18n.test.files', { count: 3 }),      // plural table
            // @ts-expect-error unknown key
            t('still.not.a.key'),
        ];
        for (const text of rendered) {
            expect(text.length).toBeGreaterThan(0);
        }
    });
});

describe('document.documentElement.lang', () => {
    it('starts at English without a pack installed', () => {
        expect(document.documentElement.lang).toBe('en');
        expect(locale.code).toBe('en');
    });

    it('follows the active locale on every switch, and back to en on reset', () => {
        setCatalog(MANY_FORMS, pack({}));
        expect(document.documentElement.lang).toBe(MANY_FORMS);

        setCatalog(SINGLE_FORM, pack({}));
        expect(document.documentElement.lang).toBe(SINGLE_FORM);

        resetToEnglish();
        expect(document.documentElement.lang).toBe('en');
    });

    it('can never disagree with the locale reader — one owner writes both', () => {
        for (const code of [TWO_FORMS, SIX_FORMS, SINGLE_FORM]) {
            setCatalog(code, pack({}));
            expect(document.documentElement.lang).toBe(locale.code);
        }
        resetToEnglish();
        expect(document.documentElement.lang).toBe(locale.code);
    });

    it('tracks the locale even when the pack translates nothing (that is a pack bug, reported per key)', () => {
        setCatalog(TWO_FORMS, pack({}));
        expect(document.documentElement.lang).toBe(TWO_FORMS);
        expect(t('i18n.test.plain')).toBe('Plain message');
    });
});
