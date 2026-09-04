/**
 * The i18n runtime.
 *
 * `t(key, params?)` resolves a message for the active locale and interpolates **named**
 * `{placeholders}` — never `+` concatenation around a value, because concatenation freezes
 * English word order into the code. Plurals ride the same call: when the message is a plural
 * table, the form comes from `Intl.PluralRules` over `params.count` — never from a
 * `count === 1` comparison, which is wrong for most languages (and `Intl` is what says which
 * ones: some languages answer `one`/`other`, some `one`/`few`/`many`/`other`, and some only
 * ever `other`, so a catalog may legitimately ship just `other`).
 *
 * The locale and the installed catalogs live in `$state`, so `t()` called from a template
 * registers the dependency: installing, replacing or removing a pack re-renders every reader
 * with no reload and no `{#key}` wrapper. `setCatalog` is the whole loading surface — calling
 * it again for the active locale is the live swap.
 *
 * **English is always the fallback**, per key and never per catalog: a partially translated
 * pack degrades key by key, and a test run (which never installs a pack) always sees the
 * English copy its locators are anchored on.
 *
 * **The fallback ladder, and what each rung costs.**
 *
 * 1. the active pack's value for the key;
 * 2. the `en.ts` value — plus one development `console.warn` naming the locale and the key,
 *    so "the pack silently never translated this" cannot rot into a half-translated UI;
 * 3. the key itself — plus a louder warning, because a missing key in the catalog of record is
 *    a hole in `en.ts`. The key is printed rather than nothing, on purpose: a visible
 *    `settings.extensions.foo` in the UI is a bug report, and an empty string is a mystery
 *    (and the a11y test compares a control's accessible name against its rendered text, so
 *    both sides must resolve to the same thing whatever the catalog state — see README.md).
 *
 * Both warnings are deduped per (locale, key) so a hot render path warns once instead of on
 * every re-render, and a production build does not pay for them at all (`import.meta.env`).
 *
 * This module is also the **only** writer of `document.documentElement.lang`: the locale and
 * the `<html lang>` attribute cannot disagree, because nothing else is allowed to touch it.
 */
import { en, type MessageKey, type MessageParams, type MessageValue, type PluralTable } from './en';

/**
 * Fallbacks are a development signal. `import.meta.env.PROD` is a build-time constant, so a
 * production bundle folds these branches away, while `vite dev` and vitest keep them live —
 * the test suite asserts on the warning precisely so the fallback cannot go quiet.
 */
const DEV = !import.meta.env.PROD;

/** Fallbacks already reported, so a hot path warns once, not once per render. */
const reported = new Set<string>();

let currentLocale = $state('en');
/** Catalogs installed by translation packs, keyed by locale code. */
let catalogs: Record<string, Record<string, MessageValue>> = $state({});

/** The active locale. Read `.code` (reactive: it is backed by `$state`). */
export const locale = {
    get code(): string {
        return currentLocale;
    },
};

/** `document.documentElement.lang` follows the locale. Nothing else writes it. */
function applyDocumentLang(): void {
    if (typeof document === 'undefined') return;
    if (document.documentElement.lang !== currentLocale) {
        document.documentElement.lang = currentLocale;
    }
}

applyDocumentLang();

function warnOnce(id: string, message: string): void {
    if (!DEV || reported.has(id)) return;
    reported.add(id);
    console.warn(`[i18n] ${message}`);
}

const pluralRules = new Map<string, Intl.PluralRules>();
function rulesFor(code: string): Intl.PluralRules {
    let rules = pluralRules.get(code);
    if (!rules) {
        rules = new Intl.PluralRules(code);
        pluralRules.set(code, rules);
    }
    return rules;
}

/**
 * Install a translation pack and make it the active locale — the one export the pack loader
 * needs. Re-calling it for the locale already active replaces that pack live: every reader of
 * every message it has translated re-renders, and every message it has not still reads
 * English.
 */
export function setCatalog(code: string, messages: Record<string, MessageValue>): void {
    catalogs[code] = messages;
    currentLocale = code;
    applyDocumentLang();
}

/**
 * Drop back to the English catalog of record, and let the next fallback report itself again:
 * the dedupe set exists to keep a console readable, not to hide a hole that came back.
 */
export function resetToEnglish(): void {
    currentLocale = 'en';
    reported.clear();
    applyDocumentLang();
}

/** Resolve a key for the active locale, reporting each fallback the first time it is taken. */
function lookup(key: MessageKey): MessageValue | undefined {
    const english = en[key] as MessageValue | undefined;
    if (currentLocale === 'en') return english;

    const translated = catalogs[currentLocale]?.[key];
    if (translated !== undefined) return translated;

    if (english !== undefined) {
        warnOnce(`pack\u0000${currentLocale}\u0000${key}`,
            `pack "${currentLocale}" has no translation for "${key}" — rendered English. ` +
            `Translate the key or drop it from the pack; an untranslated key is not an error, an unreported one is.`);
        return english;
    }
    return undefined;
}

function choose(value: MessageValue, params?: MessageParams): string {
    if (typeof value === 'string') return value;
    const table = value as PluralTable;
    const count = params?.['count'];
    const category = typeof count === 'number' ? rulesFor(currentLocale).select(count) : 'other';
    return table[category] ?? table.other;
}

function interpolate(text: string, params?: MessageParams): string {
    if (!params) return text;
    return text.replace(/\{(\w+)\}/g, (whole, name: string) =>
        Object.prototype.hasOwnProperty.call(params, name) && params[name] !== undefined
            ? String(params[name])
            : whole,
    );
}

/**
 * Translate `key`, interpolating named `{params}`; `params.count` additionally selects a
 * plural form. A key the packs and `en.ts` all lack is a bug in the caller (it does not
 * typecheck), and it answers with the key and a warning rather than with silence.
 */
export function t(key: MessageKey, params?: MessageParams): string {
    const value = lookup(key);
    if (value === undefined) {
        warnOnce(`en\u0000${key}`,
            `unknown message key "${key}" — it is not in the English catalog (en.ts), ` +
            `so nothing can translate it. Rendering the key id so the hole is visible.`);
        return key;
    }
    return interpolate(choose(value, params), params);
}
