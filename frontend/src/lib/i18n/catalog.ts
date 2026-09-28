/**
 * Catalog shape and parsing — the boundary between "a file a third party shipped" and "a
 * translation map the UI may render". A catalog is flat `key -> string` or a plural table
 * (`{ one, other, ... }`, `other` required because it is the only category every language
 * has). Nothing here touches the DOM or Svelte.
 *
 * Every rejection is reported with a reason rather than thrown: a pack that cannot be
 * understood leaves the UI in English and says so; a half-applied catalog is worse.
 */
import type { MessageValue, PluralCategory, PluralTable } from './en';

/** The categories a pack may declare, so a typo (`onet`) is refused, not ignored. */
const PLURAL_CATEGORIES: readonly PluralCategory[] = ['zero', 'one', 'two', 'few', 'many', 'other'];

/** A translation catalog: message id → translated text, or a plural table. */
export type Catalog = Record<string, MessageValue>;

/**
 * Ceilings on a catalog a pack may hand us. They are sanity bounds, not a
 * denial-of-service fight — the file is same-origin and served by our own
 * static tree, but a pack that ships a 200 MB catalog or one giant value is
 * broken, and rendering that into every string lookup is not a thing to attempt.
 */
const MAX_CATALOG_BYTES = 1_000_000;
/** @knipignore Its test builds one key more than this. */
export const MAX_CATALOG_KEYS = 20_000;
const MAX_KEY_LEN = 200;
/** @knipignore Its test writes one character more than this. */
export const MAX_VALUE_LEN = 4000;

export type CatalogParse =
    | { ok: true; catalog: Catalog }
    | { ok: false; reason: string };

/**
 * Parse a fetched catalog body. A syntax error is a rejection like any other —
 * `JSON.parse` throwing must not escape into the caller's extension load.
 */
export function parseCatalogJSON(text: string): CatalogParse {
    // Byte bound first: a pathological body should not even be parsed.
    const bytes = new TextEncoder().encode(text).length;
    if (bytes > MAX_CATALOG_BYTES) {
        return { ok: false, reason: `catalog is ${bytes} bytes, over the ${MAX_CATALOG_BYTES}-byte limit` };
    }
    let raw: unknown;
    try {
        raw = JSON.parse(text);
    } catch (e) {
        return { ok: false, reason: `not valid JSON: ${e instanceof Error ? e.message : String(e)}` };
    }
    return parseCatalog(raw);
}

/**
 * Validate an already-decoded value as a catalog.
 *
 * Strict on purpose: an array, a number, an object of objects that is not a plural
 * table, or a table missing its `other` form is a pack that does not match the
 * contract, and the answer to that is English-with-a-reason, not a UI rendering
 * `[object Object]`.
 */
export function parseCatalog(raw: unknown): CatalogParse {
    if (raw === null || typeof raw !== 'object' || Array.isArray(raw)) {
        return { ok: false, reason: 'a catalog must be a JSON object of key/value strings' };
    }
    const entries = Object.entries(raw as Record<string, unknown>);
    if (entries.length > MAX_CATALOG_KEYS) {
        return { ok: false, reason: `catalog declares ${entries.length} keys, over the ${MAX_CATALOG_KEYS}-key limit` };
    }
    const catalog: Catalog = {};
    for (const [key, value] of entries) {
        // Prototype-pollution keys are refused outright rather than skipped: a
        // pack reaching for `__proto__` is not a pack with a typo, it is a pack
        // doing something else, and "stay English and report it" is the answer.
        if (key === '__proto__' || key === 'constructor' || key === 'prototype') {
            return { ok: false, reason: `catalog declares the reserved key ${JSON.stringify(key)}` };
        }
        if (key === '' || key.length > MAX_KEY_LEN) {
            return {
                ok: false,
                reason: `catalog key ${JSON.stringify(key.slice(0, 40))} is empty or longer than ${MAX_KEY_LEN} characters`,
            };
        }
        if (typeof value === 'string') {
            if (value.length > MAX_VALUE_LEN) {
                return {
                    ok: false,
                    reason: `catalog key ${JSON.stringify(key)} is longer than ${MAX_VALUE_LEN} characters`,
                };
            }
            catalog[key] = value;
            continue;
        }
        const table = parsePluralTable(value);
        if (typeof table === 'string') {
            // A reason, never a throw: `value` is pack-authored JSON.
            return {
                ok: false,
                reason: `catalog key ${JSON.stringify(key)} is not a string and not a plural table (${table})`,
            };
        }
        catalog[key] = table;
    }
    return { ok: true, catalog };
}

/**
 * A plural table, or the reason this value is not one. Kept to the same shape the
 * runtime resolves: at least `other`, every key a real `Intl.PluralRules` category,
 * every form a bounded string. The reason reads as a fragment — the caller names the
 * key and says what was expected.
 */
function parsePluralTable(value: unknown): PluralTable | string {
    if (value === null || typeof value !== 'object' || Array.isArray(value)) {
        return `it is ${value === null ? 'null' : typeof value}`;
    }
    const entries = Object.entries(value as Record<string, unknown>);
    if (entries.length === 0) return 'an empty table can never render';
    const table: Partial<Record<PluralCategory, string>> = {};
    for (const [category, form] of entries) {
        if (!PLURAL_CATEGORIES.includes(category as PluralCategory)) {
            return `declares the plural category ${JSON.stringify(category)}, which Intl cannot answer with`;
        }
        if (typeof form !== 'string') {
            return `has a plural form ${JSON.stringify(category)} that is not a string`;
        }
        if (form.length > MAX_VALUE_LEN) {
            return `has a plural form ${JSON.stringify(category)} longer than ${MAX_VALUE_LEN} characters`;
        }
        table[category as PluralCategory] = form;
    }
    if (table.other === undefined) return 'is missing the required "other" form';
    return table as PluralTable;
}
