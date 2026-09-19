// The `..` row names the folder above the listing, so no file operation runs on it. It is still a
// folder for a paste target.

interface SelectableRow {
    path: string;
    /** false for `..` */
    selectable: boolean;
}

/** The rows in the order they are rendered. */
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

/** The selection after a click on `index`. */
export function applyRowClick(rows: RowOrder, current: FileSelection, index: number, mods: ClickModifiers): FileSelection {
    const row = rows[index];
    if (!row) return current;

    if (!mods.shift && !mods.ctrl) {
        // Clicking `..` navigates; loadDir clears the selection when the new folder arrives.
        if (!row.selectable) return current;
        return { paths: new Set([row.path]), anchor: index };
    }

    if (mods.shift) {
        // Without an anchor, the range is just the clicked row.
        const from = current.anchor >= 0 ? current.anchor : index;
        const range = indexPaths(rows, from, index);
        const paths = mods.ctrl ? new Set(current.paths) : new Set<string>();
        for (const path of range) paths.add(path);
        return { paths, anchor: current.anchor >= 0 ? current.anchor : index };
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

/** The selectable paths between `from` and `to`, both ends included. */
export function indexPaths(rows: RowOrder, from: number, to: number): string[] {
    const lo = Math.max(0, Math.min(from, to));
    const hi = Math.min(rows.length - 1, Math.max(from, to));
    const out: string[] = [];
    for (let i = lo; i <= hi; i++) {
        if (rows[i].selectable) out.push(rows[i].path);
    }
    return out;
}

/** Where a paste goes: the clicked folder, the folder holding the clicked file, or the listed folder. */
export function pasteTarget(entry: { path: string; isDir: boolean } | null, listedPath: string): string {
    if (!entry) return listedPath;
    if (entry.isDir) return entry.path;
    return parentDir(entry.path) || listedPath;
}

/** The folder containing `path`, or '' when there is none. */
export function parentDir(path: string): string {
    const cut = path.lastIndexOf('/');
    if (cut < 0) return '';
    return cut === 0 ? '/' : path.slice(0, cut);
}

/** The last segment of a path. */
export function baseName(path: string): string {
    const cut = path.lastIndexOf('/');
    return cut < 0 ? path : path.slice(cut + 1);
}

/** Whether file operations can run on this row. */
export function rowIsSelectable(entry: { name: string }): boolean {
    return entry.name !== '..';
}
