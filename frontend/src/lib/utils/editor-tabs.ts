/**
 * Pure helpers for the explorer tab list — the state behind "show this file at
 * this line" (file browser click, search result, LSP definition, restored
 * session). No DOM and no Svelte, so both explorers answer to one rule;
 * unit tested in `__tests__/editor-tabs.unit.ts`.
 */

export interface OpenFileTab {
    path: string;
    /** Line the editor was last asked to reveal; 0 = it was asked to reveal nothing. */
    targetLine: number;
    /**
     * How many reveal requests this tab has had. The editor reveals on a change
     * to this rather than to `targetLine`: the same line asked for a second time
     * is a second request, and a stored line number cannot say that.
     */
    reveal: number;
}

/** The shape `$lib/storage` keeps — see `toPersistedTabs` for why `reveal` is not in it. */
export interface PersistedFileTab {
    path: string;
    targetLine: number;
}

/**
 * Apply a request to show `path` at `line` (0 = open it without revealing anything).
 *
 * A tab that is already open keeps its place until the request names a line;
 * a new tab replaces the unpinned (preview) one when there is one, which is
 * what makes a browse-through of search results reuse a single tab.
 */
export function revealFileTab(
    files: OpenFileTab[],
    path: string,
    line: number,
    isPinned: (path: string) => boolean,
): OpenFileTab[] {
    const existing = files.findIndex(f => f.path === path);
    if (existing >= 0) {
        if (line < 1) return files;
        const next = [...files];
        next[existing] = { path, targetLine: line, reveal: next[existing].reveal + 1 };
        return next;
    }
    const tab: OpenFileTab = { path, targetLine: line > 0 ? line : 0, reveal: line > 0 ? 1 : 0 };
    const preview = files.findIndex(f => !isPinned(f.path));
    if (preview >= 0) {
        const next = [...files];
        next[preview] = tab;
        return next;
    }
    return [...files, tab];
}

/** `reveal` is a request, not a position: persisting it would re-fire a click on restore. */
export function toPersistedTabs(files: OpenFileTab[]): PersistedFileTab[] {
    return files.map(f => ({ path: f.path, targetLine: f.targetLine }));
}

/** A restored tab reveals nothing until something asks it to. */
export function fromPersistedTabs(files: readonly PersistedFileTab[]): OpenFileTab[] {
    return files.map(f => ({ path: f.path, targetLine: f.targetLine || 0, reveal: 0 }));
}
