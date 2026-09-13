import { describe, it, expect } from 'vitest';
import { ancestorInProgress, willConsolidateIntoParent, planBulkMove } from '$lib/utils/task-utils';
import type { Task } from '$lib/types';

function makeTask(id: string, status: string, parentId?: string): Task {
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

/** backlog root → in_progress child → review grandchild */
function family(): Task[] {
    return [
        makeTask('root', 'backlog'),
        makeTask('child', 'in_progress', 'root'),
        makeTask('grandchild', 'review', 'child'),
        makeTask('other', 'backlog'),
    ];
}

describe('ancestorInProgress', () => {
    it('finds an In Progress ancestor at any depth', () => {
        expect(ancestorInProgress(family(), 'grandchild')).toBe(true);
    });

    it('is false for a top-level task and for a task with no parents in this list', () => {
        expect(ancestorInProgress(family(), 'root')).toBe(false);
        expect(ancestorInProgress(family(), 'unknown-id')).toBe(false);
    });

    it('ignores a chain that never reaches an In Progress task', () => {
        const tasks = [makeTask('root', 'review'), makeTask('child', 'backlog', 'root')];
        expect(ancestorInProgress(tasks, 'child')).toBe(false);
    });

    it('stops on a parent cycle instead of looping forever', () => {
        const tasks = [makeTask('a', 'backlog', 'b'), makeTask('b', 'backlog', 'a')];
        expect(ancestorInProgress(tasks, 'a')).toBe(false);
    });
});

describe('willConsolidateIntoParent', () => {
    it('is true for a subtask moved into the column its parent already holds', () => {
        const tasks = [makeTask('root', 'review'), makeTask('child', 'backlog', 'root')];
        expect(willConsolidateIntoParent(tasks[1], tasks, 'review')).toBe(true);
    });

    it('is false for a top-level task and for a parent in another column', () => {
        const tasks = [makeTask('root', 'review'), makeTask('child', 'backlog', 'root')];
        expect(willConsolidateIntoParent(tasks[0], tasks, 'review')).toBe(false);
        expect(willConsolidateIntoParent(tasks[1], tasks, 'done')).toBe(false);
    });
});

describe('planBulkMove', () => {
    it('leaves tasks that are already in the target column out of the batch', () => {
        const plan = planBulkMove(family(), ['root', 'other'], 'backlog');
        expect(plan.movable).toEqual([]);
        expect(plan.alreadyThere).toBe(2);
        expect(plan.blocked).toEqual([]);
    });

    it('drops ids the board does not hold without counting them as already there', () => {
        const plan = planBulkMove(family(), ['other', 'gone'], 'done');
        expect(plan.movable).toEqual(['other']);
        expect(plan.alreadyThere).toBe(0);
    });

    it('reports a subtask whose In Progress parent stays where it is instead of sending it', () => {
        const plan = planBulkMove(family(), ['grandchild'], 'backlog');
        expect(plan.blocked).toEqual(['grandchild']);
        expect(plan.movable).toEqual([]);
    });

    it('does not block a subtask whose In Progress parent is in the same batch, and moves the parent first', () => {
        // Selected child-first; the parent must reach Backlog before the child may.
        const plan = planBulkMove(family(), ['grandchild', 'child'], 'backlog');
        expect(plan.blocked).toEqual([]);
        expect(plan.movable).toEqual(['child', 'grandchild']);
    });

    it('applies the Backlog rule to no other column', () => {
        const plan = planBulkMove(family(), ['grandchild'], 'done');
        expect(plan.movable).toEqual(['grandchild']);
        expect(plan.blocked).toEqual([]);
    });

    it('blocks a descendant whose blocked ancestor never leaves In Progress', () => {
        // `child` is refused and stays In Progress, so it holds the grandchild with it.
        const tasks = [
            makeTask('root', 'in_progress'),
            makeTask('child', 'in_progress', 'root'),
            makeTask('grandchild', 'review', 'child'),
        ];
        const plan = planBulkMove(tasks, ['child', 'grandchild'], 'backlog');
        expect(plan.blocked).toEqual(['child', 'grandchild']);
        expect(plan.movable).toEqual([]);
    });

    it('orders a mixed selection ancestors before descendants', () => {
        const tasks = [
            makeTask('root', 'backlog'),
            makeTask('child', 'backlog', 'root'),
            makeTask('leaf', 'backlog', 'child'),
        ];
        const plan = planBulkMove(tasks, ['leaf', 'root', 'child'], 'done');
        expect(plan.movable).toEqual(['root', 'child', 'leaf']);
    });

    it('counts a selected task already in the column while moving its sibling', () => {
        const tasks = [makeTask('a', 'backlog'), makeTask('b', 'review')];
        const plan = planBulkMove(tasks, ['a', 'b'], 'review');
        expect(plan.movable).toEqual(['a']);
        expect(plan.alreadyThere).toBe(1);
    });
});
