import { describe, it, expect, vi, beforeEach } from 'vitest';
import { ExplorerCore } from '$lib/services/explorer-core.svelte';

type ExplorerEdges = ConstructorParameters<typeof ExplorerCore>[0];

// A deleted file's tab would keep showing bytes that are gone, and its draft and undo history are
// saved under that path.

const ROOT = '/srv/proj';

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

let handedTo: (string | null)[] = [];

function core(): ExplorerCore {
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
        fetchLog: async () => ({ commits: [], current_branch: 'main', has_more: false }),
        fetchCommitDiff: async () => [],
    };
    // ExplorerCore creates effects, which need a root outside a component in a test.
    let created!: ExplorerCore;
    $effect.root(() => { created = new ExplorerCore(edges); });
    return created;
}

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
        paths.map(p => ({ path: p, targetLine: 0, reveal: 0 })),
        active,
        new Set(paths),
    );
    handedTo = [];
    return c;
};

beforeEach(() => {
    vi.clearAllMocks();
    handedTo = [];
});

describe('the explorer after the file list removed something', () => {
    it('closes the tab under the deleted folder and shows the file that survived', () => {
        const c = open(`${ROOT}/notes.md`);
        click(c, `${ROOT}/src/main.go`);
        expect(c.activeFilePath).toBe(`${ROOT}/src/main.go`);

        c.handleFilesDeleted([`${ROOT}/src`]);

        expect(c.openFiles.map(f => f.path)).toEqual([`${ROOT}/notes.md`]);
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

        expect(c.openFiles.map(f => f.path)).toEqual([`${ROOT}/src-backup/keep.go`]);
        expect(draftCleared).toHaveBeenCalledTimes(1);
    });

    it('hands over the deleted file itself when its tab was the one open', () => {
        const c = open(`${ROOT}/notes.md`);
        click(c, `${ROOT}/todo.md`);

        c.handleFilesDeleted([`${ROOT}/todo.md`]);

        expect(c.openFiles.map(f => f.path)).toEqual([`${ROOT}/notes.md`]);
        expect(c.activeFilePath).toBe(`${ROOT}/notes.md`);
    });

    it('hands the screen only the file that survived, never one it removed on the way', () => {
        const c = restored(
            core(),
            [`${ROOT}/src/one.go`, `${ROOT}/src/two.go`, `${ROOT}/notes.md`],
            `${ROOT}/src/one.go`,
        );

        c.handleFilesDeleted([`${ROOT}/src`]);

        expect(c.openFiles.map(f => f.path)).toEqual([`${ROOT}/notes.md`]);
        expect(handedTo).toEqual([`${ROOT}/notes.md`]);
    });

    it('leaves the screen alone when nothing open was touched', () => {
        const c = open(`${ROOT}/notes.md`);

        c.handleFilesDeleted([`${ROOT}/src`]);

        expect(c.openFiles.map(f => f.path)).toEqual([`${ROOT}/notes.md`]);
        expect(handedTo).toEqual([]);
        expect(draftCleared).not.toHaveBeenCalled();
    });
});
