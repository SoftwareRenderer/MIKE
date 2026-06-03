<script lang="ts">
    import { get } from 'svelte/store';
    import type { Task } from '$lib/types';
    import { startTask, stopTask, retryTask, resumeTask, moveTask } from '$lib/api';
    import { board as boardStore, selectedTask, showCardDetail } from '$lib/store';

    let { task, columnId, projectId }: { task: Task; columnId: string; projectId: string } = $props();

    function handleDragStart(e: DragEvent) {
        e.dataTransfer?.setData('text/plain', task.id);
    }

    async function handleAction(action: string) {
        try {
            let updated: Task;
            switch (action) {
                case 'start':
                    updated = await startTask(projectId, task.id);
                    break;
                case 'stop':
                    updated = await stopTask(projectId, task.id);
                    break;
                case 'retry':
                    updated = await retryTask(projectId, task.id);
                    break;
                case 'resume':
                    updated = await resumeTask(projectId, task.id);
                    break;
                case 'done':
                    updated = await moveTask(projectId, task.id, 'done');
                    break;
                default:
                    return;
            }
            const board = get(boardStore);
            if (board) {
                const idx = board.tasks.findIndex(t => t.id === task.id);
                if (idx !== -1) {
                    board.tasks[idx] = updated;
                    boardStore.set(board);
                }
            }
        } catch (e) {
            console.error('Action failed:', e);
        }
    }

    function getStatusBadge(): { text: string; color: string } | null {
        switch (task.status_state) {
            case 'running': return { text: 'Running', color: 'var(--color-status-blue)' };
            case 'queued': return { text: 'Queued', color: 'var(--color-status-yellow)' };
            case 'paused': return { text: 'Paused', color: 'var(--color-status-purple)' };
            case 'awaiting_review': return { text: 'Review', color: 'var(--color-status-orange)' };
            case 'failed': return { text: 'Failed', color: 'var(--color-status-red)' };
            default: return null;
        }
    }

    function truncate(s: string, max: number): string {
        return s.length > max ? s.slice(0, max) + '...' : s;
    }
</script>

<div class="card" draggable="true" ondragstart={handleDragStart} onclick={() => { selectedTask.set(task); showCardDetail.set(true); }}>
    <div class="card-title">{truncate(task.title, 50)}</div>
    <div class="card-preview">{truncate(task.prompt, 80)}</div>
    {#if getStatusBadge()}
        <div class="badge" style="background: {getStatusBadge()!.color}22; color: {getStatusBadge()!.color}">
            {getStatusBadge()!.text}
        </div>
    {/if}
    <div class="quick-actions">
        {#if columnId === 'backlog'}
            <button class="action-btn" title="Start" onclick={(e) => { e.stopPropagation(); handleAction('start'); }}>▶</button>
        {:else if columnId === 'in_progress'}
            {#if task.status_state === 'running'}
                <button class="action-btn stop" title="Stop" onclick={(e) => { e.stopPropagation(); handleAction('stop'); }}>■</button>
            {:else if task.status_state === 'paused'}
                <button class="action-btn resume" title="Resume" onclick={(e) => { e.stopPropagation(); handleAction('resume'); }}>▶</button>
            {:else if task.status_state === 'failed'}
                <button class="action-btn retry" title="Retry" onclick={(e) => { e.stopPropagation(); handleAction('retry'); }}>↻</button>
            {/if}
        {:else if columnId === 'review'}
            <button class="action-btn done" title="Accept" onclick={(e) => { e.stopPropagation(); handleAction('done'); }}>✓</button>
        {/if}
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
    }
    .card:hover {
        border-color: var(--color-border-bright);
    }
    .card:active {
        cursor: grabbing;
    }
    .card-title {
        font-size: 14px;
        font-weight: 500;
        margin-bottom: 6px;
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
    }
    .card-preview {
        font-size: 12px;
        color: var(--color-text-secondary);
        line-height: 1.4;
        display: -webkit-box;
        -webkit-line-clamp: 2;
        -webkit-box-orient: vertical;
        overflow: hidden;
    }
    .badge {
        display: inline-block;
        font-size: 11px;
        padding: 2px 6px;
        border-radius: var(--radius-sm);
        margin-top: 8px;
        font-weight: 500;
    }
    .quick-actions {
        position: absolute;
        top: 8px;
        right: 8px;
        display: none;
        gap: 4px;
    }
    .card:hover .quick-actions {
        display: flex;
    }
    .action-btn {
        width: 24px;
        height: 24px;
        border-radius: var(--radius-sm);
        background: var(--color-surface-3);
        font-size: 10px;
        display: flex;
        align-items: center;
        justify-content: center;
    }
    .action-btn.stop { background: var(--color-status-red)33; color: var(--color-status-red); }
    .action-btn.resume { background: var(--color-status-blue)33; color: var(--color-status-blue); }
    .action-btn.retry { background: var(--color-status-orange)33; color: var(--color-status-orange); }
    .action-btn.done { background: var(--color-status-green)33; color: var(--color-status-green); }
</style>