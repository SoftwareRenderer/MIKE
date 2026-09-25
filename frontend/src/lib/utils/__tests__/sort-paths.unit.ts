import { describe, it, expect } from 'vitest';
import { comparePathsRootLast, sortPathsRootLast } from '../sort-paths';

const paths = (...p: string[]) => p.map(path => ({ path }));

describe('sortPathsRootLast', () => {
    it('puts an entry inside a folder before a top-level one', () => {
        expect(sortPathsRootLast(paths('zeta.txt', 'a/first.txt')).map(e => e.path))
            .toEqual(['a/first.txt', 'zeta.txt']);
    });

    it('orders within each group by path', () => {
        expect(sortPathsRootLast(paths('z/last.txt', 'a/first.txt', 'root-b.txt', 'root-a.txt')).map(e => e.path))
            .toEqual(['a/first.txt', 'z/last.txt', 'root-a.txt', 'root-b.txt']);
    });

    it('reads the separator anywhere in the path, so a nested file beats a deeper one only by path order', () => {
        expect(sortPathsRootLast(paths('a/b/c/deep.txt', 'a/shallow.txt', 'top.txt')).map(e => e.path))
            .toEqual(['a/b/c/deep.txt', 'a/shallow.txt', 'top.txt']);
    });

    it('returns a new array and leaves the input order alone', () => {
        const input = paths('zeta.txt', 'a/first.txt');
        const sorted = sortPathsRootLast(input);
        expect(input.map(e => e.path)).toEqual(['zeta.txt', 'a/first.txt']);
        expect(sorted).not.toBe(input);
    });

    it('carries every field of the entry it sorted, so both callers keep their own shape', () => {
        const entries = [{ path: 'z.txt', added: true }, { path: 'a/b.txt', added: false }];
        expect(sortPathsRootLast(entries)).toEqual([{ path: 'a/b.txt', added: false }, { path: 'z.txt', added: true }]);
    });

    it('ends a comparator that ranked by something else first, so the path rule has one home', () => {
        const rank = (e: { status: string }) => (e.status === 'M' ? 0 : 1);
        const rows = [
            { path: 'root.txt', status: 'A' },
            { path: 'z.txt', status: 'M' },
            { path: 'a/first.txt', status: 'M' },
        ];
        expect([...rows].sort((a, b) => (rank(a) - rank(b)) || comparePathsRootLast(a, b)).map(r => r.path))
            .toEqual(['a/first.txt', 'z.txt', 'root.txt']);
    });
});
