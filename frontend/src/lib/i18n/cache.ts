import { loadAppSetting, saveAppSetting, deleteAppSetting } from '$lib/storage';
import { parseCatalog, type Catalog } from './catalog';

/**
 * The last catalog the server's extension list actually produced, kept so the
 * next boot can render in the right language before the round-trip finishes.
 *
 * It is a *cache of the last resolved answer*, never a setting:
 *  - it is written only after a catalog was fetched and validated, and
 *  - it is deleted whenever the live extension list resolves to English (no
 *    pack, disabled, uninstalled, or a pack that failed to load).
 *
 * That last rule is what stops a cache from outranking reality: an uninstalled
 * pack cannot leave the app speaking a language with no pack behind it, because
 * the day it stopped being the winner is the day its entry was deleted.
 */

/** Key in the `app-settings` store. */
const CACHE_KEY = 'i18n-catalog';

/**
 * Locale mirrored into `localStorage` for the pre-paint script in
 * `frontend/index.html`.
 *
 * This is the one deliberate exception to "all client-side storage goes through
 * `$lib/storage`": IndexedDB is asynchronous, and the whole point of the
 * pre-paint step is to have `<html lang>` correct before the first frame. The
 * `kanban-theme-css` key already does exactly this for a theme stylesheet
 * (localStorage for the synchronous URL, IndexedDB for everything else), and
 * what is stored here is the same size of thing — one short tag, no text. The
 * catalog itself is IndexedDB-only.
 */
export const LOCALE_HINT_KEY = 'kanban-i18n-locale';

/** What a cache entry has to say about itself, so a stale entry is detectable. */
export interface CachedCatalog {
    /** `author/name` of the pack this came from. */
    source: string;
    locale: string;
    messages: Catalog;
}

/** Read and re-validate the cache. Anything unexpected is dropped, not trusted. */
export async function readCachedCatalog(): Promise<CachedCatalog | null> {
    let entry: CachedCatalog | null = null;
    try {
        entry = await loadAppSetting<CachedCatalog | null>(CACHE_KEY, null);
    } catch (e) {
        // IndexedDB unavailable (private mode, quota, first-run race). English is
        // the answer, and it is not worth an error banner over a flash.
        console.warn('[i18n] catalog cache unreadable; starting in English:', e);
        return null;
    }
    if (!entry || typeof entry !== 'object') return null;
    const { locale, messages, source } = entry;
    if (typeof locale !== 'string' || !locale || typeof source !== 'string' || !source) {
        void clearCachedCatalog();
        return null;
    }
    // Re-validate on the way out: the shape rules live in one place
    // (`parseCatalog`), so a cache written by an older build cannot smuggle
    // something past the runtime.
    const parsed = parseCatalog(messages);
    if (!parsed.ok) {
        console.warn(`[i18n] cached catalog from ${source} is not usable (${parsed.reason}); starting in English`);
        void clearCachedCatalog();
        return null;
    }
    return { source, locale, messages: parsed.catalog };
}

/** Store a catalog that was actually applied. */
export async function writeCachedCatalog(entry: CachedCatalog): Promise<void> {
    try {
        await saveAppSetting(CACHE_KEY, entry);
    } catch (e) {
        // A cache that will not write costs a language flash on next boot, not a
        // broken UI. The language in use right now is unaffected.
        console.warn('[i18n] catalog cache not saved:', e);
    }
    setLocaleHint(entry.locale);
}

/** Drop the cache — the state after "no pack is active" and after any failure. */
export async function clearCachedCatalog(): Promise<void> {
    try {
        await deleteAppSetting(CACHE_KEY);
    } catch (e) {
        console.warn('[i18n] catalog cache not cleared:', e);
    }
    setLocaleHint(null);
}

/** The synchronous hint `index.html` reads. Cleared with the cache, never alone. */
export function setLocaleHint(locale: string | null): void {
    try {
        if (locale) window.localStorage.setItem(LOCALE_HINT_KEY, locale);
        else window.localStorage.removeItem(LOCALE_HINT_KEY);
    } catch {
        // Storage disabled: the pre-paint step is skipped and the server's answer
        // still arrives a moment later. Nothing else depends on this.
    }
}
