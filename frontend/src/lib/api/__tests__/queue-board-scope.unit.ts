import { describe, it, expect, beforeAll, beforeEach, vi } from 'vitest';
import type { Task } from '$lib/types';

// The queue holds every project's writes; the board shows one project. Replayed without the
// project filter, a queued createTask of project 1 materialised as a card on project 2's board.
//
// No IndexedDB mock: dbPut swallows an unavailable database, and the transform reads the
// in-memory queue.

function boardTask(id: string, status: string, projectId = 'p2'): Task {
    return { id, project_id: projectId, title: id, prompt: '', status, execution_state: 'idle' } as Task;
}

async function queueModule() {
    const mod = await import('$lib/api/queue');
    return mod;
}

describe('applyQueuedActionsToBoard project scope', () => {
    // Every test wants a queue of its own, so each re-imports the module after a registry reset.
    // The first import of the api graph costs more than a test's timeout on a loaded machine, so it
    // is paid for once here; a reset after that only re-instantiates transformed modules.
    beforeAll(async () => {
        await queueModule();
    }, 120_000);

    beforeEach(() => {
        vi.resetModules();
    });

    it('keeps a queued createTask of another project off this board', async () => {
        const { enqueueAction, applyQueuedActionsToBoard } = await queueModule();
        await enqueueAction({ type: 'createTask', params: { projectId: 'p1', taskData: { title: 'from p1' } } });
        await enqueueAction({ type: 'createTask', params: { projectId: 'p2', taskData: { title: 'from p2' } } });

        const board = applyQueuedActionsToBoard([], 'p2');

        expect(board.map(t => t.title)).toEqual(['from p2']);
        expect(board[0]?.project_id).toBe('p2');
    });

    it('clears the Done column only for the project whose action it was', async () => {
        const { enqueueAction, applyQueuedActionsToBoard } = await queueModule();
        await enqueueAction({ type: 'clearDoneTasks', params: { projectId: 'p1' } });

        const board = applyQueuedActionsToBoard([boardTask('kept', 'done')], 'p2');

        expect(board.map(t => t.id)).toEqual(['kept']);
    });

    it('moves this project task and leaves the other project task alone', async () => {
        const { enqueueAction, applyQueuedActionsToBoard } = await queueModule();
        await enqueueAction({ type: 'moveTask', params: { projectId: 'p1', taskId: 'theirs', status: 'review' } });
        await enqueueAction({ type: 'moveTask', params: { projectId: 'p2', taskId: 'mine', status: 'review' } });

        const board = applyQueuedActionsToBoard(
            [boardTask('mine', 'in_progress'), boardTask('theirs', 'in_progress', 'p1')],
            'p2',
        );

        expect(board.find(t => t.id === 'mine')?.status).toBe('review');
        expect(board.find(t => t.id === 'theirs')?.status).toBe('in_progress');
    });

    it('still replays every action when no project is named', async () => {
        const { enqueueAction, applyQueuedActionsToBoard } = await queueModule();
        await enqueueAction({ type: 'createTask', params: { projectId: 'p1', taskData: { title: 'from p1' } } });

        const unscoped = applyQueuedActionsToBoard([]);

        expect(unscoped.map(t => t.title)).toEqual(['from p1']);
    });
});
