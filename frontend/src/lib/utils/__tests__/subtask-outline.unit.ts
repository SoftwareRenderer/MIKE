import { describe, it, expect } from 'vitest';
import { subtaskOutlineGroups } from '$lib/utils/subtask-outline';
import type { Task } from '$lib/types';

/** `moved` is minutes from the epoch, so sibling order is something a test states, not inherits. */
function makeTask(id: string, opts: { status?: string; parent?: string; moved?: number } = {}): Task {
    const at = new Date((opts.moved ?? 0) * 60_000).toISOString();
    return {
        id,
        project_id: 'proj-1',
        title: id,
        prompt: '',
        status: opts.status ?? 'backlog',
        execution_state: 'idle',
        has_merge_conflict: false,
        max_consecutive_failed_turns: 5,
        auto_commit: false,
        auto_merge: false,
        subtasks_auto_merge: false,
        operating_mode: 'act',
        title_generating: false,
        summarizing: false,
        created_at: at,
        updated_at: at,
        moved_at: at,
        ...(opts.parent ? { parent_task_id: opts.parent } : {}),
    };
}

function mapOf(tasks: Task[]): Map<string, Task[]> {
    const map = new Map<string, Task[]>();
    for (const t of tasks) {
        if (!t.parent_task_id) continue;
        const kids = map.get(t.parent_task_id) ?? [];
        kids.push(t);
        map.set(t.parent_task_id, kids);
    }
    return map;
}

/**
 * A plan four levels deep:
 *   a(done) ├ a1(done) ─ a1a(done)   └ a2(done)
 *   b       ├ b1       ├ b1a(review) └ b1b(done)  └ b2
 *   c       ├ c1  └ c2
 *   d
 */
function plan() {
    const tasks = [
        makeTask('root'),
        makeTask('a', { status: 'done', parent: 'root' }),
        makeTask('a1', { status: 'done', parent: 'a' }),
        makeTask('a1a', { status: 'done', parent: 'a1' }),
        makeTask('a2', { status: 'done', parent: 'a' }),
        makeTask('b', { status: 'in_progress', parent: 'root' }),
        makeTask('b1', { status: 'in_progress', parent: 'b' }),
        makeTask('b1a', { status: 'review', parent: 'b1' }),
        makeTask('b1b', { status: 'done', parent: 'b1' }),
        makeTask('b2', { parent: 'b' }),
        makeTask('c', { parent: 'root' }),
        makeTask('c1', { parent: 'c' }),
        makeTask('c2', { parent: 'c' }),
        makeTask('d', { parent: 'root' }),
    ];
    const allExpanded = new Set(['root', 'a', 'a1', 'b', 'b1', 'c']);
    return { childrenMap: mapOf(tasks), expanded: allExpanded };
}

const ids = (groups: ReturnType<typeof subtaskOutlineGroups>) => groups.flatMap(g => g.rows.map(r => r.task.id));

