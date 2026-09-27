<script lang="ts">
    import type { Task } from '$lib/types';
    import { MultiSelect } from '$lib/composables/use-multi-select.svelte';
    import { appStore, dialogStore } from '$lib/stores';
    import { fade } from 'svelte/transition';
    import ChangeModelModal from './ChangeModelModal.svelte';
    import { planBulkMove } from '$lib/utils/task-utils';
    import { createStatusLabel } from '$lib/utils/subtask-status';
    import { t, type MessageKey } from '$lib/i18n';
    import { useClickOutside } from '$lib/composables/use-click-outside.svelte';

    let { multiSelect, tasks, onClear }: {
        multiSelect: MultiSelect;
        tasks: Task[];
        onClear: () => void;
    } = $props();

    let visible = $derived(multiSelect.hasSelection);

    const selectedTasks = $derived.by(() => {
        const taskMap = new Map(tasks.map(t => [t.id, t]));
        return [...multiSelect.selectedIds]
            .map(id => taskMap.get(id))
            .filter((t): t is Task => t != null);
    });

    const selectedTaskTitles = $derived(selectedTasks.map(task => task.title || t('board.untitled')));

    const allInBacklog = $derived(
        selectedTasks.length > 0 && selectedTasks.every(t => t.status === 'backlog')
    );

    const hasRunning = $derived(
        selectedTasks.some(t => t.execution_state === 'running' || t.execution_state === 'pulling_image')
    );

    /** Board order and board ids; only `labelKey` is copy. */
    const columns: { id: string; labelKey: MessageKey }[] = [
        { id: 'backlog', labelKey: 'board.column.backlog' },
        { id: 'in_progress', labelKey: 'board.column.inProgress' },
        { id: 'review', labelKey: 'board.column.review' },
        { id: 'done', labelKey: 'board.column.done' },
    ];

    const statusLabel = createStatusLabel(columns);

    let moveMenuOpen = $state(false);
    let moveWrapEl: HTMLDivElement | null = $state(null);

    useClickOutside(() => moveWrapEl, () => { moveMenuOpen = false; }, { event: 'mousedown' });

    /** With the menu open, Escape belongs to the menu: the board and the task view clear the
     * selection on that keystroke. */
    function handleMenuKey(e: KeyboardEvent) {
        if (e.key === 'Escape' && moveMenuOpen) {
            moveMenuOpen = false;
            e.stopPropagation();
        }
    }

    function titlesFor(ids: string[]): string[] {
        const byId = new Map(selectedTasks.map(task => [task.id, task]));
        return ids.map(id => byId.get(id)?.title || t('board.untitled'));
    }

    /** Plan the move against the live selection, report what cannot go, then ask. */
    async function chooseStatus(status: string) {
        moveMenuOpen = false;
        const plan = planBulkMove(tasks, multiSelect.toArray(), status);
        if (plan.blocked.length > 0) {
            dialogStore.showToast(
                t('board.toast.cannotMoveToStatus', {
                    targetStatus: statusLabel('backlog'),
                    parentStatus: statusLabel('in_progress'),
                }),
                'error',
            );
        }
        if (plan.movable.length === 0) {
            if (plan.alreadyThere > 0) {
                dialogStore.showToast(
                    t('board.multiSelect.moveAlreadyThere', { count: plan.alreadyThere, status: statusLabel(status) }),
                    'error',
                );
            }
            return;
        }
        const params = { count: plan.movable.length, status: statusLabel(status) };
        const confirmed = await dialogStore.showConfirm({
            title: t('board.multiSelect.moveTitle'),
            // The In Progress column starts what it receives, and the question says so.
            message: t(status === 'in_progress' ? 'board.multiSelect.moveStartsMessage' : 'board.multiSelect.moveMessage', params),
            items: titlesFor(plan.movable),
            confirmText: t('board.confirm.move'),
            cancelText: t('board.confirm.cancel'),
            variant: 'default',
        });
        if (!confirmed) return;
        window.dispatchEvent(new CustomEvent('multi-select-move', {
            detail: { taskIds: plan.movable, status }
        }));
    }

    let showModelModal = $state(false);
    let providers = $derived(appStore.config?.providers || []);

    async function handleStart() {
        const titles = selectedTasks.map(task => task.title || t('board.untitled'));
        const confirmed = await dialogStore.showConfirm({
            title: t('board.multiSelect.startTitle'),
            message: t('board.multiSelect.startMessage', { count: multiSelect.count }),
            items: titles,
            confirmText: t('board.confirm.start'),
            cancelText: t('board.confirm.cancel'),
            variant: 'default',
        });
        if (!confirmed) return;
        window.dispatchEvent(new CustomEvent('multi-select-start', {
            detail: { taskIds: multiSelect.toArray() }
        }));
    }

    function handleDelete() {
        const titles = selectedTasks.map(task => task.title || t('board.untitled'));
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
        transition:fade={{ duration: 150 }}
        role="toolbar"
        aria-label={t('board.multiSelect.toolbarAria')}
    >
        {#if allInBacklog}
            <button
                type="button"
                class="bar-btn bar-btn-model"
                onclick={openModelModal}
                aria-label={t('board.multiSelect.modelAria', { count: multiSelect.count })}
            >
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                    <path d="M2 5l6-3 6 3v6l-6 3-6-3V5z" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round"/>
                    <line x1="8" y1="8" x2="8" y2="14" stroke="currentColor" stroke-width="1.5"/>
                    <line x1="2" y1="5" x2="8" y2="8" stroke="currentColor" stroke-width="1.5"/>
                    <line x1="14" y1="5" x2="8" y2="8" stroke="currentColor" stroke-width="1.5"/>
                </svg>
                {t('board.multiSelect.model')}
            </button>
            <button
                type="button"
                class="bar-btn bar-btn-start"
                onclick={handleStart}
                aria-label={t('board.multiSelect.startCount', { count: multiSelect.count })}
            >
                <svg width="14" height="14" viewBox="0 0 10 10" fill="none" aria-hidden="true">
                    <polygon points="3.5,1.5 3.5,8.5 8.5,5" fill="currentColor"/>
                </svg>
                {t('board.multiSelect.startCount', { count: multiSelect.count })}
            </button>
        {/if}

        <div class="move-wrap" bind:this={moveWrapEl}>
            <button
                type="button"
                class="bar-btn bar-btn-move"
                onclick={() => { moveMenuOpen = !moveMenuOpen; }}
                onkeydown={handleMenuKey}
                aria-label={t('board.multiSelect.moveAria', { count: multiSelect.count })}
                aria-haspopup="menu"
                aria-expanded={moveMenuOpen}
            >
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                    <path d="M2 8h10M9 5l3 3-3 3" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                    <path d="M2 2.5v11" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
                </svg>
                {t('board.multiSelect.move')}
            </button>
            {#if moveMenuOpen}
                <div class="move-menu" role="menu" tabindex="-1" onkeydown={handleMenuKey}>
                    {#each columns as col (col.id)}
                        <button
                            type="button"
                            class="move-item"
                            role="menuitem"
                            onclick={() => { chooseStatus(col.id); }}
                        >
                            {t(col.labelKey)}
                        </button>
                    {/each}
                </div>
            {/if}
        </div>

        <button
            type="button"
            class="bar-btn bar-btn-delete"
            onclick={handleDelete}
            aria-label={t('board.multiSelect.deleteCount', { count: multiSelect.count })}
        >
            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                <path d="M2 4h12M5 4V3a1 1 0 011-1h4a1 1 0 011 1v1M6 7v5M10 7v5" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                <path d="M3 4l1 9a1 1 0 001 1h6a1 1 0 001-1l1-9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
            </svg>
            {t('board.multiSelect.deleteCount', { count: multiSelect.count })}
        </button>

        <button
            type="button"
            class="bar-btn bar-btn-clear"
            onclick={onClear}
            aria-label={t('explorer.shared.selectionClear')}
            title={t('explorer.shared.selectionClear')}
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
        left: 0;
        right: 0;
        margin-inline: auto;
        width: max-content;
        max-width: calc(100vw - 32px);
        display: flex;
        align-items: center;
        justify-content: center;
        flex-wrap: wrap;
        gap: 8px;
        padding: 8px 12px;
        background: var(--color-surface-3);
        border: 1px solid var(--color-border-bright);
        border-radius: var(--radius-lg);
        box-shadow: 0 8px 32px rgba(0, 0, 0, 0.35);
        z-index: var(--z-dropdown);
        user-select: none;
    }

    .bar-btn {
        display: inline-flex;
        align-items: center;
        gap: 6px;
        padding: 6px 12px;
        border: none;
        border-radius: var(--radius-sm);
        font-size: var(--text-sm);
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

    .move-wrap {
        position: relative;
    }

    .bar-btn-move {
        background: var(--color-surface-4);
        color: var(--color-text-primary);
    }

    /* The bar is fixed to the bottom of the viewport, so its menu opens upward; the offset clears
     * the bar's own padding and border. */
    .move-menu {
        position: absolute;
        bottom: calc(100% + 17px);
        left: 0;
        display: flex;
        flex-direction: column;
        min-width: 140px;
        padding: 4px;
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        box-shadow: 0 8px 24px rgba(0, 0, 0, 0.35);
    }

    .move-item {
        padding: 6px 10px;
        border: none;
        border-radius: var(--radius-sm);
        background: transparent;
        color: var(--color-text-primary);
        font-family: inherit;
        font-size: var(--text-sm);
        text-align: left;
        white-space: nowrap;
        cursor: pointer;
    }

    .move-item:hover {
        background: var(--color-surface-4);
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
            width: auto;
            max-width: calc(100vw - 16px);
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
