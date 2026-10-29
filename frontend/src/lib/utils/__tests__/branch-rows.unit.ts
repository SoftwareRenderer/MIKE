import { describe, it, expect } from 'vitest';
import { filterBranches, matchesBranch, nextBranchRow } from '$lib/utils/branch-rows';

/** What a branch switcher does with keys and with a query, measured where it has no browser. */
describe('filterBranches', () => {
    const refs = ['main', 'feature/reconcile-caches', 'task/fix-the-login-abc123'];

    it('keeps every ref, in the server order, for a blank query', () => {
        expect(filterBranches(refs, '')).toEqual(refs);
        expect(filterBranches(refs, '   ')).toEqual(refs);
    });

    it('finds a task ref by the tail of its name, which is the part that names the work', () => {
        expect(filterBranches(refs, 'login')).toEqual(['task/fix-the-login-abc123']);
    });

    it('matches across the namespace and ignores case', () => {
        expect(filterBranches(refs, 'FEATURE/RECONCILE')).toEqual(['feature/reconcile-caches']);
    });

    it('answers nothing when no ref matches, which is what makes "no branch matches" reachable', () => {
        expect(filterBranches(refs, 'nope')).toEqual([]);
    });

    it('judges a row by the same rule whether it is switchable or held by a task', () => {
        // The held rows are objects, so they use the predicate the list is built from.
        expect(refs.filter(b => matchesBranch(b, 'LOGIN'))).toEqual(['task/fix-the-login-abc123']);
        expect(matchesBranch('main', '   ')).toBe(true);
    });
});

describe('nextBranchRow', () => {
    it('lands on the first row when the list holds no focus yet', () => {
        expect(nextBranchRow(-1, 'ArrowDown', 4)).toBe(0);
        // Up from the filter field means the end of the list, not nowhere.
        expect(nextBranchRow(-1, 'ArrowUp', 4)).toBe(3);
    });

    it('wraps at both ends so a list longer than its box is escapable', () => {
        expect(nextBranchRow(3, 'ArrowDown', 4)).toBe(0);
        expect(nextBranchRow(0, 'ArrowUp', 4)).toBe(3);
    });

    it('jumps to the ends on Home and End', () => {
        expect(nextBranchRow(2, 'Home', 4)).toBe(0);
        expect(nextBranchRow(2, 'End', 4)).toBe(3);
    });

    it('leaves every other key alone, and an empty list has no row to land on', () => {
        for (const key of ['Enter', 'Escape', 'Tab', 'a', 'Backspace']) {
            expect(nextBranchRow(1, key, 4)).toBe(-1);
        }
        expect(nextBranchRow(-1, 'ArrowDown', 0)).toBe(-1);
    });
});
