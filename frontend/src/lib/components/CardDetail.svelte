<script lang="ts">
    import { onMount } from 'svelte';
    import { get } from 'svelte/store';
    import type { Task, Provider, TaskCommit } from '$lib/types';
    import type { DiffFile } from '$lib/types/diff';
    import { board as boardStore, config as configStore, selectedTask, streamingTaskIds, showTerminal, showConfirm, showToast, showCreateTask, creatingChildOf, expandedTasks as expandedTasksStore, persistCollapsedTasks, cleanupCollapsedTasks, initExpandedTasks, taskDiffStats, taskDiff as taskDiffStore } from '$lib/store';
    import { moveTask, deleteTask, getTaskDiff, getTaskCommits, updateTask } from '$lib/api';
    import { parseUnifiedDiff } from '$lib/utils/diff-parser';
import { getWorktreeDisplayPath } from '$lib/utils/path';
    import shieldIcon from '$lib/assets/icons/shield.svg';
    import ModelSelector from './Chat/ModelSelector.svelte';
    import shieldOffIcon from '$lib/assets/icons/shield-off.svg';
    import TaskChat from './Chat/TaskChat.svelte';
    import ResizeHandle from './ResizeHandle.svelte';
    import DiffViewer from './DiffViewer.svelte';
    import DiffSelect from './DiffSelect.svelte';
    import Card from './Card.svelte';

    let { task, tasks: columnTasks, projectPath, workspaceDir, sandboxEnabled, onClose, onSelectTask, onRefreshBoard }: { task: Task; tasks: Task[]; projectPath?: string; workspaceDir?: string; sandboxEnabled?: boolean; onClose: () => void; onSelectTask?: (task: Task) => void; onRefreshBoard?: () => void } = $props();

    let allTasks: Task[] = $state([]);

    const overviewSections = [
        { id: 'backlog', title: 'Backlog' },
        { id: 'in_progress', title: 'In Progress' },
        { id: 'review', title: 'Review' },
        { id: 'done', title: 'Done' },
    ];

    // Collapsible sections
    let collapsedSections: Set<string> = $state(new Set());

    function toggleSection(sectionId: string) {
        const next = new Set(collapsedSections);
        if (next.has(sectionId)) {
            next.delete(sectionId);
        } else {
            next.add(sectionId);
        }
        collapsedSections = next;
    }

    // --- Drag-and-drop for board overview ---
    let draggedTaskId: string | null = $state(null);
    let sectionDragState = $state<Map<string, { isOver: boolean; isRejected: boolean }>>(new Map());

    function handleDragStart(taskId: string) {
        draggedTaskId = taskId;
    }

    /** Walk up the ancestor chain to check if any ancestor is In Progress. */
    function hasAncestorInProgress(task: Task): boolean {
        const taskMap = new Map(allTasks.map(t => [t.id, t]));
        let current = task;
        while (current.parent_task_id) {
            const parent = taskMap.get(current.parent_task_id);
            if (!parent) break;
            if (parent.status === 'in_progress') return true;
            current = parent;
        }
        return false;
    }

    function canDrop(taskId: string, targetStatus: string): boolean {
        const task = allTasks.find(t => t.id === taskId);
        if (!task) return false;
        // Block subtasks from moving to Backlog when an ancestor is In Progress
        if (targetStatus === 'backlog' && task.parent_task_id && hasAncestorInProgress(task)) return false;
        return true;
    }

    function handleSectionDragOver(e: DragEvent, sectionId: string) {
        e.preventDefault();
        const taskId = e.dataTransfer?.getData('text/plain');
        const state = sectionDragState.get(sectionId) || { isOver: false, isRejected: false };
        if (taskId && !canDrop(taskId, sectionId)) {
            e.dataTransfer.dropEffect = 'none';
            sectionDragState = new Map(sectionDragState).set(sectionId, { isRejected: true, isOver: false });
        } else {
            sectionDragState = new Map(sectionDragState).set(sectionId, { isRejected: false, isOver: true });
        }
    }

    function handleSectionDragLeave(sectionId: string) {
        sectionDragState = new Map(sectionDragState).set(sectionId, { isOver: false, isRejected: false });
    }

    async function handleSectionDrop(e: DragEvent, targetStatus: string) {
        const sectionId = targetStatus;
        sectionDragState = new Map(sectionDragState).set(sectionId, { isOver: false, isRejected: false });
        const taskId = e.dataTransfer?.getData('text/plain');
        if (!taskId) return;
        const task = allTasks.find(t => t.id === taskId);
        if (!task || task.status === targetStatus) return;
        if (!canDrop(taskId, targetStatus)) {
            showToast('Cannot move to Backlog: parent task is In Progress', 'error');
            draggedTaskId = null;
            return;
        }
        try {
            const updated = await moveTask(task.project_id, taskId, targetStatus);
            // Skip optimistic update if this subtask will be consolidated into its parent.
            const willConsolidate = task.parent_task_id &&
                allTasks.some(t => t.id === task.parent_task_id && t.status === targetStatus);
            if (!willConsolidate) {
                boardStore.update(b => {
                    if (!b) return b;
                    return { ...b, tasks: b.tasks.map(t => t.id === taskId ? updated : t) };
                });
            }
        } catch (err) {
            console.error('Failed to move task:', err);
            showToast('Failed to move task: ' + String(err), 'error');
        }
        draggedTaskId = null;
    }

    function getSectionColor(id: string): string {
        const colors: Record<string, string> = {
            backlog: 'var(--color-column-backlog)',
            in_progress: 'var(--color-column-in-progress)',
            review: 'var(--color-column-review)',
            done: 'var(--color-column-done)',
        };
        return colors[id] || 'var(--color-border)';
    }

    // Diff state
    let diffFiles: DiffFile[] = $state([]);
    let untrackedFiles: { path: string; fileSize?: number; file: DiffFile }[] = $state([]);
    // Terminal - worktree exists when task has a branch (set on start, cleared on delete)
    let worktreeExists = $derived(!!task.branch);

    // Commit history for diff comparison
    let commits: TaskCommit[] = $state([]);
    let baseCommit: TaskCommit | null = $state(null);

    // Checkpoint diff overlay
    let checkpointDiffOpen = $state(false);
    let checkpointDiffHash = $state(''); // which checkpoint hash is currently shown
    let checkpointDiffFiles: DiffFile[] = $state([]);
    let checkpointDiffUntracked: { path: string; fileSize?: number; file: DiffFile }[] = $state([]);
    let checkpointDiffLoading = $state(false);
    let checkpointDiffLabel = $state('');
    // Two-dropdown diff: '' means base ref (original branch), 'working' means working tree
    let diffFrom = $state('');
    let diffTo = $state('working');

    function formatRelativeTime(isoStr: string): string {
        if (!isoStr) return '';
        const date = new Date(isoStr);
        if (isNaN(date.getTime())) return '';
        const now = new Date();
        const diffMs = now.getTime() - date.getTime();
        const diffMin = Math.floor(diffMs / 60000);
        if (diffMin < 1) return 'just now';
        if (diffMin < 60) return `${diffMin}m ago`;
        const diffHr = Math.floor(diffMin / 60);
        if (diffHr < 24) return `${diffHr}h ago`;
        const diffDay = Math.floor(diffHr / 24);
        if (diffDay < 7) return `${diffDay}d ago`;
        return date.toLocaleDateString([], { month: 'short', day: 'numeric' });
    }

    function getCommitSubject(commit: TaskCommit): string {
        return commit.subject.split('\n')[0].trim();
    }

    function getBaseCommitLabel(): string {
        if (!baseCommit) return 'Original branch';
        const subject = getCommitSubject(baseCommit);
        const truncated = subject.length > 20 ? subject.slice(0, 20) + '…' : subject;
        return truncated;
    }

    // Sorted commits: newest first
    let sortedCommits = $derived(
        [...commits].sort((a, b) => new Date(b.date).getTime() - new Date(a.date).getTime())
    );

    // Options for the From dropdown: commits newest→oldest, then Origin at bottom
    let fromOptions = $derived.by(() => {
        const opts: { value: string; label: string; meta?: string }[] = [];
        for (const c of sortedCommits) {
            opts.push({
                value: c.hash,
                label: getCommitSubject(c),
                meta: `${c.short.slice(0, 7)} (${formatRelativeTime(c.date)})`,
            });
        }
        if (baseCommit) {
            opts.push({
                value: '',
                label: getBaseCommitLabel(),
                meta: `${baseCommit.short.slice(0, 7)} (Origin)`,
            });
        } else {
            opts.push({ value: '', label: 'Original branch' });
        }
        return opts;
    });

    // Options for the To dropdown: Working tree at top, then commits newest→oldest
    let toOptions = $derived.by(() => {
        const opts: { value: string; label: string; meta?: string }[] = [
            { value: 'working', label: 'Working tree' },
        ];
        for (const c of sortedCommits) {
            opts.push({
                value: c.hash,
                label: getCommitSubject(c),
                meta: `${c.short.slice(0, 7)} (${formatRelativeTime(c.date)})`,
            });
        }
        return opts;
    });

    function onDiffFromChange(value: string) {
        diffFrom = value;
        loadDiffForMode();
    }

    function onDiffToChange(value: string) {
        diffTo = value;
        loadDiffForMode();
    }

    /** Set diff to compare base ref vs a specific checkpoint commit. */
    function setDiffToCommit(commitHash: string) {
        diffFrom = '';
        diffTo = commitHash;
        loadDiffForMode();
    }

    /** Handle "View Changes" from a checkpoint divider — opens an overlay in the diff panel.
     * Shows changes between origin and the state RIGHT BEFORE the checkpoint.
     * Backend resolves stash refs to their common ancestor with baseRef.
     * Toggles closed if the same checkpoint is clicked again. */
    async function handleViewChanges(commitHash: string) {
        if (isMobile) activeTab = 'diff';
        // Toggle: if same checkpoint is already open, close it
        if (checkpointDiffOpen && checkpointDiffHash === commitHash) {
            closeCheckpointDiff();
            return;
        }
        checkpointDiffOpen = true;
        checkpointDiffHash = commitHash;
        checkpointDiffLoading = true;
        checkpointDiffFiles = [];
        checkpointDiffUntracked = [];
        checkpointDiffLabel = commitHash ? commitHash.slice(0, 7) : 'current';
        try {
            const headRef = commitHash || undefined;
            const result = await getTaskDiff(task.project_id, task.id, { headRef });
            checkpointDiffFiles = parseUnifiedDiff(result.diff).filter(f => !f.path.split('/').pop()?.match(/\..*\.swp$/));
            checkpointDiffUntracked = (result.untracked || []).filter(u => !u.path.split('/').pop()?.match(/\..*\.swp$/)).map(u => ({
                path: u.path,
                fileSize: u.file_size,
                file: parseUnifiedDiff(u.diff)[0] || { path: u.path, added: 0, removed: 0, rows: [] },
            }));
        } catch (e) {
            checkpointDiffFiles = [];
            checkpointDiffUntracked = [];
        }
        checkpointDiffLoading = false;
    }

    function closeCheckpointDiff() {
        checkpointDiffOpen = false;
        checkpointDiffHash = '';
    }

    // Inline editing for task list items
    let editingTaskId = $state<string | null>(null);
    let editingTitleDraft = $state('');
    let editingTitleInput: HTMLInputElement | null = $state(null);
    let editingPlanMode = $state(false);
    let editingAutoCommit = $state(false);
    let editingAutoPr = $state(false);
    let editingModel = $state('');
    let editingUrl = $state('');
    let editingReasoningEffort = $state('');
    let editingProviders: Provider[] = $state([]);

    function startEditTaskTitle(taskId: string, currentTitle: string, e: MouseEvent) {
        e.stopPropagation();
        const t = allTasks.find(t => t.id === taskId);
        editingTaskId = taskId;
        editingTitleDraft = currentTitle;
        editingPlanMode = t?.plan_mode ?? false;
        editingAutoCommit = t?.auto_commit ?? false;
        editingAutoPr = t?.auto_pr ?? false;
        editingModel = t?.model_name ?? '';
        editingUrl = t?.llama_server_url ?? '';
        editingReasoningEffort = t?.reasoning_effort ?? '';
        // Load providers from config store
        const cfg = get(configStore);
        editingProviders = cfg?.providers || [];
    }

    function finishEditTask() {
        if (!editingTaskId || !editingTitleDraft.trim() || !task.project_id) {
            cancelEditTask();
            return;
        }
        const trimmed = editingTitleDraft.trim();
        const targetTask = allTasks.find(t => t.id === editingTaskId);
        const updates: Record<string, unknown> = { title: trimmed };
        updates.plan_mode = editingPlanMode;
        updates.auto_commit = editingAutoCommit;
        updates.auto_pr = editingAutoPr;
        if (editingModel) updates.model_name = editingModel;
        if (editingUrl) updates.llama_server_url = editingUrl;
        if (editingReasoningEffort) updates.reasoning_effort = editingReasoningEffort;
        if (targetTask) {
            targetTask.title = trimmed;
            targetTask.plan_mode = editingPlanMode;
            targetTask.auto_commit = editingAutoCommit;
            targetTask.auto_pr = editingAutoPr;
            if (editingModel) targetTask.model_name = editingModel;
            if (editingUrl) targetTask.llama_server_url = editingUrl;
            if (editingReasoningEffort) targetTask.reasoning_effort = editingReasoningEffort;
            // Update board store so TaskChat picks up the changes
            boardStore.update(b => {
                if (!b) return b;
                return { ...b, tasks: b.tasks.map(t => t.id === editingTaskId ? targetTask : t) };
            });
            updateTask(task.project_id, editingTaskId, updates).catch(err => {
                console.error('Failed to update task:', err);
            });
        }
        cancelEditTask();
    }

    function cancelEditTask() {
        editingTaskId = null;
        editingTitleDraft = '';
        editingPlanMode = false;
        editingAutoCommit = false;
        editingAutoPr = false;
        editingModel = '';
        editingUrl = '';
        editingReasoningEffort = '';
        editingProviders = [];
    }

    function handleEditModelSelect(providerName: string, modelName: string, providerUrl: string, reasoningEffort?: string) {
        editingModel = modelName;
        editingUrl = providerUrl;
        editingReasoningEffort = reasoningEffort || '';
    }

    $effect(() => {
        if (editingTaskId && editingTitleInput) {
            editingTitleInput.focus();
            editingTitleInput.select();
        }
    });

    // Status badge
    let badge = $derived.by(() => {
        const streaming = $streamingTaskIds.has(task.id);
        const state = $boardStore?.tasks.find(t => t.id === task.id)?.status_state ?? task.status_state;
        if (streaming || state === 'running') return { text: 'Running', color: 'var(--color-status-blue)' };
        if (state === 'failed') return { text: 'Failed', color: 'var(--color-status-red)' };
        if (state === 'stopped') return { text: 'Stopped', color: 'var(--color-status-yellow)' };
        return { text: 'Idle', color: 'var(--color-text-tertiary)' };
    });

    // Mobile tab
    let activeTab = $state<'context' | 'chat' | 'diff'>('chat');
    let isMobile = $derived(false);


    // Build children map for subtask hierarchy
    const childrenMap = $derived.by(() => {
        const map = new Map<string, Task[]>();
        for (const t of allTasks) {
            if (t.parent_task_id) {
                if (!map.has(t.parent_task_id)) {
                    map.set(t.parent_task_id, []);
                }
                map.get(t.parent_task_id)!.push(t);
            }
        }
        for (const [, children] of map) {
            children.sort((a, b) => a.column_order - b.column_order);
        }
        return map;
    });
    const subtaskIds = $derived(new Set(allTasks.filter(t => t.parent_task_id).map(t => t.id)));

    // Subtask IDs that should be consolidated (hidden) because any ancestor is in the same column
    const consolidatedSubtaskIds = $derived.by(() => {
        const consolidated = new Set<string>();
        const taskMap = new Map<string, Task>();
        for (const t of allTasks) taskMap.set(t.id, t);

        for (const t of allTasks) {
            if (!t.parent_task_id) continue;
            // Walk up the ancestor chain
            let parentId = t.parent_task_id;
            while (parentId) {
                const ancestor = taskMap.get(parentId);
                if (!ancestor) break;
                if (ancestor.status === t.status) {
                    consolidated.add(t.id);
                    break;
                }
                parentId = ancestor.parent_task_id ?? null;
            }
        }
        return consolidated;
    });

    // IDs of tasks that have children (potential parents)
    const parentTaskIds = $derived(new Set(allTasks.filter(t => childrenMap.has(t.id)).map(t => t.id)));

    // Initialize expanded tasks from localStorage (default: all expanded) — run once
    let expandedInitialized = $state(false);
    $effect(() => {
        if (expandedInitialized) return;
        if (parentTaskIds.size > 0) {
            expandedInitialized = true;
            const allIds = new Set(allTasks.map(t => t.id));
            cleanupCollapsedTasks(allIds);
            expandedTasksStore.set(initExpandedTasks(parentTaskIds));
        }
    });

    // Track expanded tasks (shared store for Board + CardDetail sync)
    let expandedTasks = $derived($expandedTasksStore);

    // Auto-expand parent of currently selected task (only on task change, not on updates)
    let lastAutoExpandedParent: string | null = $state(null);
    $effect(() => {
        const parentId = task.parent_task_id;
        if (parentId && parentId !== lastAutoExpandedParent) {
            lastAutoExpandedParent = parentId;
            const current = $expandedTasksStore;
            if (!current.has(parentId)) {
                const next = new Set(current);
                next.add(parentId);
                expandedTasksStore.set(next);
            }
        }
    });

    // Auto-expand parent cards when new subtasks appear
    let lastExpandedParents = $state<Set<string>>(new Set());
    $effect(() => {
        const currentParents = new Set(childrenMap.keys());
        for (const parentId of currentParents) {
            if (!lastExpandedParents.has(parentId)) {
                const current = $expandedTasksStore;
                if (!current.has(parentId)) {
                    const next = new Set(current);
                    next.add(parentId);
                    expandedTasksStore.set(next);
                }
                lastExpandedParents.add(parentId);
            }
        }
    });

    function toggleTaskExpanded(taskId: string) {
        const next = new Set($expandedTasksStore);
        if (next.has(taskId)) {
            next.delete(taskId);
        } else {
            next.add(taskId);
        }
        expandedTasksStore.set(next);
        // Persist collapsed state and clean stale entries
        const allIds = new Set(allTasks.map(t => t.id));
        persistCollapsedTasks(next, parentTaskIds);
        cleanupCollapsedTasks(allIds);
    }

    // Column resize
    let columnsRef: HTMLDivElement | null = $state(null);
    const BOARD_RATIO_KEY = 'kanban:detail:boardRatio';
    const CHAT_RATIO_KEY = 'kanban:detail:chatRatio';
    const DEFAULT_BOARD_RATIO = 0.22;  // ~220px out of ~1000px
    const DEFAULT_CHAT_RATIO = 0.55;   // 55% of remaining space
    const MIN_BOARD_RATIO = 0.12;
    const MAX_BOARD_RATIO = 0.40;
    const MIN_CHAT_RATIO = 0.15;
    const MAX_CHAT_RATIO = 0.80;

    let boardRatio = $state(loadRatio(BOARD_RATIO_KEY, DEFAULT_BOARD_RATIO));
    let chatRatio = $state(loadRatio(CHAT_RATIO_KEY, DEFAULT_CHAT_RATIO));

    function loadRatio(key: string, defaultVal: number): number {
        try {
            const stored = localStorage.getItem(key);
            if (stored !== null) {
                const val = Number.parseFloat(stored);
                if (!isNaN(val) && val > 0) return val;
            }
        } catch {}
        return defaultVal;
    }

    function saveRatio(key: string, value: number) {
        try {
            localStorage.setItem(key, String(value));
        } catch {}
    }

    function clampBoardRatio(val: number): number {
        return Math.max(MIN_BOARD_RATIO, Math.min(MAX_BOARD_RATIO, val));
    }
    function clampChatRatio(val: number): number {
        return Math.max(MIN_CHAT_RATIO, Math.min(MAX_CHAT_RATIO, val));
    }

    // Computed column widths as percentages
    let boardWidth = $derived(`${(boardRatio * 100).toFixed(1)}%`);
    let remainingWidth = $derived(`${((1 - boardRatio) * 100).toFixed(1)}%`);
    let chatWidth = $derived(`${(chatRatio * (1 - boardRatio) * 100).toFixed(1)}%`);
    let diffWidth = $derived(`${(((1 - chatRatio) * (1 - boardRatio)) * 100).toFixed(1)}%`);

    // Drag handlers for resize handles
    let boardDragStartX = $state(0);
    let boardDragStartRatio = $state(0);
    let chatDragStartX = $state(0);
    let chatDragStartRatio = $state(0);

    function onBoardDragStart(_event: MouseEvent, startX: number) {
        boardDragStartX = startX;
        boardDragStartRatio = boardRatio;
    }
    function onBoardDragMove(pointerX: number) {
        const container = columnsRef;
        if (!container) return;
        const containerWidth = Math.max(container.offsetWidth, 1);
        const delta = (pointerX - boardDragStartX) / containerWidth;
        boardRatio = clampBoardRatio(boardDragStartRatio + delta);
    }
    function onBoardDragEnd() {
        saveRatio(BOARD_RATIO_KEY, boardRatio);
    }

    function onChatDragStart(_event: MouseEvent, startX: number) {
        chatDragStartX = startX;
        chatDragStartRatio = chatRatio;
    }
    function onChatDragMove(pointerX: number) {
        const container = columnsRef;
        if (!container) return;
        // The chat column occupies (1 - boardRatio) of the container
        const remainingWidth = Math.max(container.offsetWidth * (1 - boardRatio), 1);
        const delta = (pointerX - chatDragStartX) / remainingWidth;
        chatRatio = clampChatRatio(chatDragStartRatio + delta);
    }
    function onChatDragEnd() {
        saveRatio(CHAT_RATIO_KEY, chatRatio);
    }

    $effect(() => {
        const check = () => isMobile = window.matchMedia('(max-width: 768px)').matches;
        check();
        window.addEventListener('resize', check);
        return () => window.removeEventListener('resize', check);
    });

    // Refresh diff when switching to Changes tab on mobile (WebSocket events may have been missed)
    $effect(() => {
        if (activeTab === 'diff') {
            loadDiff();
        }
    });

    // Load diff when task transitions out of backlog (worktree now exists)
    let prevStatus = $state(task.status);
    $effect(() => {
        const status = $boardStore?.tasks.find(t => t.id === task.id)?.status ?? task.status;
        if (prevStatus === 'backlog' && status && status !== 'backlog') {
            loadDiff();
        }
        prevStatus = status || '';
    });

    onMount(() => {
        const unsubBoard = boardStore.subscribe(b => {
            if (b) {
                allTasks = b.tasks;
            } else {
                allTasks = [];
            }
        });

        loadDiff();

        // Real-time diff update: apply diff from board stream broadcast
        const unsubDiff = taskDiffStore.subscribe(diffs => {
            const diffData = diffs[task.id];
            if (diffData) {
                updateDiff({ diff: diffData.diff, untracked: diffData.untracked }, true);
            }
        });

        function handleKey(e: KeyboardEvent) {
            if (e.key === 'Escape') {
                if (checkpointDiffOpen) {
                    closeCheckpointDiff();
                } else {
                    onClose();
                }
            }
        }
        window.addEventListener('keydown', handleKey);
        return () => {
            unsubBoard();
            unsubDiff();
            window.removeEventListener('keydown', handleKey);
        };
    });

    async function handleMove(status: string) {
        if (!task) return;
        try {
            const updated = await moveTask(task.project_id, task.id, status);
            task = updated;
            boardStore.update(b => {
                if (!b) return b;
                const idx = b.tasks.findIndex(t => t.id === task!.id);
                if (idx !== -1) b.tasks[idx] = updated;
                return { ...b };
            });
        } catch (e) {
            showToast('Failed to move: ' + String(e), 'error');
        }
    }

    async function handleDelete() {
        if (!task) return;
        const confirmed = await showConfirm({
            title: 'Delete Task',
            message: 'Delete this task? This cannot be undone.',
            confirmText: 'Delete',
            cancelText: 'Cancel',
        });
        if (!confirmed) return;
        const projectId = task.project_id;
        const taskId = task.id;
        try {
            await deleteTask(projectId, taskId);
            // Fallback refresh in case WebSocket is disconnected
            onRefreshBoard?.();
            onClose();
        } catch (e) {
            showToast('Failed to delete task: ' + String(e), 'error');
        }
    }

    type DiffData = { diff: string; untracked: Array<{ path: string; diff: string; file_size?: number }> };

    function updateDiff(data: DiffData, fromApi = false) {
        const hasContent = (data.diff && data.diff.trim().length > 0) || (data.untracked && data.untracked.length > 0);
        // Don't overwrite existing diff with empty data from WebSocket baseline sync
        if (!fromApi && !hasContent && (diffFiles.length > 0 || untrackedFiles.length > 0)) {
            return;
        }
        const ignoreSwp = (p: string) => !p.split('/').pop()?.match(/\..*\.swp$/);
        diffFiles = parseUnifiedDiff(data.diff).filter(f => ignoreSwp(f.path));
        untrackedFiles = (data.untracked || []).filter(u => ignoreSwp(u.path)).map(u => ({
            path: u.path,
            fileSize: u.file_size,
            file: parseUnifiedDiff(u.diff)[0] || { path: u.path, added: 0, removed: 0, rows: [] },
        }));
    }

    async function loadDiff() {
        if (!task || task.status === 'backlog') return;
        await Promise.all([loadCommits(), loadDiffForMode()]);
    }

    async function loadCommits() {
        if (!task || task.status === 'backlog') return;
        try {
            const result = await getTaskCommits(task.project_id, task.id);
            commits = result.commits || [];
            baseCommit = result.base_commit || null;
        } catch (e) {
            commits = [];
            baseCommit = null;
        }
    }

    async function loadDiffForMode() {
        if (!task || task.status === 'backlog') return;
        try {
            const headRef = diffTo === 'working' ? undefined : diffTo;
            const baseRef = diffFrom || undefined;
            const result = await getTaskDiff(task.project_id, task.id, { headRef, baseRef });
            updateDiff(result, true);
        } catch (e) {
            diffFiles = [];
            untrackedFiles = [];
        }
    }

    function getStatusBadge(t: Task): { text: string; color: string } {
        const streaming = get(streamingTaskIds).has(t.id);
        const state = $boardStore?.tasks.find(tt => tt.id === t.id)?.status_state ?? t.status_state;
        if (streaming || state === 'running') return { text: 'Running', color: 'var(--color-status-blue)' };
        if (state === 'failed') return { text: 'Failed', color: 'var(--color-status-red)' };
        if (state === 'stopped') return { text: 'Stopped', color: 'var(--color-status-yellow)' };
        return { text: '', color: 'var(--color-text-tertiary)' };
    }


