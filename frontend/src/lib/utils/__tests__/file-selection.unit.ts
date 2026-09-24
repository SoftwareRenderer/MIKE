import { describe, it, expect } from 'vitest';
import {
    allSelectablePaths, applyRowClick, confirmList, emptySelection, indexPaths,
    pasteTarget, pruneSelection, rowIsSelectable, CONFIRM_LIST_MAX,
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

describe('a shift range inside one group', () => {
    // The Review Changes sidebar: two sections of one listing, staged rows first.
    const grouped: RowOrder = [
        { path: 's1.ts', selectable: true, group: 'staged' },
        { path: 's2.ts', selectable: true, group: 'staged' },
        { path: 'u1.ts', selectable: true, group: 'unstaged' },
        { path: 'u2.ts', selectable: true, group: 'unstaged' },
    ];

    it('reaches every row of the group the click landed in', () => {
        const anchored = { paths: new Set(['u1.ts']), anchor: 2 };
        const next = applyRowClick(grouped, anchored, 3, { ctrl: false, shift: true });
        expect([...next.paths].sort()).toEqual(['u1.ts', 'u2.ts']);
        expect(next.anchor).toBe(2);
    });

    it('selects only the row it landed on when the anchor sits in another group', () => {
        const anchored = { paths: new Set(['s1.ts']), anchor: 0 };
        const next = applyRowClick(grouped, anchored, 3, { ctrl: false, shift: true });
        expect([...next.paths]).toEqual(['u2.ts']);
        expect(next.anchor).toBe(3);
    });

    it('re-anchors where it landed, so the next shift extends inside that group', () => {
        const crossed = applyRowClick(grouped, { paths: new Set(['s1.ts']), anchor: 0 }, 2, { ctrl: false, shift: true });
        expect([...crossed.paths]).toEqual(['u1.ts']);
        expect(crossed.anchor).toBe(2);
        const extended = applyRowClick(grouped, crossed, 3, { ctrl: false, shift: true });
        expect([...extended.paths].sort()).toEqual(['u1.ts', 'u2.ts']);
    });

    it('still reaches a row of its own group across a row of the other one', () => {
        const interleaved: RowOrder = [
            { path: 'a', selectable: true, group: 'x' },
            { path: 'b', selectable: true, group: 'y' },
            { path: 'c', selectable: true, group: 'x' },
        ];
        const next = applyRowClick(interleaved, { paths: new Set<string>(), anchor: 0 }, 2, { ctrl: false, shift: true });
        expect([...next.paths]).toEqual(['a', 'c']);
    });

    it('keeps rows of both groups when ctrl-click adds them', () => {
        const next = applyRowClick(grouped, { paths: new Set(['s1.ts']), anchor: 0 }, 3, { ctrl: true, shift: false });
        expect([...next.paths].sort()).toEqual(['s1.ts', 'u2.ts']);
    });

    it('names only that group when a range is asked for one', () => {
        expect(indexPaths(grouped, 0, 3, 'staged')).toEqual(['s1.ts', 's2.ts']);
        expect(indexPaths(grouped, 0, 3)).toEqual(['s1.ts', 's2.ts', 'u1.ts', 'u2.ts']);
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
    it('the parent row is the one a file operation cannot run on', () => {
        expect(rowIsSelectable({ name: '..' })).toBe(false);
        expect(rowIsSelectable({ name: '.env' })).toBe(true);
        expect(rowIsSelectable({ name: 'docs' })).toBe(true);
    });
});

describe('the names a confirmation lists', () => {
    const more = (count: number) => `${count} more`;

    it('lists every name when there is room for all of them', () => {
        expect(confirmList(['a.ts', 'b.ts'], more)).toEqual(['a.ts', 'b.ts']);
    });

    it('counts the rest once the list is cut', () => {
        const names = Array.from({ length: CONFIRM_LIST_MAX + 3 }, (_, i) => `f${i}.ts`);
        const listed = confirmList(names, more);
        expect(listed).toEqual([...names.slice(0, CONFIRM_LIST_MAX), '3 more']);
    });

    it('cuts at the cap and says nothing about a rest that does not exist', () => {
        const exact = Array.from({ length: CONFIRM_LIST_MAX }, (_, i) => `f${i}.ts`);
        expect(confirmList(exact, more)).toEqual(exact);
    });

    it('leaves the called list alone', () => {
        const names = Array.from({ length: CONFIRM_LIST_MAX + 1 }, (_, i) => `f${i}.ts`);
        confirmList(names, more);
        expect(names).toHaveLength(CONFIRM_LIST_MAX + 1);
    });
});
