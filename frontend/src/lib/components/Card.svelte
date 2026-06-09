<script lang="ts">
    import type { Task } from '$lib/types';
    import { get } from 'svelte/store';
    import { selectedTask, showCardDetail, board as boardStore, streamingTaskIds, config as configStore } from '$lib/store';
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

    // Model badge: configurable via card_model_badge setting
    // "auto" = model only (1 provider) or provider+model (2+), "always" = provider+model, "model" = model only, "none" = hidden
    // Shows even when no model is set (displays default model as placeholder)
    const MODEL_COLORS = [
        '#6E7681', '#4C9AFF', '#3FB950', '#D29922', '#F85149',
        '#A371F7', '#E3B341', '#79C0FF', '#7EE787', '#FF7B72',
        '#D2A8FF', '#F0883E', '#56B6C2', '#F778BA', '#DB6D28',
    ];
    function getModelColor(modelName: string): string {
        let hash = 0;
        for (let i = 0; i < modelName.length; i++) {
            hash = ((hash << 5) - hash + modelName.charCodeAt(i)) | 0;
        }
        return MODEL_COLORS[Math.abs(hash) % MODEL_COLORS.length];
    }
    let modelBadge = $derived.by(() => {
        const cfg = get(configStore);
        const style = (cfg?.card_model_badge || '').trim() || 'auto';
        if (style === 'none') return { text: null, color: '' };
        const providers = cfg?.providers || [];
        if (providers.length === 0) return { text: null, color: '' };
        // Use task model if set, otherwise fall back to default model
        const getModelName = (m: string | { name: string }) => typeof m === 'string' ? m : m.name;
        const activeModel = task.model_name || (providers[0]?.default_model && providers[0]?.models.some(m => getModelName(m) === providers[0].default_model) ? providers[0].default_model : providers[0]?.models.length > 0 ? getModelName(providers[0].models[0]) : null);
        if (!activeModel) return { text: null, color: '' };
        // Find matching provider by URL
        const provider = providers.find(p => p.url === task.llama_server_url);
        const showProvider = style === 'always' || (style === 'auto' && providers.length > 1);
        const text = showProvider && provider ? `${provider.display_name} · ${activeModel}` : activeModel;
        return { text, color: getModelColor(activeModel) };
    });

    // Model picker dropdown — only in Backlog column (task not yet started)
    let modelPickerOpen = $state(false);
    let modelBadgeRef: HTMLButtonElement | null = $state(null);
    let pickerRect = $state<{ top: number; left: number } | null>(null);
    let providers = $derived(get(configStore)?.providers || []);
    let isConfigurable = $derived(columnId === 'backlog');

    function togglePicker(e: MouseEvent) {
        e.stopPropagation(); // Prevent card from opening when clicking the model badge
        if (!isConfigurable) return;
        if (modelPickerOpen) {
            modelPickerOpen = false;
            pickerRect = null;
            return;
        }
        if (!modelBadgeRef || providers.length === 0) return;
        const r = modelBadgeRef.getBoundingClientRect();
        pickerRect = { top: r.bottom + 4, left: r.left };
        modelPickerOpen = true;
    }

    function closePicker() {
        modelPickerOpen = false;
        pickerRect = null;
    }

    async function selectModel(providerUrl: string, modelName: string) {
        if (!projectId) return;
        try {
            const updated = await updateTask(projectId, task.id, { model_name: modelName, llama_server_url: providerUrl });
            task.model_name = updated.model_name;
            task.llama_server_url = updated.llama_server_url;
            boardStore.update(b => {
                if (!b) return b;
                const idx = b.tasks.findIndex(t => t.id === task.id);
                if (idx !== -1) b.tasks[idx] = updated;
                return { ...b };
            });
        } catch (err) {
            console.error('Failed to update model:', err);
        }
        closePicker();
    }

    $effect(() => {
        if (!modelPickerOpen) return;
        function handleClick(e: MouseEvent) {
            if (modelBadgeRef && modelBadgeRef.contains(e.target as Node)) return;
            const portal = document.querySelector('.portal-card-model-picker');
            if (portal && portal.contains(e.target as Node)) return;
            closePicker();
        }
        function handleKey(e: KeyboardEvent) {
            if (e.key === 'Escape') closePicker();
        }
        document.addEventListener('mousedown', handleClick);
        document.addEventListener('keydown', handleKey);
        return () => {
            document.removeEventListener('mousedown', handleClick);
            document.removeEventListener('keydown', handleKey);
        };
    });

    // Adjust dropdown position after render (flip if overflowing)
    $effect(() => {
        if (!modelPickerOpen || !pickerRect || !modelBadgeRef) return;
        const raf = requestAnimationFrame(() => {
            const portal = document.querySelector('.portal-card-model-picker');
            if (!portal) return;
            const triggerTop = modelBadgeRef!.getBoundingClientRect().top;
            const dropdownHeight = portal.getBoundingClientRect().height;
            const spaceBelow = window.innerHeight - (triggerTop + modelBadgeRef!.getBoundingClientRect().height);
            if (spaceBelow < dropdownHeight + 12) {
                pickerRect = { top: triggerTop - dropdownHeight - 6, left: pickerRect.left };
            }
        });
        return () => cancelAnimationFrame(raf);
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

    const statusColors: Record<string, string> = {
        backlog: 'var(--color-column-backlog)',
        in_progress: 'var(--color-column-in-progress)',
        review: 'var(--color-column-review)',
        done: 'var(--color-column-done)',
    };

    function getNextStatus(): string {
        const currentIdx = statuses.indexOf(task.status as typeof statuses[number]);
        const nextIdx = (currentIdx + 1) % statuses.length;
        return statuses[nextIdx];
    }

    let nextStatus = $derived(getNextStatus());

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
    {#if statusBadge || modelBadge.text}
        <div class="badges-row">
            {#if statusBadge}
                <div class="badge" style="background: {statusBadge.color}22; color: {statusBadge.color}">
                    {statusBadge.text}
                </div>
            {/if}
            {#if modelBadge.text}
                {#if isConfigurable}
                    <button
                        type="button"
                        class="badge model-badge"
                        class:open={modelPickerOpen}
                        style="background: {modelBadge.color}22; color: {modelBadge.color}"
                        onclick={togglePicker}
                        bind:this={modelBadgeRef}
                        aria-label="Change model"
                        aria-haspopup="listbox"
                        aria-expanded={modelPickerOpen}
                    >
                        {modelBadge.text}
                        <svg class="badge-chevron" width="10" height="10" viewBox="0 0 16 16" fill="none"><path d="M4 6l4 4 4-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
                    </button>
                {:else}
                    <div class="badge model-badge" style="background: {modelBadge.color}22; color: {modelBadge.color}">
                        {modelBadge.text}
                    </div>
                {/if}
            {/if}
        </div>
    {/if}
    <button
        type="button"
        class="edit-btn"
        onclick={startEdit}
        aria-label="Edit task title"
        title="Edit title"
    >
        <svg width="16" height="16" viewBox="0 0 16 16" fill="none"><path d="M11.5 2.5l2 2L5 13H3v-2L11.5 2.5z" stroke="currentColor" stroke-width="1.3" stroke-linejoin="round"/></svg>
    </button>
    <button
        type="button"
        class="move-btn"
        onclick={cycleStatus}
        aria-label={`Move to ${statusLabels[nextStatus]}`}
        title={`Move to ${statusLabels[nextStatus]}`}
        style="color: {statusColors[nextStatus]}"
    >
        {#if nextStatus === 'backlog'}
            <svg width="16" height="16" viewBox="0 0 10 10" fill="none" aria-hidden="true"><circle cx="5" cy="5" r="3.5" stroke="currentColor" stroke-width="1.5"/></svg>
        {:else if nextStatus === 'in_progress'}
            <svg width="16" height="16" viewBox="0 0 10 10" fill="none" aria-hidden="true"><polygon points="3.5,1.5 3.5,8.5 8.5,5" fill="currentColor"/></svg>
        {:else if nextStatus === 'review'}
            <svg width="16" height="16" viewBox="0 0 10 10" fill="none" aria-hidden="true"><circle cx="5" cy="5" r="3.5" stroke="currentColor" stroke-width="1"/><circle cx="5" cy="5" r="1.2" fill="currentColor"/></svg>
        {:else if nextStatus === 'done'}
            <svg width="16" height="16" viewBox="0 0 10 10" fill="none" aria-hidden="true"><circle cx="5" cy="5" r="3.5" fill="currentColor"/></svg>
        {/if}
    </button>
</div>

<!-- Model picker dropdown (portal to escape card overflow) -->
{#if modelPickerOpen && pickerRect && providers.length > 0}
    <svelte:element this="div" class="portal-card-model-picker" role="listbox" aria-label="Select model" style="position:fixed;top:{pickerRect.top}px;left:{pickerRect.left}px;z-index:10000;min-width:200px;max-height:320px;overflow-y:auto;background:var(--color-surface-1,#1e1e2e);border:1px solid var(--color-border,#333);border-radius:var(--radius-md,8px);box-shadow:0 8px 24px rgba(0,0,0,0.2);padding:4px;">
        {#each providers as provider}
            {#if provider.models.length > 0}
                <div class="picker-provider-header">{provider.display_name || provider.name}</div>
                {#each provider.models as model}
                    {@const modelName = typeof model === 'string' ? model : model.name}
                    <button type="button" class="picker-model-option" class:active={modelName === task.model_name} onclick={() => selectModel(provider.url, modelName)} role="option" aria-selected={modelName === task.model_name}>
                        <span class="option-name">{modelName}</span>
                        {#if modelName === task.model_name}
                            <svg width="12" height="12" viewBox="0 0 16 16" fill="none"><path d="M3 8l4 4 6-8" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/></svg>
                        {/if}
                    </button>
                {/each}
            {/if}
        {/each}
    </svelte:element>
{/if}

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
        padding-right: 64px;
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
        padding-right: 64px;
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
    .badges-row {
        display: flex;
        flex-wrap: wrap;
        gap: 4px;
        margin-top: 8px;
    }
    .badge {
        display: inline-flex;
        align-items: center;
        gap: 3px;
        font-size: 0.75rem;
        padding: 2px 6px;
        border-radius: var(--radius-sm);
        font-weight: 500;
        font-family: inherit;
    }
    .badge[model-badge] {
        /* read-only badge (div) */
    }
    button.badge {
        border: none;
        cursor: pointer;
        transition: background 0.1s ease;
    }
    button.badge:hover {
        background: var(--color-text-tertiary)33 !important;
    }
    button.badge.open {
        background: var(--color-text-tertiary)44 !important;
    }
    .badge-chevron {
        flex-shrink: 0;
        opacity: 0.5;
        transition: transform 0.15s ease;
    }
    .badge.open .badge-chevron {
        transform: rotate(180deg);
    }

    /* Model picker dropdown styles */
    .picker-provider-header {
        padding: 6px 8px 2px;
        font-size: 0.6875rem;
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.4px;
        color: var(--color-text-tertiary);
    }
    .picker-model-option {
        display: flex;
        align-items: center;
        justify-content: space-between;
        width: 100%;
        padding: 6px 8px;
        border-radius: var(--radius-sm);
        border: none;
        background: none;
        color: var(--color-text-primary);
        font-size: 0.75rem;
        font-family: inherit;
        cursor: pointer;
        text-align: left;
        transition: background 0.1s ease;
    }
    .picker-model-option:hover {
        background: var(--color-surface-3);
    }
    .picker-model-option.active {
        background: color-mix(in srgb, var(--color-accent) 10%, transparent);
        color: var(--color-accent);
    }
    .option-name {
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }
    .edit-btn,
    .move-btn {
        position: absolute;
        top: 8px;
        width: 32px;
        height: 32px;
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
        right: 48px;
    }
    .move-btn {
        right: 8px;
    }
    .card:hover .edit-btn,
    .card:hover .move-btn,
    .card.selected .edit-btn {
        opacity: 1;
    }
    .edit-btn:hover {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }
    .move-btn:hover {
        background: var(--color-surface-3);
        /* keep column color from inline style */
    }
    @media (hover: none) {
        .edit-btn,
        .move-btn {
            opacity: 1;
            transition: background 0.15s ease;
        }
    }
</style>