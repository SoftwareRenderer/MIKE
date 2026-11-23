/**
 * What one explorer screen remembers, and what a finished or deleted task takes with it.
 *
 * A project's explorer and each of its tasks' explorers hold their own record: shared, a task view answers
 * with files from outside its worktree. The wipe is the other half — the record, its unsaved marker, and the
 * buffer and undo chain of the files that screen held — with the two rules that keep it off what is live: a
 * path another mounted explorer holds, and a path another live record names.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { createFakeIndexedDB, installFakeIndexedDB, fakeKeysByStore, type FakeIDBFactory } from './fake-indexeddb';

const DB = 'kanban-storage';
const WS = '/srv/ws';
const P = 'p1';
/** The worktree a task's files are keyed under on the host. */
const WORKTREE = `${WS}/${P}/t1`;
/** The same directory as a sandboxed project's container spells it — one spelling every task of that project
 *  shares, so a buffer at that path is somebody else's until a record claims it. */
const MOUNT = '/home/sandbox/alpha';

const WORKTREE_FILE = `${WORKTREE}/main.go`;
const MOUNT_FILE = `${MOUNT}/main.go`;
const PROJECT_FILE = '/repos/alpha/README.md';
const OTHER_WORKTREE_FILE = `${WS}/${P}/t2/main.go`;

let fake: FakeIDBFactory;
let restoreIndexedDB: (() => void) | undefined;

beforeEach(() => {
    vi.resetModules();
    fake = createFakeIndexedDB();
    restoreIndexedDB = installFakeIndexedDB(fake);
});

afterEach(() => restoreIndexedDB?.());

const stored = (store: string): string[] => fakeKeysByStore(fake)[`${DB}/${store}`] ?? [];

/** The tab record one explorer screen holds: the files it left open, and where they were scrolled. */
function tabsOf(paths: string[]) {
    return {
        openFiles: paths.map(path => ({ path, targetLine: 0 })),
        activeFilePath: paths[0] ?? '',
        pinnedFiles: paths,
        scrollTops: Object.fromEntries(paths.map(path => [path, 120])),
        fileListScroll: 40,
    };
}

/** A buffer plus the undo chain that rode in with it. */
async function seedBuffer(path: string, text: string): Promise<void> {
    const storage = await import('$lib/storage');
    await storage.setEditorDraft(path, text, 'on disk');
    await storage.saveEditorHistory(path, 'on disk', [{ from: 0, to: 0, insert: text }]);
}

async function buffers(): Promise<string[]> {
    return [...new Set([...stored('editor-drafts'), ...stored('editor-history')])].sort();
}

describe('one record per explorer screen', () => {
    it('keeps a task explorer\'s tabs out of its project\'s, and both out of another task\'s', async () => {
        const { explorerRecordKey, saveEditorTabs, loadEditorTabs } = await import('$lib/storage');

        await saveEditorTabs(explorerRecordKey(P), tabsOf([PROJECT_FILE]));
        await saveEditorTabs(explorerRecordKey(P, 't1'), tabsOf([WORKTREE_FILE]));
        await saveEditorTabs(explorerRecordKey(P, 't2'), tabsOf([OTHER_WORKTREE_FILE]));

        expect((await loadEditorTabs(explorerRecordKey(P)))?.openFiles).toEqual([{ path: PROJECT_FILE, targetLine: 0 }]);
        expect((await loadEditorTabs(explorerRecordKey(P, 't1')))?.openFiles).toEqual([{ path: WORKTREE_FILE, targetLine: 0 }]);
        expect((await loadEditorTabs(explorerRecordKey(P, 't2')))?.openFiles).toEqual([{ path: OTHER_WORKTREE_FILE, targetLine: 0 }]);
    });

    it('leaves a project\'s record at the key it has always been written under', async () => {
        const { explorerRecordKey } = await import('$lib/storage');
        // Old builds wrote the bare project id; a changed key would orphan whatever a browser holds open
        // across the upgrade and hand it to the sweep as stale.
        expect(explorerRecordKey(P)).toBe(P);
        expect(explorerRecordKey(P, 't1')).toBe(`${P}:task:t1`);
    });
});

