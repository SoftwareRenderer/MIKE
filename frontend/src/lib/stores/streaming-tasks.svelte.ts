/**
 * Tasks this browser has seen tokens for and has not seen the end of — the
 * frontend-only half of "is it running". `TaskChat` adds on the first event of a
 * turn and removes when the turn ends or the task is known to be idle.
 *
 * A task the server stopped keeps `status = in_progress` and only moves `execution_state`
 * to `paused`, so the answer to "is it still running" is `taskIsInFlight`
 * (`$lib/utils/task-utils.ts`), not membership in this set.
 */
export class StreamingTaskStore {
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

    delete(taskId: string): void {
        if (!this.ids.has(taskId)) return;
        const next = new Set(this.ids);
        next.delete(taskId);
        this.ids = next;
    }
}

export const streamingTaskStore = new StreamingTaskStore();
