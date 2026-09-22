/**
 * Browse view (Settings → Extensions) list ordering. Pure resolver only —
 * the wiring lives in ExtensionsTab.svelte (same pattern as
 * use-task-detail-column-nav.ts).
 *
 * Order: unpublished local extensions first (as before), then featured registry
 * entries that are not yet installed, then the not-yet-installed translation
 * pack for the browser's language, then the remaining registry entries in their
 * original (registry index) order. An installed entry keeps its normal position
 * — a pin only applies while the entry is not installed, so a pack the user
 * already has (enabled or disabled) is not promoted.
 *
 * The language comparison is the first-run offer's own, from
 * `$lib/utils/locale-offer`: the primary subtag on both sides, so a regional
 * browser tag answers the pack declaring the bare subtag it reduces to. No tier
 * at all when the browser speaks the fallback locale — that is what the list
 * already renders.
 *
 * A non-empty search query filters the combined list, so a pinned entry stays
 * pinned to the top of the results.
 */

import type { RegistryEntry } from '../types';
import { DEFAULT_LOCALE } from '../i18n/en';
import { offeredLocale, primarySubtag } from '../utils/locale-offer';

export function orderBrowseEntries(
    unpublished: RegistryEntry[],
    registry: RegistryEntry[],
    isInstalled: (entry: RegistryEntry) => boolean,
    query: string,
    /** `navigator.language`, or `''`/null when there is no browser language to order against. */
    browserLanguage?: string | null,
): RegistryEntry[] {
    const available = registry.filter(e => !isInstalled(e));
    const featured = available.filter(e => e.featured === true);

    const want = primarySubtag(browserLanguage);
    const forBrowser = want && want !== DEFAULT_LOCALE
        ? available.filter(e => e.featured !== true && primarySubtag(offeredLocale(e)) === want)
        : [];

    const pinned = new Set([...featured, ...forBrowser]);
    const combined = [
        ...unpublished,
        ...featured,
        ...forBrowser,
        ...registry.filter(e => !pinned.has(e)),
    ];

    if (!query) return combined;
    const q = query.toLowerCase();
    return combined.filter(entry =>
        entry.name.toLowerCase().includes(q) ||
        entry.display_name.toLowerCase().includes(q) ||
        entry.description.toLowerCase().includes(q)
    );
}
