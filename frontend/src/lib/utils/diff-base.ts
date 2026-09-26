import type { Task } from '$lib/types';

/**
 * The ref a task's diff is computed against on its far side, '' when the diff is the working tree. A done task
 * shows what was committed, because its worktree no longer holds the change.
 */
export function diffHeadRef(task: Task): string {
    if (task.status !== 'done') return '';
    return task.branch_tip || task.branch || '';
}

/** Whether the diff on screen was drawn against a different base than the task now has. Two bases cannot be
 * merged into one view. */
export function diffBaseMoved(loadedHeadRef: string, task: Task): boolean {
    return loadedHeadRef !== diffHeadRef(task);
}
