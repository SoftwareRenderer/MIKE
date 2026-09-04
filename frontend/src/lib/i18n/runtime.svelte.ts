/**
 * =============================================================================
 * i18n runtime — THE SEAM
 * =============================================================================
 *
 * One job: answer "which locale is the UI in, and what does this key say in it?".
 *
 * **This file is the seam T5 owns.** It exists so formatting and typography have a
 * single accessor to read while language packs are still landing. When T5 wires the
 * enabled pack up, it replaces WHERE the locale comes from (`readOverride()` / the
 * `active` state) and adds catalogs to `./catalogs` — nothing here's API changes, and
 * no consumer may compute a locale of its own.
 *
 * The two accessors are deliberately different questions:
 *
 *   • `activeLocale()`  → what to hand `Intl.*`. `undefined` means "the user has not
 *     chosen a locale, use the browser's" — which is exactly what every call site did
 *     before i18n existed, so landing this changes nothing for a user with no pack.
 *   • `documentLanguage()` → what to write into `<html lang>`, i.e. the language of
 *     the STRINGS actually rendered: the active pack's, else `en` (the catalog of
 *     record). It is NOT the browser's language: without a pack the UI text is
 *     English whatever the browser speaks, and `<html lang>` drives `:lang()` CSS
 *     (see the CJK typography block in `app.css`) — claiming Japanese there would
 *     restyle English chrome.
 *
 * Client storage note: the boot value is read from `localStorage`, not from
 * `$lib/storage` (IndexedDB). It has to be applied synchronously, before the first
 * paint, or the first frame renders with the wrong font stack and the wrong `lang` —
 * the same reason the theme stylesheet key is read the same way.
 */
import { catalogs, DEFAULT_LOCALE } from './catalogs';
import type { Catalog, MessageKey } from './catalogs';

export type { Catalog, MessageKey };

/**
 * A message key: a known core key for autocomplete and typo-checking, or any string
 * so an installed pack can address its own namespaced keys.
 */
export type LocaleKey = MessageKey | (string & {});

export type Interp = Record<string, string | number>;

/** Dev/test override — also the seam's persistence until T5 stores the enabled pack. */
export const LOCALE_STORAGE_KEY = 'kanban-locale';

/** `zh-Hans`, `zh-Hant`, `ja`, `ko`, `fr-FR`… Anything looser is not a tag. */
const TAG_RE = /^[A-Za-z]{2,3}(-[A-Za-z0-9]{2,8})*$/;

type LocaleListener = (locale: string | undefined) => void;

function readOverride(): string | null {
    try {
        return normalizeTag(globalThis.localStorage?.getItem(LOCALE_STORAGE_KEY) ?? null);
    } catch {
        // Storage can throw (private mode, disabled cookies). "No override" is the
        // only safe reading — formatting must never depend on storage working.
        return null;
    }
}

function normalizeTag(tag: string | null | undefined): string | null {
    const trimmed = tag?.trim();
    if (!trimmed) return null;
    return TAG_RE.test(trimmed) ? trimmed : null;
}

/** `null` = no locale chosen (browser default for Intl, `en` for the rendered strings). */
let active = $state<string | null>(readOverride());

const listeners = new Set<LocaleListener>();

/** Catalog keys, lower-cased once: `Intl` tags are case-insensitive, objects are not. */
const normalizedCatalogs = new Map<string, Catalog>(
    Object.entries(catalogs).map(([tag, catalog]) => [tag.toLowerCase(), catalog])
);

/**
 * Install a catalog at runtime — **T5's entry point**, called when a language pack is
 * enabled (a pack is fetched from the registry long after this module was imported, so
 * the table cannot be snapshotted from `./catalogs` alone). Re-registering a tag
 * replaces it; that is how an update to a pack takes effect.
 *
 * Returns false for a malformed tag, so a bad manifest cannot install a catalog that
 * nothing can ever select.
 */
export function registerCatalog(tag: string, catalog: Catalog): boolean {
    const normalized = normalizeTag(tag);
    if (!normalized) return false;
    catalogs[normalized] = catalog;
    normalizedCatalogs.set(normalized.toLowerCase(), catalog);
    return true;
}

/**
 * The catalog in use. With no locale chosen this is the `en` catalog of record; with
 * a locale chosen it is that locale's own catalog narrowed by subtag
 * (`zh-Hans-CN` → `zh-Hans` → `zh`), or `undefined` when no pack is installed for it.
 *
 * The difference matters: `packText` deliberately does NOT cross that boundary, so a
 * partial pack falls through to `Intl` (which knows the chosen locale) rather than to
 * English.
 */
