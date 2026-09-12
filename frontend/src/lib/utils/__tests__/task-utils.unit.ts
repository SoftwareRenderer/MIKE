import { describe, it, expect } from 'vitest';
import { filterTopLevelTaskIds, mergeTaskUpdateIntoBoard, resolveBoardLandingColumn, taskIsInFlight } from '$lib/utils/task-utils';
import type { Task } from '$lib/types';

function makeTask(id: string, parentId?: string): Task {
    return {
        id,
        project_id: 'proj-1',
        title: id,
        prompt: '',
        status: 'backlog',
        execution_state: 'idle',
        has_merge_conflict: false,
        max_consecutive_failed_turns: 5,
        auto_commit: false,
        auto_merge: false,
        subtasks_auto_merge: false,
        operating_mode: 'act',
        title_generating: false,
        summarizing: false,
        created_at: new Date().toISOString(),
        updated_at: new Date().toISOString(),
        moved_at: new Date().toISOString(),
        ...(parentId ? { parent_task_id: parentId } : {}),
    };
}

describe('filterTopLevelTaskIds', () => {
    const tasks = [
        makeTask('parent'),
        makeTask('child-1', 'parent'),
        makeTask('child-2', 'parent'),
        makeTask('grandchild', 'child-1'),
        makeTask('unrelated'),
    ];

    it('keeps all tasks when none has a parent in the selection', () => {
        expect(filterTopLevelTaskIds(['parent', 'unrelated'], tasks)).toEqual(['parent', 'unrelated']);
    });

    it('drops direct children whose parent is also selected', () => {
        expect(filterTopLevelTaskIds(['parent', 'child-1', 'child-2'], tasks)).toEqual(['parent']);
    });

    it('drops nested descendants recursively', () => {
        expect(filterTopLevelTaskIds(['parent', 'child-1', 'grandchild'], tasks)).toEqual(['parent']);
    });

    it('keeps a child when its parent is not selected', () => {
        expect(filterTopLevelTaskIds(['child-1', 'unrelated'], tasks)).toEqual(['child-1', 'unrelated']);
    });

    it('drops a grandchild when the intermediate child is selected', () => {
        // Deleting child-1 cascades to grandchild, so the grandchild is redundant.
        expect(filterTopLevelTaskIds(['child-1', 'grandchild'], tasks)).toEqual(['child-1']);
    });

    it('keeps unknown task IDs not present in the task list', () => {
        expect(filterTopLevelTaskIds(['parent', 'ghost'], tasks)).toEqual(['parent', 'ghost']);
    });

    it('returns an empty array for an empty selection', () => {
        expect(filterTopLevelTaskIds([], tasks)).toEqual([]);
    });

    it('preserves input order of the remaining top-level tasks', () => {
        expect(filterTopLevelTaskIds(['child-2', 'parent', 'child-1'], tasks)).toEqual(['parent']);
        expect(filterTopLevelTaskIds(['unrelated', 'child-1', 'grandchild'], tasks)).toEqual(['unrelated', 'child-1']);
    });
});

describe('resolveBoardLandingColumn', () => {
    /** Board payload with the given statuses (ids are irrelevant to the resolver). */
    const withStatuses = (...statuses: string[]): Task[] =>
        statuses.map((status, i) => ({ ...makeTask(`t-${i}`), status } as Task));

    it('lands on Review when a review task exists', () => {
        expect(resolveBoardLandingColumn(withStatuses('backlog', 'review', 'done'))).toBe('review');
    });

    it('prefers Review over In Progress when both exist', () => {
        expect(resolveBoardLandingColumn(withStatuses('in_progress', 'review'))).toBe('review');
    });

    it('lands on In Progress when nothing is in review', () => {
        expect(resolveBoardLandingColumn(withStatuses('backlog', 'in_progress', 'done'))).toBe('in_progress');
    });

    it('lands on Backlog when nothing is in review or in progress', () => {
        expect(resolveBoardLandingColumn(withStatuses('backlog', 'done'))).toBe('backlog');
    });

    it('lands on Backlog for a Done-only board', () => {
        expect(resolveBoardLandingColumn(withStatuses('done', 'done'))).toBe('backlog');
    });

    it('lands on Backlog for an empty board', () => {
        expect(resolveBoardLandingColumn([])).toBe('backlog');
    });

    it('counts consolidated subtasks, not just visible cards', () => {
        // A subtask in review collapsed into an in-progress parent is still in the payload.
        const parent = { ...makeTask('parent'), status: 'in_progress' } as Task;
        const child = { ...makeTask('child', 'parent'), status: 'review' } as Task;
        expect(resolveBoardLandingColumn([parent, child])).toBe('review');
    });
});

describe('taskIsInFlight', () => {
    // The whole vocabulary of backend/types/exec_state.go is pinned: a state added there and
    // missed here leaves a stale card spinning.
    it('is in flight only in the states whose turn can still send a frame', () => {
        expect(taskIsInFlight('running')).toBe(true);
        expect(taskIsInFlight('pulling_image')).toBe(true);
        expect(taskIsInFlight('idle')).toBe(false);
        expect(taskIsInFlight('paused')).toBe(false);
        expect(taskIsInFlight('completed')).toBe(false);
        expect(taskIsInFlight('failed')).toBe(false);
        expect(taskIsInFlight('')).toBe(false);
    });

    it('says a stopped task is NOT in flight, which is why it cannot be asked by status', () => {
        // The premise: stopping does not move status.
        const stopped = { status: 'in_progress', execution_state: 'paused' } as Task;
        expect(stopped.status).toBe('in_progress');
        expect(taskIsInFlight(stopped.execution_state)).toBe(false);
    });
});

describe('mergeTaskUpdateIntoBoard', () => {
    // A task_updated frame names its own project, and the server replays the board frames it
    // buffered for this connection — including ones from a project the client has switched away
    // from. Appending such a frame plants a foreign card on the board, where it stays until the
    // next board fetch.
    it('refuses a task of another project, which is what mixed two projects onto one board', () => {
        const board = [makeTask('mine')];
        const foreign = { ...makeTask('theirs'), project_id: 'proj-2' };

        expect(mergeTaskUpdateIntoBoard(board, foreign, 'proj-1')).toBeNull();
        expect(board.map(t => t.id)).toEqual(['mine']);
    });

    it('refuses a task whose project the frame does not name', () => {
        const board = [makeTask('mine')];
        const unknown = { ...makeTask('newcomer'), project_id: '' };

        expect(mergeTaskUpdateIntoBoard(board, unknown, 'proj-1')).toBeNull();
        expect(board).toHaveLength(1);
    });

    it('adds a task of the project on screen, so a task created elsewhere still arrives', () => {
        const board = [makeTask('mine')];
        const arrived = makeTask('created-on-another-tab');

        const next = mergeTaskUpdateIntoBoard(board, arrived, 'proj-1');

        expect(next?.map(t => t.id)).toEqual(['mine', 'created-on-another-tab']);
    });

    it('refreshes a task already on the board, whatever project the frame claims', () => {
        const board = [{ ...makeTask('mine'), status: 'backlog' }];
        const moved = { ...makeTask('mine'), status: 'review', project_id: 'proj-2' };

        const next = mergeTaskUpdateIntoBoard(board, moved, 'proj-1');

        expect(next?.[0]?.status).toBe('review');
        expect(next).toHaveLength(1);
    });

    it('answers null when the frame repeats the row already on the board', () => {
        const row = makeTask('mine');

        expect(mergeTaskUpdateIntoBoard([row], row, 'proj-1')).toBeNull();
    });
});
