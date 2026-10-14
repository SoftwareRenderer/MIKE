import { describe, it, expect, vi } from 'vitest';
import { ExplorerCore } from '$lib/services/explorer-core.svelte';
import type { GitCommit } from '$lib/types';

// The row above a commit in the History list is the next-newest commit, not its parent. A merged branch
// interleaves rows, so that row can be a sibling, a cousin, or this commit's own child — and the diff then
// shows work the selected commit never did. A commit's other side is its first parent.

const EMPTY_TREE = '4b825dc642cb6eb9a060e54bf899d69f826635b6';

vi.mock('$lib/storage', async (importOriginal) => ({
    ...(await importOriginal<typeof import('$lib/storage')>()),
    loadEditorTabs: async () => null,
    saveEditorTabs: async () => {},
    loadUnsavedFiles: async () => [],
    saveUnsavedFiles: async () => {},
}));

function commit(short: string, parents: string[]): GitCommit {
    return {
        hash: short.repeat(5),
        short,
        subject: `commit ${short}`,
        author: 'operator',
        date: '2026-10-13T21:18:47-04:00',
        parents,
    };
}

/** The log of the reported board: a merge on top, and `simplify` sitting beside `update comments`. */
const MERGE = commit('c6c6453', []);
const ON_TASK = commit('378fe90', ['4162a9c'.repeat(5)]);
const SIMPLIFY = commit('acd4613', ['4162a9c'.repeat(5)]);
const BASE = commit('4162a9c', ['11829ba'.repeat(5)]);
const ROOT = commit('11829ba', []);
const LOG = [MERGE, ON_TASK, SIMPLIFY, BASE, ROOT];
MERGE.parents = [ON_TASK.hash, SIMPLIFY.hash];

function core(commits: GitCommit[]): { c: ExplorerCore; asked: Array<[string, string]> } {
    const asked: Array<[string, string]> = [];
    const edges: ConstructorParameters<typeof ExplorerCore>[0] = {
        storageKey: () => 'explorer-test',
        pathRoots: () => ['/srv/proj'],
        isMobile: { isMobile: false },
        fsEvents: () => [],
        lineSelectionParent: () => null,
        updateFile: () => {},
        changesOpen: () => false,
        setChangesOpen: () => {},
        closeView: () => {},
        fetchLog: async () => ({ commits, current_branch: 'main', has_more: false }),
        fetchCommitDiff: async (baseHash, headHash) => {
            asked.push([baseHash, headHash]);
            return [];
        },
    };
    // ExplorerCore creates effects, which need a root outside a component in a test.
    let created!: ExplorerCore;
    $effect.root(() => { created = new ExplorerCore(edges); });
    created.commits = commits;
    return { c: created, asked };
}

describe('ExplorerCore.handleCommitSelected', () => {
    it('diffs a commit against its own parent, not against the row above it', async () => {
        const { c, asked } = core(LOG);

        await c.handleCommitSelected(SIMPLIFY, 2);

        expect(asked).toEqual([[BASE.hash, SIMPLIFY.hash]]);
    });

    it('diffs a merge against its first parent', async () => {
        const { c, asked } = core(LOG);

        await c.handleCommitSelected(MERGE, 0);

        expect(asked).toEqual([[ON_TASK.hash, MERGE.hash]]);
    });

    it('diffs a root commit against the empty tree', async () => {
        const { c, asked } = core(LOG);

        await c.handleCommitSelected(ROOT, 4);

        expect(asked).toEqual([[EMPTY_TREE, ROOT.hash]]);
    });

    it('moves along the log by parent when the reader pages through a branched history', async () => {
        const { c, asked } = core(LOG);

        await c.handleCommitSelected(SIMPLIFY, 2);
        c.handleDiffNavigate('prev'); // the older row

        expect(asked.map(a => a[1])).toEqual([SIMPLIFY.hash, BASE.hash]);
        expect(asked[1][0]).toBe(BASE.parents![0]);
    });
});
