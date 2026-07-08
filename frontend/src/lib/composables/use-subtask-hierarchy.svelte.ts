import type { Task } from '$lib/types';
import { persistCollapsedTasks, cleanupCollapsedTasks, initExpandedTasks } from '$lib/stores/app';

/** Composable for subtask hierarchy management.
 * Tracks expanded/collapsed state, consolidation of subtasks under same-column ancestors,
 * and auto-expansion of parent tasks. */
export function useSubtaskHierarchy(allTasks: Task[], expandedTasksContext: {
    set: (v: Set<string>) => void;
    subscribe: (fn: (v: Set<string>) => void) => () => void;
}, currentParentId: string | undefined) {
    // Build children map for subtask hierarchy
    const childrenMap = $derived.by(() => {
        const map = new Map<string, Task[]>();
        for (const t of allTasks) {
            if (t.parent_task_id) {
                if (!map.has(t.parent_task_id)) {
                    map.set(t.parent_task_id, []);
                }
                map.get(t.parent_task_id)!.push(t);
            }
        }
        for (const [, children] of map) {
            children.sort((a, b) => a.column_order - b.column_order);
        }
        return map;
    });
    const subtaskIds = $derived(new Set(allTasks.filter(t => t.parent_task_id).map(t => t.id)));

    // Subtask IDs that should be consolidated (hidden) because any ancestor is in the same column
    const consolidatedSubtaskIds = $derived.by(() => {
        const consolidated = new Set<string>();
        const taskMap = new Map<string, Task>();
        for (const t of allTasks) taskMap.set(t.id, t);

        for (const t of allTasks) {
            if (!t.parent_task_id) continue;
            let parentId: string | undefined = t.parent_task_id;
            while (parentId) {
                const ancestor = taskMap.get(parentId);
                if (!ancestor) break;
                if (ancestor.status === t.status) {
                    consolidated.add(t.id);
                    break;
                }
                parentId = ancestor.parent_task_id ?? undefined;
            }
        }
        return consolidated;
    });

    // IDs of tasks that have children (potential parents)
    const parentTaskIds = $derived(new Set(allTasks.filter(t => childrenMap.has(t.id)).map(t => t.id)));

    // Track expanded tasks (shared store for Board + CardDetail sync)
    let expandedTasks = $state<Set<string>>(new Set());
    $effect(() => {
        expandedTasksContext.subscribe(v => expandedTasks = v);
    });

    // Initialize expanded tasks from IndexedDB (default: all expanded) — run once
    let expandedInitialized = $state(false);
    $effect(() => {
        if (expandedInitialized) return;
        if (parentTaskIds.size > 0) {
            expandedInitialized = true;
            (async () => {
                const allIds = new Set(allTasks.map(t => t.id));
                await cleanupCollapsedTasks(allIds);
                expandedTasksContext.set(await initExpandedTasks(parentTaskIds));
            })().catch(() => {});
        }
    });

    // Auto-expand parent of currently selected task (only on task change, not on updates)
    let lastAutoExpandedParent: string | null = $state(null);
    function autoExpandParent(parentId: string | undefined) {
        if (parentId && parentId !== lastAutoExpandedParent) {
            lastAutoExpandedParent = parentId;
            const current = expandedTasks;
            if (!current.has(parentId)) {
                const next = new Set(current);
                next.add(parentId);
                expandedTasksContext.set(next);
            }
        }
    }

    // Auto-expand parent of currently selected task
    $effect(() => {
        autoExpandParent(currentParentId);
    });

    // Auto-expand parent cards when new subtasks appear
    let lastExpandedParents = $state<Set<string>>(new Set());
    $effect(() => {
        const currentParents = new Set(childrenMap.keys());
        for (const parentId of currentParents) {
            if (!lastExpandedParents.has(parentId)) {
                const current = expandedTasks;
                if (!current.has(parentId)) {
                    const next = new Set(current);
                    next.add(parentId);
                    expandedTasksContext.set(next);
                }
                lastExpandedParents.add(parentId);
            }
        }
    });

    function toggleTaskExpanded(taskId: string) {
        const next = new Set(expandedTasks);
        if (next.has(taskId)) {
            next.delete(taskId);
        } else {
            next.add(taskId);
        }
        expandedTasksContext.set(next);
        const allIds = new Set(allTasks.map(t => t.id));
        persistCollapsedTasks(next, parentTaskIds).catch(() => {});
        cleanupCollapsedTasks(allIds).catch(() => {});
    }

    return {
        childrenMap,
        subtaskIds,
        consolidatedSubtaskIds,
        parentTaskIds,
        expandedTasks,
        autoExpandParent,
        toggleTaskExpanded,
    };
}
