/**
 * `ExplorerCore` — the three decisions the explorer makes about what it is showing.
 *
 *  - a commit's other side is its **first parent**, never the row above it in the log;
 *  - the files the list removed take their tabs, drafts and undo history with them —
 *    and nothing else goes with them;
 *  - `resetTabs` answers to the subject it was called for, so a re-run of the screen's
 *    effect does not throw away the line a go-to-definition jump asked for.
 */
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { ExplorerCore } from '$lib/services/explorer-core.svelte';
import type { GitCommit } from '$lib/types';

const ROOT = '/srv/proj';
const EMPTY_TREE = '4b825dc642cb6eb9a060e54bf899d69f826635b6';

const draftCleared = vi.hoisted(() => vi.fn());
const historyCleared = vi.hoisted(() => vi.fn());

vi.mock('$lib/storage', async (importOriginal) => ({
    ...(await importOriginal<typeof import('$lib/storage')>()),
    clearEditorDraft: (path: string) => { draftCleared(path); return Promise.resolve(); },
    clearEditorHistory: (path: string) => { historyCleared(path); return Promise.resolve(); },
    loadEditorTabs: async () => null,
    saveEditorTabs: async () => {},
    loadUnsavedFiles: async () => [],
    saveUnsavedFiles: async () => {},
}));

type ExplorerEdges = ConstructorParameters<typeof ExplorerCore>[0];

/** Every fetch the core made, in the order it asked. */
let asked: Array<[string, string]> = [];
/** The paths the screen was last handed, in order. */
let handedTo: Array<string | null> = [];

