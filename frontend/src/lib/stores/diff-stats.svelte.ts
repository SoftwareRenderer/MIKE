import type { DiffStatsEntry } from '$lib/types';

/**
 * Diff stats per task, pushed over the app stream and read by the card badge, the
 * task view's diff panel and its Review bar.
 *
 * Entries survive the panel closing: the server stops pushing for a task that goes
 * idle, so a card that stays on screen must keep the last stats it was sent. Only
 * task deletion removes an entry.
 *
 * @knipignore The tests build their own instance, so one case's entries are not another's.
 */
export class DiffStatsStore {
    private entries = $state<Record<string, DiffStatsEntry>>({});

    get(taskId: string): DiffStatsEntry | null {
        return this.entries[taskId] ?? null;
    }

    has(taskId: string): boolean {
        return taskId in this.entries;
    }

    set(taskId: string, stats: DiffStatsEntry): void {
        this.entries = { ...this.entries, [taskId]: stats };
    }

    remove(taskId: string): void {
        if (!(taskId in this.entries)) return;
        const next = { ...this.entries };
        delete next[taskId];
        this.entries = next;
    }

    /** Add the mirror's entries for tasks with nothing live yet. A frame the server
     * pushed for this task is newer than anything IndexedDB held, so it wins. */
    fillAbsent(stats: Record<string, DiffStatsEntry>): void {
        const missing = Object.keys(stats).filter(id => !(id in this.entries));
        if (missing.length === 0) return;
        const next = { ...this.entries };
        for (const id of missing) next[id] = stats[id];
        this.entries = next;
    }
}

export const diffStatsStore = new DiffStatsStore();
