import { describe, it, expect } from 'vitest';
import {
    ancestorsOf,
    ancestorInProgress,
    countWithDescendants,
    filterTopLevelTaskIds,
    mergeTaskUpdateIntoBoard,
    planBulkMove,
    resolveBoardLandingColumn,
    resolveClosedTaskColumn,
    taskIsInFlight,
} from '$lib/utils/task-utils';
import type { Task } from '$lib/types';

function makeTask(id: string, parentId?: string, status = 'backlog'): Task {
    return {
        id,
        project_id: 'proj-1',
        title: id,
        prompt: '',
        status,
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

describe('resolveClosedTaskColumn', () => {
    const withStatuses = (statuses: Record<string, string>): Task[] =>
        Object.entries(statuses).map(([id, status]) => ({ ...makeTask(id), status } as Task));

    it('lands on Review after a Done move while other review tasks remain', () => {
        const tasks = withStatuses({ 't-1': 'review', 't-2': 'done', 't-3': 'backlog' });
        expect(resolveClosedTaskColumn('done', tasks, 't-2')).toBe('review');
    });

    it('lands on Backlog when nothing else is in review', () => {
        const tasks = withStatuses({ 't-1': 'backlog', 't-2': 'done' });
        expect(resolveClosedTaskColumn('done', tasks, 't-2')).toBe('backlog');
    });

    it('lands on Backlog rather than In Progress after a Done move', () => {
        // Deliberately not the project-switch preference, which would answer In Progress.
        const tasks = withStatuses({ 't-1': 'in_progress', 't-2': 'done' });
        expect(resolveClosedTaskColumn('done', tasks, 't-2')).toBe('backlog');
    });

    it('does not count the task that just closed, whose row still says review', () => {
        const tasks = withStatuses({ 't-1': 'review' });
        expect(resolveClosedTaskColumn('done', tasks, 't-1')).toBe('backlog');
    });

    it('lands on the task\'s own column for any other status', () => {
        const tasks = withStatuses({ 't-1': 'review', 't-2': 'in_progress' });
        expect(resolveClosedTaskColumn('in_progress', tasks, 't-2')).toBe('in_progress');
        expect(resolveClosedTaskColumn('backlog', tasks, 't-3')).toBe('backlog');
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

// `parent_task_id` is data a person or an agent writes — `PUT` checks only that the parent exists and is
// not the task itself — so a task can be pointed at its own descendant. The board reads that graph inside
// a `$derived`, where a walk that never ends is a tab that never paints again.
describe('a parent_task_id cycle', () => {
    const byIdOf = (tasks: Task[]) => new Map(tasks.map(t => [t.id, t]));
    const childrenByParent = (tasks: Task[]): Map<string, Task[]> => {
        const map = new Map<string, Task[]>();
        for (const t of tasks) {
            if (!t.parent_task_id) continue;
            map.set(t.parent_task_id, [...(map.get(t.parent_task_id) ?? []), t]);
        }
        return map;
    };

    const twoCycle = () => [makeTask('a', 'b', 'backlog'), makeTask('b', 'a', 'review')];
    const threeCycle = () => [
        makeTask('a', 'b', 'backlog'),
        makeTask('b', 'c', 'in_progress'),
        makeTask('c', 'a', 'review'),
    ];

    it('ends ancestorsOf at the repeat instead of hanging', () => {
        const two = twoCycle();
        const three = threeCycle();
        const findTwo = (id: string) => byIdOf(two).get(id);
        const findThree = (id: string) => byIdOf(three).get(id);

        expect(ancestorsOf(two[0], findTwo).map(t => t.id)).toEqual(['b']);
        expect(ancestorsOf(three[0], findThree).map(t => t.id)).toEqual(['b', 'c']);
    });

    // The shape that never ended: the row itself is outside the cycle, so no ancestor ever repeats its own
    // id, and its status matches none of the cycle's, so the old walk had nothing else to stop on.
    it('ends ancestorsOf on a cycle the row stands outside', () => {
        const tasks = [makeTask('x', 'a', 'backlog'), makeTask('a', 'b', 'review'), makeTask('b', 'a', 'in_progress')];
        const find = (id: string) => byIdOf(tasks).get(id);

        expect(ancestorsOf(tasks[0], find).map(t => t.id)).toEqual(['a', 'b']);
    });

    it('answers ancestorInProgress for a cycle', () => {
        const tasks = threeCycle();

        expect(ancestorInProgress(tasks, 'a')).toBe(true);
        expect(ancestorInProgress(twoCycle(), 'a')).toBe(false);
    });

    it('counts each task once in countWithDescendants', () => {
        const tasks = twoCycle();
        const children = childrenByParent(tasks);

        expect(countWithDescendants('a', 'review', children)).toBe(2);
        expect(countWithDescendants('a', 'backlog', children)).toBe(1);
    });

    it('plans a bulk move over a cycle', () => {
        const tasks = threeCycle();

        const plan = planBulkMove(tasks, ['a', 'b', 'c'], 'backlog');

        // 'a' is already in Backlog; the other two are not held by a decided ancestor.
        expect(plan.movable).toEqual(['b', 'c']);
        expect(plan.blocked).toEqual([]);
        expect(plan.alreadyThere).toBe(1);
    });
});

describe('ancestorInProgress', () => {
    it('holds for an in-progress ancestor however far up the chain', () => {
        const tasks = [
            makeTask('root', undefined, 'in_progress'),
            makeTask('child', 'root'),
            makeTask('grandchild', 'child'),
        ];

        expect(ancestorInProgress(tasks, 'grandchild')).toBe(true);
        expect(ancestorInProgress(tasks, 'root')).toBe(false);
    });

    it('ends at a parent the board does not hold', () => {
        expect(ancestorInProgress([makeTask('child', 'aboard-gone', 'in_progress')], 'child')).toBe(false);
    });
});

describe('countWithDescendants', () => {
    it('counts a task plus the matching descendants below a matching parent', () => {
        const tasks = [
            makeTask('root', undefined, 'review'),
            makeTask('child', 'root', 'review'),
            makeTask('grandchild', 'child', 'review'),
            makeTask('hidden', 'child', 'backlog'),
            makeTask('hidden-child', 'hidden', 'review'),
        ];
        const children = new Map<string, Task[]>();
        for (const t of tasks) {
            if (t.parent_task_id) children.set(t.parent_task_id, [...(children.get(t.parent_task_id) ?? []), t]);
        }

        // `hidden` is not in Review, so its Review child is not counted through it.
        expect(countWithDescendants('root', 'review', children)).toBe(3);
    });
});

describe('planBulkMove', () => {
    it('sends an ancestor before its descendants', () => {
        const tasks = [
            makeTask('root'),
            makeTask('child', 'root'),
            makeTask('grandchild', 'child'),
        ];

        const plan = planBulkMove(tasks, ['grandchild', 'root', 'child'], 'review');

        expect(plan.movable).toEqual(['root', 'child', 'grandchild']);
    });
});
