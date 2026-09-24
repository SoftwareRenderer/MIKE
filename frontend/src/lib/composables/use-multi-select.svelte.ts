/**
 * Multi-select state manager for the Kanban board.
 * Tracks selected task IDs and provides toggle / clear / bulk-action helpers.
 */
export class MultiSelect {
    public selectedIds = $state<Set<string>>(new Set());

    get count(): number {
        return this.selectedIds.size;
    }

    get hasSelection(): boolean {
        return this.selectedIds.size > 0;
    }

    /** Toggle a task's selection state. */
    toggle(taskId: string): void {
        const next = new Set(this.selectedIds);
        if (next.has(taskId)) {
            next.delete(taskId);
        } else {
            next.add(taskId);
        }
        this.selectedIds = next;
    }

    /** Add a task to the selection (without removing others). */
    add(taskId: string): void {
        const next = new Set(this.selectedIds);
        next.add(taskId);
        this.selectedIds = next;
    }

    /** Remove a task from the selection. */
    remove(taskId: string): void {
        const next = new Set(this.selectedIds);
        next.delete(taskId);
        this.selectedIds = next;
    }

    /** Check if a task is currently selected. */
    isSelected(taskId: string): boolean {
        return this.selectedIds.has(taskId);
    }

    /** Clear all selections. */
    clear(): void {
        this.selectedIds = new Set();
    }

    /** Select all provided task IDs. */
    selectAll(ids: Iterable<string>): void {
        this.selectedIds = new Set(ids);
    }

    /** Get selected task IDs as an array. */
    toArray(): string[] {
        return [...this.selectedIds];
    }
}
