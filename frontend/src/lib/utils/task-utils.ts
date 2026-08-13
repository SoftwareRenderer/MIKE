import type { Task } from '$lib/types';

/** Merge an update into a task, preserving existing fields when the update is queued.
 * Queued updates are optimistic local changes that may be incomplete and should
 * be shallow-merged into the existing task rather than replacing it entirely. */
export function mergeQueuedUpdate(task: Task, updated: Partial<Task>): Task {
    return (updated as any).queued ? { ...task, ...updated } : updated as Task;
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
