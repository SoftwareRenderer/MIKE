import type { SandboxMemoryStats } from '$lib/types';

/**
 * Most-recent sandbox memory stats per task, fed by `sandbox_stats` task
 * events over the app stream. Entries persist for the session: when a task
 * goes idle the server stops pushing, and the UI keeps showing the most
 * recent usage. Only task deletion removes an entry.
 */
export class SandboxStatsStore {
    private stats = $state<Record<string, SandboxMemoryStats>>({});

    set(taskId: string, stats: SandboxMemoryStats): void {
        this.stats = { ...this.stats, [taskId]: stats };
    }

    get(taskId: string): SandboxMemoryStats | null {
        return this.stats[taskId] ?? null;
    }

    remove(taskId: string): void {
        if (!(taskId in this.stats)) return;
        const next = { ...this.stats };
        delete next[taskId];
        this.stats = next;
    }
}

export const sandboxStatsStore = new SandboxStatsStore();
