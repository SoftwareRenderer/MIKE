import type { Task } from '$lib/types';

/** Merge an update into a task, preserving existing fields when the update is queued.
 * Queued updates are optimistic local changes that may be incomplete and should
 * be shallow-merged into the existing task rather than replacing it entirely. */
export function mergeQueuedUpdate(task: Task, updated: Partial<Task>): Task {
    return (updated as any).queued ? { ...task, ...updated } : updated as Task;
}
