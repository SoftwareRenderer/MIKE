import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { cleanup, fireEvent, waitFor } from '@testing-library/svelte';

// The global setup no-ops onMount; the panel loads its data in one.
vi.mock('svelte', async (importOriginal) => {
    const actual = await importOriginal<typeof import('svelte')>();
    return {
        ...actual,
        onMount: (cb: () => void | Promise<void>) => {
            void Promise.resolve().then(() => cb());
        },
    };
});

vi.mock('$lib/api/git', () => ({
    getGitStatus: vi.fn(),
    getProjectDiffFiles: vi.fn(),
    getProjectDiffFile: vi.fn(),
    stageProjectPaths: vi.fn(),
    discardProjectPaths: vi.fn(),
    commitProject: vi.fn(),
    pullProject: vi.fn(),
    pushProject: vi.fn(),
    getGitLogFull: vi.fn(),
    getCommitDiffFiles: vi.fn(),
}));

vi.mock('$lib/stores', () => ({
    dialogStore: {
        showToast: vi.fn(),
        showCommitDialog: vi.fn(async () => null),
        showConfirm: vi.fn(async () => true),
    },
}));

vi.mock('$lib/storage', () => ({
    loadAppSetting: vi.fn(async () => 240),
    saveAppSetting: vi.fn(async () => {}),
    // The diff pane the panel preloads keeps its collapse state here.
    loadCollapsedDiffFiles: vi.fn(async () => new Set<string>()),
    saveCollapsedDiffFiles: vi.fn(async () => {}),
}));

import {
    api, openPanel, openMenuOn, holdUnstaged, seedPanel, rowEl, selectedPaths, menu, menuLabels,
    menuItem, list,
} from './git-panel-harness';

beforeEach(() => {
    vi.clearAllMocks();
    seedPanel();
});

afterEach(() => cleanup());

describe('the changes list context menu', () => {
    it('offers the git actions, and only the ones the selected rows can take', async () => {
        const { container } = await openPanel();

        await openMenuOn(container, 'modified.ts');

        expect(menuLabels(container)).toEqual(['Stage', 'Unstage', 'Discard']);
        expect(menuItem(container, 'Stage').disabled).toBe(false);
        // An unstaged row has nothing in the index to take back out of it.
        expect(menuItem(container, 'Unstage').disabled).toBe(true);
    });

    it('names the other action for a row the index already holds', async () => {
        const { container } = await openPanel();

        await openMenuOn(container, 'staged-a.ts');

        expect(menuItem(container, 'Stage').disabled).toBe(true);
        expect(menuItem(container, 'Unstage').disabled).toBe(false);
    });

    it('points the selection at the row a right-click landed on', async () => {
        const { container } = await openPanel();
        await fireEvent.click(rowEl(container, 'modified.ts')!);

        await openMenuOn(container, 'scratch.ts');

        expect(selectedPaths(container)).toEqual(['scratch.ts']);
    });

    it('keeps the rows already held when the right-click lands on one of them', async () => {
        const { container } = await openPanel();
        await holdUnstaged(container);

        await openMenuOn(container, 'scratch.ts');

        expect(selectedPaths(container)).toEqual(['modified.ts', 'scratch.ts']);
        expect(menuItem(container, 'Stage').disabled).toBe(false);
    });

    // The keyboard has no right button either; the row under focus is the one it asks about.
    it('opens on the menu key over the focused row', async () => {
        const { container } = await openPanel();

        await fireEvent.keyDown(rowEl(container, 'staged-b.ts')!, { key: 'ContextMenu' });

        await waitFor(() => expect(menu(container)).not.toBeNull());
        expect(menuLabels(container)).toEqual(['Stage', 'Unstage', 'Discard']);
        expect(selectedPaths(container)).toEqual(['staged-b.ts']);
    });

    // The empty space below the rows is where the actions that take the whole section live.
    it('offers the section-wide actions below the rows', async () => {
        const { container } = await openPanel();

        await fireEvent.contextMenu(list(container), { clientX: 40, clientY: 200 });
        await waitFor(() => expect(menu(container)).not.toBeNull());

        expect(menuLabels(container)).toEqual(['Unstage all', 'Stage all']);

        fireEvent.click(menuItem(container, 'Stage all'));
        await waitFor(() => expect(api.stageProjectPaths).toHaveBeenCalledWith('p1', ['modified.ts', 'scratch.ts'], true));
    });
});

