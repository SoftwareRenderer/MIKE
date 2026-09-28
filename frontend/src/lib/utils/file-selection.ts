import { parentDir } from './path';

interface SelectableRow {
    path: string;
    /** false for `..`, which is never a target for a file operation but is still a paste target. */
    selectable: boolean;
    /** Rows sharing a group are one range: a shift extends inside its own group only. Omitted for a
     *  listing that is a single run of rows. */
    group?: string;
}

export type RowOrder = readonly SelectableRow[];

/** The selected paths, plus the row index a shift-click extends from (-1 for none). */
export interface FileSelection {
    paths: Set<string>;
    anchor: number;
}

export const emptySelection = (): FileSelection => ({ paths: new Set(), anchor: -1 });

/** Ctrl and ⌘ count as the same modifier. */
export interface ClickModifiers {
    ctrl: boolean;
    shift: boolean;
}

export function applyRowClick(rows: RowOrder, current: FileSelection, index: number, mods: ClickModifiers): FileSelection {
    const row = rows[index];
    if (!row) return current;

    if (!mods.shift && !mods.ctrl) {
        // Clicking `..` navigates; loadDir clears the selection when the new folder arrives.
        if (!row.selectable) return current;
        return { paths: new Set([row.path]), anchor: index };
    }

    if (mods.shift) {
        // An anchor in another group is not the end of a range in this one.
        const group = row.group;
        const anchor = current.anchor >= 0 && rows[current.anchor]?.group === group ? current.anchor : index;
        const range = indexPaths(rows, anchor, index, group);
        const paths = mods.ctrl ? new Set(current.paths) : new Set<string>();
        for (const path of range) paths.add(path);
        return { paths, anchor };
    }

    const paths = new Set(current.paths);
    if (row.selectable) {
        if (paths.has(row.path)) paths.delete(row.path);
        else paths.add(row.path);
    }
    // A ctrl-click on `..` selects nothing, but shift then extends from there.
    return { paths, anchor: index };
}

export function allSelectablePaths(rows: RowOrder): Set<string> {
    return new Set(rows.filter(r => r.selectable).map(r => r.path));
}

/** Drop paths that are no longer in the listing, so actions don't run on rows that have gone. */
export function pruneSelection(rows: RowOrder, selection: FileSelection): FileSelection {
    const present = new Set(rows.map(r => r.path));
    const paths = new Set([...selection.paths].filter(p => present.has(p)));
    if (paths.size === selection.paths.size) return selection;
    return { paths, anchor: paths.size === 0 ? -1 : selection.anchor };
}

/**
 * The selectable paths between `from` and `to`, both ends included. With `withinGroup` a range drawn
 * across a group boundary stops at the edge of the one it started in.
 *
 * @knipignore An inclusive range and its clamping are asserted on index pairs; a click names real rows.
 */
export function indexPaths(rows: RowOrder, from: number, to: number, withinGroup?: string): string[] {
    const lo = Math.max(0, Math.min(from, to));
    const hi = Math.min(rows.length - 1, Math.max(from, to));
    const out: string[] = [];
    for (let i = lo; i <= hi; i++) {
        if (!rows[i].selectable) continue;
        if (withinGroup !== undefined && rows[i].group !== withinGroup) continue;
        out.push(rows[i].path);
    }
    return out;
}

/**
 * How many names a destructive confirmation lists before counting the rest.
 *
 * @knipignore The list is measured at this count and one name past it.
 */
export const CONFIRM_LIST_MAX = 8;

/** The names a confirm dialog lists: at most `CONFIRM_LIST_MAX`, then one line counting the rest, which
 *  `more` words because the phrase lives in the catalog. */
export function confirmList(names: string[], more: (count: number) => string): string[] {
    const listed = names.slice(0, CONFIRM_LIST_MAX);
    const rest = names.length - listed.length;
    return rest > 0 ? [...listed, more(rest)] : listed;
}

/** Where a paste goes: the clicked folder, the folder holding the clicked file, or the listed folder. */
export function pasteTarget(entry: { path: string; isDir: boolean } | null, listedPath: string): string {
    if (!entry) return listedPath;
    if (entry.isDir) return entry.path;
    return parentDir(entry.path) || listedPath;
}

export function rowIsSelectable(entry: { name: string }): boolean {
    return entry.name !== '..';
}
