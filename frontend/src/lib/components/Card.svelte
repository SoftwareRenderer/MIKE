<script lang="ts">
    import type { Task } from '$lib/types';
    import { AGENT_URL_PREFIX, isAgentProvider } from '$lib/types';
    import { useIsMobile } from '$lib/composables/use-is-mobile.svelte';
    import { MultiSelect } from '$lib/composables/use-multi-select.svelte';
    import { appStore, uiStore, dialogStore, updateTaskInBoard, removeTaskFromBoard } from '$lib/stores';
    import { resolveWorker, getModelName, getProviderDisplayName, displayModelName } from '$lib/utils/providers';
    import { getStreamingContext, getTaskDiffStatsContext } from '$lib/context';
    import Card from './Card.svelte';
    import { moveTask, updateTask, stopTask, resumeTask, deleteTask, commitAndDone, getQueuedTaskIds, getQueuedDeleteTaskIds, onQueueChanged } from '$lib/api';
    import { mergeQueuedUpdate } from '$lib/utils/task-utils';
    import commitPushIcon from '$lib/assets/icons/commit-push.svg?raw';
    import Icon from './Icon.svelte';
    import { fade, slide } from 'svelte/transition';
    import { onMount, onDestroy } from 'svelte';
    import { uiInjector, INJECTION_POINTS } from '$lib/services/ui-injector.svelte';

    // UI extension injection point — re-render when extensions change
    $effect(() => {
        const _v = uiInjector.getPointVersion(INJECTION_POINTS.TASK_CARD);
        uiInjector.refreshSlot(INJECTION_POINTS.TASK_CARD, { component: 'Card', taskId: task.id, projectId });
    });

    let { task, columnId, projectId, childrenMap, expandedTasks, onToggleExpanded, onCreateChildTask, columnColor, inline = true, onSelect, parentSelected = false, showSubtaskBorder = false, onRefreshBoard, multiSelect }: { task: Task; columnId: string; projectId?: string; childrenMap?: Map<string, Task[]>; expandedTasks?: Set<string>; onToggleExpanded?: (taskId: string) => void; onCreateChildTask?: (parentId: string) => void; columnColor?: string; inline?: boolean; onSelect?: (task: Task) => void; parentSelected?: boolean; showSubtaskBorder?: boolean; onRefreshBoard?: () => void; multiSelect?: MultiSelect | null } = $props();

    // Track if this task has queued offline actions (for diagonal fade effect)
    let taskHasQueuedActions = $state(false);
    let taskIsQueuedDelete = $state(false);

    // Safety net: stop title generating spinner after a timeout (in case backend
    // broadcast is lost). Backend has a 30s timeout; we add 15s buffer.
    const TITLE_GEN_TIMEOUT_MS = 45_000;
    let titleGenTimer: ReturnType<typeof setTimeout> | null = null;
    $effect(() => {
        if (task.title_generating) {
            // Reset the timer whenever title_generating becomes true
            if (titleGenTimer) clearTimeout(titleGenTimer);
            titleGenTimer = setTimeout(() => {
                // Title generation took too long — clear the flag locally
                // so the spinner stops even if the backend broadcast was lost.
                task.title_generating = false;
                updateTaskInBoard(appStore, task.id, task);
            }, TITLE_GEN_TIMEOUT_MS);
        } else {
            // Title generation finished — clear the safety timer
            if (titleGenTimer) {
                clearTimeout(titleGenTimer);
                titleGenTimer = null;
            }
        }
        return () => {
            if (titleGenTimer) {
                clearTimeout(titleGenTimer);
                titleGenTimer = null;
            }
        };
    });
    function checkQueuedActions() {
        taskHasQueuedActions = getQueuedTaskIds().has(task.id);
        taskIsQueuedDelete = getQueuedDeleteTaskIds().has(task.id);
    }
    let unsubscribeQueue: (() => void) | null = null;
    onMount(() => {
        // Defer so IndexedDB queue is loaded before check
        queueMicrotask(checkQueuedActions);
        unsubscribeQueue = onQueueChanged(checkQueuedActions);
    });
    onDestroy(() => {
        unsubscribeQueue?.();
    });

    // Children of this task (from the map passed by Board)
    let children = $derived(childrenMap?.get(task.id) ?? []);
    let hasChildren = $derived(children.length > 0);

    /** Collect all descendant task IDs recursively from childrenMap. */
    function collectDescendantIds(taskId: string): string[] {
        const ids: string[] = [];
        const directChildren = childrenMap?.get(taskId) ?? [];
        for (const child of directChildren) {
            ids.push(child.id);
            ids.push(...collectDescendantIds(child.id));
        }
        return ids;
    }

    /** Toggle a task's multi-select state, recursively including all descendants. */
    function toggleWithDescendants(taskId: string) {
        if (!multiSelect) return;
        const descendantIds = collectDescendantIds(taskId);
        const willSelect = !multiSelect.isSelected(taskId);
        // Toggle the task itself
        multiSelect.toggle(taskId);
        // Add or remove all descendants to match
        for (const id of descendantIds) {
            if (willSelect) {
                multiSelect.add(id);
            } else {
                multiSelect.remove(id);
            }
        }
    }

    // Subtask progress: counts ALL descendants regardless of column
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
    let statusColor = $derived(
        task.status === 'backlog' ? 'var(--color-column-backlog)' :
        task.status === 'in_progress' ? 'var(--color-column-in-progress)' :
        task.status === 'review' ? 'var(--color-column-review)' :
        task.status === 'done' ? 'var(--color-column-done)' :
        'var(--color-text-tertiary)'
    );

    // Expand/collapse state (managed by Board for stability)
    let expanded = $derived(expandedTasks?.has(task.id) ?? false);

    // Check if any descendant is running (for pause button on parent)
    let hasRunningDescendant = $derived.by(() => {
        const isActive = (s: string) => s === 'running' || s === 'pulling_image';
        if (children.length === 0) return isActive(task.execution_state);
        for (const child of children) {
            if (isActive(child.execution_state)) return true;
            // Recursively check (childrenMap has all tasks, not just same-column)
            const grandChildren = childrenMap?.get(child.id) ?? [];
            for (const gc of grandChildren) {
                if (isActive(gc.execution_state)) return true;
            }
        }
        return isActive(task.execution_state);
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

    // Long-press detection for mobile multi-select
    let longPressTimer: ReturnType<typeof setTimeout> | null = null;
    let longPressTriggered = $state(false);
    const LONG_PRESS_DURATION = 400; // ms

    function handleMouseDown(e: MouseEvent) {
        mouseDownPos = { x: e.clientX, y: e.clientY };
    }

    function handleDragStart(e: DragEvent) {
        e.stopPropagation();
        if (!e.dataTransfer) return;
        // If this task is part of an active multi-selection, drag all selected tasks
        if (multiSelect?.hasSelection && multiSelect.isSelected(task.id)) {
            e.dataTransfer.setData('text/plain', multiSelect.toArray().join(','));
        } else {
            e.dataTransfer.setData('text/plain', task.id);
        }
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
        // Skip if a long-press multi-select was triggered (mobile)
        if (longPressTriggered) {
            longPressTriggered = false;
            return;
        }
        // Skip if multi-select mode is active on mobile (tap toggled selection)
        if (isMobileCtx.isMobile && multiSelect && multiSelect.hasSelection) {
            return;
        }
        // Ctrl/Cmd + Click: toggle multi-select (with descendants)
        if (multiSelect && (e.ctrlKey || e.metaKey)) {
            e.stopPropagation();
            toggleWithDescendants(task.id);
            return;
        }
        if (inline && task.parent_task_id) e.stopPropagation();
        if (onSelect) { onSelect(task); } else { appStore.selectedTask = task; uiStore.showCardDetail = true; }
    }

    // Touch handlers for mobile long-press multi-select
    // First selection requires long-press; subsequent taps toggle without long-press
    function handleTouchStart(e: TouchEvent) {
        if (!multiSelect || !isMobileCtx.isMobile) return;
        // Skip if touching a button or interactive element
        const target = e.target as HTMLElement;
        if (target.closest('button, input, a, select, .card-edit-panel')) return;
        longPressTriggered = false;
        // Only require long-press when nothing is selected yet
        if (!multiSelect.hasSelection) {
            longPressTimer = setTimeout(() => {
                longPressTriggered = true;
                if (navigator.vibrate) navigator.vibrate(30);
                toggleWithDescendants(task.id);
            }, LONG_PRESS_DURATION);
        }
    }

    function handleTouchEnd(e: TouchEvent) {
        if (longPressTimer) {
            clearTimeout(longPressTimer);
            longPressTimer = null;
        }
        // If tasks are already selected, tap toggles selection (no long-press needed)
        if (multiSelect && multiSelect.hasSelection && !longPressTriggered) {
            e.preventDefault();
            e.stopPropagation();
            toggleWithDescendants(task.id);
        }
    }

    function handleTouchMove() {
        // Cancel long-press if finger moves (user is scrolling)
        if (longPressTimer) {
            clearTimeout(longPressTimer);
            longPressTimer = null;
        }
    }

    const isMobileCtx = useIsMobile();

    // Mobile subtask overflow menu ("..." button)
    let isMobileSubtask = $derived(isMobileCtx.isMobile && inline && !!task.parent_task_id);
    let overflowOpen = $state(false);
    let overflowRef: HTMLButtonElement | null = $state(null);
    let overflowMenuEl: HTMLDivElement | null = null;

    function buildOverflowMenu(): HTMLDivElement {
        const menu = document.createElement('div');
        menu.className = 'card-overflow-menu';
        menu.setAttribute('role', 'menu');
        // Inline styles to escape Svelte scoping (element is on document.body)
        menu.style.cssText = 'position:fixed;z-index:10000;background:var(--color-surface-3,#1e1e2e);border:1px solid var(--color-border,#333);border-radius:var(--radius-md,8px);padding:4px;width:140px;box-shadow:0 8px 24px rgba(0,0,0,0.3);display:flex;flex-direction:column;gap:2px;';

        const items: Array<{ icon: string; label: string; action: (e: MouseEvent) => void; danger?: boolean }> = [];

        if (!inline || !task.parent_task_id || hasChildren || showSubtaskBorder) {
            items.push({ icon: '<path d="M8 3v10M3 8h10" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>', label: 'Create subtask', action: handleCreateChild });
        }
        if (!isDoneSubtask) {
            items.push({ icon: '<path d="M11.5 2.5l2 2L5 13H3v-2L11.5 2.5z" stroke="currentColor" stroke-width="1.3" stroke-linejoin="round"/>', label: 'Edit', action: startEdit });
        }
        if (isPausable) {
            items.push({ icon: '<rect x="2.5" y="1.5" width="1.5" height="7" fill="currentColor"/><rect x="6" y="1.5" width="1.5" height="7" fill="currentColor"/>', label: 'Pause', action: handlePause });
        } else if (isResumable) {
            items.push({ icon: '<polygon points="3.5,1.5 3.5,8.5 8.5,5" fill="currentColor"/>', label: 'Resume', action: handleResume });
        }
        if ((columnId === 'done' || isDoneSubtask) && !(multiSelect?.isSelected(task.id) ?? false)) {
            items.push({ icon: '<path d="M2 4h12M5 4V3a1 1 0 011-1h4a1 1 0 011 1v1M6 7v5M10 7v5" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/><path d="M3 4l1 9a1 1 0 001 1h6a1 1 0 001-1l1-9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>', label: 'Delete', action: handleDelete, danger: true });
        }

        for (const item of items) {
            const btn = document.createElement('button');
            btn.type = 'button';
            btn.className = 'overflow-item' + (item.danger ? ' overflow-delete' : '');
            btn.setAttribute('role', 'menuitem');
            btn.style.cssText = 'display:flex;align-items:center;gap:10px;width:100%;padding:10px 12px;border:none;border-radius:var(--radius-sm,6px);background:transparent;color:var(--color-text-primary,#ccc);font-size:0.875rem;font-family:inherit;font-weight:500;cursor:pointer;text-align:left;touch-action:manipulation;-webkit-tap-highlight-color:transparent;';
            btn.innerHTML = `<svg width="16" height="16" viewBox="0 0 16 16" fill="none" style="flex-shrink:0">${item.icon}</svg>${item.label}`;
            btn.addEventListener('click', (e: MouseEvent) => { closeOverflow(); item.action(e); });
            menu.appendChild(btn);
        }

        return menu;
    }

    function toggleOverflow(e: MouseEvent) {
        e.stopPropagation();
        if (overflowOpen) {
            closeOverflow();
            return;
        }
        if (overflowRef) {
            const r = overflowRef.getBoundingClientRect();
            const menuWidth = 140;
            let left = r.left - menuWidth - 4;
            const vw = window.visualViewport?.width || window.innerWidth;
            if (left < 8) left = r.right + 4;
            if (left + menuWidth > vw - 8) left = vw - menuWidth - 8;
            overflowMenuEl = buildOverflowMenu();
            overflowMenuEl.style.top = r.top + 'px';
            overflowMenuEl.style.left = left + 'px';
            document.body.appendChild(overflowMenuEl);
        }
        overflowOpen = true;
    }

    function closeOverflow() {
        overflowOpen = false;
        if (overflowMenuEl) {
            overflowMenuEl.remove();
            overflowMenuEl = null;
        }
    }

    $effect(() => {
        if (!overflowOpen) return;
        function handleClick(e: MouseEvent) {
            if (overflowRef && overflowRef.contains(e.target as Node)) return;
            if (overflowMenuEl && overflowMenuEl.contains(e.target as Node)) return;
            closeOverflow();
        }
        function handleKey(e: KeyboardEvent) {
            if (e.key === 'Escape') closeOverflow();
        }
        document.addEventListener('mousedown', handleClick);
        document.addEventListener('keydown', handleKey);
        return () => {
            document.removeEventListener('mousedown', handleClick);
            document.removeEventListener('keydown', handleKey);
        };
    });

    let statusBadge = $derived.by(() => {
        const streaming = streamingIds.has(task.id);
        const state = appStore.board?.tasks.find(t => t.id === task.id)?.execution_state ?? task.execution_state;
        const hasConflict = (appStore.board?.tasks.find(t => t.id === task.id)?.has_merge_conflict ?? task.has_merge_conflict) ?? false;
        if (streaming || state === 'running') return { text: 'Running', color: 'var(--color-status-blue)' };
        if (state === 'pulling_image') return { text: 'Pulling image', color: 'var(--color-status-blue)' };
        if (hasConflict) return { text: 'Resolving conflict', color: 'var(--color-status-orange)' };
        if (state === 'idle' && task.status === 'in_progress') return { text: 'Pending', color: 'var(--color-status-orange)' };
        if (state === 'failed') return { text: 'Failed', color: 'var(--color-status-red)' };
        if (state === 'stopped') return { text: 'Stopped', color: 'var(--color-status-yellow)' };
        return null;
    });

    // Model badge: always "auto" behavior
    // Model only (1 provider) or provider+model (2+)
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
    let modelBadge = $derived.by(() => {
        const cfg = appStore.config;
        const providers = cfg?.providers || [];
        if (providers.length === 0) return { text: null, provider: null, model: null, color: '' };
        // Check if task uses an agent (e.g. Pi Coding Agent)
        if (task.llama_server_url?.startsWith(AGENT_URL_PREFIX)) {
            // Look up the agent provider's display name from the config
            const agentProvider = providers.find(p => p.url === task.llama_server_url);
            const agentText = agentProvider ? getProviderDisplayName(agentProvider, providers) : 'Coding Agent';
            return { text: agentText, provider: null, model: null, color: '#A371F7', isAgent: true };
        }
        // Use task model if set, otherwise fall back to the configured Worker model
        const worker = resolveWorker(cfg);
        const activeModel = task.model_name || (worker.provider && worker.modelName ? worker.modelName : providers[0]?.models.length > 0 ? getModelName(providers[0].models[0]) : null);
        if (!activeModel) return { text: null, provider: null, model: null, color: '' };
        // Find matching provider by URL
        const provider = providers.find(p => p.url === task.llama_server_url);
        // Auto: show provider name only when 2+ providers
        const showProvider = providers.length > 1;
        const provName = showProvider && provider ? getProviderDisplayName(provider, providers) : null;
        const displayModel = displayModelName(activeModel);
        const text = provName ? `${provName} · ${displayModel}` : displayModel;
        return { text, provider: provName, model: displayModel, color: getModelColor(activeModel), isAgent: false };
    });

    // Context stores (must be retrieved at top level, not inside derived/effects)
    let streamingIdsContext = getStreamingContext();
    let taskDiffStatsStore = getTaskDiffStatsContext();
    // Sync streamingIds store to $state for reactive tracking in $derived.by
    // (get() on a Svelte store inside $derived.by does NOT establish a dependency)
    let streamingIds = $state<Set<string>>(new Set());
    $effect(() => {
        const unsub = streamingIdsContext.subscribe(v => { streamingIds = v; });
        return unsub;
    });
    let diffStats = $derived($taskDiffStatsStore[task.id] || null);
    let hasChanges = $derived(!!diffStats?.hash && ((diffStats.staged_paths?.length || 0) > 0 || !!diffStats.has_committed_changes));

    let isSelected = $state(false);
    // Don't show selected highlight on parent when any descendant is selected
    let showSelected = $derived.by(() => {
        if (!isSelected) return false;
        const sel = appStore.selectedTask;
        if (!sel || !sel.parent_task_id) return true;
        // Walk up the ancestor chain to see if this task is an ancestor of the selected task
        let ancestor: Task | null = sel;
        let found = false;
        while (ancestor) {
            if (ancestor.parent_task_id === task.id) {
                found = true;
                break;
            }
            const nextId: string | undefined = ancestor.parent_task_id;
            ancestor = nextId ? (appStore.board?.tasks.find(t => t.id === nextId) ?? null) : null;
        }
        if (found) return false;
        return true;
    });

    $effect(() => {
        isSelected = appStore.selectedTask?.id === task.id;
    });

    function startEdit(e: MouseEvent) {
        e.stopPropagation();
        const cfg = appStore.config;
        const providers = cfg?.providers || [];
        const selProject = appStore.selectedProject;
        appStore.editTaskData = {
            taskId: task.id,
            projectId: projectId!,
            taskTitle: task.title,
            taskPrompt: task.prompt,
            editProviders: providers,
            editModel: task.model_name || '',
            editUrl: task.llama_server_url || '',
            editReasoningEffort: task.reasoning_effort || '',
            editOperatingMode: task.operating_mode ?? 'act',
            editAutoMerge: task.auto_merge ?? true,
            editSubtasksAutoMerge: task.subtasks_auto_merge ?? true,
            taskHasParent: !!task.parent_task_id,
            targetBranch: selProject?.origin_branch || 'main',
            baseRef: task.base_ref || '',
            isAgentSelected: () => {
                const url = task.llama_server_url;
                if (!url) return false;
                if (url.startsWith(AGENT_URL_PREFIX)) return true;
                const p = providers.find(p => p.url === url);
                return p ? isAgentProvider(p) : false;
            },
        };
        appStore.showEditTask = true;
    }

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
            appStore.selectedTask = task;
            uiStore.showCardDetail = true;
        }
        // Alt+ArrowLeft/ArrowRight: move task to previous/next column
        if (e.altKey && (e.key === 'ArrowLeft' || e.key === 'ArrowRight')) {
            e.preventDefault();
            e.stopPropagation();
            if (!projectId) return;
            const currentIdx = statuses.indexOf(task.status as typeof statuses[number]);
            const targetIdx = e.key === 'ArrowLeft'
                ? (currentIdx - 1 + statuses.length) % statuses.length
                : (currentIdx + 1) % statuses.length;
            const targetStatus = statuses[targetIdx];
            (async () => {
                try {
                    const { moveTask: apiMoveTask } = await import('$lib/api');
                    const updated = await apiMoveTask(projectId, task.id, targetStatus);
                    Object.assign(task, mergeQueuedUpdate(task, updated));
                    updateTaskInBoard(appStore, task.id, task);
                } catch (err) {
                    console.error('Failed to move task:', err);
                }
            })();
        }
    }

    // Pause/Resume for In Progress column
    // Derive liveExecutionState from appStore so it updates reactively
    // when WebSocket broadcasts a board refresh. Falls back to task prop.
    const STOPPED_STATES = ['stopped', 'failed'];
    let liveExecutionState = $derived(
        appStore.board?.tasks.find(t => t.id === task.id)?.execution_state ?? task.execution_state
    );

    // Pause/Resume: for parent tasks, show based on descendant state
    let isPausable = $derived(columnId === 'in_progress' && hasRunningDescendant);
    let isResumable = $derived(columnId === 'in_progress' && STOPPED_STATES.includes(liveExecutionState));
    let isIdle = $derived(columnId === 'in_progress' && liveExecutionState === 'idle' && !hasRunningDescendant);

    async function handlePause(e: MouseEvent) {
        e.stopPropagation();
        if (!projectId) return;
        try {
            const updated = await stopTask(projectId, task.id);
            // Apply optimistic update to task object
            Object.assign(task, mergeQueuedUpdate(task, updated));
            updateTaskInBoard(appStore, task.id, task);
        } catch (err) {
            console.error('Failed to pause task:', err);
        }
    }

    async function handleResume(e: MouseEvent) {
        e.stopPropagation();
        if (!projectId) return;
        try {
            const updated = await resumeTask(projectId, task.id);
            Object.assign(task, mergeQueuedUpdate(task, updated));
            updateTaskInBoard(appStore, task.id, task);
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
                // Open dialog — it will stream the commit message from the backend
                const confirmedMessage = await new Promise<string | null>((resolve) => {
                    dialogStore.commitDialog = {
                        streaming: true,
                        message: '',
                        confirmText: 'Commit and move to Done',
                        cancelText: 'Cancel',
                        projectId: projectId!,
                        taskId: task.id,
                        _resolve: resolve,
                    };
                });
                if (!confirmedMessage) {
                    return; // User cancelled
                }
                updated = await commitAndDone(projectId, task.id, confirmedMessage);
                // Clear stale diff stats immediately so the button updates right away
                taskDiffStatsStore.update(map => { delete map[task.id]; return { ...map }; });
            } else {
                updated = await moveTask(projectId, task.id, nextStatus);
            }
            Object.assign(task, mergeQueuedUpdate(task, updated));
            updateTaskInBoard(appStore, task.id, task);
        } catch (err) {
            console.error('Failed to move task:', err);
        }
    }

    async function handleDelete(e: MouseEvent) {
        e.stopPropagation();
        if (!projectId) return;
        const confirmed = await dialogStore.showConfirm({
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
                removeTaskFromBoard(appStore, task.id);
                onRefreshBoard?.();
            } else {
                dialogStore.showToast('Failed to delete task: ' + String(err), 'error');
            }
        }
    }

    function handleCreateChild(e: MouseEvent) {
        e.stopPropagation();
        if (onCreateChildTask) {
            onCreateChildTask(task.id);
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
    class:multi-selected={multiSelect?.isSelected(task.id) ?? false}
    class:has-children={hasChildren}
    class:is-expanded={expanded}
    class:is-subtask={inline && !!task.parent_task_id}
    class:is-sidebar-subtask={showSubtaskBorder && !!task.parent_task_id}
    class:queued={taskHasQueuedActions}
    class:queued-delete={taskIsQueuedDelete}
    data-task-id={task.id}
    draggable={!isMobileCtx.isMobile}
    onmousedown={handleMouseDown}
    ondragstart={handleDragStart}
    onclick={handleCardClick}
    onkeydown={handleCardKeydown}
    ontouchstart={handleTouchStart}
    ontouchend={handleTouchEnd}
    ontouchmove={handleTouchMove}
    role="button"
    tabindex="0"
    aria-label={`Open task: ${task.title || '(untitled)'}, Status: ${task.status === 'backlog' ? 'Backlog' : task.status === 'in_progress' ? 'In Progress' : task.status === 'review' ? 'Review' : 'Done'}`}
    aria-keyshortcuts="Alt+ArrowLeft, Alt+ArrowRight"
    aria-current={isSelected ? 'page' : undefined}
    style={`--actions: ${actionCount}; --column-color: ${columnColor || ""}; --status-color: ${statusColor}${parentSelected && inline && task.parent_task_id ? `; border-left: 3px solid ${statusColor}` : ""}`}
>
    <div class="card-title">
            {#if task.title_generating}
                <span class="title-spinner" aria-label="Generating title">
                    <svg viewBox="0 0 16 16" width="12" height="12"><circle cx="8" cy="8" r="6" fill="none" stroke="currentColor" stroke-width="2" stroke-dasharray="30 14" stroke-linecap="round"/></svg>
                </span>
            {/if}
            <span class="card-title-text">{truncate(task.title, 80)}</span>
            <div class="card-actions">
                {#if isMobileSubtask}
                    <!-- Overflow menu for mobile subtasks -->
                    <button
                        type="button"
                        class="action-icon action-overflow"
                        class:is-open={overflowOpen}
                        onclick={toggleOverflow}
                        bind:this={overflowRef}
                        aria-label="Actions"
                        aria-expanded={overflowOpen}
                    >
                        <svg width="18" height="18" viewBox="0 0 16 16" fill="currentColor"><circle cx="4" cy="8" r="1.5"/><circle cx="8" cy="8" r="1.5"/><circle cx="12" cy="8" r="1.5"/></svg>
                    </button>
                    {#if isIdle}
                        <span class="action-idle" aria-label="Idle" title="Idle">Idle</span>
                    {/if}
                {:else}
                    {#if !inline || !task.parent_task_id || hasChildren || showSubtaskBorder}
                        <button
                            type="button"
                            class="action-icon action-add"
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
                    {:else if columnId === 'done' && !(multiSelect?.isSelected(task.id) ?? false)}
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
                    {:else if isDoneSubtask && !(multiSelect?.isSelected(task.id) ?? false)}
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
                    {:else if task.status === 'in_progress'}
                        <!-- no move button for in_progress tasks -->
                    {:else}
                        <button
                            type="button"
                            class="action-icon action-move"
                            onclick={cycleStatus}
                            aria-label={nextStatus === 'done' ? (hasChanges ? 'Commit and move to done' : `Move to ${statusLabels[nextStatus]}`) : `Move to ${statusLabels[nextStatus]}`}
                            title={nextStatus === 'done' ? (hasChanges ? 'Commit and move to done' : `Move to ${statusLabels[nextStatus]}`) : `Move to ${statusLabels[nextStatus]}`}
                            style="color: {nextStatus === 'done' && hasChanges ? '#EAB308' : statusColors[nextStatus]}"
                        >
                            {#if nextStatus === 'backlog'}
                                <svg width="14" height="14" viewBox="0 0 10 10" fill="none" aria-hidden="true"><circle cx="5" cy="5" r="3.5" stroke="currentColor" stroke-width="1.5"/></svg>
                            {:else if nextStatus === 'in_progress'}
                                <svg width="14" height="14" viewBox="0 0 10 10" fill="none" aria-hidden="true"><polygon points="3.5,1.5 3.5,8.5 8.5,5" fill="currentColor"/></svg>
                            {:else if nextStatus === 'review'}
                                <svg width="14" height="14" viewBox="0 0 10 10" fill="none" aria-hidden="true"><circle cx="5" cy="5" r="3.5" stroke="currentColor" stroke-width="1"/><circle cx="5" cy="5" r="1.2" fill="currentColor"/></svg>
                            {:else if nextStatus === 'done' && hasChanges}
                                <Icon src={commitPushIcon} color="#eab308" width={14} height={14} />
                            {:else if nextStatus === 'done'}
                                <svg width="14" height="14" viewBox="0 0 10 10" fill="none" aria-hidden="true"><circle cx="5" cy="5" r="3.5" fill="currentColor"/></svg>
                            {/if}
                        </button>
                    {/if}
                {/if}
            </div>
        </div>
    {#if statusBadge || modelBadge.text || subtaskProgress}
        <div class="badges-row">
            {#if statusBadge}
                <div class="badge" style="background: {statusBadge.color}11; color: {statusBadge.color}">
                    {statusBadge.text}
                </div>
            {/if}
            {#if modelBadge.text}
                {#if modelBadge.isAgent}
                    <div class="badge model-badge agent-badge" style="background: {modelBadge.color}11; color: {modelBadge.color}">
                        <svg width="12" height="12" viewBox="0 0 16 16" fill="none" style="margin-right: 3px">
                            <rect x="3" y="5" width="10" height="8" rx="1.5" stroke="currentColor" stroke-width="1.5"/>
                            <circle cx="6" cy="9" r="1" fill="currentColor"/>
                            <circle cx="10" cy="9" r="1" fill="currentColor"/>
                        </svg>
                        {modelBadge.text}
                    </div>
                {:else}
                    <div class="badge model-badge" style="background: {modelBadge.color}11; color: {modelBadge.color}">
                        {#if modelBadge.provider}<span class="badge-provider">{modelBadge.provider}</span><span class="badge-sep"> · </span>{/if}<span class="badge-model">{modelBadge.model}</span>
                    </div>
                {/if}
            {/if}
        </div>
    {/if}
    {#if diffStats}
        <div class="diff-stats-row">
            <span class="diff-stats">
                {#if diffStats.files > 0}<span class="diff-files">{diffStats.files} file{diffStats.files !== 1 ? 's' : ''}</span>{/if}
                {#if diffStats.files > 0 && (diffStats.additions > 0 || diffStats.deletions > 0)}<span class="diff-sep"> · </span>{/if}
                {#if diffStats.additions > 0}<span class="diff-add">+{diffStats.additions}</span>{/if}
                {#if diffStats.deletions > 0}<span class="diff-del">-{diffStats.deletions}</span>{/if}
            </span>
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
        <div class="subtask-list" transition:slide={{ duration: 200 }}>
            {#each children as child}
                <div class="subtask-wrapper" transition:fade={{ duration: 200 }}>
                    <Card task={child} {columnId} {projectId} {childrenMap} {expandedTasks} {onToggleExpanded} {onCreateChildTask} columnColor={columnColor} inline={true} parentSelected={showSelected} {showSubtaskBorder} {onSelect} {multiSelect} {onRefreshBoard} />
                </div>
            {/each}
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
        --depth: 0;
        user-select: none;
        -webkit-user-select: none;
        -webkit-user-drag: element;
        will-change: transform;
        contain: layout style paint;
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
    /* Multi-select visual state */
    .card.multi-selected {
        border-color: var(--color-accent);
        background: color-mix(in srgb, var(--color-accent) 12%, var(--color-surface-2));
    }
    .card.multi-selected.is-subtask {
        border-left-color: var(--status-color, var(--color-border));
    }
    .card.multi-selected.has-children:not(.is-subtask) {
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
        background: var(--color-accent-dark);
        color: white;
    }
    .action-icon.action-move:hover {
        background: var(--color-accent-dark);
        color: white !important;
    }
    .action-icon.action-delete:hover {
        background: var(--color-status-red-dark);
        color: white;
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
    .action-overflow.is-open {
        background: var(--color-surface-4);
        color: var(--color-text-primary);
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
    .diff-stats-row {
        display: flex;
        align-items: center;
        gap: 3px;
        padding: 2px 0;
    }
    .diff-stats {
        display: inline-flex;
        align-items: center;
        gap: 3px;
        font-size: 0.75rem;
        color: var(--color-text-tertiary);
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
    @media (hover: none) {
        .card-actions {
            opacity: 1;
            transition: none;
            gap: 4px;
        }
        .card-title-text {
            padding-right: calc(var(--actions) * 48px + 10px);
        }
        .card.is-subtask > .card-title > .card-title-text {
            padding-right: 58px;
        }
        .action-icon {
            width: 44px;
            height: 44px;
            padding: 0;
            touch-action: manipulation;
            -webkit-tap-highlight-color: transparent;
        }
        .action-icon svg {
            width: 18px;
            height: 18px;
        }
        .action-idle {
            min-width: 44px;
            min-height: 44px;
            padding: 0 8px;
        }
        .subtask-progress-bar {
            height: 44px;
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
        background: var(--color-status-green);
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
        color: var(--color-text-primary);
        white-space: nowrap;
        pointer-events: none;
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