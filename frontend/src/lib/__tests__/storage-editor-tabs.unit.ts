import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { installFakeIndexedDB } from './fake-indexeddb';

// ─── Tests ───────────────────────────────────────────────────────────────────

describe('editor-tabs storage', () => {
    let restoreIndexedDB: (() => void) | undefined;

    beforeEach(() => {
        vi.resetModules();
        restoreIndexedDB = installFakeIndexedDB();
    });

    afterEach(() => restoreIndexedDB?.());

    it('saveEditorTabs and loadEditorTabs round-trips correctly', async () => {
        const { saveEditorTabs, loadEditorTabs } = await import('$lib/storage');

        const state = {
            openFiles: [
                { path: '/home/user/project/src/main.go', targetLine: 42 },
                { path: '/home/user/project/README.md', targetLine: 0 },
            ],
            activeFilePath: '/home/user/project/src/main.go',
            pinnedFiles: ['/home/user/project/src/main.go'],
        };

        await saveEditorTabs('project-123', state);

        const loaded = await loadEditorTabs('project-123');
        expect(loaded).not.toBeNull();
        expect(loaded!.openFiles).toEqual(state.openFiles);
        expect(loaded!.activeFilePath).toEqual(state.activeFilePath);
        expect(loaded!.pinnedFiles).toEqual(state.pinnedFiles);
    });

    it('loadEditorTabs returns null for unknown project', async () => {
        const { loadEditorTabs } = await import('$lib/storage');

        const loaded = await loadEditorTabs('nonexistent-project');
        expect(loaded).toBeNull();
    });

    it('overwrites previous state on save', async () => {
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

        const loaded = await loadEditorTabs('project-789');
        expect(loaded!.openFiles).toEqual([{ path: '/new', targetLine: 10 }]);
        expect(loaded!.activeFilePath).toBe('/new');
        expect(loaded!.pinnedFiles).toEqual(['/new']);
    });

    it('isolates state between different projects', async () => {
        const { saveEditorTabs, loadEditorTabs } = await import('$lib/storage');

        await saveEditorTabs('proj-a', {
            openFiles: [{ path: '/a/file', targetLine: 1 }],
            activeFilePath: '/a/file',
            pinnedFiles: [],
        });

        await saveEditorTabs('proj-b', {
            openFiles: [{ path: '/b/file', targetLine: 2 }],
            activeFilePath: '/b/file',
            pinnedFiles: ['/b/file'],
        });

        const a = await loadEditorTabs('proj-a');
        const b = await loadEditorTabs('proj-b');

        expect(a!.openFiles[0].path).toBe('/a/file');
        expect(b!.openFiles[0].path).toBe('/b/file');
        expect(b!.pinnedFiles).toEqual(['/b/file']);
    });

    it('handles empty openFiles array', async () => {
        const { saveEditorTabs, loadEditorTabs } = await import('$lib/storage');

        await saveEditorTabs('empty-proj', {
            openFiles: [],
            activeFilePath: '',
            pinnedFiles: [],
        });

        const loaded = await loadEditorTabs('empty-proj');
        expect(loaded!.openFiles).toEqual([]);
        expect(loaded!.activeFilePath).toBe('');
        expect(loaded!.pinnedFiles).toEqual([]);
    });
});
