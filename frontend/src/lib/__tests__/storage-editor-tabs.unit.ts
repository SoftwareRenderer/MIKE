import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { installFakeIndexedDB } from './fake-indexeddb';

// ─── Tests ───────────────────────────────────────────────────────────────────

const TABS = {
    openFiles: [{ path: '/repo/main.go', targetLine: 42 }],
    activeFilePath: '/repo/main.go',
    pinnedFiles: ['/repo/main.go'],
};

describe('editor-tabs storage', () => {
    let restoreIndexedDB: (() => void) | undefined;

    beforeEach(() => {
        vi.resetModules();
        restoreIndexedDB = installFakeIndexedDB();
    });

    afterEach(() => restoreIndexedDB?.());

    // The record is keyed by project: two boards must not read each other's open files, and a
    // project with no record must read as "nothing to restore" rather than as an empty tab list.
    it('comes back per project as it was saved, and nothing for a project never saved', async () => {
        const { saveEditorTabs, loadEditorTabs } = await import('$lib/storage');

        await saveEditorTabs('proj-a', TABS);
        await saveEditorTabs('proj-b', {
            openFiles: [{ path: '/other/file', targetLine: 2 }],
            activeFilePath: '/other/file',
            pinnedFiles: ['/other/file'],
        });

        expect(await loadEditorTabs('proj-a')).toEqual(TABS);
        expect(await loadEditorTabs('proj-b')).toEqual({
            openFiles: [{ path: '/other/file', targetLine: 2 }],
            activeFilePath: '/other/file',
            pinnedFiles: ['/other/file'],
        });
        expect(await loadEditorTabs('nonexistent-project')).toBeNull();
    });

    // Closing a tab has to survive the next reload: the save replaces, it never merges.
    it('replaces the previous state rather than merging into it', async () => {
        const { saveEditorTabs, loadEditorTabs } = await import('$lib/storage');

        await saveEditorTabs('project-789', {
            openFiles: [{ path: '/old', targetLine: 0 }],
            activeFilePath: '/old',
            pinnedFiles: [],
        });
        await saveEditorTabs('project-789', {
            openFiles: [{ path: '/new', targetLine: 10 }],
            activeFilePath: '/new',
            pinnedFiles: ['/new'],
        });

        expect(await loadEditorTabs('project-789')).toEqual({
            openFiles: [{ path: '/new', targetLine: 10 }],
            activeFilePath: '/new',
            pinnedFiles: ['/new'],
        });
    });
});
