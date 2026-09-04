/**
 * The i18n runtime (T5 surface, minimum viable — see TODO.md).
 *
 * `t(key, params?)` resolves a message for the current locale and interpolates
 * **named** `{placeholders}`. Plurals ride the same call: when the message is a plural
 * table, the form comes from `Intl.PluralRules` over `params.count` — never from a
 * `count === 1` comparison, which is wrong for every language but English.
 *
 * Locale lives in a `$state`, so `t()` called from a template registers the dependency
 * and the component re-renders on a locale change without any `{#key}` wrapper.
 *
 * **English is always the fallback** — a missing key in a partially translated pack
 * degrades to English, and a test run (which never installs a pack) always sees the
 * English copy its locators are anchored on. A missing key in the English catalog
 * itself answers with the key, because a visible `settings.extensions.foo` in the UI
 * is a bug report, and an empty string is a mystery.
 */
import { en, type MessageKey, type MessageParams, type MessageValue, type PluralTable } from './en';

let currentLocale = $state('en');
/** Catalogs installed by translation packs, keyed by locale code. */
let catalogs: Record<string, Record<string, MessageValue>> = $state({});

/** The active locale. Read `.code` (reactive: it is backed by `$state`). */
export const locale = {
    get code(): string {
        return currentLocale;
    },
};

const pluralRules = new Map<string, Intl.PluralRules>();
function rulesFor(code: string): Intl.PluralRules {
    let rules = pluralRules.get(code);
    if (!rules) {
        rules = new Intl.PluralRules(code);
        pluralRules.set(code, rules);
    }
    return rules;
}

/** Install a translation pack and switch to it. */
export function setCatalog(code: string, messages: Record<string, MessageValue>): void {
    catalogs[code] = messages;
    currentLocale = code;
}

/** Drop back to the English catalog of record. */
export function resetToEnglish(): void {
    currentLocale = 'en';
}

function lookup(key: MessageKey): MessageValue | undefined {
    if (currentLocale !== 'en') {
        const translated = catalogs[currentLocale]?.[key];
        if (translated !== undefined) return translated;
    }
    return en[key] as MessageValue | undefined;
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

/** Translate `key`, interpolating named `{params}`; `params.count` selects a plural form. */
export function t(key: MessageKey, params?: MessageParams): string {
    const value = lookup(key);
    if (value === undefined) return key;
    return interpolate(choose(value, params), params);
}
