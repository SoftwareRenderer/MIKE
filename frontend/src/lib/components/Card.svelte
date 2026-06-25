<script lang="ts">
    import type { Task, Provider } from '$lib/types';
    import { selectedTask, showCardDetail, board as boardStore, streamingTaskIds, config as configStore, taskDiffStats as taskDiffStatsStore, showConfirm, showToast } from '$lib/store';
    import ModelSelector from './Chat/ModelSelector.svelte';
    import { moveTask, updateTask, stopTask, resumeTask, deleteTask, startTask, commitAndDone, getQueuedTaskIds, getQueuedDeleteTaskIds, onQueueChanged } from '$lib/api';
    import { fade } from 'svelte/transition';
    import { onMount, onDestroy } from 'svelte';

    let { task, columnId, projectId, childrenMap, expandedTasks, onToggleExpanded, onCreateChildTask, columnColor, inline = true, onSelect, parentSelected = false, showSubtaskBorder = false, onRefreshBoard }: { task: Task; columnId: string; projectId?: string; childrenMap?: Map<string, Task[]>; expandedTasks?: Set<string>; onToggleExpanded?: (taskId: string) => void; onCreateChildTask?: (parentId: string) => void; columnColor?: string; inline?: boolean; onSelect?: (task: Task) => void; parentSelected?: boolean; showSubtaskBorder?: boolean; onRefreshBoard?: () => void } = $props();

    // Track if this task has queued offline actions (for diagonal fade effect)
    let taskHasQueuedActions = $state(false);
    let taskIsQueuedDelete = $state(false);
    function checkQueuedActions() {
        taskHasQueuedActions = getQueuedTaskIds().has(task.id);
        taskIsQueuedDelete = getQueuedDeleteTaskIds().has(task.id);
    }
    onMount(() => {
        checkQueuedActions();
        return onQueueChanged(() => checkQueuedActions());
    });

    // Children of this task (from the map passed by Board)
    let children = $derived(childrenMap?.get(task.id) ?? []);
    let hasChildren = $derived(children.length > 0);

    // Subtask progress: parent counts as 1 unit, pct = (doneChildren + parentDone) / (totalChildren + 1)
    let subtaskProgress = $derived.by(() => {
        if (!hasChildren) return null;
        // Recursively count all nested descendants
        const countDescendants = (taskId: string): { total: number; done: number } => {
            const directChildren = childrenMap?.get(taskId) ?? [];
            let total = directChildren.length;
            let done = directChildren.filter(c => c.status === 'done').length;
            for (const child of directChildren) {
                const nested = countDescendants(child.id);
                total += nested.total;
                done += nested.done;
            }
            return { total, done };
        };
        const descendants = countDescendants(task.id);
        return { done: descendants.done, total: descendants.total, pct: descendants.total > 0 ? descendants.done / descendants.total : 0 };
    });

    // Whether this is a completed subtask (hide edit/move buttons)
    let isDoneSubtask = $derived(inline && task.parent_task_id && task.status === 'done');

    // Status color for subtask indicator
    let statusColor = $derived.by(() => {
        // Use column color for subtask left border
        if (task.status === 'backlog') return 'var(--color-column-backlog)';
        if (task.status === 'in_progress') return 'var(--color-column-in-progress)';
        if (task.status === 'review') return 'var(--color-column-review)';
        if (task.status === 'done') return 'var(--color-column-done)';
        return 'var(--color-text-tertiary)';
    });

    // Expand/collapse state (managed by Board for stability)
    let expanded = $derived(expandedTasks?.has(task.id) ?? false);

    // Check if any descendant is running (for pause button on parent)
    let hasRunningDescendant = $derived.by(() => {
        const isActive = (s: string) => s === 'running' || s === 'pulling_image';
        if (!hasChildren) return isActive(task.status_state);
        for (const child of children) {
            if (isActive(child.status_state)) return true;
            // Recursively check (childrenMap has all tasks, not just same-column)
            const grandChildren = childrenMap?.get(child.id) ?? [];
            for (const gc of grandChildren) {
                if (isActive(gc.status_state)) return true;
            }
        }
        return isActive(task.status_state);
    });

    // Number of action icons in the title bar (for dynamic padding)
    let actionCount = $derived(
        (!inline || !task.parent_task_id || hasChildren || showSubtaskBorder ? 1 : 0) // + subtask
        + (!isDoneSubtask ? 1 : 0) // edit
        + (inline && task.parent_task_id ? 0 : 1) // move/pause/resume/idle (move hidden for inline subtasks)
    );

    const statuses = ['backlog', 'in_progress', 'review', 'done'] as const;
    const statusLabels: Record<string, string> = {
        backlog: 'Backlog',
        in_progress: 'In Progress',
        review: 'Review',
        done: 'Done',
    };

    let mouseDownPos = $state<{ x: number; y: number } | null>(null);

    function handleMouseDown(e: MouseEvent) {
        mouseDownPos = { x: e.clientX, y: e.clientY };
    }

    function handleDragStart(e: DragEvent) {
        e.stopPropagation();
        e.dataTransfer?.setData('text/plain', task.id);
        e.dataTransfer.effectAllowed = 'move';
    }

    function handleCardClick(e: MouseEvent) {
        // If the mouse moved significantly between mousedown and click,
        // it was a drag gesture — suppress the click to avoid opening CardDetail
        if (mouseDownPos) {
            const dx = e.clientX - mouseDownPos.x;
            const dy = e.clientY - mouseDownPos.y;
            if (Math.sqrt(dx * dx + dy * dy) > 5) {
                mouseDownPos = null;
                if (inline && task.parent_task_id) e.stopPropagation();
                return;
            }
        }
        mouseDownPos = null;
        // Skip if the click originated from a child button (progress bar, action icons, etc.)
        const target = e.target as HTMLElement;
        if (target.closest('button, input, a, select, .card-edit-panel')) return;
        if (inline && task.parent_task_id) e.stopPropagation();
        if (onSelect) { onSelect(task); } else { selectedTask.set(task); showCardDetail.set(true); }
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
        if (state === 'pulling_image') return { text: 'Pulling image', color: 'var(--color-status-blue)' };
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
        if (providers.length === 0) return { text: null, provider: null, model: null, color: '' };
        // Check if task uses Pi agent
        if (task.llama_server_url === 'pi://agent') {
                                    return { text: 'Pi Coding Agent', provider: null, model: null, color: '#A371F7', isAgent: true };
        }
        // Use task model if set, otherwise fall back to default model
        const getModelName = (m: string | { name: string }) => typeof m === 'string' ? m : m.name;
        const activeModel = task.model_name || (providers[0]?.default_model && providers[0]?.models.some(m => getModelName(m) === providers[0].default_model) ? providers[0].default_model : providers[0]?.models.length > 0 ? getModelName(providers[0].models[0]) : null);
        if (!activeModel) return { text: null, provider: null, model: null, color: '' };
        // Find matching provider by URL
        const provider = providers.find(p => p.url === task.llama_server_url);
        // Auto: show provider name only when 2+ providers
        const showProvider = providers.length > 1;
        const provName = showProvider && provider ? provider.display_name : null;
        const text = provName ? `${provName} · ${activeModel}` : activeModel;
        return { text, provider: provName, model: activeModel, color: getModelColor(activeModel), isAgent: false };
    });

    // Model picker dropdown - only in Backlog column (task not yet started)
    let modelPickerOpen = $state(false);
    let modelBadgeRef: HTMLButtonElement | null = $state(null);
    let pickerRect = $state<{ top: number; left: number } | null>(null);
    let providers = $derived($configStore?.providers || []);
    let isConfigurable = $derived(columnId === 'backlog');

    // Diff stats: pushed via WebSocket from the backend
    let diffStats = $derived($taskDiffStatsStore[task.id] || null);
    let hasChanges = $derived(!!diffStats?.hash);

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
    }

    async function selectAgent() {
        if (!projectId) return;
        try {
            const updated = await updateTask(projectId, task.id, { model_name: null, llama_server_url: 'pi://agent', reasoning_effort: null });
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
            console.error('Failed to select agent:', err);
        }
        modelPickerOpen = false;
        pickerRect = null;
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
    // Don't show selected highlight on parent when any descendant is selected
    let showSelected = $derived.by(() => {
        if (!isSelected) return false;
        const sel = $selectedTask;
        if (!sel || !sel.parent_task_id) return true;
        // Walk up the ancestor chain to see if this task is an ancestor of the selected task
        let current = sel;
        while (current) {
            if (current.parent_task_id === task.id) return false;
            // Find the parent task in the board store
            const parent = $boardStore?.tasks.find(t => t.id === current.parent_task_id);
            current = parent || null;
        }
        return true;
    });

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
        if (!s) return '';
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

    // Pause/Resume: for parent tasks, show based on descendant state
    let isPausable = $derived(columnId === 'in_progress' && hasRunningDescendant);
    let isResumable = $derived(columnId === 'in_progress' && STOPPED_STATES.includes(liveStatusState));
    let isIdle = $derived(columnId === 'in_progress' && liveStatusState === 'idle' && !hasRunningDescendant);

    async function handlePause(e: MouseEvent) {
        e.stopPropagation();
        if (!projectId) return;
        try {
            const updated = await stopTask(projectId, task.id);
            // Apply optimistic update to task object
            Object.assign(task, (updated as any).queued ? { ...task, ...updated } : updated);
            boardStore.update(b => b ? { ...b, tasks: b.tasks.map(t => t.id === task.id ? task : t) } : b);
        } catch (err) {
            console.error('Failed to pause task:', err);
        }
    }

    async function handleResume(e: MouseEvent) {
        e.stopPropagation();
        if (!projectId) return;
        try {
            const updated = await resumeTask(projectId, task.id);
            Object.assign(task, (updated as any).queued ? { ...task, ...updated } : updated);
            boardStore.update(b => b ? { ...b, tasks: b.tasks.map(t => t.id === task.id ? task : t) } : b);
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
            let updated: Task;
            if (nextStatus === 'done' && hasChanges) {
                updated = await commitAndDone(projectId, task.id);
            } else {
                updated = await moveTask(projectId, task.id, nextStatus);
            }
            Object.assign(task, (updated as any).queued ? { ...task, ...updated } : updated);
            boardStore.update(b => b ? { ...b, tasks: b.tasks.map(t => t.id === task.id ? task : t) } : b);
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
            // Fallback refresh in case WebSocket is disconnected
            onRefreshBoard?.();
        } catch (err) {
            // If task has queued actions (optimistic ID), server may not know about it.
            // Remove from UI anyway and let the queue handle cleanup.
            if (getQueuedTaskIds().has(task.id)) {
                boardStore.update(b => {
                    if (!b) return b;
                    return { ...b, tasks: b.tasks.filter(t => t.id !== task.id) };
                });
                onRefreshBoard?.();
            } else {
                showToast('Failed to delete task: ' + String(err), 'error');
            }
        }
    }

    function handleCreateChild(e: MouseEvent) {
        e.stopPropagation();
        if (onCreateChildTask) {
            onCreateChildTask(task.id);
        }
    }

    async function handleStart(e: MouseEvent) {
        e.stopPropagation();
        if (!projectId) return;
        try {
            // If this is a subtask, use the start-subtask endpoint
            if (task.parent_task_id) {
                const { startSubtask } = await import('$lib/api');
                const updated = await startSubtask(projectId, task.id);
                boardStore.update(b => {
                    if (!b) return b;
                    return { ...b, tasks: b.tasks.map(t => t.id === updated.id ? updated : t) };
                });
            } else {
                // Parent task or root task — use normal start (backend handles LLM subtask selection)
                const updated = await startTask(projectId, task.id);
                boardStore.update(b => {
                    if (!b) return b;
                    return { ...b, tasks: b.tasks.map(t => t.id === updated.id ? updated : t) };
                });
            }
        } catch (err) {
            console.error('Failed to start task:', err);
            showToast('Failed to start task: ' + String(err), 'error');
        }
    }

    function toggleExpanded(e: MouseEvent) {
        e.stopPropagation();
        if (onToggleExpanded) {
            onToggleExpanded(task.id);
        }
    }
</script>

<div
    class="card"
    class:selected={showSelected}
    class:has-children={hasChildren}
    class:is-expanded={expanded}
    class:is-subtask={inline && !!task.parent_task_id}
    class:is-sidebar-subtask={showSubtaskBorder && !!task.parent_task_id}
    class:queued={taskHasQueuedActions}
    class:queued-delete={taskIsQueuedDelete}
    data-task-id={task.id}
    draggable={!isMobile}
    onmousedown={handleMouseDown}
    ondragstart={handleDragStart}
    onclick={handleCardClick}
    onkeydown={handleCardKeydown}
    role="button"
    tabindex="0"
    aria-label={`Open task: ${task.title || '(untitled)'}`}
    aria-current={isSelected ? 'page' : undefined}
    style={`--actions: ${actionCount}; --column-color: ${columnColor || ""}; --status-color: ${statusColor}${parentSelected && inline && task.parent_task_id ? `; border-left: 3px solid ${statusColor}` : ""}`}
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
                            <span class="card-edit-option-desc">AI decomposes work into subtasks on the board, then executes after approval</span>
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
                {#if !inline || !task.parent_task_id || hasChildren || showSubtaskBorder}
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
                {#if !isDoneSubtask}
                <button
                    type="button"
                    class="action-icon"
                    onclick={startEdit}
                    aria-label="Edit task"
                    title="Edit Task"
                >
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none"><path d="M11.5 2.5l2 2L5 13H3v-2L11.5 2.5z" stroke="currentColor" stroke-width="1.3" stroke-linejoin="round"/></svg>
                </button>
                {/if}
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
                {:else if isDoneSubtask}
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
                {:else if inline && task.parent_task_id}
                    <!-- no move button for inline subtasks -->
                {:else}
                    <button
                        type="button"
                        class="action-icon action-move"
                        onclick={cycleStatus}
                        aria-label={nextStatus === 'done' ? (hasChanges ? 'Commit and move to done' : `Move to ${statusLabels[nextStatus]}`) : `Move to ${statusLabels[nextStatus]}`}
                        title={nextStatus === 'done' ? (hasChanges ? 'Commit and move to done' : `Move to ${statusLabels[nextStatus]}`) : `Move to ${statusLabels[nextStatus]}`}
                        style="color: {nextStatus === 'done' && hasChanges ? '#3FB950' : statusColors[nextStatus]}"
                    >
                        {#if nextStatus === 'backlog'}
                            <svg width="14" height="14" viewBox="0 0 10 10" fill="none" aria-hidden="true"><circle cx="5" cy="5" r="3.5" stroke="currentColor" stroke-width="1.5"/></svg>
                        {:else if nextStatus === 'in_progress'}
                            <svg width="14" height="14" viewBox="0 0 10 10" fill="none" aria-hidden="true"><polygon points="3.5,1.5 3.5,8.5 8.5,5" fill="currentColor"/></svg>
                        {:else if nextStatus === 'review'}
                            <svg width="14" height="14" viewBox="0 0 10 10" fill="none" aria-hidden="true"><circle cx="5" cy="5" r="3.5" stroke="currentColor" stroke-width="1"/><circle cx="5" cy="5" r="1.2" fill="currentColor"/></svg>
                        {:else if nextStatus === 'done' && hasChanges}
                            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true"><line x1="4" y1="2" x2="4" y2="14" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/><circle cx="4" cy="2" r="2.5" fill="currentColor"/><circle cx="4" cy="14" r="2.5" fill="currentColor"/><line x1="12" y1="4" x2="12" y2="14" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/><circle cx="12" cy="14" r="2.5" fill="currentColor"/><path d="M12 4 C8 4, 7 2, 5.5 2" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" fill="none"/></svg>
                        {:else if nextStatus === 'done'}
                            <svg width="14" height="14" viewBox="0 0 10 10" fill="none" aria-hidden="true"><circle cx="5" cy="5" r="3.5" fill="currentColor"/></svg>
                        {/if}
                    </button>
                {/if}
            </div>
        </div>
    {/if}
    {#if statusBadge || modelBadge.text || subtaskProgress}
        <div class="badges-row">
            {#if statusBadge}
                <div class="badge" style="background: {statusBadge.color}22; color: {statusBadge.color}">
                    {statusBadge.text}
                </div>
            {/if}
            {#if modelBadge.text}
                {#if modelBadge.isAgent}
                    <div class="badge model-badge pi-badge" style="background: {modelBadge.color}22; color: {modelBadge.color}">
                        <svg width="12" height="12" viewBox="0 0 16 16" fill="none" style="margin-right: 3px">
                            <rect x="3" y="5" width="10" height="8" rx="1.5" stroke="currentColor" stroke-width="1.5"/>
                            <circle cx="6" cy="9" r="1" fill="currentColor"/>
                            <circle cx="10" cy="9" r="1" fill="currentColor"/>
                        </svg>
                        Pi Coding Agent
                    </div>
                {:else if isConfigurable}
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
                        {#if modelBadge.provider}<span class="badge-provider">{modelBadge.provider}</span><span class="badge-sep"> · </span>{/if}<span class="badge-model">{modelBadge.model}</span>
                        <svg class="badge-chevron" width="10" height="10" viewBox="0 0 16 16" fill="none"><path d="M4 6l4 4 4-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
                    </button>
                {:else}
                    <div class="badge model-badge" style="background: {modelBadge.color}22; color: {modelBadge.color}">
                        {#if modelBadge.provider}<span class="badge-provider">{modelBadge.provider}</span><span class="badge-sep"> · </span>{/if}<span class="badge-model">{modelBadge.model}</span>
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
    {#if subtaskProgress}
        <button
            type="button"
            class="subtask-progress-bar"
            class:is-expanded={expanded}
            style="--pct: {subtaskProgress.pct * 100}"
            onclick={toggleExpanded}
            aria-expanded={expanded}
        >
            <span class="subtask-progress-fill" style="width: {subtaskProgress.pct * 100}%"></span>
            <span class="subtask-progress-count">{subtaskProgress.total} subtask{subtaskProgress.total !== 1 ? 's' : ''}</span>
            <svg class="subtask-chevron" width="10" height="10" viewBox="0 0 16 16" fill="none"><path d="M4 6l4 4 4-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
        </button>
    {/if}
    {#if expanded && hasChildren}
        <div class="subtask-list">
            {#each children as child}
                <div class="subtask-wrapper" transition:fade={{ duration: 200 }}>
                    <svelte:self task={child} {columnId} {projectId} {childrenMap} {expandedTasks} {onToggleExpanded} {onCreateChildTask} columnColor={columnColor} inline={true} parentSelected={showSelected} {showSubtaskBorder} {onSelect} {onRefreshBoard} />
                </div>
            {/each}
        </div>
    {/if}
</div>

<!-- Model picker dropdown (portal to escape card overflow) -->
{#if modelPickerOpen && pickerRect && providers.length > 0}
        {@const isAgentActive = task.llama_server_url === 'pi://agent'}
    <svelte:element this={"div"} class="portal-card-model-picker" role="listbox" aria-label="Select model" style="position:fixed;top:{pickerRect.top}px;left:{pickerRect.left}px;z-index:10000;min-width:200px;max-height:320px;overflow-y:auto;background:var(--color-surface-1,#1e1e2e);border:1px solid var(--color-border,#333);border-radius:var(--radius-md,8px);box-shadow:0 8px 24px rgba(0,0,0,0.2);padding:4px;">
        <button type="button" class="picker-model-option picker-agent-option" class:active={isAgentActive} onclick={selectAgent} role="option" aria-selected={isAgentActive}>
            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" style="margin-right:6px;flex-shrink:0">
                <rect x="3" y="5" width="10" height="8" rx="1.5" stroke="currentColor" stroke-width="1.5"/>
                <circle cx="6" cy="9" r="1" fill="currentColor"/>
                <circle cx="10" cy="9" r="1" fill="currentColor"/>
            </svg>
            <span class="option-name">Pi Coding Agent</span>
            {#if isAgentActive}
                <svg width="12" height="12" viewBox="0 0 16 16" fill="none"><path d="M3 8l4 4 6-8" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/></svg>
            {/if}
        </button>
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
        user-select: none;
        -webkit-user-select: none;
        -webkit-user-drag: element;
    }
    /* Diagonal fade overlay for tasks with queued offline actions */
    .card.queued::after {
        content: '';
        position: absolute;
        inset: 0;
        border-radius: inherit;
        background: linear-gradient(
            135deg,
            transparent 0%,
            transparent 35%,
            rgba(0, 0, 0, 0.25) 65%,
            rgba(0, 0, 0, 0.5) 100%
        );
        pointer-events: none;
        z-index: 1;
    }
    .card.queued > * {
        position: relative;
        z-index: 2;
    }
    /* Inverted diagonal fade for tasks being deleted offline */
    .card.queued-delete::after {
        content: '';
        position: absolute;
        inset: 0;
        border-radius: inherit;
        background: linear-gradient(
            135deg,
            rgba(0, 0, 0, 0.5) 0%,
            rgba(0, 0, 0, 0.25) 35%,
            transparent 65%,
            transparent 100%
        );
        pointer-events: none;
        z-index: 1;
    }
    .card.has-children:not(.is-subtask) {
        border-left: 3px solid var(--column-color, var(--color-border));
    }
    .card.is-subtask {
        border-top: 1px solid var(--color-border);
        border-right: 1px solid var(--color-border);
        border-bottom: 1px solid var(--color-border);
        border-left: 3px solid var(--status-color, var(--color-border));
    }
    .card.is-sidebar-subtask {
        border-top: 1px solid var(--color-border);
        border-right: 1px solid var(--color-border);
        border-bottom: 1px solid var(--color-border);
        border-left: 3px solid var(--status-color, var(--color-border));
    }
    .card.is-sidebar-subtask:hover {
        border-top-color: var(--color-border-bright);
        border-right-color: var(--color-border-bright);
        border-bottom-color: var(--color-border-bright);
        border-left-color: var(--status-color, var(--color-border));
    }
    .card.selected.is-sidebar-subtask {
        border-top-color: var(--color-accent);
        border-right-color: var(--color-accent);
        border-bottom-color: var(--color-accent);
        border-left-color: var(--status-color, var(--color-border));
    }
    .card:hover {
        border-color: var(--color-border-bright);
    }
    .card.has-children:not(.is-subtask):hover {
        border-left-color: var(--column-color, var(--color-border));
    }
    .card.is-subtask:hover {
        border-left-color: var(--status-color, var(--color-border));
    }
    .card.selected {
        border-color: var(--color-accent);
        background: var(--color-surface-3);
    }
    .card.selected.is-subtask {
        border-left-color: var(--status-color, var(--color-border));
    }
    .card.selected.has-children:not(.is-subtask) {
        border-left-color: var(--column-color, var(--color-border));
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
    .card:hover:not(:has(.card:hover)) > .card-title > .card-title-text,
    .card.selected > .card-title > .card-title-text {
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
    .card:hover:not(:has(.card:hover)) > .card-title > .card-actions,
    .card.selected > .card-title > .card-actions {
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
    .model-badge {
        max-width: 100%;
        overflow: hidden;
    }
    .badge-provider {
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
        max-width: 100%;
    }
    .badge-model {
        flex-shrink: 0;
    }
    .badge-sep {
        flex-shrink: 0;
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
    .picker-agent-option {
        color: #A371F7;
    }
    .picker-agent-option.active {
        background: rgba(163, 113, 247, 0.1);
        color: #A371F7;
    }
    .picker-agent-option:hover {
        background: rgba(163, 113, 247, 0.08);
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

    /* Subtask progress bar (full-width) */
    .subtask-progress-bar {
        display: flex;
        align-items: center;
        justify-content: space-between;
        width: 100%;
        height: 22px;
        margin-top: 8px;
        padding: 0 6px;
        border: none;
        border-radius: var(--radius-sm);
        background: var(--color-surface-3);
        cursor: pointer;
        position: relative;
        overflow: hidden;
        transition: background 0.15s ease;
    }
    .subtask-progress-bar:hover {
        background: var(--color-surface-4);
    }
    .subtask-progress-bar.is-expanded {
        background: var(--color-surface-4);
    }
    .subtask-progress-fill {
        position: absolute;
        top: 0;
        left: 0;
        height: 100%;
        opacity: 0.35;
        transition: width 0.3s ease, opacity 0.15s ease;
        pointer-events: none;
        background: #009933;
    }
    .subtask-progress-bar.is-expanded .subtask-progress-fill {
        opacity: 0.5;
    }
    .subtask-progress-count {
        position: absolute;
        left: 50%;
        top: 50%;
        transform: translate(-50%, -50%);
        z-index: 1;
        font-size: 0.75rem;
        font-weight: 600;
        font-family: inherit;
        font-variant-numeric: tabular-nums;
        color: #FAFAFA;
        white-space: nowrap;
        pointer-events: none;
    }
    .subtask-progress-bar.is-expanded .subtask-progress-count {
        color: #FAFAFA;
    }
    .subtask-progress-bar.is-expanded .subtask-progress-count {
        color: #FAFAFA;
    }
    .subtask-chevron {
        position: absolute;
        right: 6px;
        top: 50%;
        transform: translateY(-50%);
        z-index: 1;
        opacity: 0.5;
        transition: transform 0.15s ease;
        pointer-events: none;
    }
    .subtask-progress-bar.is-expanded .subtask-chevron {
        transform: translateY(-50%) rotate(180deg);
    }

    /* Subtask list (expandable) */
    .subtask-list {
        margin-top: 6px;
        display: flex;
        flex-direction: column;
        gap: 6px;
    }
    .subtask-list :global(.card) {
        background: var(--color-surface-1);
    }


    /* Expanded state indicated by progress bar, not border */
</style>