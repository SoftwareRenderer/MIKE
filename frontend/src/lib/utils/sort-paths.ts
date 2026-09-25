/**
 * Order for a flat list of paths: an entry inside a folder sorts before a top-level one, so a
 * list groups by directory instead of scattering root entries between it. Within each group the
 * path decides.
 *
 * `localeCompare` carries no locale argument on purpose: a file list keeps the same order
 * whatever the UI language is (`AGENTS.md → Frontend` — sorting never follows the locale).
 *
 * A caller that ranks by something first — the diff panel ranks by change status — ends its
 * comparator with `comparePathsRootLast` instead of restating the rule.
 */
export function comparePathsRootLast(a: { path: string }, b: { path: string }): number {
    const aHasDir = a.path.includes('/');
    const bHasDir = b.path.includes('/');
    if (aHasDir && !bHasDir) return -1;
    if (!aHasDir && bHasDir) return 1;
    return a.path.localeCompare(b.path);
}

/** A sorted copy; the caller's array keeps its order. */
export function sortPathsRootLast<T extends { path: string }>(entries: T[]): T[] {
    return [...entries].sort(comparePathsRootLast);
}
