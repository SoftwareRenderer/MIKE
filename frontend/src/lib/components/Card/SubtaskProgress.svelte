<script lang="ts">
    import type { Task } from '$lib/types';
    import Card from '../Card.svelte';
    import { fade, slide } from 'svelte/transition';

    let {
        subtaskProgress,
        expanded,
        hasChildren,
        children,
        columnId,
        projectId,
        childrenMap,
        expandedTasks,
        onToggleExpanded,
        onCreateChildTask,
        columnColor,
        parentSelected,
        showSubtaskBorder,
        onSelect,
        onRefreshBoard,
    }: {
        subtaskProgress: { done: number; total: number; pct: number } | null;
        expanded: boolean;
        hasChildren: boolean;
        children: Task[];
        columnId: string;
        projectId?: string;
        childrenMap?: Map<string, Task[]>;
        expandedTasks?: Set<string>;
        onToggleExpanded?: (taskId: string) => void;
        onCreateChildTask?: (parentId: string) => void;
        columnColor?: string;
        parentSelected: boolean;
        showSubtaskBorder: boolean;
        onSelect?: (task: Task) => void;
        onRefreshBoard?: () => void;
    } = $props();
</script>

{#if subtaskProgress}
    <button
        type="button"
        class="subtask-progress-bar"
        class:is-expanded={expanded}
        style="--pct: {subtaskProgress.pct * 100}"
        onclick={onToggleExpanded}
        aria-expanded={expanded}
    >
        <span class="subtask-progress-fill" style="width: {subtaskProgress.pct * 100}%"></span>
        <span class="subtask-progress-count">{subtaskProgress.total} subtask{subtaskProgress.total !== 1 ? 's' : ''}</span>
        <svg class="subtask-chevron" width="10" height="10" viewBox="0 0 16 16" fill="none"><path d="M4 6l4 4 4-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
    </button>
{/if}
{#if expanded && hasChildren}
    <div class="subtask-list" transition:slide={{ duration: 200 }}>
        {#each children as child}
            <div class="subtask-wrapper" transition:fade={{ duration: 200 }}>
                <Card
                    task={child}
                    {columnId}
                    {projectId}
                    {childrenMap}
                    {expandedTasks}
                    {onToggleExpanded}
                    {onCreateChildTask}
                    columnColor={columnColor}
                    inline={true}
                    parentSelected={parentSelected}
                    {showSubtaskBorder}
                    {onSelect}
                    {onRefreshBoard}
                />
            </div>
        {/each}
    </div>
{/if}

<style>
    .subtask-progress-bar {
        display: flex;
        align-items: center;
        justify-content: space-between;
        width: 100%;
        height: 22px;
        margin-top: 8px;
        padding: 0 6px;
        border: none;
        border-radius: var(--radius-sm);
        background: var(--color-surface-3);
        cursor: pointer;
        position: relative;
        overflow: hidden;
        transition: background 0.15s ease;
    }
    .subtask-progress-bar:hover {
        background: var(--color-surface-4);
    }
    .subtask-progress-bar.is-expanded {
        background: var(--color-surface-4);
    }
    .subtask-progress-fill {
        position: absolute;
        top: 0;
        left: 0;
        height: 100%;
        opacity: 0.35;
        transition: width 0.3s ease, opacity 0.15s ease;
        pointer-events: none;
        background: #009933;
    }
    .subtask-progress-bar.is-expanded .subtask-progress-fill {
        opacity: 0.5;
    }
    .subtask-progress-count {
        position: absolute;
        left: 50%;
        top: 50%;
        transform: translate(-50%, -50%);
        z-index: 1;
        font-size: 0.75rem;
        font-weight: 600;
        font-family: inherit;
        font-variant-numeric: tabular-nums;
        color: #FAFAFA;
        white-space: nowrap;
        pointer-events: none;
    }
    .subtask-chevron {
        position: absolute;
        right: 6px;
        top: 50%;
        transform: translateY(-50%);
        z-index: 1;
        opacity: 0.5;
        transition: transform 0.15s ease;
        pointer-events: none;
    }
    .subtask-progress-bar.is-expanded .subtask-chevron {
        transform: translateY(-50%) rotate(180deg);
    }

    .subtask-list {
        margin-top: 6px;
        display: flex;
        flex-direction: column;
        gap: 6px;
    }
    .subtask-list :global(.card) {
        background: var(--color-surface-1);
    }
</style>
