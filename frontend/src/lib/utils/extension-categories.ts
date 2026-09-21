/**
 * Category quick-search for Settings → Extensions: a category is the extension `type`, the field the card's
 * icon badge already labels. Pure resolver only — the wiring lives in ExtensionsTab.svelte.
 *
 * Counts are measured over the list the text query left, never over the narrowed one: a number says what
 * clicking that badge would show, not what is on screen already.
 */

import type { ExtensionType } from '../types';

/** Badge order. Fixed, so the row does not reorder itself as the query narrows the list. */
const EXTENSION_CATEGORIES: ExtensionType[] = ['lsp', 'agent', 'ui', 'css', 'i18n', 'llm', 'tool'];

type CategoryCounts = Map<ExtensionType, number>;

/** A stored count is never zero: only a seen type is a key. */
export function countByCategory(items: readonly { type: ExtensionType }[]): CategoryCounts {
    const counts: CategoryCounts = new Map();
    for (const item of items) {
        counts.set(item.type, (counts.get(item.type) ?? 0) + 1);
    }
    return counts;
}

/**
 * Categories to offer, in EXTENSION_CATEGORIES order. Every counted type is offered, including one the
 * fixed list does not name — a local manifest whose capabilities map to no type arrives as `""` — because
 * a badge row that hides an item the list shows is a bug.
 */
export function visibleCategories(counts: CategoryCounts): ExtensionType[] {
    const known = EXTENSION_CATEGORIES.filter((type) => counts.has(type));
    const rest = [...counts.keys()].filter((type) => !known.includes(type)).sort();
    return [...known, ...rest];
}

/**
 * A selection whose category the query has since emptied keeps narrowing the list with no badge of its own:
 * the Clear control in ExtensionsTab is the way out, so the row must render while a selection stands.
 */
export function filterByCategory<T extends { type: ExtensionType }>(
    items: readonly T[],
    selected: Set<ExtensionType>,
): T[] {
    if (selected.size === 0) return [...items];
    return items.filter((item) => selected.has(item.type));
}
