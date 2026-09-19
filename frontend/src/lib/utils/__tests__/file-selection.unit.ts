import { describe, it, expect } from 'vitest';
import {
    allSelectablePaths, applyRowClick, baseName, emptySelection, indexPaths,
    parentDir, pasteTarget, pruneSelection, rowIsSelectable,
    type RowOrder,
} from '$lib/utils/file-selection';

// The rows in the order they render: `..`, then folders, then files.
const rows: RowOrder = [
    { path: '/p', selectable: false },              // the `..` row
    { path: '/p/docs', selectable: true },
    { path: '/p/a.ts', selectable: true },
    { path: '/p/b.ts', selectable: true },
    { path: '/p/c.ts', selectable: true },
];

const select = (...paths: string[]) => ({ paths: new Set(paths), anchor: -1 });

describe('applyRowClick', () => {
    it('a plain click selects the row it landed on and nothing else', () => {
        const next = applyRowClick(rows, select('/p/a.ts'), 3, { ctrl: false, shift: false });
        expect([...next.paths]).toEqual(['/p/b.ts']);
        expect(next.anchor).toBe(3);
    });

    it('a plain click on the parent row leaves the selection for the next listing', () => {
        const before = select('/p/a.ts', '/p/b.ts');
        expect(applyRowClick(rows, before, 0, { ctrl: false, shift: false })).toBe(before);
    });

    it('a ctrl-click adds a row and a second one takes it back off', () => {
        const added = applyRowClick(rows, select('/p/a.ts'), 3, { ctrl: true, shift: false });
        expect([...added.paths].sort()).toEqual(['/p/a.ts', '/p/b.ts']);
        const removed = applyRowClick(rows, added, 3, { ctrl: true, shift: false });
        expect([...removed.paths]).toEqual(['/p/a.ts']);
    });

    it('a ctrl-click on the parent row selects nothing but moves the anchor', () => {
        const next = applyRowClick(rows, select('/p/a.ts'), 0, { ctrl: true, shift: false });
        expect([...next.paths]).toEqual(['/p/a.ts']);
        expect(next.anchor).toBe(0);
    });

    it('a shift-click selects the inclusive range and replaces what was selected', () => {
        const anchored = { paths: new Set(['/p/a.ts']), anchor: 2 };
        const next = applyRowClick(rows, anchored, 4, { ctrl: false, shift: true });
        expect([...next.paths].sort()).toEqual(['/p/a.ts', '/p/b.ts', '/p/c.ts']);
        expect(next.anchor).toBe(2);
    });

    it('a shift-click backwards from the anchor is the same range', () => {
        const anchored = { paths: new Set(['/p/c.ts']), anchor: 4 };
        const next = applyRowClick(rows, anchored, 1, { ctrl: false, shift: true });
        expect([...next.paths].sort()).toEqual(['/p/a.ts', '/p/b.ts', '/p/c.ts', '/p/docs']);
    });

    it('a shift range that spans the parent row leaves it out', () => {
        const anchored = { paths: new Set<string>(), anchor: 0 };
        const next = applyRowClick(rows, anchored, 2, { ctrl: false, shift: true });
        expect([...next.paths].sort()).toEqual(['/p/a.ts', '/p/docs']);
    });

    it('a shift-click with no anchor selects the row it landed on', () => {
        const next = applyRowClick(rows, emptySelection(), 3, { ctrl: false, shift: true });
        expect([...next.paths]).toEqual(['/p/b.ts']);
        expect(next.anchor).toBe(3);
    });

    it('a shift-ctrl-click extends the selection instead of replacing it', () => {
        const anchored = { paths: new Set(['/p/docs']), anchor: 1 };
        const next = applyRowClick(rows, anchored, 3, { ctrl: true, shift: true });
        expect([...next.paths].sort()).toEqual(['/p/a.ts', '/p/b.ts', '/p/docs']);
    });

    it('a click on a row that is not in the listing says nothing', () => {
        const before = select('/p/a.ts');
        expect(applyRowClick(rows, before, 9, { ctrl: true, shift: false })).toBe(before);
    });
});

describe('selection over a changed listing', () => {
    it('select-all takes every selectable row', () => {
        expect([...allSelectablePaths(rows)].sort()).toEqual(['/p/a.ts', '/p/b.ts', '/p/c.ts', '/p/docs']);
    });

    it('a vanished path leaves the selection, and the anchor goes with an empty one', () => {
        const before = { paths: new Set(['/p/b.ts', '/p/c.ts']), anchor: 3 };
        const withoutC = rows.filter(r => r.path !== '/p/c.ts');
        const afterDelete = pruneSelection(withoutC, before);
        expect([...afterDelete.paths]).toEqual(['/p/b.ts']);
        expect(afterDelete.anchor).toBe(3);

        const emptied = pruneSelection(rows.filter(r => !r.selectable), afterDelete);
        expect(emptied.paths.size).toBe(0);
        expect(emptied.anchor).toBe(-1);
    });

    it('an unchanged listing returns the same selection', () => {
        const before = select('/p/a.ts');
        expect(pruneSelection(rows, before)).toBe(before);
    });

    it('indexes name the selectable rows between them, both ends included', () => {
        expect(indexPaths(rows, 4, 1).sort()).toEqual(['/p/a.ts', '/p/b.ts', '/p/c.ts', '/p/docs']);
        expect(indexPaths(rows, -3, 99).sort()).toEqual(['/p/a.ts', '/p/b.ts', '/p/c.ts', '/p/docs']);
    });
});

describe('pasteTarget', () => {
    const folder = { path: '/p/docs', isDir: true };
    const parent = { path: '/p', isDir: true };

    it('a folder row is the folder to paste into', () => {
        expect(pasteTarget(folder, '/p')).toBe('/p/docs');
    });

    it('a file row means the folder holding it', () => {
        expect(pasteTarget({ path: '/p/docs/a.md', isDir: false }, '/p')).toBe('/p/docs');
    });

    it('the parent row is the folder above what is listed', () => {
        expect(pasteTarget(parent, '/p/docs')).toBe('/p');
    });

    it('the listing itself is the target when no row was clicked', () => {
        expect(pasteTarget(null, '/p/docs')).toBe('/p/docs');
    });

    it('a file with no directory of its own falls back to what is listed', () => {
        expect(pasteTarget({ path: 'a.ts', isDir: false }, '/p')).toBe('/p');
    });
});

describe('paths as the listing spells them', () => {
    it('a directory above the root of the filesystem is the root', () => {
        expect(parentDir('/home')).toBe('/');
        expect(parentDir('solo')).toBe('');
    });

    it('a row is called by its last segment', () => {
        expect(baseName('/home/sandbox/bear/main.go')).toBe('main.go');
        expect(baseName('main.go')).toBe('main.go');
    });

    it('the parent row is the one a file operation cannot run on', () => {
        expect(rowIsSelectable({ name: '..' })).toBe(false);
        expect(rowIsSelectable({ name: '.env' })).toBe(true);
        expect(rowIsSelectable({ name: 'docs' })).toBe(true);
    });
});
