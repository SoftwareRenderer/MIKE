import { describe, it, expect, beforeAll, beforeEach, vi } from 'vitest';
import type { Task } from '$lib/types';

// The queue holds every project's writes; the board shows one project, and a mirror landing replays the queue
// onto what the browser holds (`applyBoard` is the one place that does it). Two decisions follow: an action
// belongs to the project it names — one from another project would add a card that is not this board's — and
// the replay is an overlay, so it may run over a board that already carries the card without opening a second
// row for one id (`each_key_duplicate` in the column that draws them).
//
// No IndexedDB mock: the transform reads the in-memory queue, and dbPut swallows an unavailable database.

function boardTask(id: string, status = 'backlog', projectId = 'p2'): Task {
    return { id, project_id: projectId, title: id, prompt: '', status, execution_state: 'idle' } as Task;
}

async function queueModule() {
    const mod = await import('$lib/api/queue');
    return mod;
}

describe('the queue replayed onto a board', () => {
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

    // A board rebuilt while a dismissal waits must still look dismissed.
    it('keeps a queued dismissal on the row the board shows', async () => {
        const { enqueueAction, applyQueuedActionsToBoard } = await queueModule();
        const ack = { peak_mb: 1024, oom_kills: 2, limit_mb: 1024 };
        await enqueueAction({ type: 'dismissMemoryAlert', params: { projectId: 'p2', taskId: 'mine', ack } });
        await enqueueAction({ type: 'dismissMemoryAlert', params: { projectId: 'p1', taskId: 'theirs', ack } });

        const board = applyQueuedActionsToBoard(
            [boardTask('mine', 'in_progress'), boardTask('theirs', 'in_progress', 'p1')],
            'p2',
        );

        expect(board.find(t => t.id === 'mine')?.memory_alert_ack).toEqual(ack);
        expect(board.find(t => t.id === 'theirs')?.memory_alert_ack).toBeUndefined();
    });

    it('shows a queued createTask once, names it as queued, and survives being overlaid again', async () => {
        const { enqueueAction, applyQueuedActionsToBoard, getQueuedTaskIds } = await queueModule();
        await enqueueAction({
            type: 'createTask',
            params: { projectId: 'p2', taskData: { id: 'opt-1', title: 'written offline' } },
        });

        const once = applyQueuedActionsToBoard([boardTask('t1')], 'p2');
        expect(once.map(t => t.id)).toEqual(['t1', 'opt-1']);
        expect(getQueuedTaskIds().has('opt-1')).toBe(true);

        // The card's id is the one the click minted, so a board holding it was overlaid once already.
        const twice = applyQueuedActionsToBoard(once, 'p2');
        expect(twice.filter(t => t.id === 'opt-1')).toHaveLength(1);
        expect(twice.map(t => t.id)).toEqual(['t1', 'opt-1']);
    });

    it('takes the card of a queued deleteTask off the board, as the in-session delete does', async () => {
        const { enqueueAction, applyQueuedActionsToBoard } = await queueModule();
        await enqueueAction({ type: 'deleteTask', params: { projectId: 'p2', taskId: 't1' } });

        const board = applyQueuedActionsToBoard([boardTask('t1'), boardTask('t2')], 'p2');

        // The in-session delete removes the card at once, so a mirror board must not keep it.
        expect(board.map(t => t.id)).toEqual(['t2']);
    });
});
