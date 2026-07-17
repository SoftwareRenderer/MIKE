<script lang="ts">
    import type { Task } from '$lib/types';
    import commitPushIcon from '$lib/assets/icons/commit-push.svg?raw';
    import Icon from '../Icon.svelte';

    let {
        task,
        columnId,
        actionCount,
        isDoneSubtask,
        isPausable,
        isResumable,
        isIdle,
        hasChildren,
        inline,
        showSubtaskBorder,
        hasChanges,
        nextStatus,
        statusLabels,
        statusColors,
        truncate,
        onMouseDown,
        onDragStart,
        onClick,
        onKeydown,
        onCreateChild,
        onEdit,
        onPause,
        onResume,
        onDelete,
        onCycleStatus,
    }: {
        task: Task;
        columnId: string;
        actionCount: number;
        isDoneSubtask: boolean;
        isPausable: boolean;
        isResumable: boolean;
        isIdle: boolean;
        hasChildren: boolean;
        inline: boolean;
        showSubtaskBorder: boolean;
        hasChanges: boolean;
        nextStatus: string;
        statusLabels: Record<string, string>;
        statusColors: Record<string, string>;
        truncate: (s: string, max: number) => string;
        onMouseDown: (e: MouseEvent) => void;
        onDragStart: (e: DragEvent) => void;
        onClick: (e: MouseEvent) => void;
        onKeydown: (e: KeyboardEvent) => void;
        onCreateChild: (e: MouseEvent) => void;
        onEdit: (e: MouseEvent) => void;
        onPause: (e: MouseEvent) => void;
        onResume: (e: MouseEvent) => void;
        onDelete: (e: MouseEvent) => void;
        onCycleStatus: (e: MouseEvent) => void;
    } = $props();
</script>

<div
    class="card"
    data-task-id={task.id}
    onmousedown={onMouseDown}
    ondragstart={onDragStart}
    onclick={onClick}
    onkeydown={onKeydown}
    role="button"
    tabindex="0"
    aria-label={`Open task: ${task.title || '(untitled)'}, Status: ${statusLabels[task.status] || task.status}`}
    aria-keyshortcuts="Alt+ArrowLeft, Alt+ArrowRight"
    style="--actions: {actionCount}; --column-color: {statusColors[columnId] || ''}"