function core(commits: GitCommit[] = []): ExplorerCore {
    const edges: ExplorerEdges = {
        storageKey: () => 'explorer-test',
        pathRoots: () => [ROOT],
        isMobile: { isMobile: false },
        fsEvents: () => [],
        lineSelectionParent: () => null,
        updateFile: (path) => { handedTo.push(path); },
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
    return created;
}

beforeEach(() => {
    vi.clearAllMocks();
    asked = [];
    handedTo = [];
});

// ─── The commit's other side ────────────────────────────────────────────────
// The row above a commit in the History list is the next-newest commit, not its parent.
// A merged branch interleaves rows, so that row can be a sibling, a cousin, or this
// commit's own child — and the diff then shows work the selected commit never did.

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
const ROOT_COMMIT = commit('11829ba', []);
const LOG = [MERGE, ON_TASK, SIMPLIFY, BASE, ROOT_COMMIT];
MERGE.parents = [ON_TASK.hash, SIMPLIFY.hash];

describe('the commit a history row diffs against', () => {
    it('diffs a commit against its own parent, not against the row above it', async () => {
        const c = core(LOG);
        await c.handleCommitSelected(SIMPLIFY, 2);
        expect(asked).toEqual([[BASE.hash, SIMPLIFY.hash]]);
    });

    it('diffs a merge against its first parent', async () => {
        const c = core(LOG);
        await c.handleCommitSelected(MERGE, 0);
        expect(asked).toEqual([[ON_TASK.hash, MERGE.hash]]);
    });

    it('diffs a root commit against the empty tree', async () => {
        const c = core(LOG);
        await c.handleCommitSelected(ROOT_COMMIT, 4);
        expect(asked).toEqual([[EMPTY_TREE, ROOT_COMMIT.hash]]);
    });

    it('moves along the log by parent when the reader pages through a branched history', async () => {
        const c = core(LOG);
        await c.handleCommitSelected(SIMPLIFY, 2);
        c.handleDiffNavigate('prev'); // the older row

        expect(asked.map((a) => a[1])).toEqual([SIMPLIFY.hash, BASE.hash]);
        expect(asked[1][0]).toBe(BASE.parents![0]);
    });
});

// ─── What the file list removing something takes with it ────────────────────
// A deleted file's tab would keep showing bytes that are gone, and its draft and undo
// history are saved under that path.

// Open a file and pin it, so the next open doesn't replace it as the preview tab.
const open = (path: string) => {
    const c = core();
    c.handleFileSelected(path, undefined, true);
    handedTo = [];
    return c;
};

// Open a file the way a single click does: unpinned, as the preview tab.
const click = (c: ExplorerCore, path: string) => c.handleFileSelected(path);

// Restore a tab list with every tab pinned, as a saved screen comes back.
const restored = (c: ExplorerCore, paths: string[], active: string) => {
    c.applyTabs(
        paths.map((p) => ({ path: p, targetLine: 0, reveal: 0 })),
        active,
        new Set(paths),
    );
    handedTo = [];
    return c;
};

describe('the explorer after the file list removed something', () => {
    it('closes the tab under the deleted folder and shows the file that survived', () => {
        const c = open(`${ROOT}/notes.md`);
        click(c, `${ROOT}/src/main.go`);
        expect(c.activeFilePath).toBe(`${ROOT}/src/main.go`);

        c.handleFilesDeleted([`${ROOT}/src`]);

        expect(c.openFiles.map((f) => f.path)).toEqual([`${ROOT}/notes.md`]);
        expect(c.activeFilePath).toBe(`${ROOT}/notes.md`);
        expect(handedTo[handedTo.length - 1]).toBe(`${ROOT}/notes.md`);
        expect(draftCleared).toHaveBeenCalledWith(`${ROOT}/src/main.go`);
        expect(historyCleared).toHaveBeenCalledWith(`${ROOT}/src/main.go`);
    });

    it('leaves the editor with nothing when every open file went', () => {
        const c = open(`${ROOT}/src/main.go`);
        click(c, `${ROOT}/src/util.go`);

        c.handleFilesDeleted([`${ROOT}/src`]);

        expect(c.openFiles).toEqual([]);
        expect(c.activeFilePath).toBe('');
        expect(handedTo[handedTo.length - 1]).toBeNull();
    });

    it('leaves a folder whose name merely starts with the one deleted', () => {
        const c = open(`${ROOT}/src-backup/keep.go`);
        click(c, `${ROOT}/src/main.go`);

        c.handleFilesDeleted([`${ROOT}/src`]);

        expect(c.openFiles.map((f) => f.path)).toEqual([`${ROOT}/src-backup/keep.go`]);
        expect(draftCleared).toHaveBeenCalledTimes(1);
    });

    it('hands over the deleted file itself when its tab was the one open', () => {
        const c = open(`${ROOT}/notes.md`);
        click(c, `${ROOT}/todo.md`);

        c.handleFilesDeleted([`${ROOT}/todo.md`]);

        expect(c.openFiles.map((f) => f.path)).toEqual([`${ROOT}/notes.md`]);
        expect(c.activeFilePath).toBe(`${ROOT}/notes.md`);
    });

    it('hands the screen only the file that survived, never one it removed on the way', () => {
        const c = restored(
            core(),
            [`${ROOT}/src/one.go`, `${ROOT}/src/two.go`, `${ROOT}/notes.md`],
            `${ROOT}/src/one.go`,
        );

        c.handleFilesDeleted([`${ROOT}/src`]);

        expect(c.openFiles.map((f) => f.path)).toEqual([`${ROOT}/notes.md`]);
        expect(handedTo).toEqual([`${ROOT}/notes.md`]);
    });

    it('leaves the screen alone when nothing open was touched', () => {
        const c = open(`${ROOT}/notes.md`);

        c.handleFilesDeleted([`${ROOT}/src`]);

        expect(c.openFiles.map((f) => f.path)).toEqual([`${ROOT}/notes.md`]);
        expect(handedTo).toEqual([]);
        expect(draftCleared).not.toHaveBeenCalled();
    });
});

// ─── resetTabs answers to its subject ───────────────────────────────────────
// The address bar names a file and never a line, so a tab list cleared and re-opened
// from the URL loses the line a go-to-definition jump asked for. `resetTabs` is what
// clears it, so it answers to the subject it was called for rather than to every
// re-run of the screen's effect.

describe('ExplorerCore.resetTabs', () => {
    it('keeps the jumped-to line when the same subject re-runs its effect', () => {
        const c = core();
        c.resetTabs('task-1');                         // the mount
        c.handleFileSelected(`${ROOT}/a.go`, 6, true); // the jump that opened this explorer
        c.resetTabs('task-1');                         // a re-run for the subject already shown

        expect(c.openFiles.map((f) => [f.path, f.targetLine])).toEqual([[`${ROOT}/a.go`, 6]]);
    });

    it('empties the screen when it moves to another subject', () => {
        const c = core();
        c.resetTabs('task-1');
        c.handleFileSelected(`${ROOT}/a.go`, 6, true);
        c.resetTabs('task-2');

        expect(c.openFiles).toEqual([]);
        expect(c.activeFilePath).toBe('');
    });
});
