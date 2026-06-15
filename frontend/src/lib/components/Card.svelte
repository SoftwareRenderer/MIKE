<script lang="ts">
    import type { Task, Provider } from '$lib/types';
    import { selectedTask, showCardDetail, board as boardStore, streamingTaskIds, config as configStore, taskDiffStats as taskDiffStatsStore, showConfirm, showToast } from '$lib/store';
    import ModelSelector from './Chat/ModelSelector.svelte';
    import { moveTask, updateTask, stopTask, resumeTask, deleteTask } from '$lib/api';

    let { task, columnId, projectId, depth = 0, onCreateChildTask }: { task: Task; columnId: string; projectId?: string; depth?: number; onCreateChildTask?: (parentId: string) => void } = $props();

    // Number of action icons in the title bar (for dynamic padding)
    let actionCount = $derived(
        (columnId === 'backlog' && !task.parent_task_id ? 1 : 0) // + subtask
        + 1 // edit
        + 1 // move/pause/resume/idle
    );

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
        const streaming = $streamingTaskIds.has(task.id);
        const state = $boardStore?.tasks.find(t => t.id === task.id)?.status_state ?? task.status_state;
        if (streaming || state === 'running') return { text: 'Running', color: 'var(--color-status-blue)' };
        if (state === 'failed') return { text: 'Failed', color: 'var(--color-status-red)' };
        if (state === 'stopped') return { text: 'Stopped', color: 'var(--color-status-yellow)' };
        return null;
    });

    // Model badge: always "auto" behavior
    // Model only (1 provider) or provider+model (2+)
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
        const cfg = $configStore;
        const providers = cfg?.providers || [];
        if (providers.length === 0) return { text: null, color: '' };
        // Use task model if set, otherwise fall back to default model
        const getModelName = (m: string | { name: string }) => typeof m === 'string' ? m : m.name;
        const activeModel = task.model_name || (providers[0]?.default_model && providers[0]?.models.some(m => getModelName(m) === providers[0].default_model) ? providers[0].default_model : providers[0]?.models.length > 0 ? getModelName(providers[0].models[0]) : null);
        if (!activeModel) return { text: null, color: '' };
        // Find matching provider by URL
        const provider = providers.find(p => p.url === task.llama_server_url);
        // Auto: show provider name only when 2+ providers
        const showProvider = providers.length > 1;
        const text = showProvider && provider ? `${provider.display_name} · ${activeModel}` : activeModel;
        return { text, color: getModelColor(activeModel) };
    });

    // Model picker dropdown - only in Backlog column (task not yet started)
    let modelPickerOpen = $state(false);
    let modelBadgeRef: HTMLButtonElement | null = $state(null);
    let pickerRect = $state<{ top: number; left: number } | null>(null);
    let providers = $derived($configStore?.providers || []);
    let isConfigurable = $derived(columnId === 'backlog');

    // Diff stats: pushed via WebSocket from the backend
    let diffStats = $derived($taskDiffStatsStore[task.id] || null);

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

    async function selectModel(providerUrl: string, modelName: string, reasoningEffort?: string) {
        if (!projectId) return;
        try {
            const updated = await updateTask(projectId, task.id, { model_name: modelName, llama_server_url: providerUrl, reasoning_effort: reasoningEffort || null });
            task.model_name = updated.model_name;
            task.llama_server_url = updated.llama_server_url;
            task.reasoning_effort = updated.reasoning_effort;
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
            const dropdownWidth = portal.getBoundingClientRect().width;
            const viewportHeight = window.visualViewport?.height || window.innerHeight;
            const viewportWidth = window.visualViewport?.width || window.innerWidth;
            let top = pickerRect.top;
            let left = pickerRect.left;
            const spaceBelow = viewportHeight - (triggerTop + modelBadgeRef!.getBoundingClientRect().height);
            if (spaceBelow < dropdownHeight + 12) {
                top = triggerTop - dropdownHeight - 6;
            }
            if (left + dropdownWidth > viewportWidth - 8) {
                left = viewportWidth - dropdownWidth - 8;
            }
            if (left < 8) left = 8;
            pickerRect = { top, left };
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

    // Inline editing
    let editingTitle = $state(false);
    let titleDraft = $state('');
    let titleInputRef: HTMLInputElement | null = $state(null);
    let editPlanMode = $state(false);
    let editAutoCommit = $state(false);
    let editAutoPr = $state(false);
    let editModel = $state('');
    let editUrl = $state('');
    let editReasoningEffort = $state('');
    let editProviders: Provider[] = $state([]);

    function startEdit(e: MouseEvent) {
        e.stopPropagation();
        editingTitle = true;
        titleDraft = task.title;
        editPlanMode = task.plan_mode ?? false;
        editAutoCommit = task.auto_commit ?? false;
        editAutoPr = task.auto_pr ?? false;
        editModel = task.model_name ?? '';
        editUrl = task.llama_server_url ?? '';
        editReasoningEffort = task.reasoning_effort ?? '';
        const cfg = $configStore;
        editProviders = cfg?.providers || [];
    }

    function saveEdit() {
        const trimmed = titleDraft.trim();
        if (!trimmed || !projectId) {
            cancelEdit();
            return;
        }
        const updates: Record<string, unknown> = { title: trimmed };
        updates.plan_mode = editPlanMode;
        updates.auto_commit = editAutoCommit;
        updates.auto_pr = editAutoPr;
        if (editModel) updates.model_name = editModel;
        if (editUrl) updates.llama_server_url = editUrl;
        if (editReasoningEffort) updates.reasoning_effort = editReasoningEffort;
        task.title = trimmed;
        task.plan_mode = editPlanMode;
        task.auto_commit = editAutoCommit;
        task.auto_pr = editAutoPr;
        if (editModel) task.model_name = editModel;
        if (editUrl) task.llama_server_url = editUrl;
        if (editReasoningEffort) task.reasoning_effort = editReasoningEffort;
        boardStore.update(b => {
            if (!b) return b;
            return { ...b, tasks: b.tasks.map(t => t.id === task.id ? task : t) };
        });
        updateTask(projectId, task.id, updates).catch(err => {
            console.error('Failed to update task:', err);
        });
        cancelEdit();
    }

    function cancelEdit() {
        editingTitle = false;
        titleDraft = '';
        editPlanMode = false;
        editAutoCommit = false;
        editAutoPr = false;
        editModel = '';
        editUrl = '';
        editReasoningEffort = '';
        editProviders = [];
    }

    function handleEditModelSelect(providerName: string, modelName: string, providerUrl: string, reasoningEffort?: string) {
        editModel = modelName;
        editUrl = providerUrl;
        editReasoningEffort = reasoningEffort || '';
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

    // Pause/Resume for In Progress column
    // Derive liveStatusState from boardStore so it updates reactively
    // when WebSocket broadcasts a board refresh. Falls back to task prop.
    const STOPPED_STATES = ['stopped', 'paused', 'failed'];
    let liveStatusState = $derived(
        $boardStore?.tasks.find(t => t.id === task.id)?.status_state ?? task.status_state
    );

    let isPausable = $derived(columnId === 'in_progress' && liveStatusState !== 'idle' && !STOPPED_STATES.includes(liveStatusState));
    let isResumable = $derived(columnId === 'in_progress' && STOPPED_STATES.includes(liveStatusState));
    let isIdle = $derived(columnId === 'in_progress' && liveStatusState === 'idle');

    async function handlePause(e: MouseEvent) {
        e.stopPropagation();
        if (!projectId) return;
        try {
            const updated = await stopTask(projectId, task.id);
            boardStore.update(b => {
                if (!b) return b;
                return { ...b, tasks: b.tasks.map(t => t.id === task.id ? updated : t) };
            });
        } catch (err) {
            console.error('Failed to pause task:', err);
        }
    }

    async function handleResume(e: MouseEvent) {
        e.stopPropagation();
        if (!projectId) return;
        try {
            const updated = await resumeTask(projectId, task.id);
            boardStore.update(b => {
                if (!b) return b;
                return { ...b, tasks: b.tasks.map(t => t.id === task.id ? updated : t) };
            });
        } catch (err) {
            console.error('Failed to resume task:', err);
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
            boardStore.update(b => {
                if (!b) return b;
                return { ...b, tasks: b.tasks.map(t => t.id === task.id ? updated : t) };
            });
        } catch (err) {
            console.error('Failed to move task:', err);
        }
    }

    async function handleDelete(e: MouseEvent) {
        e.stopPropagation();
        if (!projectId) return;
        const confirmed = await showConfirm({
            title: 'Delete Task',
            message: 'Delete this task? This cannot be undone.',
            confirmText: 'Delete',
            cancelText: 'Cancel',
        });
        if (!confirmed) return;
        try {
            await deleteTask(projectId, task.id);
            // Board stream WebSocket will push the update
        } catch (err) {
            showToast('Failed to delete task: ' + String(err), 'error');
        }
    }

    function handleCreateChild(e: MouseEvent) {
        e.stopPropagation();
        if (onCreateChildTask) {
            onCreateChildTask(task.id);
        }
    }
</script>

<div
    class="card"
    class:selected={isSelected}
    class:indented={depth > 0}
    data-task-id={task.id}
    draggable={!isMobile}
    ondragstart={handleDragStart}
    onclick={() => { selectedTask.set(task); showCardDetail.set(true); }}
    onkeydown={handleCardKeydown}
    role="button"
    tabindex="0"
    aria-label={`Open task: ${task.title || '(untitled)'}`}
    aria-current={isSelected ? 'page' : undefined}
    style={`--depth: ${depth}; --actions: ${actionCount}`}
>
    {#if editingTitle}
        <div class="card-edit-panel" onclick={(e) => e.stopPropagation()}>
            <div class="card-edit-header">
                <span class="card-edit-title">Edit Task</span>
                <button type="button" class="card-edit-close" onclick={(e) => { e.stopPropagation(); cancelEdit(); }} aria-label="Close">✕</button>
            </div>
            <div class="card-edit-body">
                <label class="card-edit-field">
                    <span class="card-edit-label">Title</span>
                    <input
                        class="card-edit-input"
                        type="text"
                        bind:value={titleDraft}
                        onkeydown={(e) => { if (e.key === 'Enter') saveEdit(); if (e.key === 'Escape') cancelEdit(); e.stopPropagation(); }}
                        oninput={(e) => e.stopPropagation()}
                        bind:this={titleInputRef}
                        placeholder="Task title"
                    />
                </label>
                {#if editProviders.length > 0}
                    <div class="card-edit-model-row">
                        <span class="card-edit-label">Model</span>
                        <ModelSelector
                            providers={editProviders}
                            currentModel={editModel}
                            currentUrl={editUrl}
                            currentReasoningEffort={editReasoningEffort}
                            onSelect={handleEditModelSelect}
                        />
                    </div>
                {/if}
                <div class="card-edit-options">
                    <label class="card-edit-checkbox">
                        <input type="checkbox" bind:checked={editPlanMode} />
                        <span class="card-edit-option-text">
                            <span class="card-edit-option-title">Plan mode</span>
                            <span class="card-edit-option-desc">AI creates a TODO.md plan first, then executes tasks after approval</span>
                        </span>
                    </label>
                    <label class="card-edit-checkbox">
                        <input type="checkbox" bind:checked={editAutoCommit} />
                        <span>Auto-commit on completion</span>
                    </label>
                    <label class="card-edit-checkbox" class:disabled={!editAutoCommit}>
                        <input type="checkbox" bind:checked={editAutoPr} disabled={!editAutoCommit} />
                        <span>Auto-create PR branch</span>
                    </label>
                </div>
            </div>
            <div class="card-edit-footer">
                <button type="button" class="card-edit-btn-cancel" onclick={(e) => { e.stopPropagation(); cancelEdit(); }}>Cancel</button>
                <button type="button" class="card-edit-btn-save" onclick={(e) => { e.stopPropagation(); saveEdit(); }}>Save</button>
            </div>
        </div>
    {:else}
        <div class="card-title">
            {#if task.title_generating}
                <span class="title-spinner" aria-label="Generating title">
                    <svg viewBox="0 0 16 16" width="12" height="12"><circle cx="8" cy="8" r="6" fill="none" stroke="currentColor" stroke-width="2" stroke-dasharray="30 14" stroke-linecap="round"/></svg>
                </span>
            {/if}
            <span class="card-title-text">{truncate(task.title, 80)}</span>
            <div class="card-actions">
                {#if columnId === 'backlog' && !task.parent_task_id}
                    <button
                        type="button"
                        class="action-icon"
                        onclick={handleCreateChild}
                        aria-label="Create subtask"
                        title="Create subtask"
                    >
                        <svg width="14" height="14" viewBox="0 0 16 16" fill="none"><path d="M8 3v10M3 8h10" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg>
                    </button>
                {/if}
                <button
                    type="button"
                    class="action-icon"
                    onclick={startEdit}
                    aria-label="Edit task title"
                    title="Edit title"
                >
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none"><path d="M11.5 2.5l2 2L5 13H3v-2L11.5 2.5z" stroke="currentColor" stroke-width="1.3" stroke-linejoin="round"/></svg>
                </button>
                {#if isPausable}
                    <button
                        type="button"
                        class="action-icon"
                        onclick={handlePause}
                        aria-label="Pause task"
                        title="Pause task"
                    >
                        <svg width="14" height="14" viewBox="0 0 10 10" fill="none" aria-hidden="true"><rect x="2.5" y="1.5" width="1.5" height="7" fill="currentColor"/><rect x="6" y="1.5" width="1.5" height="7" fill="currentColor"/></svg>
                    </button>
                {:else if isResumable}
                    <button
                        type="button"
                        class="action-icon"
                        onclick={handleResume}
                        aria-label="Resume task"
                        title="Resume task"
                    >
                        <svg width="14" height="14" viewBox="0 0 10 10" fill="none" aria-hidden="true"><polygon points="3.5,1.5 3.5,8.5 8.5,5" fill="currentColor"/></svg>
                    </button>
                {:else if isIdle}
                    <span class="action-idle" aria-label="Idle" title="Idle">Idle</span>
                {:else if columnId === 'done'}
                    <button
                        type="button"
                        class="action-icon action-delete"
                        onclick={handleDelete}
                        aria-label="Delete task"
                        title="Delete task"
                    >
                        <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                            <path d="M2 4h12M5 4V3a1 1 0 011-1h4a1 1 0 011 1v1M6 7v5M10 7v5" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                            <path d="M3 4l1 9a1 1 0 001 1h6a1 1 0 001-1l1-9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                        </svg>
                    </button>
                {:else}
                    <button
                        type="button"
                        class="action-icon action-move"
                        onclick={cycleStatus}
                        aria-label={`Move to ${statusLabels[nextStatus]}`}
                        title={`Move to ${statusLabels[nextStatus]}`}
                        style="color: {statusColors[nextStatus]}"
                    >
                        {#if nextStatus === 'backlog'}
                            <svg width="14" height="14" viewBox="0 0 10 10" fill="none" aria-hidden="true"><circle cx="5" cy="5" r="3.5" stroke="currentColor" stroke-width="1.5"/></svg>
                        {:else if nextStatus === 'in_progress'}
                            <svg width="14" height="14" viewBox="0 0 10 10" fill="none" aria-hidden="true"><polygon points="3.5,1.5 3.5,8.5 8.5,5" fill="currentColor"/></svg>
                        {:else if nextStatus === 'review'}
                            <svg width="14" height="14" viewBox="0 0 10 10" fill="none" aria-hidden="true"><circle cx="5" cy="5" r="3.5" stroke="currentColor" stroke-width="1"/><circle cx="5" cy="5" r="1.2" fill="currentColor"/></svg>
                        {:else if nextStatus === 'done'}
                            <svg width="14" height="14" viewBox="0 0 10 10" fill="none" aria-hidden="true"><circle cx="5" cy="5" r="3.5" fill="currentColor"/></svg>
                        {/if}
                    </button>
                {/if}
            </div>
        </div>
    {/if}
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
                        title="Change model"
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
            {#if diffStats}
                <span class="diff-stats">
                    {#if diffStats.files > 0}<span class="diff-files">{diffStats.files} file{diffStats.files !== 1 ? 's' : ''}</span>{/if}
                    {#if diffStats.files > 0 && (diffStats.additions > 0 || diffStats.deletions > 0)}<span class="diff-sep"> · </span>{/if}
                    {#if diffStats.additions > 0}<span class="diff-add">+{diffStats.additions}</span>{/if}
                    {#if diffStats.deletions > 0}<span class="diff-del">-{diffStats.deletions}</span>{/if}
                </span>
            {/if}
        </div>
    {/if}
</div>

<!-- Model picker dropdown (portal to escape card overflow) -->
{#if modelPickerOpen && pickerRect && providers.length > 0}
    <svelte:element this={"div"} class="portal-card-model-picker" role="listbox" aria-label="Select model" style="position:fixed;top:{pickerRect.top}px;left:{pickerRect.left}px;z-index:10000;min-width:200px;max-height:320px;overflow-y:auto;background:var(--color-surface-1,#1e1e2e);border:1px solid var(--color-border,#333);border-radius:var(--radius-md,8px);box-shadow:0 8px 24px rgba(0,0,0,0.2);padding:4px;">
        {#each providers as provider}
            {#if provider.models.length > 0}
                <div class="picker-provider-header">{provider.display_name || provider.name}</div>
                {@const pairs = (() => {
                    const seen = new Set<string>();
                    const result: Array<{name: string; effort: string}> = [];
                    for (const m of provider.models) {
                        const name = typeof m === 'string' ? m : m.name;
                        const effort = (typeof m === 'object' && m.reasoning_effort) ? m.reasoning_effort : 'medium';
                        const key = `${name}|${effort}`;
                        if (!seen.has(key) && name.trim()) {
                            seen.add(key);
                            result.push({ name, effort });
                        }
                    }
                    return result;
                })()}
                {#each pairs as m}
                    {@const isActive = m.name === task.model_name && m.effort === (task.reasoning_effort || 'medium')}
                    <button type="button" class="picker-model-option" class:active={isActive} onclick={() => selectModel(provider.url, m.name, m.effort)} role="option" aria-selected={isActive}>
                        <span class="option-name">{m.name}</span>
                        {#if m.effort && m.effort !== 'medium'}
                            <span class="effort-badge">{m.effort}</span>
                        {/if}
                        {#if isActive}
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
        --depth: 0;
    }
    .card.indented {
        padding-left: calc(12px + var(--depth) * 1.25rem);
        border-left: 2px solid var(--color-border);
    }
    .card.indented::before {
        content: '';
        position: absolute;
        left: calc(var(--depth) * 1.25rem - 0.5px);
        top: 0;
        bottom: 0;
        width: 1px;
        background: var(--color-border);
        pointer-events: none;
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
    .card:hover .card-title-text,
    .card.selected .card-title-text {
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
    .card:hover .card-actions,
    .card.selected .card-actions {
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
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }
    .action-icon.action-move:hover {
        background: var(--color-surface-3);
        /* keep column color from inline style */
    }
    .action-icon.action-delete:hover {
        background: var(--color-status-red)22;
        color: var(--color-status-red) !important;
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
    .card-edit-panel {
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        overflow: hidden;
    }
    .card-edit-header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 8px 10px;
        border-bottom: 1px solid var(--color-border);
    }
    .card-edit-title {
        font-size: 0.8125rem;
        font-weight: 600;
        color: var(--color-text-primary);
    }
    .card-edit-close {
        font-size: 1rem;
        color: var(--color-text-secondary);
        background: none;
        border: none;
        cursor: pointer;
        padding: 2px 4px;
        line-height: 1;
    }
    .card-edit-close:hover {
        color: var(--color-text-primary);
    }
    .card-edit-body {
        padding: 10px;
        display: flex;
        flex-direction: column;
        gap: 10px;
    }
    .card-edit-field {
        display: flex;
        flex-direction: column;
        gap: 4px;
    }
    .card-edit-label {
        font-size: 0.8125rem;
        font-weight: 500;
        color: var(--color-text-secondary);
    }
    .card-edit-input {
        width: 100%;
        font-size: 0.8125rem;
        color: var(--color-text-primary);
        background: var(--color-surface-2);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        padding: 6px 8px;
        font-family: inherit;
        outline: none;
    }
    .card-edit-input:focus {
        border-color: var(--color-accent);
    }
    .card-edit-model-row {
        display: flex;
        align-items: center;
        flex-wrap: wrap;
        gap: 8px;
    }
    .card-edit-options {
        display: flex;
        flex-direction: column;
        gap: 8px;
    }
    .card-edit-checkbox {
        display: flex;
        align-items: flex-start;
        gap: 8px;
        font-size: 0.8125rem;
        color: var(--color-text-secondary);
        cursor: pointer;
    }
    .card-edit-checkbox.disabled {
        opacity: 0.5;
        pointer-events: none;
    }
    .card-edit-checkbox input[type="checkbox"] {
        accent-color: var(--color-accent);
        margin-top: 1px;
        cursor: pointer;
        flex-shrink: 0;
    }
    .card-edit-option-text {
        display: flex;
        flex-direction: column;
        align-items: flex-start;
        gap: 2px;
    }
    .card-edit-option-title {
        font-weight: 600;
        font-size: 0.8125rem;
    }
    .card-edit-option-desc {
        font-size: 0.75rem;
        color: var(--color-text-tertiary);
        font-weight: 400;
    }
    .card-edit-footer {
        display: flex;
        justify-content: flex-end;
        gap: 8px;
        padding: 8px 10px;
        border-top: 1px solid var(--color-border);
        background: var(--color-surface-2);
    }
    .card-edit-btn-cancel {
        padding: 6px 14px;
        border-radius: var(--radius-sm);
        background: var(--color-surface-3);
        color: var(--color-text-primary);
        font-size: 0.8125rem;
        font-weight: 500;
        cursor: pointer;
        border: none;
        font-family: inherit;
    }
    .card-edit-btn-cancel:hover {
        background: var(--color-surface-4);
    }
    .card-edit-btn-save {
        padding: 6px 16px;
        border-radius: var(--radius-sm);
        background: var(--color-accent);
        color: #fff;
        font-size: 0.8125rem;
        font-weight: 500;
        cursor: pointer;
        border: none;
        font-family: inherit;
    }
    .card-edit-btn-save:hover {
        background: var(--color-accent-hover);
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
    .diff-stats {
        display: inline-flex;
        align-items: center;
        gap: 3px;
        font-size: 0.75rem;
        color: var(--color-text-tertiary);
        padding: 2px 0;
        font-variant-numeric: tabular-nums;
    }
    .diff-files {
        color: var(--color-text-secondary);
    }
    .diff-add {
        color: var(--color-status-green, #3FB950);
        margin-left: -1px;
    }
    .diff-del {
        color: var(--color-status-red, #F85149);
        margin-left: -1px;
    }
    .diff-sep {
        color: var(--color-text-tertiary);
        opacity: 0.5;
        margin: 0 2px;
    }
    .badge.open .badge-chevron {
        transform: rotate(180deg);
    }

    /* Model picker dropdown styles */
    .picker-provider-header {
        padding: 6px 8px 2px;
        font-size: 0.75rem;
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.4px;
        color: var(--color-text-tertiary);
    }
    .picker-model-option {
        display: flex;
        align-items: center;
        gap: 6px;
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
    .effort-badge {
        font-size: 0.65rem;
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.04em;
        color: var(--color-text-tertiary);
        background: var(--color-surface-3);
        padding: 1px 5px;
        border-radius: 3px;
        flex-shrink: 0;
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