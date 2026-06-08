<script lang="ts">
    import type { Task } from '$lib/types';
    import { get } from 'svelte/store';
    import { selectedTask, showCardDetail, board as boardStore, streamingTaskIds } from '$lib/store';
    import { moveTask, updateTask } from '$lib/api';

    let { task, columnId, projectId }: { task: Task; columnId: string; projectId?: string } = $props();

    const statuses = ['backlog', 'in_progress', 'review', 'done'] as const;
    const statusLabels: Record<string, string> = {
        backlog: 'Backlog',
        in_progress: 'In Progress',
        review: 'Review',
        done: 'Done',
    };

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

    let statusBadge = $derived.by(() => {
        const streaming = get(streamingTaskIds).has(task.id);
        if (streaming) return { text: 'Running', color: 'var(--color-status-blue)' };
        if (task.status_state === 'failed') return { text: 'Failed', color: 'var(--color-status-red)' };
        return null;
    });

    let isSelected = $state(false);

    $effect(() => {
        const unsub = selectedTask.subscribe(t => {
            isSelected = t?.id === task.id;
        });
        return unsub;
    });

    // Inline title editing
    let editingTitle = $state(false);
    let titleDraft = $state('');
    let titleInputRef: HTMLInputElement | null = $state(null);

    function startEdit(e: MouseEvent) {
        e.stopPropagation();
        editingTitle = true;
        titleDraft = task.title;
    }

    function saveTitle() {
        const trimmed = titleDraft.trim();
        if (trimmed && trimmed !== task.title && projectId) {
            task.title = trimmed;
            updateTask(projectId, task.id, { title: trimmed }).catch(err => {
                task.title = titleDraft; // revert on error
                console.error('Failed to update title:', err);
            });
        }
        editingTitle = false;
    }

    function cancelEdit() {
        editingTitle = false;
        titleDraft = '';
    }

    $effect(() => {
        if (editingTitle && titleInputRef) {
            titleInputRef.focus();
            titleInputRef.select();
        }
    });

    function truncate(s: string, max: number): string {
        return s.length > max ? s.slice(0, max) + '...' : s;
    }

    function handleCardKeydown(e: KeyboardEvent) {
        if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault();
            selectedTask.set(task);
            showCardDetail.set(true);
        }
    }

    async function cycleStatus(e: MouseEvent) {
        e.stopPropagation();
        if (!projectId) return;
        const currentIdx = statuses.indexOf(task.status as typeof statuses[number]);
        const nextIdx = (currentIdx + 1) % statuses.length;
        const nextStatus = statuses[nextIdx];
        try {
            const updated = await moveTask(projectId, task.id, nextStatus);
            task.status = updated.status;
            task.column_order = updated.column_order;
            boardStore.update(b => {
                if (!b) return b;
                const idx = b.tasks.findIndex(t => t.id === task.id);
                if (idx !== -1) b.tasks[idx] = updated;
                return { ...b, tasks: [...b.tasks] };
            });
        } catch (err) {
            console.error('Failed to move task:', err);
        }
    }
</script>

<div
    class="card"
    class:selected={isSelected}
    draggable={!isMobile}
    ondragstart={handleDragStart}
    onclick={() => { selectedTask.set(task); showCardDetail.set(true); }}
    onkeydown={handleCardKeydown}
    role="button"
    tabindex="0"
    aria-label={`Open task: ${task.title || '(untitled)'}`}
    aria-current={isSelected ? 'page' : undefined}
>
    {#if editingTitle}
        <input
            class="card-title-input"
            type="text"
            bind:value={titleDraft}
            onblur={saveTitle}
            onkeydown={(e) => { if (e.key === 'Enter') saveTitle(); if (e.key === 'Escape') cancelEdit(); e.stopPropagation(); }}
            oninput={(e) => e.stopPropagation()}
            bind:this={titleInputRef}
            placeholder="Task title"
        />
    {:else}
        <div class="card-title">{truncate(task.title, 50)}</div>
    {/if}
    <div class="card-preview">{truncate(task.prompt, 80)}</div>
    {#if statusBadge}
        <div class="badge" style="background: {statusBadge.color}22; color: {statusBadge.color}">
            {statusBadge.text}
        </div>
    {/if}
    <button
        type="button"
        class="edit-btn"
        onclick={startEdit}
        aria-label="Edit task title"
        title="Edit title"
    >
        <svg width="12" height="12" viewBox="0 0 16 16" fill="none"><path d="M11.5 2.5l2 2L5 13H3v-2L11.5 2.5z" stroke="currentColor" stroke-width="1.3" stroke-linejoin="round"/></svg>
    </button>
    <button
        type="button"
        class="move-btn"
        onclick={cycleStatus}
        aria-label={`Move to ${statusLabels[statuses[(statuses.indexOf(task.status as typeof statuses[number]) + 1) % statuses.length]]}`}
        title={`Move to ${statusLabels[statuses[(statuses.indexOf(task.status as typeof statuses[number]) + 1) % statuses.length]]}`}
    >
        ↗
    </button>
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
    .card.selected {
        border-color: var(--color-accent);
        box-shadow: 0 0 0 2px var(--color-accent);
        background: var(--color-surface-3);
    }
    .card:active {
        cursor: grabbing;
    }
    .card-title {
        font-size: 0.875rem;
        font-weight: 500;
        margin-bottom: 6px;
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
    }
    .card-title-input {
        font-size: 0.875rem;
        font-weight: 500;
        margin-bottom: 6px;
        width: 100%;
        background: var(--color-surface-1);
        border: 1px solid var(--color-accent);
        border-radius: var(--radius-sm);
        padding: 2px 6px;
        color: var(--color-text-primary);
        font-family: inherit;
        outline: none;
    }
    .card-preview {
        font-size: 0.75rem;
        color: var(--color-text-secondary);
        line-height: 1.4;
        display: -webkit-box;
        -webkit-line-clamp: 2;
        -webkit-box-orient: vertical;
        overflow: hidden;
    }
    .badge {
        display: inline-block;
        font-size: 0.75rem;
        padding: 2px 6px;
        border-radius: var(--radius-sm);
        margin-top: 8px;
        font-weight: 500;
    }
    .edit-btn,
    .move-btn {
        position: absolute;
        top: 8px;
        width: 24px;
        height: 24px;
        display: flex;
        align-items: center;
        justify-content: center;
        border-radius: var(--radius-sm);
        background: transparent;
        color: var(--color-text-tertiary);
        font-size: 0.875rem;
        opacity: 0;
        transition: opacity 0.15s ease, background 0.15s ease;
        padding: 0;
        border: none;
        cursor: pointer;
    }
    .edit-btn {
        right: 36px;
    }
    .move-btn {
        right: 8px;
    }
    .card:hover .edit-btn,
    .card:hover .move-btn,
    .card.selected .edit-btn {
        opacity: 1;
    }
    .edit-btn:hover,
    .move-btn:hover {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }
</style>