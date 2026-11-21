import type { Task } from '$lib/types';
import type { DiffFile } from '$lib/types/diff';

/**
 * Which comparison a file changed in: the working tree against HEAD, or HEAD against the base. A text file
 * says so with its rows; the flags say it for a file with none.
 */
export function hasWorkingChanges(file: DiffFile): boolean {
    return (file.rows?.length ?? 0) > 0 || !!file.hasWorking;
}

export function hasCommittedChanges(file: DiffFile): boolean {
    return (file.committedRows?.length ?? 0) > 0 || !!file.hasCommitted;
}

/** A file the panel can draw something from. A mode-only change has nothing and would render as empty. */
export function hasAnythingToShow(file: DiffFile): boolean {
    return (file.rows?.length ?? 0) > 0 || file.fileSize !== undefined || !!file.isDeleted || !!file.isNewFile;
}

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
