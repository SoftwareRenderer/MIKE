import type { Task } from '$lib/types';
import { sortByLastMoved } from './sort-tasks';

/** One row of a card's subtask outline. */
export interface SubtaskRow {
    task: Task;
    /** 1 for a leaf, otherwise 1 + the wave of its deepest descendant. */
    wave: number;
    /** Every ancestor below the card, outermost first. */
    owners: string[];
    /** Every descendant, collapsed or not — hiding a branch does not finish it. */
    progress: { done: number; total: number };
}

/** One top-level subtask and everything that has to finish before it. */
export interface SubtaskGroup {
    root: Task;
    rows: SubtaskRow[];
}

/**
 * The descendants of `rootId` as one group per top-level subtask, rows ordered by wave so none comes before
 * its own descendant, mirroring the schedule `backend/exec` runs (`IsStartableNow`). A collapsed parent
 * keeps its row, since it is the control that reopens the branch, and contributes none below it.
 */
export function subtaskOutlineGroups(
    rootId: string,
    childrenMap: Map<string, Task[]>,
    expanded: Set<string>,
    sortOldestFirst = false,
): SubtaskGroup[] {
    const childrenOf = (id: string): Task[] => sortByLastMoved(childrenMap.get(id) ?? [], sortOldestFirst);

    // Each recursion carries its own path, so a parent_task_id cycle ends instead of hanging.
    const waves = new Map<string, number>();
    const waveOf = (task: Task, path: Set<string>): number => {
        const known = waves.get(task.id);
        if (known !== undefined) return known;
        if (path.has(task.id)) return 1;
        path.add(task.id);
        let deepest = 0;
        for (const child of childrenOf(task.id)) deepest = Math.max(deepest, waveOf(child, path));
        path.delete(task.id);
        waves.set(task.id, deepest + 1);
        return deepest + 1;
    };

    const progresses = new Map<string, { done: number; total: number }>();
    const progressOf = (task: Task, path: Set<string>): { done: number; total: number } => {
        const known = progresses.get(task.id);
        if (known !== undefined) return known;
        if (path.has(task.id)) return { done: 0, total: 0 };
        path.add(task.id);
        let done = 0;
        let total = 0;
        for (const child of childrenOf(task.id)) {
            const nested = progressOf(child, path);
            done += nested.done + (child.status === 'done' ? 1 : 0);
            total += nested.total + 1;
        }
        path.delete(task.id);
        progresses.set(task.id, { done, total });
        return { done, total };
    };

    const groups: SubtaskGroup[] = [];
    for (const branch of childrenOf(rootId)) {
        const rows: SubtaskRow[] = [];
        const collect = (task: Task, owners: string[], path: Set<string>) => {
            rows.push({
                task,
                wave: waveOf(task, new Set()),
                owners,
                progress: progressOf(task, new Set()),
            });
            if (!expanded.has(task.id)) return;
            path.add(task.id);
            for (const kid of childrenOf(task.id)) {
                if (path.has(kid.id)) continue;
                collect(kid, [...owners, task.id], path);
            }
            path.delete(task.id);
        };
        collect(branch, [], new Set());
        rows.sort((a, b) => a.wave - b.wave);
        groups.push({ root: branch, rows });
    }
    return groups;
}