>
    <div class="card-title">
        {#if task.title_generating}
            <span class="title-spinner" aria-label="Generating title">
                <svg viewBox="0 0 16 16" width="12" height="12"><circle cx="8" cy="8" r="6" fill="none" stroke="currentColor" stroke-width="2" stroke-dasharray="30 14" stroke-linecap="round"/></svg>
            </span>
        {/if}
        <span class="card-title-text">{truncate(task.title, 80)}</span>
        <div class="card-actions">
            {#if !inline || !task.parent_task_id || hasChildren || showSubtaskBorder}
                <button type="button" class="action-icon action-add" onclick={onCreateChild} aria-label="Create subtask" title="Create subtask">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none"><path d="M8 3v10M3 8h10" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg>
                </button>
            {/if}
            {#if !isDoneSubtask}
                <button type="button" class="action-icon" onclick={onEdit} aria-label="Edit task" title="Edit Task">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none"><path d="M11.5 2.5l2 2L5 13H3v-2L11.5 2.5z" stroke="currentColor" stroke-width="1.3" stroke-linejoin="round"/></svg>
                </button>
            {/if}
            {#if isPausable}
                <button type="button" class="action-icon" onclick={onPause} aria-label="Pause task" title="Pause task">
                    <svg width="14" height="14" viewBox="0 0 10 10" fill="none" aria-hidden="true"><rect x="2.5" y="1.5" width="1.5" height="7" fill="currentColor"/><rect x="6" y="1.5" width="1.5" height="7" fill="currentColor"/></svg>
                </button>
            {:else if isResumable}
                <button type="button" class="action-icon" onclick={onResume} aria-label="Resume task" title="Resume task">
                    <svg width="14" height="14" viewBox="0 0 10 10" fill="none" aria-hidden="true"><polygon points="3.5,1.5 3.5,8.5 8.5,5" fill="currentColor"/></svg>
                </button>
            {:else if isIdle}
                <span class="action-idle" aria-label="Idle" title="Idle">Idle</span>
            {:else if columnId === 'done'}
                <button type="button" class="action-icon action-delete" onclick={onDelete} aria-label="Delete task" title="Delete task">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                        <path d="M2 4h12M5 4V3a1 1 0 011-1h4a1 1 0 011 1v1M6 7v5M10 7v5" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                        <path d="M3 4l1 9a1 1 0 001 1h6a1 1 0 001-1l1-9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                    </svg>
                </button>
            {:else if isDoneSubtask}
                <button type="button" class="action-icon action-delete" onclick={onDelete} aria-label="Delete task" title="Delete task">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                        <path d="M2 4h12M5 4V3a1 1 0 011-1h4a1 1 0 011 1v1M6 7v5M10 7v5" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                        <path d="M3 4l1 9a1 1 0 001 1h6a1 1 0 001-1l1-9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                    </svg>
                </button>
            {:else if inline && task.parent_task_id}
                <!-- no move button for inline subtasks -->
            {:else if task.status === 'in_progress'}
                <!-- no move button for in_progress tasks -->
            {:else}
                <button
                    type="button"
                    class="action-icon action-move"
                    onclick={onCycleStatus}
                    aria-label={nextStatus === 'done' ? (hasChanges ? 'Commit and move to done' : `Move to ${statusLabels[nextStatus]}`) : `Move to ${statusLabels[nextStatus]}`}
                    title={nextStatus === 'done' ? (hasChanges ? 'Commit and move to done' : `Move to ${statusLabels[nextStatus]}`) : `Move to ${statusLabels[nextStatus]}`}
                    style="color: {nextStatus === 'done' && hasChanges ? '#EAB308' : statusColors[nextStatus]}"
                >
                    {#if nextStatus === 'backlog'}
                        <svg width="14" height="14" viewBox="0 0 10 10" fill="none" aria-hidden="true"><circle cx="5" cy="5" r="3.5" stroke="currentColor" stroke-width="1.5"/></svg>
                    {:else if nextStatus === 'in_progress'}
                        <svg width="14" height="14" viewBox="0 0 10 10" fill="none" aria-hidden="true"><polygon points="3.5,1.5 3.5,8.5 8.5,5" fill="currentColor"/></svg>
                    {:else if nextStatus === 'review'}
                        <svg width="14" height="14" viewBox="0 0 10 10" fill="none" aria-hidden="true"><circle cx="5" cy="5" r="3.5" stroke="currentColor" stroke-width="1"/><circle cx="5" cy="5" r="1.2" fill="currentColor"/></svg>
                    {:else if nextStatus === 'done' && hasChanges}
                        <Icon src={commitPushIcon} color="#eab308" width={14} height={14} />
                    {:else if nextStatus === 'done'}
                        <svg width="14" height="14" viewBox="0 0 10 10" fill="none" aria-hidden="true"><circle cx="5" cy="5" r="3.5" fill="currentColor"/></svg>
                    {/if}
                </button>
            {/if}
        </div>
    </div>
</div>

<style>
    .card {
        background: var(--color-surface-2);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-md);
        padding: 12px;
        cursor: grab;
        position: relative;
        transition: border-color 0.15s ease;
        user-select: none;
        -webkit-user-select: none;
        -webkit-user-drag: element;
        will-change: transform;
        contain: layout style paint;
    }
    .card:hover {
        border-color: var(--color-border-bright);
    }
    .card:active {
        cursor: grabbing;
    }

    .card-title {
        display: flex;
        align-items: center;
        font-size: 0.875rem;
        font-weight: 500;
        margin-bottom: 6px;
        position: relative;
    }
    .card-title-text {
        flex: 1;
        min-width: 0;
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
        padding-right: 0;
        transition: padding-right 0.15s ease;
    }
    .title-spinner {
        display: inline-flex;
        align-items: center;
        color: var(--color-text-tertiary);
        margin-right: 6px;
        flex-shrink: 0;
        animation: spin 1s linear infinite;
    }
    @keyframes spin {
        from { transform: rotate(0deg); }
        to { transform: rotate(360deg); }
    }
    .card:hover:not(:has(.card:hover)) > .card-title > .card-title-text {
        padding-right: calc(var(--actions) * 28px + 6px);
    }

    .card-actions {
        position: absolute;
        top: 0;
        right: 0;
        display: flex;
        align-items: center;
        gap: 2px;
        opacity: 0;
        pointer-events: none;
        transition: opacity 0.15s ease;
    }
    .card:hover:not(:has(.card:hover)) > .card-title > .card-actions {
        opacity: 1;
        pointer-events: auto;
    }
    .action-icon {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 26px;
        height: 26px;
        border-radius: var(--radius-sm);
        background: transparent;
        color: var(--color-text-tertiary);
        padding: 0;
        border: none;
        cursor: pointer;
        transition: background 0.1s ease, color 0.1s ease;
    }
    .action-icon:hover {
        background: var(--color-accent-dark);
        color: white;
    }
    .action-icon.action-move:hover {
        background: var(--color-accent-dark);
        color: white !important;
    }
    .action-icon.action-delete:hover {
        background: var(--color-status-red-dark);
        color: white;
    }
    .action-idle {
        display: flex;
        align-items: center;
        justify-content: center;
        font-size: 0.75rem;
        font-weight: 500;
        text-transform: uppercase;
        letter-spacing: 0.3px;
        color: var(--color-text-tertiary);
        padding: 0 4px;
        pointer-events: none;
    }

    @media (hover: none) {
        .card-actions {
            opacity: 1;
            transition: none;
        }
        .card-title-text {
            padding-right: calc(var(--actions) * 28px + 6px);
        }
    }
</style>
