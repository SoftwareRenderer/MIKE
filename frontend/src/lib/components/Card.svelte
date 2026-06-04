<script lang="ts">
    import type { Task } from '$lib/types';
    import { selectedTask, showCardDetail } from '$lib/store';

    let { task, columnId }: { task: Task; columnId: string } = $props();

    function handleDragStart(e: DragEvent) {
        e.dataTransfer?.setData('text/plain', task.id);
    }

    let isMobile = $derived(false);

    $effect(() => {
        const check = () => isMobile = window.matchMedia('(max-width: 768px)').matches;
        check();
        window.addEventListener('resize', check);
        return () => window.removeEventListener('resize', check);
    });

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

<div class="card" draggable={!isMobile} ondragstart={handleDragStart} onclick={() => { selectedTask.set(task); showCardDetail.set(true); }}>
    <div class="card-title">{truncate(task.title, 50)}</div>
    <div class="card-preview">{truncate(task.prompt, 80)}</div>
    {#if getStatusBadge()}
        <div class="badge" style="background: {getStatusBadge()!.color}22; color: {getStatusBadge()!.color}">
            {getStatusBadge()!.text}
        </div>
    {/if}
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
</style>