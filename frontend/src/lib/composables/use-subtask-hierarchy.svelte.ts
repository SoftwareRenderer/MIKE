import type { Task } from '$lib/types';
import { appStore } from '$lib/stores';
import { expandedTasksStore } from '$lib/stores/expanded-tasks.svelte';
import { sortByLastMoved } from '$lib/utils/sort-tasks';
import { ancestorsOf } from '$lib/utils/task-utils';

/** Composable for subtask hierarchy management.
 * Tracks expanded/collapsed state, consolidation of subtasks under same-column ancestors,
 * and auto-expansion of parent tasks.
 * Accepts getter functions for reactive props so $derived/$effect see live values. */
export function useSubtaskHierarchy(getAllTasks: () => Task[], getCurrentParentId: () => string | undefined) {
    // Build children map for subtask hierarchy
    const childrenMap = $derived.by(() => {
        const tasks = getAllTasks();
        const map = new Map<string, Task[]>();
        for (const t of tasks) {
            if (t.parent_task_id) {
                if (!map.has(t.parent_task_id)) {
                    map.set(t.parent_task_id, []);
                }
                map.get(t.parent_task_id)!.push(t);
            }
        }
        // Same direction as the columns (Settings → Preferences); returns a new array.
        const sortOldestFirst = appStore.config?.sort_oldest_first === true;
        for (const [id, children] of map) {
            map.set(id, sortByLastMoved(children, sortOldestFirst));
        }
        return map;
    });
    const subtaskIds = $derived.by(() => new Set(getAllTasks().filter(t => t.parent_task_id).map(t => t.id)));

    // Subtask IDs that should be consolidated (hidden) because any ancestor is in the same column
    const consolidatedSubtaskIds = $derived.by(() => {
        const tasks = getAllTasks();
        const consolidated = new Set<string>();
        const taskMap = new Map<string, Task>();
        for (const t of tasks) taskMap.set(t.id, t);

        for (const t of tasks) {
            if (!t.parent_task_id) continue;
            if (ancestorsOf(t, id => taskMap.get(id)).some(a => a.status === t.status)) {
                consolidated.add(t.id);
            }
        }
        return consolidated;
    });

    // IDs of tasks that have children (potential parents)
    const parentTaskIds = $derived.by(() => new Set(getAllTasks().filter(t => childrenMap.has(t.id)).map(t => t.id)));

    // Initialize expanded tasks from IndexedDB (default: all expanded) — run once
    let expandedInitialized = $state(false);
    $effect(() => {
        if (expandedInitialized) return;
        if (parentTaskIds.size > 0) {
            expandedInitialized = true;
            const allIds = new Set(getAllTasks().map(t => t.id));
            expandedTasksStore.load(parentTaskIds, allIds).catch(() => {});
        }
    });

    // Auto-expand parent of currently selected task (only on task change, not on updates)
    let lastAutoExpandedParent: string | null = $state(null);
    function autoExpandParent(parentId: string | undefined) {
        if (parentId && parentId !== lastAutoExpandedParent) {
            lastAutoExpandedParent = parentId;
            expandedTasksStore.add(parentId);
        }
    }

    // Auto-expand parent of currently selected task
    $effect(() => {
        autoExpandParent(getCurrentParentId());
    });

    // Auto-expand parent cards when new subtasks appear
    let lastExpandedParents = $state<Set<string>>(new Set());
    $effect(() => {
        const currentParents = new Set(childrenMap.keys());
        for (const parentId of currentParents) {
            if (!lastExpandedParents.has(parentId)) {
                expandedTasksStore.add(parentId);
                lastExpandedParents = new Set([...lastExpandedParents, parentId]);
            }
        }
    });

    function toggleTaskExpanded(taskId: string) {
        expandedTasksStore.toggle(taskId);
        const allIds = new Set(getAllTasks().map(t => t.id));
        expandedTasksStore.persist(parentTaskIds, allIds).catch(() => {});
    }

    return {
        get childrenMap() { return childrenMap; },
        get subtaskIds() { return subtaskIds; },
        get consolidatedSubtaskIds() { return consolidatedSubtaskIds; },
        get parentTaskIds() { return parentTaskIds; },
        get expandedTasks() { return expandedTasksStore.all; },
        toggleTaskExpanded,
    };
}
