import { describe, it, expect, vi } from 'vitest';
import { ExplorerCore } from '$lib/services/explorer-core.svelte';

type ExplorerEdges = ConstructorParameters<typeof ExplorerCore>[0];

// The address bar names a file and never a line, so a tab list cleared and re-opened from the URL loses
// the line a go-to-definition jump asked for. `resetTabs` is what clears it, so it answers to the subject
// it was called for rather than to every re-run of the screen's effect.

const ROOT = '/srv/proj';

vi.mock('$lib/storage', async (importOriginal) => ({
    ...(await importOriginal<typeof import('$lib/storage')>()),
    loadEditorTabs: async () => null,
    saveEditorTabs: async () => {},
    loadUnsavedFiles: async () => [],
    saveUnsavedFiles: async () => {},
}));

function core(): ExplorerCore {
    const edges: ExplorerEdges = {
        storageKey: () => 'explorer-test',
        pathRoots: () => [ROOT],
        isMobile: { isMobile: false },
        fsEvents: () => [],
        lineSelectionParent: () => null,
        updateFile: () => {},
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

describe('ExplorerCore.resetTabs', () => {
    it('keeps the jumped-to line when the same subject re-runs its effect', () => {
        const c = core();
        c.resetTabs('task-1');                         // the mount
        c.handleFileSelected(`${ROOT}/a.go`, 6, true); // the jump that opened this explorer
        c.resetTabs('task-1');                         // a re-run for the subject already shown

        expect(c.openFiles.map(f => [f.path, f.targetLine])).toEqual([[`${ROOT}/a.go`, 6]]);
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
