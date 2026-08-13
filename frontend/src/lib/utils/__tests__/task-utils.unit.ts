import { describe, it, expect } from 'vitest';
import { filterTopLevelTaskIds } from '$lib/utils/task-utils';
import type { Task } from '$lib/types';

function makeTask(id: string, parentId?: string): Task {
    return {
        id,
        project_id: 'proj-1',
        title: id,
        prompt: '',
        status: 'backlog',
        column_order: 0,
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
