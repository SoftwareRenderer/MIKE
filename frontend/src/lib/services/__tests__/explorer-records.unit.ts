/**
 * The board is what says a task's explorer state is no longer wanted.
 *
 * Done is judged as a **move into** it, so the record an explorer writes afterwards — the files a person
 * opens while reading a finished task — is left alone until that task leaves the board. A row the board no
 * longer holds is deleted, whoever deleted it, and a board reading this browser has already judged asks
 * nothing of IndexedDB twice.
 */
import { describe, it, expect, vi, beforeEach } from 'vitest';
import type { Task } from '$lib/types';
import { appStore } from '$lib/stores';

const { forgotten, pruned, openPaths } = vi.hoisted(() => ({
    forgotten: vi.fn(),
    pruned: vi.fn(),
    openPaths: vi.fn((): Set<string> => new Set()),
}));

vi.mock('$lib/storage', async (importOriginal) => ({
    ...(await importOriginal<typeof import('$lib/storage')>()),
    forgetTaskExplorer: (projectId: string, taskId: string, options: unknown) => {
        forgotten(projectId, taskId, options);
        return Promise.resolve();
    },
    pruneTaskExplorerRecords: (projectId: string, liveTaskIds: ReadonlySet<string>, options: unknown) => {
        pruned(projectId, liveTaskIds, options);
        return Promise.resolve();
    },
}));

vi.mock('$lib/services/explorer-core.svelte', () => ({
    openExplorerPaths: () => openPaths(),
}));

import { explorerRecords } from '$lib/services/explorer-records';

const PROJECT = 'p1';

function task(id: string, status: string): Task {
    return { id, project_id: PROJECT, title: id, prompt: '', status } as Task;
}

/** The paths an explorer this browser has mounted is holding open. */
function held(...paths: string[]): void {
    openPaths.mockReturnValue(new Set(paths));
}

beforeEach(() => {
    vi.clearAllMocks();
    openPaths.mockReturnValue(new Set());
    explorerRecords.reset();
    appStore.config = { workspace_dir: '/srv/ws' } as never;
});

describe('a task that finished', () => {
    it('forgets its explorer state on the move into Done', () => {
        explorerRecords.observeBoard(PROJECT, [task('t1', 'review')]);
        explorerRecords.observeBoard(PROJECT, [task('t1', 'done')]);

        expect(forgotten).toHaveBeenCalledWith(PROJECT, 't1', {
            workspaceDir: '/srv/ws',
            heldPaths: new Set<string>(),
        });
    });

    it('asks once: a board that still holds the Done task does not sweep it again', () => {
        explorerRecords.observeBoard(PROJECT, [task('t1', 'review')]);
        explorerRecords.observeBoard(PROJECT, [task('t1', 'done')]);
        forgotten.mockClear();

        // The same board reported again — a re-render, a frame about something else — and a frame that
        // changes another task: neither is news about t1.
        explorerRecords.observeBoard(PROJECT, [task('t1', 'done')]);
        explorerRecords.observeBoard(PROJECT, [task('t1', 'done'), task('t2', 'backlog')]);
        explorerRecords.observeBoard(PROJECT, [task('t2', 'backlog'), task('t1', 'done')]);

        expect(forgotten).not.toHaveBeenCalled();
    });

    it('leaves the state an explorer wrote after the task was Done for the deletion to take', () => {
        explorerRecords.observeBoard(PROJECT, [task('t1', 'done')]);
        // The first look only seeds the ledger: a task already finished when this browser arrived has had
        // its one drop, and the files a person opened since are what the record holds now.
        expect(forgotten).not.toHaveBeenCalled();

        explorerRecords.observeBoard(PROJECT, [task('t1', 'in_progress')]);
        explorerRecords.observeBoard(PROJECT, [task('t1', 'done')]);
        expect(forgotten).toHaveBeenCalledTimes(1);
    });

    it('never touches another task of the board', () => {
        explorerRecords.observeBoard(PROJECT, [task('t1', 'review'), task('t2', 'review')]);
        explorerRecords.observeBoard(PROJECT, [task('t1', 'done'), task('t2', 'done')]);
        forgotten.mockClear();

        explorerRecords.observeBoard(PROJECT, [task('t1', 'done'), task('t2', 'done'), task('t3', 'backlog')]);

        expect(forgotten).not.toHaveBeenCalled();
    });
});

describe('a task the board no longer holds', () => {
    it('is pruned with the ids the board does hold', () => {
        explorerRecords.observeBoard(PROJECT, [task('t1', 'backlog'), task('t2', 'backlog')]);
        explorerRecords.observeBoard(PROJECT, [task('t1', 'backlog')]);

        expect(pruned).toHaveBeenLastCalledWith(PROJECT, new Set(['t1']), {
            workspaceDir: '/srv/ws',
            heldPaths: new Set<string>(),
        });
    });

    it('is pruned on the first look too, because a deletion elsewhere never passed through here', () => {
        explorerRecords.observeBoard(PROJECT, [task('t1', 'backlog')]);

        expect(pruned).toHaveBeenCalledWith(PROJECT, new Set(['t1']), expect.anything());
    });

    it('hands the sweep the files an open editor is standing in', () => {
        held('/repos/alpha/README.md');
        explorerRecords.observeBoard(PROJECT, [task('t1', 'backlog')]);

        expect(pruned).toHaveBeenCalledWith(PROJECT, new Set(['t1']), {
            workspaceDir: '/srv/ws',
            heldPaths: new Set(['/repos/alpha/README.md']),
        });
    });
});

describe('a board this browser has already judged', () => {
    it('asks nothing of IndexedDB again', () => {
        explorerRecords.observeBoard(PROJECT, [task('t1', 'backlog')]);
        pruned.mockClear();

        explorerRecords.observeBoard(PROJECT, [task('t1', 'backlog')]);

        expect(pruned).not.toHaveBeenCalled();
    });

    it('judges each project by its own board', () => {
        explorerRecords.observeBoard(PROJECT, [task('t1', 'review')]);
        pruned.mockClear();

        // The other project's board holds none of this project's tasks; that is a switch, not a deletion.
        explorerRecords.observeBoard('p2', [task('t9', 'backlog')]);

        expect(pruned).toHaveBeenCalledWith('p2', new Set(['t9']), expect.anything());
        explorerRecords.observeBoard(PROJECT, [task('t1', 'review')]);
        expect(pruned).toHaveBeenCalledTimes(1);
    });

    it('has no workspace to name without the config, and still sweeps the record', () => {
        appStore.config = null as never;
        explorerRecords.observeBoard(PROJECT, [task('t1', 'review')]);
        explorerRecords.observeBoard(PROJECT, [task('t1', 'done')]);

        expect(forgotten).toHaveBeenCalledWith(PROJECT, 't1', {
            workspaceDir: '',
            heldPaths: new Set<string>(),
        });
    });
});
