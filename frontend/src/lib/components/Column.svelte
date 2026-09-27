<script lang="ts">
    import type { Task } from '$lib/types';
    import type { MultiSelect } from '$lib/composables/use-multi-select.svelte';
    import { appStore } from '$lib/stores';
    import { effectiveCreateTaskKey, formatShortcut, shortcutHintsHidden } from '$lib/utils/keyboard';
    import Card from './Card.svelte';
    import { fade } from 'svelte/transition';
    import { countWithDescendants } from '$lib/utils/task-utils';
    import { t } from '$lib/i18n';

    let { columnId, title, label = title, color, tasks, childrenMap, expandedTasks, onToggleExpanded, onDrop, onAddTask, projectId, onClearAll, onCreateChildTask, onSelect, canDrop, onRefreshBoard, multiSelect }: {
        columnId: string;
        /** English a11y name — `aria-label` text is never translated, and Board finds a column by it. */
        title: string;
        /** Visible (translatable) column label; falls back to the English `title`. */
        label?: string;
        color: string;
        tasks: Task[];
        childrenMap: Map<string, Task[]>;
        expandedTasks: Set<string>;
        onToggleExpanded: (taskId: string) => void;
        onDrop: (taskId: string) => void;
        onAddTask?: () => void;
        projectId?: string;
        onClearAll?: () => void;
        onCreateChildTask?: (parentId: string) => void;
        onSelect?: (task: Task) => void;
        canDrop?: (taskId: string, columnId: string) => boolean;
        onRefreshBoard?: () => void;
        multiSelect?: MultiSelect | null;
    } = $props();

    // Create Task shortcut hint (empty = disabled).
    const createTaskShortcutLabel = $derived.by(() => {
        const key = effectiveCreateTaskKey(appStore.config);
        return key ? formatShortcut(key) : '';
    });
    const hintsHidden = $derived(shortcutHintsHidden(appStore.config));

    let isOver = $state(false);
    let isDropRejected = $state(false);
    let addBtnVisible = $state(true);
    let cardListEl: HTMLDivElement | null = null;

    let taskCount = $derived(tasks.reduce((sum, t) => sum + countWithDescendants(t.id, columnId, childrenMap), 0));

    function handleScroll() {
        if (!cardListEl) return;
        addBtnVisible = cardListEl.scrollTop === 0;
    }

    function handleDragOver(e: DragEvent) {
        e.preventDefault();
        if (!e.dataTransfer) return;
        const taskIds = e.dataTransfer.getData('text/plain').split(',');
        // Reject if any task in the selection cannot be dropped
        for (const taskId of taskIds) {
            if (taskId && canDrop && !canDrop(taskId, columnId)) {
                e.dataTransfer.dropEffect = 'none';
                isDropRejected = true;
                isOver = false;
                return;
            }
        }
        isDropRejected = false;
        isOver = true;
    }

    function handleDragLeave() {
        isOver = false;
        isDropRejected = false;
    }

    function handleDrop(e: DragEvent) {
        isOver = false;
        const taskIds = (e.dataTransfer?.getData('text/plain') || '').split(',');
        for (const taskId of taskIds) {
            if (taskId && canDrop && !canDrop(taskId, columnId)) continue;
            if (taskId) onDrop(taskId);
        }
        // Clear multi-select after a successful drop
        if (multiSelect && multiSelect.hasSelection) {
            multiSelect.clear();
        }
    }
</script>