describe('subtaskOutlineGroups', () => {
    it('lists a row after every descendant of its own, which is when the executor can start it', () => {
        const { childrenMap, expanded } = plan();
        const groups = subtaskOutlineGroups('root', childrenMap, expanded);
        const order = ids(groups);

        expect(order).toEqual(['a1a', 'a2', 'a1', 'a', 'b1a', 'b1b', 'b2', 'b1', 'b', 'c1', 'c2', 'c', 'd']);

        for (const group of groups) {
            for (const row of group.rows) {
                for (const descendant of group.rows) {
                    if (descendant.owners.includes(row.task.id)) {
                        expect(order.indexOf(descendant.task.id), `${descendant.task.id} waits for ${row.task.id}`)
                            .toBeLessThan(order.indexOf(row.task.id));
                    }
                }
            }
        }
    });

    it('keeps each top-level branch in its own contiguous group', () => {
        const { childrenMap, expanded } = plan();
        const groups = subtaskOutlineGroups('root', childrenMap, expanded);
        expect(groups.map(g => g.root.id)).toEqual(['a', 'b', 'c', 'd']);
        expect(groups.map(g => g.rows.map(r => r.task.id))).toEqual([
            ['a1a', 'a2', 'a1', 'a'],
            ['b1a', 'b1b', 'b2', 'b1', 'b'],
            ['c1', 'c2', 'c'],
            ['d'],
        ]);
    });

    it('never lists the card whose subtasks these are', () => {
        const { childrenMap, expanded } = plan();
        expect(ids(subtaskOutlineGroups('root', childrenMap, expanded))).not.toContain('root');

        // One level down the card is `a`, so its two children are each their own group.
        const underA = subtaskOutlineGroups('a', childrenMap, expanded);
        expect(underA.map(g => g.root.id)).toEqual(['a1', 'a2']);
        expect(underA.map(g => g.rows.map(r => r.task.id))).toEqual([['a1a', 'a1'], ['a2']]);
    });

    it('reports the wave of each row and the ancestors that own it', () => {
        const { childrenMap, expanded } = plan();
        const byId = new Map(subtaskOutlineGroups('root', childrenMap, expanded).flatMap(g => g.rows).map(r => [r.task.id, r]));

        expect(byId.get('a1a')).toMatchObject({ wave: 1, owners: ['a', 'a1'] });
        expect(byId.get('a2')).toMatchObject({ wave: 1, owners: ['a'] });
        expect(byId.get('a1')).toMatchObject({ wave: 2, owners: ['a'] });
        expect(byId.get('a')).toMatchObject({ wave: 3, owners: [] });
    });

    it('counts every descendant in a row’s progress, including the rows a collapse hides', () => {
        const { childrenMap, expanded } = plan();
        const progress = (id: string, exp = expanded) => {
            const row = subtaskOutlineGroups('root', childrenMap, exp).flatMap(g => g.rows).find(r => r.task.id === id);
            return row?.progress;
        };

        expect(progress('a1')).toEqual({ done: 1, total: 1 });
        expect(progress('a')).toEqual({ done: 3, total: 3 });
        expect(progress('b1')).toEqual({ done: 1, total: 2 });
        expect(progress('b')).toEqual({ done: 1, total: 4 });
        expect(progress('c')).toEqual({ done: 0, total: 2 });
        expect(progress('d')).toEqual({ done: 0, total: 0 });

        // Hide a1's branch: its own row stays (it is the control) and still says 1 of 1.
        expect(progress('a1', new Set(['root', 'a', 'b', 'b1', 'c']))).toEqual({ done: 1, total: 1 });
    });

    it('drops a collapsed branch and leaves its siblings where they were', () => {
        const { childrenMap } = plan();

        const collapsedLeafParent = subtaskOutlineGroups('root', childrenMap, new Set(['root', 'a', 'b', 'b1', 'c']));
        expect(collapsedLeafParent.map(g => g.rows.map(r => r.task.id))).toEqual([
            ['a2', 'a1', 'a'],
            ['b1a', 'b1b', 'b2', 'b1', 'b'],
            ['c1', 'c2', 'c'],
            ['d'],
        ]);

        const collapsedGroup = subtaskOutlineGroups('root', childrenMap, new Set(['root', 'a1', 'b', 'b1', 'c']));
        expect(collapsedGroup.map(g => g.rows.map(r => r.task.id))).toEqual([['a'], ['b1a', 'b1b', 'b2', 'b1', 'b'], ['c1', 'c2', 'c'], ['d']]);
    });

    it('orders siblings the way the column does, in either direction', () => {
        const tasks = [
            makeTask('root'),
            makeTask('p', { parent: 'root' }),
            makeTask('older', { parent: 'p', moved: 1 }),
            makeTask('newer', { parent: 'p', moved: 9 }),
        ];
        const childrenMap = mapOf(tasks);
        const expanded = new Set(['root', 'p']);

        expect(ids(subtaskOutlineGroups('root', childrenMap, expanded, false))).toEqual(['newer', 'older', 'p']);
        expect(ids(subtaskOutlineGroups('root', childrenMap, expanded, true))).toEqual(['older', 'newer', 'p']);
    });

    it('sorts by wave before sibling order, so a ready leaf outruns a deeper sibling', () => {
        const tasks = [
            makeTask('root'),
            makeTask('p', { parent: 'root' }),
            makeTask('deep-parent', { parent: 'p', moved: 90 }),
            makeTask('deep-leaf', { parent: 'deep-parent', moved: 1 }),
            makeTask('ready-leaf', { parent: 'p', moved: 2 }),
        ];
        const expanded = new Set(['root', 'p', 'deep-parent']);

        expect(ids(subtaskOutlineGroups('root', mapOf(tasks), expanded))).toEqual(['deep-leaf', 'ready-leaf', 'deep-parent', 'p']);
    });

    it('returns no groups for a card without subtasks', () => {
        const { childrenMap, expanded } = plan();
        expect(subtaskOutlineGroups('d', childrenMap, expanded)).toEqual([]);
        expect(subtaskOutlineGroups('missing', childrenMap, expanded)).toEqual([]);
    });

    it('terminates when parent_task_id contains a cycle', () => {
        const loopA = makeTask('loop-a', { parent: 'root' });
        const loopB = makeTask('loop-b', { parent: 'loop-a' });
        const childrenMap = new Map<string, Task[]>([
            ['root', [loopA]],
            ['loop-a', [loopB]],
            ['loop-b', [loopA]],
        ]);

        const groups = subtaskOutlineGroups('root', childrenMap, new Set(['root', 'loop-a', 'loop-b']));

        expect(groups.map(g => g.rows.map(r => r.task.id))).toEqual([['loop-b', 'loop-a']]);
    });
});
