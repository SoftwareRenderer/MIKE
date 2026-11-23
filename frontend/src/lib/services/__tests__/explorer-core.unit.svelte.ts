/**
 * `ExplorerCore` — the four decisions the explorer makes about what it is showing.
 *
 *  - a commit's other side is its **first parent**, never the row above it in the log;
 *  - the files the list removed take their tabs, drafts and undo history with them —
 *    and nothing else goes with them;
 *  - `resetTabs` answers to the subject it was called for, so a re-run of the screen's
 *    effect does not throw away the line a go-to-definition jump asked for;
 *  - the tabs are written to the record of the subject that held them, even when the write
 *    lands after the screen has moved on.
 */
import { tick } from 'svelte';
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { ExplorerCore } from '$lib/services/explorer-core.svelte';
import type { GitCommit } from '$lib/types';

const ROOT = '/srv/proj';
const EMPTY_TREE = '4b825dc642cb6eb9a060e54bf899d69f826635b6';

const draftCleared = vi.hoisted(() => vi.fn());
const historyCleared = vi.hoisted(() => vi.fn());
/** Every tab write, with the record key it went to. */
const savedTabs = vi.hoisted(() => vi.fn());

vi.mock('$lib/storage', async (importOriginal) => ({
    ...(await importOriginal<typeof import('$lib/storage')>()),
    clearEditorDraft: (path: string) => { draftCleared(path); return Promise.resolve(); },
    clearEditorHistory: (path: string) => { historyCleared(path); return Promise.resolve(); },
    loadEditorTabs: async () => null,
    saveEditorTabs: (key: string, state: unknown) => { savedTabs(key, state); return Promise.resolve(); },
    loadUnsavedFiles: async () => [],
    saveUnsavedFiles: async () => {},
}));

type ExplorerEdges = ConstructorParameters<typeof ExplorerCore>[0];

/** Every fetch the core made, in the order it asked. */
let asked: Array<[string, string]> = [];
/** The paths the screen was last handed, in order. */
let handedTo: Array<string | null> = [];

function core(commits: GitCommit[] = [], overrides: Partial<ExplorerEdges> = {}): ExplorerCore {
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
        ...overrides,
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

// ─── Which record the tabs are written to ───────────────────────────────────
// A project's explorer and each of its tasks' explorers hold their own list, and the
// address bar moves before the screen's effect does. A write that read the key when it
// fired would land a screen's tabs in the record of whoever arrived next.

describe('the record an explorer writes its tabs to', () => {
    it('leaves the record alone on the first subject it adopts, which is the one it is about to restore', () => {
        vi.useFakeTimers();
        const c = core([], { storageKey: () => 'task-1' });   // built for this subject, holding nothing
        c.queueTabSave();                                     // the write its construction queued
        savedTabs.mockClear();

        c.resetTabs('task-1');                                // the screen's effect, ahead of the restore

        expect(savedTabs).not.toHaveBeenCalled();
        vi.useRealTimers();
    });

    it('writes the tabs it still held to the subject it is leaving', () => {
        vi.useFakeTimers();
        let subject = 'task-1';
        const c = core([], { storageKey: () => subject });
        c.resetTabs('task-1');
        c.handleFileSelected(`${ROOT}/a.go`, 0, true);
        c.queueTabSave();                       // a click's write, still waiting out the quiet period
        savedTabs.mockClear();

        subject = 'task-2';                     // the address bar moved; the screen's effect runs next
        c.resetTabs('task-2');

        const [key, state] = savedTabs.mock.calls[0] as [string, { openFiles: unknown }];
        expect(key).toBe('task-1');
        expect(state.openFiles).toEqual([{ path: `${ROOT}/a.go`, targetLine: 0 }]);
        vi.useRealTimers();
    });

    it('lands a write that fires after the address bar moved under the subject whose tabs it holds', () => {
        vi.useFakeTimers();
        let subject = 'task-1';
        const c = core([], { storageKey: () => subject });
        c.resetTabs('task-1');
        c.handleFileSelected(`${ROOT}/a.go`, 0, true);
        c.queueTabSave();
        savedTabs.mockClear();

        subject = 'task-2';                     // the move, with the effect not yet run
        vi.advanceTimersByTime(300);

        expect(savedTabs).toHaveBeenCalledTimes(1);
        expect(savedTabs.mock.calls[0][0]).toBe('task-1');
        vi.useRealTimers();
    });
});

// ─── The listing's own scroll ───────────────────────────────────────────────
// The file listing keeps one offset per screen, written beside the tabs and handed back
// once — restoring it to every rebuilt listing would fight a reader already moving.
// Both explorers mount the same core, so this is measured here rather than twice.

describe("the listing's scroll", () => {
    /** The scroller as `ExplorerWorkspace` binds it, with the geometry jsdom cannot give it. */
    function listing(scrollHeight = 0, clientHeight = 0): { el: HTMLDivElement; list: HTMLDivElement } {
        const el = document.createElement('div');
        const list = document.createElement('div');
        list.className = 'file-browser-list';
        Object.defineProperty(list, 'scrollHeight', { configurable: true, get: () => scrollHeight });
        Object.defineProperty(list, 'clientHeight', { configurable: true, get: () => clientHeight });
        el.appendChild(list);
        return { el, list };
    }

    it("writes the listing's offset with the tabs of the screen that scrolled", async () => {
        vi.useFakeTimers();
        const c = core();
        const { el, list } = listing(3000, 300);
        c.fileBrowserEl = el;
        await tick();                           // the effect that binds the scroller is queued

        list.scrollTop = 240;
        list.dispatchEvent(new Event('scroll'));
        c.queueTabSave();
        vi.advanceTimersByTime(300);

        expect(c.fileListScroll).toBe(240);
        expect(savedTabs).toHaveBeenCalledWith('explorer-test', expect.objectContaining({ fileListScroll: 240 }));
        vi.useRealTimers();
    });

    it('hands the offset back to the first listing that can scroll, and to no later one', async () => {
        vi.useFakeTimers();
        const c = core();
        c.fileListScroll = 150;
        const first = listing(3000, 300);
        c.fileBrowserEl = first.el;
        await tick();

        vi.advanceTimersByTime(100);
        expect(first.list.scrollTop).toBe(150);

        // The reader has since moved on, and the listing was rebuilt: their position stands.
        first.list.scrollTop = 40;
        const second = listing(3000, 300);
        c.fileBrowserEl = second.el;
        await tick();
        vi.advanceTimersByTime(1000);
        expect(second.list.scrollTop).toBe(0);
        vi.useRealTimers();
    });
});