describe('the changes list batch actions', () => {
    it('sends the held files in one request and reloads once', async () => {
        const { container } = await openPanel();
        await holdUnstaged(container);
        await openMenuOn(container, 'modified.ts');
        const loadsBefore = api.getGitStatus.mock.calls.length;

        fireEvent.click(menuItem(container, 'Stage'));

        await waitFor(() => expect(api.stageProjectPaths).toHaveBeenCalledWith('p1', ['modified.ts', 'scratch.ts'], true));
        expect(api.stageProjectPaths).toHaveBeenCalledTimes(1);
        await waitFor(() => expect(api.getGitStatus.mock.calls.length).toBe(loadsBefore + 1));
    });

    it('takes only the rows the action can act on from a mixed selection', async () => {
        const { container } = await openPanel();
        await holdUnstaged(container);
        await fireEvent.click(rowEl(container, 'staged-a.ts')!, { ctrlKey: true });
        await openMenuOn(container, 'staged-a.ts');

        fireEvent.click(menuItem(container, 'Stage'));

        // staged-a.ts is held too, but its change is already in the index.
        await waitFor(() => expect(api.stageProjectPaths).toHaveBeenCalledTimes(1));
        expect(api.stageProjectPaths.mock.calls[0][1].slice().sort())
            .toEqual(['modified.ts', 'scratch.ts']);
        expect(api.stageProjectPaths.mock.calls[0][2]).toBe(true);
    });

    it('reports what git refused beside what it took', async () => {
        const { container } = await openPanel();
        await holdUnstaged(container);
        api.stageProjectPaths.mockImplementationOnce(async () => ({
            status: 'ok', action: 'staged', applied: ['modified.ts'],
            failed: [{ path: 'scratch.ts', error: 'git stage failed: index.lock exists' }],
        }));
        await openMenuOn(container, 'modified.ts');

        fireEvent.click(menuItem(container, 'Stage'));

        await waitFor(() => expect(api.showToast).toHaveBeenCalledWith(
            '1 file was not staged — git stage failed: index.lock exists', 'error',
        ));
    });

    it('asks once for the whole batch and says which shape of loss each file faces', async () => {
        const { container } = await openPanel();
        await holdUnstaged(container);
        await openMenuOn(container, 'modified.ts');

        fireEvent.click(menuItem(container, 'Discard'));

        await waitFor(() => expect(api.showConfirm).toHaveBeenCalled());
        expect(api.showConfirm.mock.calls[0][0]).toMatchObject({
            title: 'Discard changes?',
            message: 'Every file goes back to its last commit, and a file no commit holds is deleted from disk. Neither can be undone.',
            items: ['modified.ts', 'scratch.ts'],
            variant: 'destructive',
        });
        await waitFor(() => expect(api.discardProjectPaths).toHaveBeenCalledWith('p1', ['modified.ts', 'scratch.ts']));
        await waitFor(() => expect(api.showToast).toHaveBeenCalledWith('2 files discarded', 'success'));
    });

    it('sends nothing when the batch confirmation is declined', async () => {
        const { container } = await openPanel();
        await holdUnstaged(container);
        api.showConfirm.mockResolvedValueOnce(false);
        await openMenuOn(container, 'modified.ts');

        fireEvent.click(menuItem(container, 'Discard'));

        await waitFor(() => expect(api.showConfirm).toHaveBeenCalled());
        // A macrotask: the requests are queued behind the dialog's promise.
        await new Promise((resolve) => setTimeout(resolve, 0));

        expect(api.discardProjectPaths).not.toHaveBeenCalled();
        expect(api.showToast).not.toHaveBeenCalled();
    });

    it('counts what it could not discard, and keeps the rows that landed out of the list', async () => {
        const { container } = await openPanel();
        await holdUnstaged(container);
        // What the reload will find: scratch.ts is gone, modified.ts refused.
        seedPanel('M  staged-a.ts\nA  staged-b.ts\n M modified.ts');
        api.discardProjectPaths.mockImplementationOnce(async () => ({
            status: 'ok', action: 'discarded', applied: ['scratch.ts'], failed: [{ path: 'modified.ts', error: 'git discard failed: index.lock exists' }],
        }));
        await openMenuOn(container, 'modified.ts');

        fireEvent.click(menuItem(container, 'Discard'));

        await waitFor(() => expect(api.showToast).toHaveBeenCalledWith(
            '1 file was not discarded — git discard failed: index.lock exists', 'error',
        ));
        await waitFor(() => expect(rowEl(container, 'scratch.ts')).toBeNull());
        expect(rowEl(container, 'modified.ts')).not.toBeNull();
    });
});
