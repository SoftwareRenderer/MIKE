<script lang="ts">
    import type { Task } from '$lib/types';
    import Card from './Card.svelte';
    import { createEventDispatcher } from 'svelte';

    export let columnId: string;
    export let title: string;
    export let color: string;
    export let tasks: Task[];

    let isOver = $state(false);

    function handleDragOver(e: DragEvent) {
        e.preventDefault();
        isOver = true;
    }

    function handleDragLeave() {
        isOver = false;
    }

    function handleDrop(e: DragEvent) {
        isOver = false;
        const taskId = e.dataTransfer?.getData('text/plain');
        if (taskId) {
            createEventDispatcher().dispatch('drop', { taskId, columnId });
        }
    }
</script>

<div class="column" class:is-over ondragover={handleDragOver} ondragleave={handleDragLeave} ondrop={handleDrop}>
    <div class="column-header" style="--col-color: {color}">
        <h3>{title}</h3>
        <span class="count">{tasks.length}</span>
    </div>
    <div class="card-list">
        {#each tasks as task (task.id)}
            <Card {task} columnId={columnId} projectId={columnId} />
        {/each}
    </div>
</div>

<style>
    .column {
        flex: 1;
        min-width: 260px;
        max-width: 340px;
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
    .column-header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 12px 16px;
        border-bottom: 2px solid var(--col-color);
    }
    .column-header h3 {
        font-size: 13px;
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.5px;
        color: var(--color-text-secondary);
    }
    .count {
        background: var(--color-surface-3);
        border-radius: 10px;
        padding: 2px 8px;
        font-size: 12px;
        color: var(--color-text-secondary);
    }
    .card-list {
        flex: 1;
        overflow-y: auto;
        padding: 8px;
        display: flex;
        flex-direction: column;
        gap: 8px;
    }
</style>