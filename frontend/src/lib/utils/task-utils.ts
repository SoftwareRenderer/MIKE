import type { Task } from '$lib/types';

/** Merge an update into a task, preserving existing fields when the update is queued.
 * Queued updates are optimistic local changes that may be incomplete and should
 * be shallow-merged into the existing task rather than replacing it entirely. */
export function mergeQueuedUpdate(task: Task, updated: Partial<Task>): Task {
    return (updated as any).queued ? { ...task, ...updated } : updated as Task;
}

/**
 * True while the executor can still write to this task's transcript. The vocabulary is
 * `backend/types/exec_state.go`'s (mirrored server-side by `terminalTurnStates`), and a
 * task's `status` is never the answer: stopping one keeps `in_progress` and only moves
 * `execution_state` to `paused`.
 */
export function taskIsInFlight(executionState: string): boolean {
    return executionState === 'running' || executionState === 'pulling_image';
}

/** Recursively count a task and all its descendants that match the given status. */
export function countWithDescendants(taskId: string, status: string, childrenMap: Map<string, Task[]>): number {
    const children = childrenMap.get(taskId) ?? [];
    let count = 1;
    for (const child of children) {
        if (child.status === status) {
            count += countWithDescendants(child.id, status, childrenMap);
        }
    }
    return count;
}

/** Column the mobile board shows after switching projects: the one with the most urgent
 * work — Review, else In Progress, else Backlog. Tasks hidden inside a collapsed parent
 * card still count. */
export function resolveBoardLandingColumn(tasks: Pick<Task, 'status'>[]): 'review' | 'in_progress' | 'backlog' {
    if (tasks.some(t => t.status === 'review')) return 'review';
    if (tasks.some(t => t.status === 'in_progress')) return 'in_progress';
    return 'backlog';
}

/** Reduce a set of task IDs to those whose parent is NOT also in the set.
 * Descendants are excluded because the server recursively deletes them */
export function filterTopLevelTaskIds(taskIds: string[], tasks: Task[]): string[] {
    const selected = new Set(taskIds);
    const taskById = new Map(tasks.map(t => [t.id, t]));
    return taskIds.filter(id => {
        const task = taskById.get(id);
        if (!task?.parent_task_id) return true;
        return !selected.has(task.parent_task_id);
    });
}

/**
 * Apply a `task_updated` frame to the board on screen, or return null when there is nothing to apply.
 *
 * A task the board has never held is appended only when the frame names this project: the server
 * replays the board frames it buffered for this connection, and those include projects the client
 * switched away from. A task already on the board is updated whatever the frame claims, and one whose
 * project the frame does not name is never appended — refreshing a row we already hold is safe,
 * adding a card of unknown ownership is not.
 */
export function mergeTaskUpdateIntoBoard(tasks: Task[], updated: Task, projectId: string): Task[] | null {
    const idx = tasks.findIndex(t => t.id === updated.id);
    if (idx < 0) return updated.project_id === projectId ? [...tasks, updated] : null;
    if (tasks[idx] === updated) return null;
    return tasks.map((t, i) => (i === idx ? updated : t));
}
