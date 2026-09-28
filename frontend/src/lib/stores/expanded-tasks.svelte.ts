import { pruneCollapsedTasks, saveCollapsedTasks } from '$lib/storage';
import { IdSet } from './id-set.svelte';

/**
 * Which task cards show their subtasks, shared by the board and the task view so
 * collapsing one collapses the other. Membership here means expanded; IndexedDB
 * (`storage.ts`) holds the opposite — what this browser collapsed — so a task with
 * no stored entry starts expanded.
 *
 * Only a deliberate collapse is written — an auto-expand (the selected task's
 * parent, a parent that just gained a subtask) stays in memory.
 *
 * @knipignore The tests build their own instance.
 */
export class ExpandedTasksStore extends IdSet {
    toggle(taskId: string): void {
        if (this.has(taskId)) {
            this.delete(taskId);
        } else {
            this.add(taskId);
        }
    }

    /** Become exactly `next`. */
    replace(next: Set<string>): void {
        this.ids = new Set(next);
    }

    /** Start from what this browser left: drop the stored entries whose task is gone
     * (`allTaskIds`), then expand every parent that is not collapsed. */
    async load(parentTaskIds: Set<string>, allTaskIds: Set<string>): Promise<void> {
        await pruneCollapsedTasks(allTaskIds);
        const collapsed = await pruneCollapsedTasks(parentTaskIds);
        const expanded = new Set<string>();
        for (const id of parentTaskIds) {
            if (!collapsed.has(id)) expanded.add(id);
        }
        this.replace(expanded);
    }

    /** Record which parents are collapsed, and forget the stored ones whose task no
     * longer exists. Neither outcome is fatal to the board on screen. */
    async persist(parentTaskIds: Set<string>, allTaskIds: Set<string>): Promise<void> {
        const collapsed = new Set<string>();
        for (const id of parentTaskIds) {
            if (!this.has(id)) collapsed.add(id);
        }
        await Promise.all([saveCollapsedTasks(collapsed), pruneCollapsedTasks(allTaskIds)]);
    }
}

export const expandedTasksStore = new ExpandedTasksStore();
