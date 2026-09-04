/**
 * The i18n runtime (T5).
 *
 * One reactive read, one function: `t(key, params?)`. `activeLocale` and the catalog table are `$state`, so
 * `t` re-runs wherever it is read — inline in markup, or behind `$derived` for a script-side value:
 *
 *     <span>{t('sandbox.isolation')}</span>
 *     const label = $derived(t('sandbox.mounts.count', { count: mounts.length }));
 *
 * Rules that must not break:
 * - **English only from `en.ts`.** A value in the catalog is byte-identical to the string it replaced
 *   (aria-label/title/placeholder included), so switching back to English restores the old UI exactly.
 * - **Named interpolation only** (`{name}`), and plurals come from `params.count` through
 *   `Intl.PluralRules` — never `n === 1 ? '' : 's'` at a call site.
 * - **Server-returned text is never passed through `t`.** It arrives already written in the user's
 *   language or in none; wrapping it produces a mixed-language message.
 * - A missing key answers with the key itself. That is a bug in the catalog, and it must be visible
 *   rather than blank.
 */
import { enCatalog, type Catalog, type MessageKey, type PluralCategories } from './en';

/** `t` accepts scalars for named placeholders; objects/arrays would stringify to `[object Object]`. */
export type MessageParams = Record<string, string | number>;

const EN = 'en';

let activeLocale = $state<string>(EN);
const catalogs = $state<Record<string, Catalog>>({ [EN]: enCatalog });

/** PluralRules is per-locale and construction is not free; one instance per locale seen. */
const pluralRules = new Map<string, Intl.PluralRules>();

function rulesFor(loc: string): Intl.PluralRules {
    let rules = pluralRules.get(loc);
    if (!rules) {
        // A tag the runtime doesn't know (or a bare language subtag with no data) still has to select.
        try {
            rules = new Intl.PluralRules(loc, { type: 'cardinal' });
        } catch {
            rules = new Intl.PluralRules(EN, { type: 'cardinal' });
        }
        pluralRules.set(loc, rules);
    }
    return rules;
}

/** Replace every `{name}` with `params.name`. An unknown placeholder is left as written — a typo in the
 * catalog stays visible instead of silently swallowing the value around it. */
function interpolate(text: string, params?: MessageParams): string {
    if (!params || text.indexOf('{') === -1) return text;
    return text.replace(/\{([A-Za-z_][A-Za-z0-9_]*)\}/g, (whole, name: string) =>
        Object.prototype.hasOwnProperty.call(params, name) ? String(params[name]) : whole,
    );
}

function pickForm(value: PluralCategories, params?: MessageParams): string {
    const count = Number(params?.count ?? 0);
    const n = Number.isFinite(count) ? count : 0;
    return value[rulesFor(activeLocale).select(n) as keyof PluralCategories] ?? value.other;
}

/**
 * Translate `key`, substituting named placeholders. Reactive: reading it inside a component, `$derived` or
 * `$effect` re-runs it when the locale or a catalog changes.
 *
 * Plural catalogs (`{ one, other }`) select on `params.count`; a plain string ignores `count`.
 */
export function t(key: MessageKey, params?: MessageParams): string {
    // Both reads are `$state`, and both belong here: a locale switch and a late `setCatalog` must
    // each re-run the caller.
    const catalog = catalogs[activeLocale];
    const value = catalog?.[key] ?? catalogs[EN]?.[key];
    if (value === undefined) return key;
    return interpolate(typeof value === 'string' ? value : pickForm(value, params), params);
}

/** The locale `t` is currently reading. Reactive. */
export function locale(): string {
    return activeLocale;
}

/** Install (or replace) one locale's catalog and switch to it. */
export function setCatalog(loc: string, catalog: Catalog): void {
    catalogs[loc] = catalog;
    activeLocale = loc;
}

/** Back to the catalog of record. Loaded translations stay installed, so a switch back is free. */
export function resetToEnglish(): void {
    activeLocale = EN;
}
