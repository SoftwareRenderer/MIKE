<script lang="ts">
    import type { Task, Provider } from '$lib/types';
    import { PI_SENTINEL_URL, isAgentProvider } from '$lib/types';
    import { useIsMobile } from '$lib/composables/use-is-mobile.svelte';
    import { useCardModelPicker } from '$lib/composables/use-card-model-picker.svelte';
    import { useCardActions } from '$lib/composables/use-card-actions.svelte';
    import { get } from 'svelte/store';
    import { selectedTask, board as boardStore, config as configStore, selectedProject } from '$lib/stores/app';
    import { showCardDetail } from '$lib/stores/ui';
    import { getStreamingContext, getTaskDiffStatsContext } from '$lib/context';
    import { getQueuedTaskIds, getQueuedDeleteTaskIds, onQueueChanged } from '$lib/api';
    import ModelPickerPortal from './Card/ModelPickerPortal.svelte';
    import CardHeader from './Card/CardHeader.svelte';
    import CardBadges from './Card/CardBadges.svelte';
    import SubtaskProgress from './Card/SubtaskProgress.svelte';
    import { fade, slide } from 'svelte/transition';
    import { onMount, onDestroy } from 'svelte';

    let { task, columnId, projectId, childrenMap, expandedTasks, onToggleExpanded, onCreateChildTask, columnColor, inline = true, onSelect, parentSelected = false, showSubtaskBorder = false, onRefreshBoard }: {
        task: Task;
        columnId: string;
        projectId?: string;
        childrenMap?: Map<string, Task[]>;
        expandedTasks?: Set<string>;
        onToggleExpanded?: (taskId: string) => void;
        onCreateChildTask?: (parentId: string) => void;
        columnColor?: string;
        inline?: boolean;
        onSelect?: (task: Task) => void;
        parentSelected?: boolean;
        showSubtaskBorder?: boolean;
        onRefreshBoard?: () => void;
    } = $props();

    // ── Queued actions tracking ──
    let taskHasQueuedActions = $state(false);
    let taskIsQueuedDelete = $state(false);
    function checkQueuedActions() {
        taskHasQueuedActions = getQueuedTaskIds().has(task.id);
        taskIsQueuedDelete = getQueuedDeleteTaskIds().has(task.id);
    }
    let unsubscribeQueue: (() => void) | null = null;
    onMount(() => {
        queueMicrotask(checkQueuedActions);
        unsubscribeQueue = onQueueChanged(checkQueuedActions);
    });
    onDestroy(() => {
        unsubscribeQueue?.();
    });

    // ── Derived state ──
    let children = $derived(childrenMap?.get(task.id) ?? []);
    let hasChildren = $derived(children.length > 0);

    let subtaskProgress = $derived(() => {
        if (!hasChildren) return null;
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

    let isDoneSubtask = $derived(inline && task.parent_task_id && task.status === 'done');

    let statusColor = $derived(
        task.status === 'backlog' ? 'var(--color-column-backlog)' :
        task.status === 'in_progress' ? 'var(--color-column-in-progress)' :
        task.status === 'review' ? 'var(--color-column-review)' :
        task.status === 'done' ? 'var(--color-column-done)' :
        'var(--color-text-tertiary)'
    );

    let expanded = $derived(expandedTasks?.has(task.id) ?? false);

    let hasRunningDescendant = $derived(() => {
        const isActive = (s: string) => s === 'running' || s === 'pulling_image';
        if (!hasChildren) return isActive(task.execution_state);
        for (const child of children) {
            if (isActive(child.execution_state)) return true;
            const grandChildren = childrenMap?.get(child.id) ?? [];
            for (const gc of grandChildren) {
                if (isActive(gc.execution_state)) return true;
            }
        }
        return isActive(task.execution_state);
    });

    const statuses = ['backlog', 'in_progress', 'review', 'done'] as const;
    let actionCount = $derived(
        (!inline || !task.parent_task_id || hasChildren || showSubtaskBorder ? 1 : 0)
        + (!isDoneSubtask ? 1 : 0)
        + (inline && task.parent_task_id || task.status === 'in_progress' ? 0 : 1)
    );

    const isMobileCtx = useIsMobile();

    let statusBadge = $derived(() => {
        const streamingIdsContext = getStreamingContext();
        const streaming = get(streamingIdsContext).has(task.id);
        const state = $boardStore?.tasks.find(t => t.id === task.id)?.execution_state ?? task.execution_state;
        const hasConflict = ($boardStore?.tasks.find(t => t.id === task.id)?.has_merge_conflict ?? task.has_merge_conflict) ?? false;
        if (streaming || state === 'running') return { text: 'Running', color: 'var(--color-status-blue)' };
        if (state === 'pulling_image') return { text: 'Pulling image', color: 'var(--color-status-blue)' };
        if (state === 'idle' && task.status === 'in_progress') return { text: 'Pending', color: 'var(--color-status-orange)' };
        if (state === 'failed') return { text: 'Failed', color: 'var(--color-status-red)' };
        if (state === 'stopped') return { text: 'Stopped', color: 'var(--color-status-yellow)' };
        if (hasConflict) return { text: 'Resolving conflict', color: 'var(--color-status-orange)' };
        return null;
    });

    const MODEL_COLORS = [
        '#888888', '#4C9AFF', '#3FB950', '#D29922', '#F85149',
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

    let modelBadge = $derived(() => {
        const cfg = $configStore;
        const providers = cfg?.providers || [];
        if (providers.length === 0) return { text: null, provider: null, model: null, color: '', isAgent: false };
        if (task.llama_server_url === 'pi://agent') {
            return { text: 'Pi Coding Agent', provider: null, model: null, color: '#A371F7', isAgent: true };
        }
        const getModelName = (m: string | { name: string }) => typeof m === 'string' ? m : m.name;
        const activeModel = task.model_name || (providers[0]?.default_model && providers[0]?.models.some(m => getModelName(m) === providers[0].default_model) ? providers[0].default_model : providers[0]?.models.length > 0 ? getModelName(providers[0].models[0]) : null);
        if (!activeModel) return { text: null, provider: null, model: null, color: '', isAgent: false };
        const provider = providers.find(p => p.url === task.llama_server_url);
        const showProvider = providers.length > 1;
        const provName = showProvider && provider ? provider.display_name : null;
        const text = provName ? `${provName} · ${activeModel}` : activeModel;
        return { text, provider: provName, model: activeModel, color: getModelColor(activeModel), isAgent: false };
    });

    let providers = $derived($configStore?.providers || []);
    let isConfigurable = $derived(columnId === 'backlog');

    let taskDiffStatsStore = getTaskDiffStatsContext();
    let diffStats = $derived($taskDiffStatsStore[task.id] || null);
    let hasChanges = $derived(!!diffStats?.hash && (diffStats.files || 0) > 0);

    let liveExecutionState = $derived(
        $boardStore?.tasks.find(t => t.id === task.id)?.execution_state ?? task.execution_state
    );

    let isPausable = $derived(columnId === 'in_progress' && hasRunningDescendant);
    let isResumable = $derived(columnId === 'in_progress' && ['stopped', 'failed'].includes(liveExecutionState));
    let isIdle = $derived(columnId === 'in_progress' && liveExecutionState === 'idle' && !hasRunningDescendant);

    let nextStatus = $derived(() => {
        const currentIdx = statuses.indexOf(task.status as typeof statuses[number]);
        return statuses[(currentIdx + 1) % statuses.length];
    });

    const statusColors: Record<string, string> = {
        backlog: 'var(--color-column-backlog)',
        in_progress: 'var(--color-column-in-progress)',
        review: 'var(--color-column-review)',
        done: 'var(--color-column-done)',
    };

    // ── Selection tracking ──
    let isSelected = $state(false);
    let showSelected = $derived(() => {
        if (!isSelected) return false;
        const sel = $selectedTask;
        if (!sel || !sel.parent_task_id) return true;
        let ancestor: Task | null = sel;
        let found = false;
        while (ancestor) {
            if (ancestor.parent_task_id === task.id) { found = true; break; }
            const nextId = ancestor.parent_task_id;
            ancestor = nextId ? ($boardStore?.tasks.find(t => t.id === nextId) ?? null) : null;
        }
        return !found;
    });

    $effect(() => {
        const unsub = selectedTask.subscribe(t => {
            isSelected = t?.id === task.id;
        });
        return unsub;
    });

    // ── Composables ──
    let picker = useCardModelPicker({
        projectId: projectId!,
        taskId: task.id,
        taskModelName: task.model_name || '',
        taskProviderUrl: task.llama_server_url || '',
        taskReasoningEffort: task.reasoning_effort || '',
        providers,
        isConfigurable,
    });

    let cardActions = useCardActions({
        task,
        columnId,
        projectId: projectId!,
        hasChildren,
        hasChanges,
        hasRunningDescendant,
        liveExecutionState,
        childrenMap,
        expandedTasks,
        onRefreshBoard,
        onToggleExpanded,
        onCreateChildTask,
        onSelect,
    });

    // ── Draggable ──
    let draggable = $derived(!isMobileCtx.isMobile);
</script>

<div
    class="card-wrapper"
    class:selected={showSelected}
    class:has-children={hasChildren}
    class:is-expanded={expanded}
    class:is-subtask={inline && !!task.parent_task_id}
    class:is-sidebar-subtask={showSubtaskBorder && !!task.parent_task_id}
    class:queued={taskHasQueuedActions}
    class:queued-delete={taskIsQueuedDelete}
    draggable={draggable}
    style="--column-color: {columnColor || ''}; --status-color: {statusColor}{parentSelected && inline && task.parent_task_id ? `; border-left: 3px solid ${statusColor}` : ''}"
>
    <CardHeader
        {task}
        {columnId}
        {actionCount}
        {isDoneSubtask}
        {isPausable}
        {isResumable}
        {isIdle}
        {hasChildren}
        {inline}
        {showSubtaskBorder}
        {hasChanges}
        nextStatus={nextStatus}
        {statusLabels}
        {statusColors}
        truncate={cardActions.truncate}
        onMouseDown={cardActions.handleMouseDown}
        onDragStart={cardActions.handleDragStart}
        onClick={cardActions.handleCardClick}
        onKeydown={cardActions.handleCardKeydown}
        onCreateChild={cardActions.handleCreateChild}
        onEdit={cardActions.startEdit}
        onPause={cardActions.handlePause}
        onResume={cardActions.handleResume}
        onDelete={cardActions.handleDelete}
        onCycleStatus={cardActions.cycleStatus}
    />

    <CardBadges
        statusBadge={statusBadge}
        modelBadge={modelBadge}
        {diffStats}
    />

    <SubtaskProgress
        subtaskProgress={subtaskProgress}
        {expanded}
        {hasChildren}
        {children}
        {columnId}
        {projectId}
        {childrenMap}
        {expandedTasks}
        {onToggleExpanded}
        {onCreateChildTask}
        {columnColor}
        {parentSelected}
        {showSubtaskBorder}
        {onSelect}
        {onRefreshBoard}
    />
</div>

<!-- Model picker dropdown (portal to escape card overflow) -->
{#if picker.modelPickerOpen && picker.pickerRect && providers.length > 0}
    <ModelPickerPortal
        {providers}
        currentUrl={task.llama_server_url}
        currentModel={task.model_name}
        currentEffort={task.reasoning_effort || 'medium'}
        rect={picker.pickerRect}
        onSelectAgent={picker.selectAgent}
        onSelectModel={picker.selectModel}
    />
{/if}

<!-- Model badge trigger (invisible button for picker) -->
{#if isConfigurable}
    <button
        type="button"
        class="model-badge-trigger"
        bind:this={picker.modelBadgeRef}
        onclick={picker.togglePicker}
        aria-label="Change model"
        style="position: absolute; opacity: 0; pointer-events: none;"
    />
{/if}

<style>
    .card-wrapper {
        position: relative;
    }
    /* Diagonal fade overlay for tasks with queued offline actions */
    .card-wrapper.queued::after {
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
    .card-wrapper.queued > * {
        position: relative;
        z-index: 2;
    }
    /* Inverted diagonal fade for tasks being deleted offline */
    .card-wrapper.queued-delete::after {
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
    .card-wrapper.has-children:not(.is-subtask) :global(> .card) {
        border-left: 3px solid var(--column-color, var(--color-border));
    }
    .card-wrapper.is-subtask :global(> .card) {
        border-top: 1px solid var(--color-border);
        border-right: 1px solid var(--color-border);
        border-bottom: 1px solid var(--color-border);
        border-left: 3px solid var(--status-color, var(--color-border));
    }
    .card-wrapper.is-sidebar-subtask :global(> .card) {
        border-top: 1px solid var(--color-border);
        border-right: 1px solid var(--color-border);
        border-bottom: 1px solid var(--color-border);
        border-left: 3px solid var(--status-color, var(--color-border));
    }
    .card-wrapper.is-sidebar-subtask :global(> .card):hover {
        border-top-color: var(--color-border-bright);
        border-right-color: var(--color-border-bright);
        border-bottom-color: var(--color-border-bright);
        border-left-color: var(--status-color, var(--color-border));
    }
    .card-wrapper.selected :global(> .card) {
        border-color: var(--color-accent);
        background: var(--color-surface-3);
    }
    .card-wrapper.selected.is-subtask :global(> .card) {
        border-left-color: var(--status-color, var(--color-border));
    }
    .card-wrapper.selected.has-children:not(.is-subtask) :global(> .card) {
        border-left-color: var(--column-color, var(--color-border));
    }
    .model-badge-trigger {
        width: 0;
        height: 0;
    }
</style>
