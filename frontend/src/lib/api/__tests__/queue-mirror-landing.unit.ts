import { describe, it, expect, beforeAll, beforeEach, vi } from 'vitest';
import type { Task } from '$lib/types';

// A mirror landing replays the queue onto the board, and `applyBoard` is the one place that does it:
// the replay is not idempotent, so a second pass opens a second card.
//
// No IndexedDB mock: the transform reads the in-memory queue.

function boardTask(id: string, status = 'backlog'): Task {
    return { id, project_id: 'p1', title: id, prompt: '', status, execution_state: 'idle' } as Task;
}

async function queueModule() {
    return await import('$lib/api/queue');
}

describe('a mirror board plus the pending queue', () => {
    beforeAll(async () => {
        await queueModule();
    }, 120_000);

    beforeEach(() => {
        vi.resetModules();
    });

    it('shows a queued createTask once, and names it as queued', async () => {
        const { enqueueAction, applyQueuedActionsToBoard, getQueuedTaskIds } = await queueModule();
        await enqueueAction({
            type: 'createTask',
            params: { projectId: 'p1', taskData: { id: 'opt-1', title: 'written offline' } },
        });

        const board = applyQueuedActionsToBoard([boardTask('t1')], 'p1');

        expect(board.map(t => t.id)).toEqual(['t1', 'opt-1']);
        expect(getQueuedTaskIds().has('opt-1')).toBe(true);
    });

    it('takes the card of a queued deleteTask off the board, as the in-session delete does', async () => {
        const { enqueueAction, applyQueuedActionsToBoard } = await queueModule();
        await enqueueAction({ type: 'deleteTask', params: { projectId: 'p1', taskId: 't1' } });

        const board = applyQueuedActionsToBoard([boardTask('t1'), boardTask('t2')], 'p1');

        // The in-session delete removes the card at once, so a mirror board must not keep it.
        expect(board.map(t => t.id)).toEqual(['t2']);
    });

    it('carries a queued move into the column the person dropped the card in', async () => {
        const { enqueueAction, applyQueuedActionsToBoard } = await queueModule();
        await enqueueAction({ type: 'moveTask', params: { projectId: 'p1', taskId: 't1', status: 'review' } });

        const board = applyQueuedActionsToBoard([boardTask('t1')], 'p1');

        expect(board[0]?.status).toBe('review');
    });

    it('is not idempotent: replaying one queue twice opens two cards', async () => {
        const { enqueueAction, applyQueuedActionsToBoard } = await queueModule();
        await enqueueAction({
            type: 'createTask',
            params: { projectId: 'p1', taskData: { id: 'opt-1', title: 'written offline' } },
        });

        const once = applyQueuedActionsToBoard([boardTask('t1')], 'p1');
        const twice = applyQueuedActionsToBoard(once, 'p1');

        expect(twice.filter(t => t.id === 'opt-1')).toHaveLength(2);
    });
});