<div class={`column${isOver ? ' is-over' : ''}${isDropRejected ? ' is-drop-rejected' : ''}`} ondragover={handleDragOver} ondragleave={handleDragLeave} ondrop={handleDrop} role="list" aria-label={`${title} column`}>
    <div class="column-header" style="--col-color: {color}">
        <h3>
            {#if columnId === 'backlog'}
                <svg class="col-icon" width="12" height="12" viewBox="0 0 10 10" fill="none" aria-hidden="true"><circle cx="5" cy="5" r="3.5" stroke="currentColor" stroke-width="1.5"/></svg>
            {:else if columnId === 'in_progress'}
                <svg class="col-icon" width="12" height="12" viewBox="0 0 10 10" fill="none" aria-hidden="true"><polygon points="3.5,1.5 3.5,8.5 8.5,5" fill="currentColor"/></svg>
            {:else if columnId === 'review'}
                <svg class="col-icon" width="12" height="12" viewBox="0 0 10 10" fill="none" aria-hidden="true"><circle cx="5" cy="5" r="3.5" stroke="currentColor" stroke-width="1"/><circle cx="5" cy="5" r="1.2" fill="currentColor"/></svg>
            {:else if columnId === 'done'}
                <svg class="col-icon" width="12" height="12" viewBox="0 0 10 10" fill="none" aria-hidden="true"><circle cx="5" cy="5" r="3.5" fill="currentColor"/></svg>
            {/if}
            {label}
        </h3>
        <div class="header-right">
            <span class="count">{taskCount}</span>
            {#if columnId === 'backlog' && onAddTask && !addBtnVisible}
                <button class="add-btn" onclick={() => onAddTask()} type="button" aria-label="Create a new task" title="Create a new task">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none"><path d="M8 3v10M3 8h10" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg>
                </button>
            {/if}
            {#if onClearAll && taskCount > 0}
                <button class="clear-btn" onclick={(e) => { e.stopPropagation(); onClearAll(); }} aria-label={`Clear all ${taskCount} done tasks`} title={`Clear all ${taskCount} done tasks`}>
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                        <path d="M2 4h12M5 4V3a1 1 0 011-1h4a1 1 0 011 1v1M6 7v5M10 7v5" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                        <path d="M3 4l1 9a1 1 0 001 1h6a1 1 0 001-1l1-9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                    </svg>
                </button>
            {/if}
        </div>
    </div>
    <div class="card-list" bind:this={cardListEl} onscroll={handleScroll}>
        {#if onAddTask}
            <button class="add-task-btn" onclick={onAddTask} title={createTaskShortcutLabel ? `Create a new task (${createTaskShortcutLabel})` : 'Create a new task'}>{t('board.column.addTask')}{#if createTaskShortcutLabel && !hintsHidden}<kbd class="key-hint">{createTaskShortcutLabel}</kbd>{/if}</button>
        {/if}
        {#each tasks as task (task.id)}
            <div class="task-wrapper" transition:fade={{ duration: 200 }}>
                <Card {task} {columnId} {projectId} {childrenMap} {expandedTasks} {onToggleExpanded} {onCreateChildTask} columnColor={color} inline={false} showSubtaskBorder={true} onSelect={onSelect} {multiSelect} onRefreshBoard={onRefreshBoard} />
            </div>
        {/each}
    </div>
</div>

<style>
    .column {
        flex: 1;
        min-width: 260px;
        background: var(--color-surface-1);
        border-radius: var(--radius-lg);
        display: flex;
        flex-direction: column;
        overflow: hidden;
        transition: background 0.15s ease;
    }
    .column.is-over {
        background: var(--color-surface-2);
    }
    .column.is-drop-rejected {
        outline: 2px dashed var(--color-status-red);
        outline-offset: -2px;
        cursor: no-drop;
    }

    @media (max-width: 768px) {
        .column {
            flex: 0 0 auto !important;
            min-width: 280px;
            max-width: none;
            width: 92vw;
            scroll-snap-align: center;
        }
    }
    @media (hover: none) and (min-width: 769px) {
        .column {
            min-width: 340px;
        }
    }
    .column-header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 12px 16px;
        border-bottom: 2px solid var(--col-color);
    }
    .column-header h3 {
        display: flex;
        align-items: center;
        gap: 5px;
        font-size: var(--text-base);
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.5px;
        color: var(--color-text-secondary);
    }
    .col-icon {
        flex-shrink: 0;
        color: var(--col-color);
    }
    .count {
        background: var(--color-surface-3);
        border-radius: var(--radius-lg);
        padding: 2px 8px;
        font-size: var(--text-xs);
        color: var(--color-text-secondary);
    }
    .header-right {
        display: flex;
        align-items: center;
        gap: 6px;
    }
    .add-btn {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 22px;
        height: 22px;
        border: none;
        border-radius: var(--radius-sm);
        background: none;
        color: var(--color-text-tertiary);
        cursor: pointer;
        transition: background 0.15s ease, color 0.15s ease;
        padding: 0;
        flex-shrink: 0;
        opacity: 1;
    }
    .add-btn:hover {
        background: var(--color-accent-dark);
        color: white;
    }
    @media (max-width: 768px) {
        .add-btn {
            width: 44px;
            height: 44px;
        }
    }
    .clear-btn {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 22px;
        height: 22px;
        border: none;
        border-radius: var(--radius-sm);
        background: transparent;
        color: var(--color-text-tertiary);
        cursor: pointer;
        transition: background 0.15s ease, color 0.15s ease;
        padding: 0;
        flex-shrink: 0;
    }
    .clear-btn:hover {
        background: var(--color-status-red-dark);
        color: white;
    }
    @media (max-width: 768px) {
        .clear-btn {
            width: 44px;
            height: 44px;
        }
    }
    .card-list {
        flex: 1;
        overflow-y: auto;
        padding: 8px;
        display: flex;
        flex-direction: column;
        gap: 8px;
    }
    .add-task-btn {
        width: 100%;
        padding: 8px;
        border-radius: var(--radius-md);
        background: var(--color-surface-3);
        font-size: var(--text-base);
        color: var(--color-text-secondary);
        cursor: pointer;
        transition: background 0.15s ease;
        display: flex;
        align-items: center;
        justify-content: center;
    }
    .add-task-btn:hover {
        background: var(--color-accent-dark);
        color: white;
    }
    .add-task-btn:hover .key-hint {
        color: rgba(255, 255, 255, 0.85);
        border-color: rgba(255, 255, 255, 0.3);
        background: rgba(255, 255, 255, 0.1);
    }
</style>