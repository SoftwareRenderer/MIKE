<script lang="ts">
    import type { Task } from '$lib/types';
    import type { MultiSelect } from '$lib/composables/use-multi-select.svelte';
    import { appStore } from '$lib/stores';
    import { effectiveCreateTaskKey, formatShortcut, shortcutHintsHidden } from '$lib/utils/keyboard';
    import Card from './Card.svelte';
    import { fade } from 'svelte/transition';
    import { countWithDescendants } from '$lib/utils/task-utils';
    import { storageStatus } from '$lib/services/storage-usage.svelte';
    import { isStoragePressured, storageTasksSentence, storageUsageSentence } from '$lib/utils/storage-usage';
    import { t } from '$lib/i18n';
    import Icon from './Icon.svelte';
    import discardIcon from '$lib/assets/icons/discard.svg?raw';

    let { columnId, label, color, tasks, childrenMap, expandedTasks, onToggleExpanded, onDrop, onAddTask, projectId, onClearAll, onCreateChildTask, onSelect, canDrop, onRefreshBoard, multiSelect }: {
        columnId: string;
        /** Visible column label; also the word in this column's accessible name. */
        label: string;
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

    // Only the Done column, and only with a reading.
    let storageUsage = $derived(columnId === 'done' ? storageStatus.usage : null);
    let storageTip = $derived(storageUsageSentence(storageUsage));
    let storageTasksTip = $derived(storageTasksSentence(storageUsage));
    let storageLow = $derived(isStoragePressured(storageUsage));
    // A tap pins: a touch screen has no pointer leave to close it.
    let storageHovered = $state(false);
    let storagePinned = $state(false);
    let storageOpen = $derived(storageHovered || storagePinned);
    let storageTipId = $derived(`storage-tip-${columnId}`);

    // A tap raises pointerenter too, which would reopen what its own click just closed.
    function hoverStorage(e: PointerEvent, on: boolean) {
        if (e.pointerType === 'mouse') storageHovered = on;
    }

    function dismissStorage() {
        storageHovered = false;
        storagePinned = false;
    }

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

<div class={`column${isOver ? ' is-over' : ''}${isDropRejected ? ' is-drop-rejected' : ''}`} ondragover={handleDragOver} ondragleave={handleDragLeave} ondrop={handleDrop} role="list" aria-label={t('board.columnAria', { name: label })} data-column-id={columnId}>
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
            {#if storageTip}
                <button
                    type="button"
                    class="count storage-count"
                    class:is-low={storageLow}
                    class:is-open={storageOpen}
                    aria-label={storageLow ? t('board.column.storageLowAria') : t('board.column.storageAria')}
                    aria-expanded={storageOpen}
                    aria-describedby={storageOpen ? storageTipId : undefined}
                    onpointerenter={(e) => hoverStorage(e, true)}
                    onpointerleave={(e) => hoverStorage(e, false)}
                    onclick={() => { storagePinned = !storagePinned; }}
                    onkeydown={(e) => { if (e.key === 'Escape') dismissStorage(); }}
                >
                    {#if storageLow}
                        <svg class="storage-warn" width="11" height="11" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                            <path d="M8 2 14.6 13.5H1.4L8 2Z" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round"/>
                            <path d="M8 6.4v3.1" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
                            <circle cx="8" cy="11.7" r="0.85" fill="currentColor"/>
                        </svg>
                    {/if}
                    <span>{taskCount}</span>
                </button>
                {#if storageOpen}
                    <div id={storageTipId} class="storage-tip" role="tooltip">
                        <div>{storageTip}</div>
                        {#if storageTasksTip}
                            <div class="storage-tip-tasks">{storageTasksTip}</div>
                        {/if}
                    </div>
                {/if}
            {:else}
                <span class="count">{taskCount}</span>
            {/if}
            {#if columnId === 'backlog' && onAddTask && !addBtnVisible}
                <button class="add-btn" onclick={() => onAddTask()} type="button" aria-label={t('board.column.newTaskAria')} title={t('board.column.newTaskAria')}>
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none"><path d="M8 3v10M3 8h10" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg>
                </button>
            {/if}
            {#if onClearAll && taskCount > 0}
                <button class="clear-btn" onclick={(e) => { e.stopPropagation(); onClearAll(); }} aria-label={t('board.column.clearAllAria', { count: taskCount })} title={t('board.column.clearAllAria', { count: taskCount })}>
                    <Icon src={discardIcon} width={14} height={14} />
                </button>
            {/if}
        </div>
    </div>
    <div class="card-list" bind:this={cardListEl} onscroll={handleScroll}>
        {#if onAddTask}
            <button class="add-task-btn" onclick={onAddTask} title={createTaskShortcutLabel ? t('board.column.addTaskTitle', { shortcut: createTaskShortcutLabel }) : t('board.column.newTaskAria')}>{t('board.column.addTask')}{#if createTaskShortcutLabel && !hintsHidden}<kbd class="key-hint">{createTaskShortcutLabel}</kbd>{/if}</button>
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
        transition: background var(--dur-base) ease;
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
    /* Widen the tap target where there is no hover. */
    @media (hover: none) {
        .storage-count {
            padding: 8px 12px;
        }
    }
    .column-header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 12px 16px;
        border-bottom: 2px solid var(--col-color);
        /* Against the header: the column clips overflow. */
        position: relative;
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
    .storage-count {
        display: inline-flex;
        align-items: center;
        gap: 4px;
        border: none;
        font: inherit;
        font-size: var(--text-xs);
        cursor: pointer;
    }
    .storage-count.is-open {
        background: var(--color-surface-4);
    }
    .storage-count.is-low {
        color: var(--color-status-red);
    }
    .storage-warn {
        flex-shrink: 0;
    }
    .storage-tip {
        position: absolute;
        top: calc(100% - 2px);
        right: 12px;
        z-index: var(--z-dropdown);
        padding: 6px 9px;
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        background: var(--color-surface-2);
        box-shadow: var(--shadow-popover);
        color: var(--color-text-secondary);
        font-size: var(--text-xs);
        white-space: nowrap;
    }
    /* Secondary to the reading above, so dimmer. */
    .storage-tip-tasks {
        margin-top: 2px;
        color: var(--color-text-tertiary);
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
        transition: background var(--dur-base) ease, color var(--dur-base) ease;
        padding: 0;
        flex-shrink: 0;
        opacity: 1;
    }
    .add-btn:hover {
        opacity: 1;
        background: var(--color-accent-dark);
        color: var(--color-btn-text);
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
        transition: background var(--dur-base) ease, color var(--dur-base) ease;
        padding: 0;
        flex-shrink: 0;
    }
    .clear-btn:hover {
        opacity: 1;
        background: var(--color-status-red-dark);
        color: var(--color-btn-text);
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
        transition: background var(--dur-base) ease;
        display: flex;
        align-items: center;
        justify-content: center;
    }
    .add-task-btn:hover {
        opacity: 1;
        background: var(--color-accent-dark);
        color: var(--color-btn-text);
    }
    /* The fill arrives only under the pointer, so the family rule in app.css cannot carry this one. */
    .add-task-btn:hover .key-hint {
        opacity: 1;
        color: rgb(from var(--color-btn-text) r g b / 0.85);
        border-color: rgb(from var(--color-btn-text) r g b / 0.3);
        background: rgb(from var(--color-btn-text) r g b / 0.1);
    }
</style>