describe('forgetting a task that finished', () => {
    it('takes its tabs, its unsaved marker, and the files it was holding', async () => {
        const storage = await import('$lib/storage');
        await storage.saveEditorTabs(storage.explorerRecordKey(P, 't1'), tabsOf([WORKTREE_FILE, MOUNT_FILE]));
        await storage.saveUnsavedFiles(storage.explorerRecordKey(P, 't1'), [WORKTREE_FILE]);
        await seedBuffer(WORKTREE_FILE, 'typed in the worktree');
        await seedBuffer(MOUNT_FILE, 'typed through the mount');

        await storage.forgetTaskExplorer(P, 't1', { workspaceDir: WS });

        expect(stored('editor-tabs')).toEqual([]);
        expect(stored('unsaved-files')).toEqual([]);
        expect(await buffers()).toEqual([]);
    });

    it('takes the buffer of a file under its worktree that no tab names', async () => {
        const storage = await import('$lib/storage');
        await storage.saveEditorTabs(storage.explorerRecordKey(P, 't1'), tabsOf([WORKTREE_FILE]));
        await seedBuffer(WORKTREE_FILE, 'one');
        await seedBuffer(`${WORKTREE}/other.go`, 'two');

        await storage.forgetTaskExplorer(P, 't1', { workspaceDir: WS });

        expect(await buffers()).toEqual([]);
    });

    it('leaves the buffer another live record of the project names — the mount is shared spelling', async () => {
        const storage = await import('$lib/storage');
        await storage.saveEditorTabs(storage.explorerRecordKey(P, 't1'), tabsOf([MOUNT_FILE]));
        await storage.saveEditorTabs(storage.explorerRecordKey(P), tabsOf([MOUNT_FILE]));
        await seedBuffer(MOUNT_FILE, 'typed while reading the project');

        await storage.forgetTaskExplorer(P, 't1', { workspaceDir: WS });

        expect(await buffers()).toEqual([MOUNT_FILE]);
    });

    it('leaves the buffer an explorer this browser has mounted is standing in', async () => {
        const storage = await import('$lib/storage');
        await storage.saveEditorTabs(storage.explorerRecordKey(P, 't1'), tabsOf([WORKTREE_FILE]));
        await seedBuffer(WORKTREE_FILE, 'typed while the card was being moved');

        await storage.forgetTaskExplorer(P, 't1', { workspaceDir: WS, heldPaths: new Set([WORKTREE_FILE]) });

        expect(await buffers()).toEqual([WORKTREE_FILE]);
    });

    it('leaves another task\'s record and its files alone', async () => {
        const storage = await import('$lib/storage');
        const other = OTHER_WORKTREE_FILE;
        await storage.saveEditorTabs(storage.explorerRecordKey(P, 't1'), tabsOf([WORKTREE_FILE]));
        await storage.saveEditorTabs(storage.explorerRecordKey(P, 't2'), tabsOf([other]));
        await storage.saveUnsavedFiles(storage.explorerRecordKey(P, 't2'), [other]);
        await seedBuffer(WORKTREE_FILE, 'gone with t1');
        await seedBuffer(other, 'stays with t2');

        await storage.forgetTaskExplorer(P, 't1', { workspaceDir: WS });

        expect(stored('editor-tabs')).toEqual([storage.explorerRecordKey(P, 't2')]);
        expect(stored('unsaved-files')).toEqual([storage.explorerRecordKey(P, 't2')]);
        expect(await buffers()).toEqual([other]);
    });
});

describe('the board\'s sweep of explorer records', () => {
    it('takes the record of a task the board no longer holds and leaves the ones it holds', async () => {
        const storage = await import('$lib/storage');
        const gone = `${WS}/${P}/t9/main.go`;
        await storage.saveEditorTabs(storage.explorerRecordKey(P), tabsOf([PROJECT_FILE]));
        await storage.saveEditorTabs(storage.explorerRecordKey(P, 't1'), tabsOf([WORKTREE_FILE]));
        await storage.saveEditorTabs(storage.explorerRecordKey(P, 't9'), tabsOf([gone]));
        await seedBuffer(WORKTREE_FILE, 'on the board');
        await seedBuffer(gone, 'deleted with its task');

        await storage.pruneTaskExplorerRecords(P, new Set(['t1']), { workspaceDir: WS });

        expect(stored('editor-tabs').sort()).toEqual([P, storage.explorerRecordKey(P, 't1')].sort());
        expect(await buffers()).toEqual([WORKTREE_FILE]);
    });

    it('judges a task\'s record by the project its key names when its project is gone', async () => {
        const storage = await import('$lib/storage');
        await storage.saveEditorTabs(storage.explorerRecordKey(P), tabsOf([PROJECT_FILE]));
        await storage.saveEditorTabs(storage.explorerRecordKey(P, 't1'), tabsOf([WORKTREE_FILE]));
        await storage.saveUnsavedFiles(storage.explorerRecordKey(P, 't1'), [WORKTREE_FILE]);
        await storage.saveEditorTabs(storage.explorerRecordKey('p2'), tabsOf(['/repos/beta/x.go']));
        await storage.saveEditorTabs(storage.explorerRecordKey('p2', 't1'), tabsOf([`${WS}/p2/t1/x.go`]));

        // A task id is no project id, so a sweep that read every key as a project would erase the live
        // project's task records too.
        await storage.gc(new Set(['t1']), undefined, new Set([P]));

        expect(stored('editor-tabs').sort()).toEqual([P, storage.explorerRecordKey(P, 't1')].sort());
        expect(stored('unsaved-files')).toEqual([storage.explorerRecordKey(P, 't1')]);
    });
});
