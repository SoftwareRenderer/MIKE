/**
 * The board's sweep of stored editor records runs against the paths an editor can key a record by, not
 * against the selected project's checkout alone: a sandboxed project's files are keyed by the container's
 * mount, and a task editor's by a worktree under the workspace. Erasing either spelling deletes a buffer
 * the reader is standing in.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { createFakeIndexedDB, installFakeIndexedDB, type FakeIDBFactory } from './fake-indexeddb';

const WORKSPACE = '/srv/mike/workspace';

const PROJECTS = [
    { id: 'p1', path: '/repos/alpha', sandbox_enabled: false },
    { id: 'p2', path: '/repos/beta', sandbox_enabled: true },
];

let fakeIndexedDB: FakeIDBFactory;
let restoreIndexedDB: (() => void) | undefined;

beforeEach(() => {
    vi.resetModules();
    fakeIndexedDB = createFakeIndexedDB();
    restoreIndexedDB = installFakeIndexedDB(fakeIndexedDB);
});

afterEach(() => restoreIndexedDB?.());

/** The buffers the sweep left behind, keyed by the path each was written under. */
function survivingDrafts(): string[] {
    return [...fakeIndexedDB.databases.get('kanban-storage')!.stores.get('editor-drafts')!.data.keys()];
}

async function sweep(): Promise<void> {
    const storage = await import('$lib/storage');
    const roots = storage.editorRecordRoots(PROJECTS, WORKSPACE);
    await storage.gc(new Set(['t1']), roots);
}

describe('the board sweep of editor records', () => {
    it('keeps the buffer of every project the browser holds, in the spelling its editor used', async () => {
        const storage = await import('$lib/storage');
        await storage.setEditorDraft('/repos/alpha/README.md', 'typed in alpha', 'disk');
        await storage.setEditorDraft('/home/sandbox/beta/README.md', 'typed in the container mount', 'disk');
        await storage.setEditorDraft(`${WORKSPACE}/p1/t1/README.md`, 'typed in a task worktree', 'disk');
        await storage.setEditorDraft(`${WORKSPACE}/p2/t9/README.md`, 'typed in a sandboxed worktree', 'disk');

        await sweep();

        expect(survivingDrafts().sort()).toEqual([
            '/home/sandbox/beta/README.md',
            '/repos/alpha/README.md',
            `${WORKSPACE}/p1/t1/README.md`,
            `${WORKSPACE}/p2/t9/README.md`,
        ].sort());
    });

    it('keeps the undo chain a buffer rode in on', async () => {
        const storage = await import('$lib/storage');
        await storage.saveEditorHistory('/home/sandbox/beta/README.md', 'disk', [{ from: 0, to: 0, insert: 'x' }]);

        await sweep();

        const history = fakeIndexedDB.databases.get('kanban-storage')!.stores.get('editor-history')!;
        expect([...history.data.keys()]).toEqual(['/home/sandbox/beta/README.md']);
    });

    it('drops the buffer of a project that is gone', async () => {
        const storage = await import('$lib/storage');
        await storage.setEditorDraft('/repos/gone/README.md', 'typed in a deleted project', 'disk');
        await storage.setEditorDraft('/home/sandbox/gone/README.md', 'typed there too', 'disk');

        await sweep();

        expect(survivingDrafts()).toEqual([]);
    });

    it('drops nothing when the browser holds no projects', async () => {
        const storage = await import('$lib/storage');
        await storage.setEditorDraft('/repos/alpha/README.md', 'typed with no project list', 'disk');

        // The caller's rule: an empty root set is passed as no roots at all, so nothing is judged stale.
        expect(storage.editorRecordRoots([], WORKSPACE).size).toBe(0);
        await storage.gc(new Set(['t1']), undefined);

        expect(survivingDrafts()).toEqual(['/repos/alpha/README.md']);
    });
});
