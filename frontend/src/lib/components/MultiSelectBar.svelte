<script lang="ts">
    import type { Task } from '$lib/types';
    import { MultiSelect } from '$lib/composables/use-multi-select.svelte';
    import { appStore, dialogStore } from '$lib/stores';
    import { fade } from 'svelte/transition';
    import ChangeModelModal from './ChangeModelModal.svelte';

    let { multiSelect, tasks, projectId, onClear }: {
        multiSelect: MultiSelect;
        tasks: Task[];
        projectId: string;
        onClear: () => void;
    } = $props();

    let visible = $derived(multiSelect.hasSelection);

    // Build a task lookup for the selected tasks
    const selectedTasks = $derived.by(() => {
        const taskMap = new Map(tasks.map(t => [t.id, t]));
        return [...multiSelect.selectedIds]
            .map(id => taskMap.get(id))
            .filter((t): t is Task => t != null);
    });

    // Task titles for the model change modal
    const selectedTaskTitles = $derived(selectedTasks.map(t => t.title || '(untitled)'));

    // Check if all selected tasks are in Backlog
    const allInBacklog = $derived(
        selectedTasks.length > 0 && selectedTasks.every(t => t.status === 'backlog')
    );

    // Check if any selected task is running
    const hasRunning = $derived(
        selectedTasks.some(t => t.execution_state === 'running' || t.execution_state === 'pulling_image')
    );

    // Bar positioning (fixed at bottom of viewport, centered)
    let barRef: HTMLDivElement | null = $state(null);

    // Model change modal
    let showModelModal = $state(false);
    let providers = $derived(appStore.config?.providers || []);

    async function handleStart() {
        const titles = selectedTasks.map(t => t.title || '(untitled)');
        const confirmed = await dialogStore.showConfirm({
            title: 'Start Tasks',
            message: `Start ${multiSelect.count} task${multiSelect.count > 1 ? 's' : ''}?`,
            items: titles,
            confirmText: 'Start',
            cancelText: 'Cancel',
            variant: 'default',
        });
        if (!confirmed) return;
        window.dispatchEvent(new CustomEvent('multi-select-start', {
            detail: { taskIds: multiSelect.toArray() }
        }));
    }

    function handleDelete() {
        const titles = selectedTasks.map(t => t.title || '(untitled)');
        window.dispatchEvent(new CustomEvent('multi-select-delete', {
            detail: { taskIds: multiSelect.toArray(), titles }
        }));
    }

    function openModelModal() {
        showModelModal = true;
    }

    function closeModelModal() {
        showModelModal = false;
    }
</script>

{#if visible}
    <div
        class="multi-select-bar"
        class:has-running={hasRunning}
        bind:this={barRef}
        transition:fade={{ duration: 150 }}
        role="toolbar"
        aria-label="Multi-select actions"
    >
        {#if allInBacklog}
            <button
                type="button"
                class="bar-btn bar-btn-model"
                onclick={openModelModal}
                aria-label="Change model for {multiSelect.count} tasks"
            >
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                    <path d="M2 5l6-3 6 3v6l-6 3-6-3V5z" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round"/>
                    <line x1="8" y1="8" x2="8" y2="14" stroke="currentColor" stroke-width="1.5"/>
                    <line x1="2" y1="5" x2="8" y2="8" stroke="currentColor" stroke-width="1.5"/>
                    <line x1="14" y1="5" x2="8" y2="8" stroke="currentColor" stroke-width="1.5"/>
                </svg>
                Model
            </button>
            <button
                type="button"
                class="bar-btn bar-btn-start"
                onclick={handleStart}
                aria-label="Start {multiSelect.count} tasks"
            >
                <svg width="14" height="14" viewBox="0 0 10 10" fill="none" aria-hidden="true">
                    <polygon points="3.5,1.5 3.5,8.5 8.5,5" fill="currentColor"/>
                </svg>
                Start {multiSelect.count} task{multiSelect.count !== 1 ? 's' : ''}
            </button>
        {/if}

        <button
            type="button"
            class="bar-btn bar-btn-delete"
            onclick={handleDelete}
            aria-label="Delete {multiSelect.count} tasks"
        >
            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                <path d="M2 4h12M5 4V3a1 1 0 011-1h4a1 1 0 011 1v1M6 7v5M10 7v5" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                <path d="M3 4l1 9a1 1 0 001 1h6a1 1 0 001-1l1-9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
            </svg>
            Delete {multiSelect.count} task{multiSelect.count !== 1 ? 's' : ''}
        </button>

        <button
            type="button"
            class="bar-btn bar-btn-clear"
            onclick={onClear}
            aria-label="Clear selection"
        >
            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                <path d="M4 4l8 8M12 4l-8 8" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
            </svg>
        </button>
    </div>
{/if}

{#if showModelModal}
    <ChangeModelModal
        providers={providers}
        config={appStore.config}
        taskCount={multiSelect.count}
        taskTitles={selectedTaskTitles}
        onClose={closeModelModal}
    />
{/if}

<style>
    .multi-select-bar {
        position: fixed;
        bottom: 24px;
        left: 50%;
        transform: translateX(-50%);
        display: flex;
        align-items: center;
        gap: 8px;
        padding: 8px 12px;
        background: var(--color-surface-3);
        border: 1px solid var(--color-border-bright);
        border-radius: var(--radius-lg);
        box-shadow: 0 8px 32px rgba(0, 0, 0, 0.35);
        z-index: 100;
        user-select: none;
    }

    .bar-btn {
        display: inline-flex;
        align-items: center;
        gap: 6px;
        padding: 6px 12px;
        border: none;
        border-radius: var(--radius-sm);
        font-size: 0.8125rem;
        font-weight: 500;
        font-family: inherit;
        cursor: pointer;
        transition: background 0.15s ease, color 0.15s ease, opacity 0.15s ease;
        white-space: nowrap;
        color: var(--color-text-primary);
        background: var(--color-surface-4);
    }

    .bar-btn:hover {
        opacity: 0.85;
    }

    .bar-btn-start {
        background: var(--color-accent-dark);
        color: white;
    }
    .bar-btn-start:hover {
        background: var(--color-accent-hover);
        opacity: 1;
    }

    .bar-btn-model {
        background: var(--color-surface-4);
        color: var(--color-text-primary);
    }
    .bar-btn-model:hover {
        background: var(--color-surface-3);
        opacity: 1;
    }

    .bar-btn-delete {
        background: var(--color-status-red-dark);
        color: white;
    }

    .bar-btn-clear {
        padding: 6px 8px;
        background: transparent;
        color: var(--color-text-tertiary);
    }

    .bar-btn-clear:hover {
        background: var(--color-surface-4);
        color: var(--color-text-primary);
    }

    @media (max-width: 768px) {
        .multi-select-bar {
            bottom: 16px;
            left: 8px;
            right: 8px;
            transform: none;
            padding: 10px 12px;
        }

        .bar-btn {
            padding: 8px 14px;
        }

        .bar-btn-clear {
            margin-left: auto;
        }
    }
</style>
