import type { StorageUsage, TaskSpaceFigure } from '$lib/types';

/**
 * The server's filesystem, as the app stream last pushed it; `null` until a frame arrives, never
 * persisted and never fetched. One filesystem sits under every project, so it is one value.
 *
 * Task figures are kept per project. The frame carrying one names the project its walk measured, and it
 * reaches only the browsers holding that project, so a browser holds no figure for a project it never
 * opened. Its board then draws no line rather than "0 B in 0 worktrees".
 *
 * docs/api-contract.md → The filesystem reading is pushed
 */
class StorageStatusStore {
    usage = $state<StorageUsage | null>(null);
    taskFigures = $state<Record<string, TaskSpaceFigure>>({});

    update(usage: StorageUsage): void {
        this.usage = usage;
    }

    updateTasks(projectId: string, figure: TaskSpaceFigure): void {
        this.taskFigures[projectId] = figure;
    }

    /** Returns what one project's worktrees hold, or null when the server has measured nothing for it. */
    tasksFor(projectId: string | undefined): TaskSpaceFigure | null {
        return projectId ? this.taskFigures[projectId] ?? null : null;
    }
}

export const storageStatus = new StorageStatusStore();