</script>

    <div class="task-detail-page">
        <!-- Header -->
        <div class="header">
            <div class="header-left">
                <button class="btn-back" onclick={onClose} aria-label="Back to board (Esc)" title="Back to board (Esc)">
                    <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                        <path d="M10 3l-5 5 5 5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                    </svg>
                </button>
                <div class="header-main">
                    {#if sandboxEnabled !== undefined}
                        <span class="sandbox-badge" class:sandbox-badge-off={!sandboxEnabled} title={sandboxEnabled ? 'Sandbox enabled (gVisor)' : 'Sandbox disabled'}>
                            <img src={sandboxEnabled ? shieldIcon : shieldOffIcon} width="14" height="14" alt="" />
                        </span>
                    {/if}
                    <span class="project-prefix" title={getWorktreeDisplayPath(workspaceDir || '', task.id)}>{getWorktreeDisplayPath(workspaceDir || '', task.id)}</span>
                    <span class="badge" style="background:{badge.color}18;color:{badge.color};border-color:{badge.color}40">
                        {badge.text}
                    </span>
                </div>
            </div>
            <div class="header-actions">
                <button class="btn-icon" onclick={() => showTerminal.set(true)} disabled={!worktreeExists} aria-label="Open terminal" title="Open terminal">
                    <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                        <rect x="1" y="2" width="14" height="12" rx="1.5" stroke="currentColor" stroke-width="1.3"/>
                        <path d="M4 6l2 2-2 2M7 10h3" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                    </svg>
                </button>
                <button class="btn-icon" onclick={handleDelete} aria-label="Delete task" title="Delete task">
                    <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                        <path d="M2 4h12M5 4V3a1 1 0 011-1h4a1 1 0 011 1v1M6 7v5M10 7v5" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                        <path d="M3 4l1 9a1 1 0 001 1h6a1 1 0 001-1l1-9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                    </svg>
                </button>
            </div>
        </div>

        <!-- Mobile Tab Bar -->
        {#if isMobile}
            <div class="mobile-tab-bar">
                <button
                    class="tab-btn {activeTab === 'context' ? 'active' : ''}"
                    onclick={() => activeTab = 'context'}
                    title="Board overview"
                >
                    <svg width="12" height="12" viewBox="0 0 16 16" fill="none"><path d="M2 2h5v5H2zM9 2h5v5H9zM2 9h5v5H2zM9 9h5v5H9z" stroke="currentColor" stroke-width="1.2"/></svg>
                    Board
                </button>
                <button
                    class="tab-btn {activeTab === 'chat' ? 'active' : ''}"
                    onclick={() => activeTab = 'chat'}
                    title="Agent chat"
                >
                    <svg width="12" height="12" viewBox="0 0 16 16" fill="none"><path d="M2 3h12v8H5l-3 3V3z" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round"/></svg>
                    Agent
                </button>
                <button
                    class="tab-btn {activeTab === 'diff' ? 'active' : ''}"
                    onclick={() => activeTab = 'diff'}
                    title="Working changes"
                >
                    <svg width="12" height="12" viewBox="0 0 16 16" fill="none"><rect x="2" y="2" width="5" height="12" rx="1" stroke="currentColor" stroke-width="1.2"/><rect x="9" y="2" width="5" height="12" rx="1" stroke="currentColor" stroke-width="1.2"/><path d="M4 5h1M4 7h1M4 9h1" stroke="currentColor" stroke-width="1" stroke-linecap="round"/><path d="M11 5h1M11 7l1 1M11 9l1-1" stroke="currentColor" stroke-width="1" stroke-linecap="round" stroke-linejoin="round"/></svg>
                    Changes
                </button>
            </div>
        {/if}

        <!-- 3-Column Layout -->
        <div bind:this={columnsRef} class="columns"  role="region" aria-label="Task detail panels">
            <!-- Column 1: Board Overview -->
            <div class="column column-context" class:hidden={isMobile && activeTab !== 'context'} style:width={boardWidth}>
                <div class="task-list">
                    {#each overviewSections as section}
                        {@const sectionTasks = allTasks.filter(t => { if (t.status !== section.id) return false; if (consolidatedSubtaskIds.has(t.id)) return false; if (!subtaskIds.has(t.id)) return true; return true; }).sort((a, b) => a.column_order - b.column_order)}
                        {@const sectionState = sectionDragState.get(section.id) || { isOver: false, isRejected: false }}
                        <div
                            class="section-group"
                            class:is-over={sectionState.isOver}
                            class:is-drop-rejected={sectionState.isRejected}
                            style="--col-color: {getSectionColor(section.id)}"
                            ondragover={(e) => handleSectionDragOver(e, section.id)}
                            ondragleave={() => handleSectionDragLeave(section.id)}
                            ondrop={(e) => handleSectionDrop(e, section.id)}
                        >
                            <button class="section-header" onclick={() => toggleSection(section.id)} type="button" aria-expanded={!collapsedSections.has(section.id)} title={collapsedSections.has(section.id) ? `Expand ${section.title}` : `Collapse ${section.title}`}>
                                <span class="section-title">
                                    {#if section.id === 'backlog'}
                                        <svg class="col-icon" width="12" height="12" viewBox="0 0 10 10" fill="none" aria-hidden="true"><circle cx="5" cy="5" r="3.5" stroke="currentColor" stroke-width="1.5"/></svg>
                                    {:else if section.id === 'in_progress'}
                                        <svg class="col-icon" width="12" height="12" viewBox="0 0 10 10" fill="none" aria-hidden="true"><polygon points="3.5,1.5 3.5,8.5 8.5,5" fill="currentColor"/></svg>
                                    {:else if section.id === 'review'}
                                        <svg class="col-icon" width="12" height="12" viewBox="0 0 10 10" fill="none" aria-hidden="true"><circle cx="5" cy="5" r="3.5" stroke="currentColor" stroke-width="1"/><circle cx="5" cy="5" r="1.2" fill="currentColor"/></svg>
                                    {:else if section.id === 'done'}
                                        <svg class="col-icon" width="12" height="12" viewBox="0 0 10 10" fill="none" aria-hidden="true"><circle cx="5" cy="5" r="3.5" fill="currentColor"/></svg>
                                    {/if}
                                    {section.title}
                                </span>
                                <span class="section-count">{sectionTasks.length}</span>
                                <svg class="section-chevron" width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true"><path d="M4 6l4 4 4-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
                            </button>
                            {#if !collapsedSections.has(section.id)}
                            <div class="section-body">
                                {#each sectionTasks as t (t.id)}
                                    <Card
                                        task={t}
                                        columnId={section.id}
                                        projectId={task.project_id}
                                        childrenMap={childrenMap}
                                        expandedTasks={expandedTasks}
                                        onToggleExpanded={toggleTaskExpanded}
                                        onCreateChildTask={(parentId: string) => { creatingChildOf.set(parentId); showCreateTask.set(true); }}
                                        columnColor={getSectionColor(section.id)}
                                        inline={false}
                                        showSubtaskBorder={true}
                                        onSelect={(selected) => {
                                            if (onSelectTask) onSelectTask(selected); else selectedTask.set(selected); activeTab = 'chat';
                                        }}
                                        onRefreshBoard={onRefreshBoard}
                                    />
                                {/each}
                                {#if section.id === 'backlog'}
                                    <button class="task-item-add" onclick={() => showCreateTask.set(true)} type="button" title="Add a new task">+ Add Task</button>
                                {/if}
                            </div>
                            {/if}
                        </div>
                    {/each}
                </div>
            </div>

            <!-- Resize handle: Board ↔ Chat -->
            {#if !isMobile}
                <ResizeHandle
                    ariaLabel="Resize board and agent panels"
                    onDragStart={onBoardDragStart}
                    onDragMove={onBoardDragMove}
                    onDragEnd={onBoardDragEnd}
                />
            {/if}

            <!-- Column 2: Chat / Agent Interface -->
            <div class="column column-chat" class:hidden={isMobile && activeTab !== 'chat'} style:width={chatWidth}>
                <div style="flex:1;min-height:0">
                    {#key task.id}
                        <TaskChat
                            projectId={task.project_id}
                            taskId={task.id}
                            taskPrompt={task.prompt}
                            taskStatus={task.status}
                            taskTitle={task.title}
                            onRefreshDiff={updateDiff}
                            onMoveTask={handleMove}
                            onThreadSwitch={() => loadDiff()}
                            onViewChanges={handleViewChanges}
                        />
                    {/key}
                </div>
            </div>

            <!-- Resize handle: Chat ↔ Diff -->
            {#if !isMobile}
                <ResizeHandle
                    ariaLabel="Resize agent and changes panels"
                    onDragStart={onChatDragStart}
                    onDragMove={onChatDragMove}
                    onDragEnd={onChatDragEnd}
                />
            {/if}

            <!-- Column 3: Working Changes (Diff) -->
            <div class="column column-diff" class:hidden={isMobile && activeTab !== 'diff'} style:width={diffWidth}>
                <div class="diff-content">
                    {#if worktreeExists || task.branch}
                        <div class="diff-header">
                            <DiffSelect options={fromOptions} value={diffFrom} onChange={onDiffFromChange} ariaLabel="From" />
                            <span class="diff-arrow">→</span>
                            <DiffSelect options={toOptions} value={diffTo} onChange={onDiffToChange} ariaLabel="To" />
                        </div>
                    {/if}
                    <DiffViewer files={diffFiles} untracked={untrackedFiles} />

                    <!-- Checkpoint diff overlay -->
                    {#if checkpointDiffOpen}
                        <div class="checkpoint-diff-overlay">
                            <div class="checkpoint-diff-header">
                                <div class="checkpoint-diff-title">
                                    <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
                                        <path d="M19 21l-7-5-7 5V5a2 2 0 0 1 2-2h10a2 2 0 0 1 2 2z"/>
                                    </svg>
                                    <span>Changes before this checkpoint</span>
                                </div>
                                <button class="btn-close-checkpoint" onclick={closeCheckpointDiff} title="Close (Esc)" aria-label="Close">
                                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round">
                                        <path d="M18 6L6 18M6 6l12 12"/>
                                    </svg>
                                </button>
                            </div>
                            {#if checkpointDiffLoading}
                                <div class="checkpoint-diff-loading">
                                    <svg class="checkpoint-spinner" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5">
                                        <path d="M12 2a10 10 0 0 1 10 10"/>
                                    </svg>
                                </div>
                            {:else}
                                <DiffViewer files={checkpointDiffFiles} untracked={checkpointDiffUntracked} />
                            {/if}
                        </div>
                    {/if}
                </div>
            </div>
        </div>
    </div>

<style>
    .task-detail-page {
        flex: 1;
        display: flex;
        flex-direction: column;
        overflow: hidden;
        height: 100%;
    }
    .header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 8px 16px;
        border-bottom: 1px solid var(--color-border);
        flex-shrink: 0;
        gap: 12px;
        background: var(--color-surface-1);
    }
    .header-left {
        display: flex;
        align-items: center;
        gap: 10px;
        min-width: 0;
        flex: 1;
    }
    .btn-back {
        background: none;
        border: none;
        color: var(--color-text-secondary);
        cursor: pointer;
        padding: 6px;
        border-radius: var(--radius-sm);
        display: flex;
        align-items: center;
        justify-content: center;
        flex-shrink: 0;
    }
    .btn-back:hover { background: var(--color-surface-3); color: var(--color-text-primary); }
    .header-main {
        display: flex;
        align-items: center;
        gap: 10px;
        min-width: 0;
        flex: 1;
    }
    .project-prefix {
        font-size: 0.875rem;
        font-weight: 400;
        color: var(--color-text-tertiary);
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
        flex: 1 1 0;
        min-width: 0;
    }
    .badge {
        padding: 2px 8px;
        border-radius: 10px;
        font-size: 0.75rem;
        font-weight: 600;
        flex-shrink: 0;
        border: 1px solid;
        text-transform: uppercase;
        letter-spacing: 0.3px;
    }
    .sandbox-badge {
        flex-shrink: 0;
        color: var(--color-primary);
        display: flex;
        align-items: center;
        height: 1em;
    }
    .sandbox-badge-off {
        color: var(--color-status-red, #f85149);
    }
    .header-actions {
        display: flex;
        align-items: center;
        gap: 2px;
    }
    .btn-icon,
    .btn-icon-sm {
        background: none;
        border: none;
        color: var(--color-text-secondary);
        cursor: pointer;
        border-radius: var(--radius-sm);
        display: flex;
        align-items: center;
        justify-content: center;
    }
    .btn-icon { padding: 6px; }
    .btn-icon-sm { padding: 2px; }
    .btn-icon:hover { background: var(--color-surface-3); color: var(--color-text-primary); }
    .btn-icon:disabled { opacity: 0.35; cursor: not-allowed; }
    .btn-icon:disabled:hover { background: none; color: var(--color-text-secondary); }
    .columns {
        display: flex;
        flex: 1;
        min-height: 0;
        overflow: hidden;
    }
    .column {
        display: flex;
        flex-direction: column;
        min-height: 0;
        overflow: hidden;
    }
    .column-context {
        display: block;
        overflow: auto;
        flex-shrink: 0;
        border-right: 1px solid var(--color-border);
        background: var(--color-surface-0);
    }
    .task-list {
        flex: 1;
        overflow-y: auto;
        padding: 8px 6px 6px 6px;
        display: flex;
        flex-direction: column;
        gap: 8px;
    }
    .section-group {
        background: var(--color-surface-1);
        border-radius: var(--radius-lg);
        overflow: hidden;
        transition: background 0.15s ease;
    }
    .section-group.is-over {
        background: var(--color-surface-2);
    }
    .section-group.is-drop-rejected {
        outline: 2px dashed var(--color-status-red);
        outline-offset: -2px;
        cursor: no-drop;
    }
    .section-header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 10px 12px;
        cursor: pointer;
        background: transparent;
        border: none;
        border-bottom: 2px solid var(--col-color);
        width: 100%;
        text-align: left;
        font: inherit;
        color: inherit;
    }
    .section-header:hover {
        background: var(--color-surface-2);
    }
    .section-title {
        display: flex;
        align-items: center;
        gap: 6px;
        font-size: 0.8rem;
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.4px;
        color: var(--color-text-secondary);
        flex: 1;
        min-width: 0;
        overflow: hidden;
        text-overflow: ellipsis;
    }
    .section-title .col-icon {
        flex-shrink: 0;
        color: var(--col-color);
    }
    .section-count {
        background: var(--color-surface-3);
        border-radius: 10px;
        padding: 2px 8px;
        font-size: 0.75rem;
        color: var(--color-text-secondary);
        flex-shrink: 0;
        margin-left: 8px;
    }
    .section-chevron {
        flex-shrink: 0;
        margin-left: 4px;
        color: var(--color-text-tertiary);
        transition: transform 0.2s ease;
    }
    .section-header[aria-expanded="false"] .section-chevron {
        transform: rotate(-90deg);
    }
    .section-body {
        padding: 8px 6px 6px 6px;
        display: flex;
        flex-direction: column;
        gap: 8px;
    }
    .task-item-add {
        width: 100%;
        padding: 6px 8px;
        border-radius: var(--radius-sm);
        background: var(--color-surface-3);
        font-size: 0.75rem;
        color: var(--color-text-secondary);
        cursor: pointer;
        transition: background 0.15s ease, color 0.15s ease;
        border: none;
        font-family: inherit;
    }
    .task-item-add:hover {
        background: var(--color-accent);
        color: #fff;
    }
    .task-item {
        padding: 6px 8px;
        border-radius: var(--radius-sm);
        cursor: pointer;
        display: flex;
        justify-content: space-between;
        align-items: center;
        gap: 8px;
        transition: background 0.1s ease;
        text-align: left;
        width: 100%;
        font: inherit;
        color: inherit;
        background: var(--color-surface-2);
        border: none;
        box-sizing: border-box;
    }
    .task-item:hover { background: var(--color-surface-3); }
    .task-item.selected {
        background: var(--color-accent)18;
        outline: 1px solid var(--color-accent);
        outline-offset: 1px;
        color: var(--color-accent);
        font-weight: 600;
    }
    .task-item-title {
        font-size: 0.75rem;
        color: var(--color-text-primary);
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
        flex: 1;
        min-width: 0;
    }
    .task-item.selected .task-item-title {
        color: var(--color-accent);
    }
    .task-item-edit-btn {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 28px;
        height: 28px;
        padding: 0;
        background: transparent;
        border: none;
        border-radius: var(--radius-sm);
        color: var(--color-text-tertiary);
        cursor: pointer;
        opacity: 0;
        transition: opacity 0.15s ease, background 0.15s ease;
        flex-shrink: 0;
    }
    .task-item:hover .task-item-edit-btn,
    .task-item.selected .task-item-edit-btn {
        opacity: 1;
    }
    .task-item-edit-btn:hover {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }
    @media (hover: none) {
        .task-item-edit-btn {
            opacity: 1;
            transition: background 0.15s ease;
        }
    }
    .task-item-editing {
        padding: 4px 6px;
    }
    .task-item-title-input {
        width: 100%;
        font-size: 0.75rem;
        font-weight: 600;
        color: var(--color-text-primary);
        background: var(--color-surface-1);
        border: 1px solid var(--color-accent);
        border-radius: var(--radius-sm);
        padding: 3px 6px;
        font-family: inherit;
        outline: none;
    }
    .task-item-editing-panel {
        background: var(--color-surface-2);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-md);
        overflow: hidden;
    }
    .task-item-editing-header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 10px 12px;
        border-bottom: 1px solid var(--color-border);
    }
    .task-item-editing-title {
        font-size: 0.8125rem;
        font-weight: 600;
        color: var(--color-text-primary);
    }
    .task-item-editing-close {
        font-size: 1rem;
        color: var(--color-text-secondary);
        background: none;
        border: none;
        cursor: pointer;
        padding: 2px 4px;
        line-height: 1;
    }
    .task-item-editing-close:hover {
        color: var(--color-text-primary);
    }
    .task-item-editing-body {
        padding: 12px;
        display: flex;
        flex-direction: column;
        gap: 10px;
    }
    .task-item-editing-field {
        display: flex;
        flex-direction: column;
        gap: 4px;
    }
    .task-item-editing-label {
        font-size: 0.8125rem;
        font-weight: 500;
        color: var(--color-text-secondary);
    }
    .task-item-editing-input {
        width: 100%;
        font-size: 0.8125rem;
        color: var(--color-text-primary);
        background: var(--color-surface-3);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        padding: 6px 8px;
        font-family: inherit;
        outline: none;
    }
    .task-item-editing-input:focus {
        border-color: var(--color-accent);
    }
    .task-item-editing-model-row {
        display: flex;
        align-items: center;
        flex-wrap: wrap;
        gap: 8px;
    }
    .task-item-editing-options {
        display: flex;
        flex-direction: column;
        gap: 8px;
    }
    .task-item-editing-checkbox {
        display: flex;
        align-items: flex-start;
        gap: 8px;
        font-size: 0.8125rem;
        color: var(--color-text-secondary);
        cursor: pointer;
    }
    .task-item-editing-checkbox.disabled {
        opacity: 0.5;
        pointer-events: none;
    }
    .task-item-editing-checkbox input[type="checkbox"] {
        accent-color: var(--color-accent);
        margin-top: 1px;
        cursor: pointer;
        flex-shrink: 0;
    }
    .task-item-editing-option-text {
        display: flex;
        flex-direction: column;
        align-items: flex-start;
        gap: 2px;
    }
    .task-item-editing-option-title {
        font-weight: 600;
        font-size: 0.8125rem;
    }
    .task-item-editing-option-desc {
        font-size: 0.75rem;
        color: var(--color-text-tertiary);
        font-weight: 400;
    }
    .task-item-editing-footer {
        display: flex;
        justify-content: flex-end;
        gap: 8px;
        padding: 10px 12px;
        border-top: 1px solid var(--color-border);
        background: var(--color-surface-1);
    }
    .task-item-editing-btn-cancel {
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
    .task-item-editing-btn-cancel:hover {
        background: var(--color-surface-4);
    }
    .task-item-editing-btn-save {
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
    .task-item-editing-btn-save:hover {
        background: var(--color-accent-hover);
    }
    .task-item-badge {
        font-size: 0.75rem;
        font-weight: 600;
        text-transform: uppercase;
        flex-shrink: 0;
    }
    .column-chat {
        min-width: 0;
        border-right: 1px solid var(--color-border);
    }
    .column-diff {
        min-width: 0;
    }
    .btn-icon-sm:hover { background: var(--color-surface-3); color: var(--color-text-primary); }
    .btn-icon-sm:disabled { opacity: 0.4; cursor: not-allowed; }
    .diff-content {
        flex: 1;
        display: flex;
        flex-direction: column;
        min-height: 0;
        overflow: hidden;
        position: relative;
    }
    .diff-header {
        display: flex;
        align-items: center;
        gap: 6px;
        padding: 6px 8px;
        border-bottom: 1px solid var(--color-border);
        background: var(--color-surface-1);
        flex-shrink: 0;
    }
    .diff-arrow {
        font-size: 0.875rem;
        color: var(--color-text-tertiary);
        padding: 0 2px;
        user-select: none;
        flex-shrink: 0;
    }
    .mobile-tab-bar {
        display: none;
    }
    .column.hidden {
        display: none;
    }

    @media (max-width: 768px) {
        .mobile-tab-bar {
            display: flex;
            border-bottom: 1px solid var(--color-border);
            background: var(--color-surface-1);
            flex-shrink: 0;
        }
        .tab-btn {
            flex: 1;
            display: flex;
            align-items: center;
            justify-content: center;
            gap: 4px;
            padding: 10px 8px;
            border: none;
            background: none;
            color: var(--color-text-tertiary);
            font-size: 0.75rem;
            font-weight: 500;
            cursor: pointer;
            border-bottom: 2px solid transparent;
            transition: all 0.15s ease;
        }
        .tab-btn.active {
            color: var(--color-accent);
            border-bottom-color: var(--color-accent);
        }
        .tab-btn:hover:not(.active) {
            color: var(--color-text-primary);
        }
        .column-context {
            border-right: none;
        }
        .column-chat {
            border-right: none;
        }
        .column:not(.hidden) {
            flex: 0 0 100% !important;
            min-width: 100% !important;
            width: 100% !important;
        }
        .task-list {
            padding: 0 8px 8px 8px;
        }
        .section-header {
            padding: 12px 10px;
        }
        .section-body {
            padding: 6px 8px 8px 8px;
        }
        .task-item {
            padding: 10px 12px;
            min-height: 44px;
            align-items: center;
        }
        .task-item-title {
            font-size: 0.875rem;
        }
    }

    /* Checkpoint diff overlay */
    .checkpoint-diff-overlay {
        position: absolute;
        top: 0;
        left: 0;
        right: 0;
        bottom: 0;
        background: var(--color-surface-1);
        display: flex;
        flex-direction: column;
        z-index: 50;
        animation: overlayFadeIn 150ms ease-out;
    }
    @keyframes overlayFadeIn {
        from { opacity: 0; }
        to { opacity: 1; }
    }
    .checkpoint-diff-header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 8px 12px;
        border-bottom: 1px solid var(--color-border);
        background: var(--color-surface-2);
        flex-shrink: 0;
    }
    .checkpoint-diff-title {
        display: flex;
        align-items: center;
        gap: 6px;
        font-size: 0.8125rem;
        font-weight: 600;
        color: white;
    }
    .checkpoint-diff-title svg {
        opacity: 0.7;
    }
    .btn-close-checkpoint {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 24px;
        height: 24px;
        border-radius: var(--radius-sm);
        background: none;
        border: none;
        color: var(--color-text-secondary);
        cursor: pointer;
        transition: all 0.15s ease;
    }
    .btn-close-checkpoint:hover {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }
    .checkpoint-diff-loading {
        flex: 1;
        display: flex;
        align-items: center;
        justify-content: center;
        color: var(--color-text-tertiary);
    }
    .checkpoint-spinner {
        animation: checkpoint-spin 1s linear infinite;
    }
    @keyframes checkpoint-spin {
        from { transform: rotate(0deg); }
        to { transform: rotate(360deg); }
    }
</style>