function catalogInUse(): Catalog | undefined {
    if (!active) return normalizedCatalogs.get(DEFAULT_LOCALE);
    const parts = active.toLowerCase().split('-');
    while (parts.length > 0) {
        const catalog = normalizedCatalogs.get(parts.join('-'));
        if (catalog) return catalog;
        parts.pop();
    }
    return undefined;
}

function interpolate(template: string, params?: Interp): string {
    if (!params) return template;
    return template.replace(/\{(\w+)\}/g, (whole, name: string) =>
        Object.prototype.hasOwnProperty.call(params, name) ? String(params[name]) : whole
    );
}

/**
 * The value of `key` in the catalog actually in use, or `undefined` when that catalog
 * does not ship it. For callers that have a better answer than English — the relative
 * time rungs hand the job to `Intl.RelativeTimeFormat` instead.
 */
export function packText(key: LocaleKey, params?: Interp): string | undefined {
    const value = catalogInUse()?.[key];
    return value === undefined ? undefined : interpolate(value, params);
}

/**
 * The value of `key`, falling back to the `en` catalog of record and then to the key
 * itself. It never throws and never returns `undefined`: an untranslated key must
 * degrade to something readable, not blank out a row.
 */
export function t(key: LocaleKey, params?: Interp): string {
    const localized = packText(key, params);
    if (localized !== undefined) return localized;
    const fallback = normalizedCatalogs.get(DEFAULT_LOCALE)?.[key];
    return fallback === undefined ? key : interpolate(fallback, params);
}

/** Whether a key resolves in the catalog in use (badges, "is this translated?" UI). */
export function hasKey(key: LocaleKey): boolean {
    return catalogInUse()?.[key] !== undefined;
}

/**
 * The locale to pass to `Intl.*` — `undefined` means "ask the browser", preserving
 * pre-i18n behaviour for users with no pack. THIS is the accessor formatting reads.
 */
export function activeLocale(): string | undefined {
    return active ?? undefined;
}

/** The concrete tag `Intl` will end up using (for debug output and probes). */
export function resolvedLocale(): string {
    if (active) return active;
    const browser = globalThis.navigator?.language;
    return normalizeTag(browser) ?? DEFAULT_LOCALE;
}

/** The tag describing the rendered strings — what `<html lang>` must say. */
export function documentLanguage(): string {
    return active ?? DEFAULT_LOCALE;
}

/**
 * Subscribe to locale changes. Reactive callers do NOT need this (they read
 * `activeLocale()`/`t()` and re-run on their own); it exists for imperative
 * consumers that hold cached state — a canvas redraw, a formatter memo, a
 * CodeMirror reconfigure. Returns the unsubscribe function.
 */
export function onLocaleChange(listener: LocaleListener): () => void {
    listeners.add(listener);
    return () => { listeners.delete(listener); };
}

function applyDocumentLanguage(): void {
    const root = globalThis.document?.documentElement;
    if (root) root.lang = documentLanguage();
}

/**
 * Choose the active locale. `null` clears the choice ("no pack — follow the
 * browser"), which restores pre-i18n behaviour everywhere.
 *
 * `persist: false` is for tests and for a preview that should not survive a reload.
 * Listeners fire only on an actual change, so a caller may call this on every render.
 */
export function setLocale(tag: string | null, { persist = true }: { persist?: boolean } = {}): void {
    // An explicit null clears the choice; a malformed tag does nothing at all, so a
    // bad value read from storage or a pack manifest cannot silently drop the user
    // out of their language.
    if (tag !== null && normalizeTag(tag) === null) return;
    const next = normalizeTag(tag);
    if (persist) {
        try {
            if (next) globalThis.localStorage?.setItem(LOCALE_STORAGE_KEY, next);
            else globalThis.localStorage?.removeItem(LOCALE_STORAGE_KEY);
        } catch {
            // Non-fatal: the locale applies for this session either way.
        }
    }
    if (next === active) {
        applyDocumentLanguage();
        return;
    }
    active = next;
    applyDocumentLanguage();
    // One listener throwing must not stop the others (or the locale switch itself).
    for (const listener of [...listeners]) {
        try {
            listener(active ?? undefined);
        } catch (err) {
            console.error('[i18n] locale change listener threw:', err);
        }
    }
}

/**
 * Apply the boot value. Called once from `main.ts` before `mount()`; `index.html`'s
 * `lang="en"` is the initial value only, and this is the only writer after that.
 */
export function initLocale(): void {
    active = readOverride();
    applyDocumentLanguage();
}
