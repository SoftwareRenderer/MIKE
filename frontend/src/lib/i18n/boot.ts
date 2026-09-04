import { readCachedCatalog, setLocaleHint, clearCachedCatalog } from './cache';
import { DEFAULT_LOCALE } from './en';
import { i18n } from './runtime.svelte';

/**
 * Applying the last resolved catalog before the first frame.
 *
 * The language comes from the server's extension list, which is a round-trip
 * away, so on a cold boot the UI would otherwise paint English and then switch
 * mid-view. This closes that gap the same way `kanban-theme-css` closes it for a
 * theme stylesheet: apply what was true last time, immediately, and let the live
 * extension list correct it a moment later.
 *
 * The cache is never the answer — `loadI18NFromExtensions` is. Two things keep
 * that honest:
 *  - the entry is deleted the moment the live list resolves to English, so a
 *    pack that is disabled or uninstalled leaves nothing to apply, and
 *  - whatever is applied here is replaced by the first `loadExtensions()`,
 *    including being replaced by English.
 */

/**
 * How long to wait for IndexedDB before mounting anyway.
 *
 * Opening a database is normally single-digit milliseconds, but it is not
 * guaranteed to answer at all: another tab holding a version-change lock, or a
 * browser that refuses the store. A language flash is a cosmetic failure; a
 * blank screen because a boot step is awaiting storage is not, so the pre-paint
 * step gives up rather than block.
 */
const PREPAINT_DEADLINE_MS = 250;

/**
 * Apply the cached catalog to the runtime. Returns whether a pack was applied —
 * `false` meaning "start in English", which is also the answer for every failure
 * here. Never throws: nothing on the boot path may depend on a cache.
 */
export async function applyCachedCatalog(): Promise<boolean> {
    let cached;
    try {
        cached = await Promise.race([
            readCachedCatalog(),
            new Promise<null>((resolve) => setTimeout(() => resolve(null), PREPAINT_DEADLINE_MS)),
        ]);
    } catch (e) {
        console.warn('[i18n] catalog cache unavailable; starting in English:', e);
        return false;
    }
    if (!cached) return false;

    // The entry is only a hint about the *last* answer. If it names the fallback
    // locale it is not a pack at all (a stale write), so drop it here rather than
    // mount with a language that nothing backs.
    if (cached.locale === DEFAULT_LOCALE) {
        void clearCachedCatalog();
        return false;
    }

    i18n.setCatalog(cached.locale, cached.messages);
    // Re-arm the synchronous hint in case localStorage was cleared under us while
    // IndexedDB survived — the two are written together and must stay together.
    setLocaleHint(cached.locale);
    return true;
}
