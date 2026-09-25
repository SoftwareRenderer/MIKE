import type { CommitQueueEntry } from '$lib/types';

/**
 * The server's commit queue, one slot per project: project id → the task occupying
 * it. The frame that fills it names no project, so `App.svelte` decides which board
 * owns the task before writing; `TaskChat` clears its own project's slot when its
 * commit-and-done call returns.
 */
export class CommitQueueStore {
    private slots = $state<Record<string, CommitQueueEntry>>({});

    get(projectId: string): CommitQueueEntry | null {
        return this.slots[projectId] ?? null;
    }

    set(projectId: string, entry: CommitQueueEntry): void {
        this.slots = { ...this.slots, [projectId]: entry };
    }

    remove(projectId: string): void {
        if (!(projectId in this.slots)) return;
        const next = { ...this.slots };
        delete next[projectId];
        this.slots = next;
    }

    /** The queue badge for one task: a status only while that task holds its project's slot. */
    statusOf(projectId: string, taskId: string): CommitQueueEntry['status'] | null {
        const entry = this.slots[projectId];
        return entry && entry.taskID === taskId ? entry.status : null;
    }
}

export const commitQueueStore = new CommitQueueStore();
