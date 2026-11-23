/**
 * Forgetting what a task's explorer left behind: its open files, their scroll, and the buffers of the files it
 * was holding.
 *
 * The board is what says a task is finished or gone, so this reads the board rather than being called from a
 * Done button: the move is made by a card, by the commit dialog, by another browser and by the executor, and a
 * rule hung on one of those misses the others. `docs/reconnection.md` owns the rules.
 */
import type { Task } from '$lib/types';
import { forgetTaskExplorer, pruneTaskExplorerRecords } from '$lib/storage';
import { openExplorerPaths } from '$lib/services/explorer-core.svelte';
import { appStore } from '$lib/stores';

const DONE = 'done';

/** Two readings of one project's board that say the same tasks hold the same statuses. */
function sameReading(held: Map<string, string> | undefined, next: Map<string, string>): boolean {
    return held !== undefined
        && held.size === next.size
        && [...next].every(([taskId, status]) => held.get(taskId) === status);
}

class ExplorerRecords {
    /** Per project, the status every task held at the last judgement: a board that says the same thing asks
     *  nothing of IndexedDB again. */
    private seen = new Map<string, Map<string, string>>();

    /** Judge the board this browser is looking at, and forget what its tasks no longer need. */
    observeBoard(projectId: string, tasks: readonly Task[]): void {
        const statuses = new Map(tasks.map(t => [t.id, t.status]));
        const previous = this.seen.get(projectId);
        if (sameReading(previous, statuses)) return;
        this.seen.set(projectId, statuses);

        // A buffer under a mounted editor is being typed in, whoever the task is.
        const options = { workspaceDir: appStore.config?.workspace_dir || '', heldPaths: openExplorerPaths() };
        // Done is forgotten on the move into it, not in it: a first look only seeds the ledger, so the record
        // an explorer writes while reading a finished task's files survives until that task leaves the board.
        if (previous) {
            for (const [taskId, status] of statuses) {
                if (status === DONE && previous.get(taskId) !== DONE) {
                    void forgetTaskExplorer(projectId, taskId, options);
                }
            }
        }
        void pruneTaskExplorerRecords(projectId, new Set(statuses.keys()), options);
    }

    /** Forget every flag. For tests, which boot the module more than once per instance. */
    reset(): void {
        this.seen.clear();
    }
}

export const explorerRecords = new ExplorerRecords();
