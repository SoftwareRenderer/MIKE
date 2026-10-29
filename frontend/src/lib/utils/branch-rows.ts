/** This module holds the two key decisions a branch list makes, kept pure so they are testable without a browser. */

/** matchesBranch tells whether a query names a ref, matched case-insensitively anywhere in it. An empty
 * query names everything. */
export function matchesBranch(name: string, query: string): boolean {
    const needle = query.trim().toLowerCase();
    return needle === '' || name.toLowerCase().includes(needle);
}

/** filterBranches returns the rows a query leaves, in the order the server returned them. */
export function filterBranches(branches: string[], query: string): string[] {
    return branches.filter(b => matchesBranch(b, query));
}

/** nextBranchRow returns the row the arrows move focus to, or -1 to leave the key alone. `index` is the row
 * holding focus now, or -1 when the list holds none. */
export function nextBranchRow(index: number, key: string, count: number): number {
    if (count <= 0) return -1;
    if (key === 'ArrowDown') return index < 0 ? 0 : (index + 1) % count;
    if (key === 'ArrowUp') return index <= 0 ? count - 1 : index - 1;
    if (key === 'Home') return 0;
    if (key === 'End') return count - 1;
    return -1;
}
