import { pruneCollapsedTasks, saveCollapsedTasks } from '$lib/storage';

/**
 * Which task cards show their subtasks, shared by the board and the task view so
 * collapsing one collapses the other. What this browser collapsed is stored in
 * IndexedDB (`storage.ts`); a parent is expanded unless it is in that set, so a
 * task with no stored entry is expanded.
 *
 * Only a deliberate collapse is written — an auto-expand (the selected task's
 * parent, a parent that just gained a subtask) stays in memory.
 */
export class ExpandedTasksStore {
    private ids = $state<Set<string>>(new Set());

    /** The set as it stands. Mutate through the methods: Svelte sees the new reference, not an in-place edit. */
    get all(): Set<string> {
        return this.ids;
    }

    has(taskId: string): boolean {
        return this.ids.has(taskId);
    }

    add(taskId: string): void {
        if (this.ids.has(taskId)) return;
        const next = new Set(this.ids);
        next.add(taskId);
        this.ids = next;
    }

    toggle(taskId: string): void {
        const next = new Set(this.ids);
        if (next.has(taskId)) {
            next.delete(taskId);
        } else {
            next.add(taskId);
        }
        this.ids = next;
    }

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
            if (!this.ids.has(id)) collapsed.add(id);
        }
        await Promise.all([saveCollapsedTasks(collapsed), pruneCollapsedTasks(allTaskIds)]);
    }
}

export const expandedTasksStore = new ExpandedTasksStore();
