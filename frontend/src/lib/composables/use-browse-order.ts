/**
 * Browse view (Settings → Extensions) list ordering. Pure resolver only —
 * the wiring lives in ExtensionsTab.svelte (same pattern as
 * use-task-detail-column-nav.ts).
 *
 * Order: unpublished local extensions first (as before), then featured
 * registry entries that are not yet installed, then the remaining registry
 * entries in their original (registry index) order. An installed featured
 * entry keeps its normal position — the top pin only applies while it is
 * not installed. A non-empty search query filters the combined list, so
 * matching featured entries stay pinned to the top of the results.
 */

import type { RegistryEntry } from '../types';

export function orderBrowseEntries(
    unpublished: RegistryEntry[],
    registry: RegistryEntry[],
    isInstalled: (entry: RegistryEntry) => boolean,
    query: string,
): RegistryEntry[] {
    const featured = registry.filter(e => e.featured === true && !isInstalled(e));
    const featuredSet = new Set(featured);
    const combined = [
        ...unpublished,
        ...featured,
        ...registry.filter(e => !featuredSet.has(e)),
    ];

    if (!query) return combined;
    const q = query.toLowerCase();
    return combined.filter(entry =>
        entry.name.toLowerCase().includes(q) ||
        entry.display_name.toLowerCase().includes(q) ||
        entry.description.toLowerCase().includes(q)
    );
}
