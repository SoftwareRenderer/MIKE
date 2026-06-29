<script lang="ts">
    import { onMount } from 'svelte';
    import { get } from 'svelte/store';
    import type { Task, Provider, TaskCommit } from '$lib/types';
    import type { DiffFile } from '$lib/types/diff';
    import { board as boardStore, config as configStore, selectedTask, showTerminal, showConfirm, showToast, showCreateTask, creatingChildOf, persistCollapsedTasks, cleanupCollapsedTasks, initExpandedTasks } from '$lib/store';
    import { getStreamingContext, getTaskDiffStatsContext, getTaskDiffContext, getExpandedTasksContext } from '$lib/context';
    import { moveTask, deleteTask, getTaskDiff, getTaskCommits, getDiffFile, updateTask, clearDoneTasks } from '$lib/api';
    import { parseUnifiedDiff } from '$lib/utils/diff-parser';
    import { loadCollapsedSections, saveCollapsedSections } from '$lib/storage';
    import ModelSelector from './Chat/ModelSelector.svelte';
    import TaskChat from './Chat/TaskChat.svelte';
    import ResizeHandle from './ResizeHandle.svelte';
    import DiffViewer from './DiffViewer.svelte';
    import DiffSelect from './DiffSelect.svelte';
    // Lazy-loaded on first file edit
    let FileEditorComponent: typeof import('./FileEditor.svelte').default | null = $state(null);
    let fileEditorLoading = $state(false);
    function loadFileEditor(): void {
        if (FileEditorComponent || fileEditorLoading) return;
        fileEditorLoading = true;
        import('./FileEditor.svelte').then((mod) => {
            FileEditorComponent = mod.default;
            fileEditorLoading = false;
        }).catch(() => { fileEditorLoading = false; });
    }
    import Card from './Card.svelte';
    import MobileTabBar from './CardDetail/MobileTabBar.svelte';
    import TaskHeader from './CardDetail/TaskHeader.svelte';
    import BoardOverview from './CardDetail/BoardOverview.svelte';

    let { task, tasks: columnTasks, projectPath, workspaceDir, sandboxEnabled, onClose, onSelectTask, onRefreshBoard }: { task: Task; tasks: Task[]; projectPath?: string; workspaceDir?: string; sandboxEnabled?: boolean; onClose: () => void; onSelectTask?: (task: Task) => void; onRefreshBoard?: () => void } = $props();

    let allTasks: Task[] = $state([]);

    const overviewSections = [
        { id: 'backlog', title: 'Backlog' },
        { id: 'in_progress', title: 'In Progress' },
        { id: 'review', title: 'Review' },
        { id: 'done', title: 'Done' },
    ];

    // Collapsible sections
    let collapsedSections: Set<string> = $state(loadCollapsedSections());

    function toggleSection(sectionId: string) {
        const next = new Set(collapsedSections);
        if (next.has(sectionId)) {
            next.delete(sectionId);
        } else {
            next.add(sectionId);
        }
        collapsedSections = next;
        saveCollapsedSections(next);
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
        if (!e.dataTransfer) return;
        const taskId = e.dataTransfer.getData('text/plain');
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

    async function handleClearDone(count: number) {
        if (!task.project_id) return;
        const confirmed = await showConfirm({
            title: 'Clear Done Tasks',
            message: `Permanently delete ${count} done task${count > 1 ? 's' : ''}? This cannot be undone.`,
            confirmText: 'Delete',
            cancelText: 'Cancel',
        });
        if (!confirmed) return;
        try {
            await clearDoneTasks(task.project_id);
            // Board stream WebSocket will push the update
        } catch (e) {
            showToast('Failed to clear done tasks: ' + String(e), 'error');
        }
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

    // Diff state — per-file cache for incremental updates
    let diffFileCache = $state<Record<string, DiffFile>>({});       // tracked files: path → DiffFile
    let untrackedCache = $state<Record<string, { fileSize?: number; truncated?: boolean; origAdded?: number; loading?: boolean }>>({}); // untracked: path → metadata
    // Derived arrays for DiffViewer (sorted)
    let diffFiles = $derived(Object.values(diffFileCache).filter(f => !untrackedCache[f.path]));
    let untrackedFiles = $derived(Object.entries(untrackedCache).map(([path, meta]) => ({
        path,
        ...meta,
        file: diffFileCache[path] || { path, rows: [], added: 0, removed: 0 },
    })));
    // Terminal - worktree exists when task has a branch (set on start, cleared on delete)
    let worktreeExists = $derived(!!task.branch);

    // Commit history for diff comparison
    let commits: TaskCommit[] = $state([]);
    let baseCommit: TaskCommit | null = $state(null);

    // Checkpoint diff overlay
    let checkpointDiffOpen = $state(false);
    let checkpointDiffHash = $state(''); // which checkpoint hash is currently shown
    let checkpointDiffFiles: DiffFile[] = $state([]);
    let checkpointDiffUntracked: { path: string; fileSize?: number; file: DiffFile; truncated?: boolean; origAdded?: number }[] = $state([]);
    let checkpointDiffLoading = $state(false);
    let checkpointDiffLabel = $state('');
    // Two-dropdown diff: '' means base ref (original branch), 'working' means working tree
    let diffFrom = $state('');
    let diffTo = $state('working');

    // File editor overlay
    let editingFile = $state<{ path: string; lineNumber: number; displayPath: string } | null>(null);

    function resolveFilePath(relativePath: string): string | null {
        if (!workspaceDir || !task.id) return null;
        if (sandboxEnabled) {
            return `/sandbox/${task.id}/${relativePath}`;
        }
        return `${workspaceDir.replace(/\/+$/, '')}/${task.id}/${relativePath}`;
    }

    function handleDiffLineClick(relativePath: string, lineNumber: number) {
        const fullPath = resolveFilePath(relativePath);
        if (!fullPath) return;
        loadFileEditor();
        editingFile = { path: fullPath, lineNumber, displayPath: relativePath };
    }

    function closeFileEditor(saved: boolean) {
        editingFile = null;
    }

    /** Build a DiffFile from raw file lines (untracked/new files, all additions).
     * When lines is null, uses lineCount to show a placeholder (content loaded on-demand). */
    function buildUntrackedDiffFile(path: string, lines: string[] | null | undefined, lineCount?: number): DiffFile {
        if (!lines || lines.length === 0) {
            const count = lineCount || 0;
            return { path, added: count, removed: 0, rows: [] };
        }
        const rows = lines.map((text, i) => ({
            lineNumber: i + 1,
            variant: 'added' as const,
            text,
        }));
        return { path, added: lines.length, removed: 0, rows };
    }

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
            const truncatedSet = new Set(result.truncated_files || []);
            const origStats = result.original_file_stats || {};
            checkpointDiffFiles = parseUnifiedDiff(result.diff).filter(f => !f.path.split('/').pop()?.match(/\..*\.swp$/)).map(f => ({
                ...f,
                truncated: truncatedSet.has(f.path),
                origAdded: origStats[f.path]?.added,
                origRemoved: origStats[f.path]?.removed,
            }));
            checkpointDiffUntracked = (result.untracked || []).filter(u => !u.path.split('/').pop()?.match(/\..*\.swp$/)).map(u => ({
                path: u.path,
                fileSize: u.file_size,
                truncated: u.truncated,
                origAdded: u.orig_added,
                file: buildUntrackedDiffFile(u.path, u.lines, u.line_count),
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
    let editingMergeOnCommit = $state(true);
    let editingMergeChildren = $state(true);
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
        editingMergeOnCommit = t?.merge_on_commit ?? true;
        editingMergeChildren = t?.merge_children ?? true;
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
        updates.auto_commit = editingMergeOnCommit; // auto-commit is prerequisite for auto-merge
        updates.merge_on_commit = editingMergeOnCommit;
        updates.merge_children = editingMergeChildren;
        if (editingModel) updates.model_name = editingModel;
        if (editingUrl) updates.llama_server_url = editingUrl;
        if (editingReasoningEffort) updates.reasoning_effort = editingReasoningEffort;
        if (targetTask) {
            targetTask.title = trimmed;
            targetTask.plan_mode = editingPlanMode;
            targetTask.auto_commit = editingAutoCommit;
            targetTask.merge_on_commit = editingMergeOnCommit;
            targetTask.merge_children = editingMergeChildren;
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
        editingMergeOnCommit = true;
        editingMergeChildren = true;
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

    // Context stores
    let streamingIds = getStreamingContext();
    let expandedTasksContext = getExpandedTasksContext();
    let taskDiffStats = getTaskDiffStatsContext();
    // Sync store to reactive state for $effect tracking
    let diffStatsMap = $state<Record<string, { hash: string; files: number; additions: number; deletions: number; file_hashes?: Record<string, string>; removed_files?: string[] }>>({});

    $effect(() => {
        if (editingTaskId && editingTitleInput) {
            const input = editingTitleInput as HTMLInputElement;
            input.focus();
            input.select();
        }
    });

    // Reload diff when task changes (switching tasks via task view keeps CardDetail mounted)
    let previousTaskId = $state<string | null>(null);
    $effect(() => {
        const taskId = task.id;
        if (previousTaskId && taskId !== previousTaskId) {
            // Task changed — reload diff and reset state
            diffTracker.hash = null;
            diffFileCache = {};
            untrackedCache = {};
            diffFileHashes = {};
            commits = [];
            baseCommit = null;
            diffFrom = '';
            diffTo = 'working';
            loadDiff();
        }
        previousTaskId = taskId;
    });



    // Status badge
    let badge = $derived.by(() => {
        const streaming = get(streamingIds).has(task.id);
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
            let parentId: string | undefined = t.parent_task_id;
            while (parentId) {
                const ancestor = taskMap.get(parentId);
                if (!ancestor) break;
                if (ancestor.status === t.status) {
                    consolidated.add(t.id);
                    break;
                }
                parentId = ancestor.parent_task_id ?? undefined;
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
            expandedTasksContext.set(initExpandedTasks(parentTaskIds));
        }
    });

    // Track expanded tasks (shared store for Board + CardDetail sync)
    let expandedTasks = $state<Set<string>>(new Set());
    $effect(() => {
        expandedTasksContext.subscribe(v => expandedTasks = v);
    });

    // Auto-expand parent of currently selected task (only on task change, not on updates)
    let lastAutoExpandedParent: string | null = $state(null);
    $effect(() => {
        const parentId = task.parent_task_id;
        if (parentId && parentId !== lastAutoExpandedParent) {
            lastAutoExpandedParent = parentId;
            const current = expandedTasks;
            if (!current.has(parentId)) {
                const next = new Set(current);
                next.add(parentId);
                expandedTasksContext.set(next);
            }
        }
    });

    // Auto-expand parent cards when new subtasks appear
    let lastExpandedParents = $state<Set<string>>(new Set());
    $effect(() => {
        const currentParents = new Set(childrenMap.keys());
        for (const parentId of currentParents) {
            if (!lastExpandedParents.has(parentId)) {
                const current = expandedTasks;
                if (!current.has(parentId)) {
                    const next = new Set(current);
                    next.add(parentId);
                    expandedTasksContext.set(next);
                }
                lastExpandedParents.add(parentId);
            }
        }
    });

    function toggleTaskExpanded(taskId: string) {
        const next = new Set(expandedTasks);
        if (next.has(taskId)) {
            next.delete(taskId);
        } else {
            next.add(taskId);
        }
        expandedTasksContext.set(next);
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
    let prevTab = $state(activeTab);
    $effect(() => {
        const currentTab = activeTab;
        if (currentTab !== prevTab && currentTab === 'diff') {
            loadDiff();
        }
        prevTab = currentTab;
    });

    // Load diff when task transitions out of backlog (worktree now exists)
    let prevStatus = $state(task.status);
    $effect(() => {
        const currentStatus = task.status;
        const status = $boardStore?.tasks.find(t => t.id === task.id)?.status ?? currentStatus;
        if (prevStatus === 'backlog' && status && status !== 'backlog') {
            loadDiff();
        }
        prevStatus = status || '';
    });



    // WebSocket diff data (pushed via task_diff events)
    let wsDiffMap = $state<Record<string, { hash: string; diff?: string; untracked?: any[]; files?: Record<string, { hash: string; type: string }>; updated_at: number }>>({});

    onMount(() => {
        const unsubBoard = boardStore.subscribe(b => {
            if (b) {
                allTasks = b.tasks;
            } else {
                allTasks = [];
            }
        });
        const unsubDiff = taskDiffStats.subscribe(v => { diffStatsMap = v; });
        const unsubWsDiff = getTaskDiffContext().subscribe(v => { wsDiffMap = v; });

        loadDiff();

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
            unsubWsDiff();
            window.removeEventListener('keydown', handleKey);
        };
    });

    // Real-time diff update: re-fetch when the diff hash changes.
    // Skip HTTP fetch if WebSocket already pushed matching diff data.
    // Plain object (not $state) to avoid re-triggering $effect on writes
    let diffTracker = { hash: null as string | null };
    // Per-file hash tracker for incremental updates
    let diffFileHashes = $state<Record<string, string>>({});
    $effect(() => {
        const stat = diffStatsMap[task.id];
        const currentHash = stat?.hash || null;
        if (currentHash !== diffTracker.hash) {
            diffTracker.hash = currentHash;
            const fileHashes = stat?.file_hashes;
            const removedFiles = stat?.removed_files || [];
            if (fileHashes) {
                // Delta update: file_hashes only contains changed/new files
                // Remove deleted files from cache
                if (removedFiles.length > 0) {
                    const newDiff = { ...diffFileCache };
                    const newUntracked = { ...untrackedCache };
                    const newHashes = { ...diffFileHashes };
                    for (const path of removedFiles) {
                        delete newDiff[path];
                        delete newUntracked[path];
                        delete newHashes[path];
                    }
                    diffFileCache = newDiff;
                    untrackedCache = newUntracked;
                    diffFileHashes = newHashes;
                }
                // Initial load: fetch full manifest + files
                if (Object.keys(diffFileCache).length === 0) {
                    diffFileHashes = { ...diffFileHashes, ...fileHashes };
                    loadDiff();
                    return;
                }
                // Delta: fetch only changed files directly (skip manifest)
                if (Object.keys(fileHashes).length > 0) {
                    const prevHashes = { ...diffFileHashes };
                    diffFileHashes = { ...diffFileHashes, ...fileHashes };
                    fetchDeltaFiles(fileHashes, prevHashes);
                }
                return;
            }
            // Check if WebSocket already pushed this diff
            const wsDiff = wsDiffMap[task.id];
            if (wsDiff && wsDiff.hash === currentHash) {
                // WebSocket data is fresh — apply it directly, skip HTTP
                updateDiff({ diff: wsDiff.diff, untracked: wsDiff.untracked, files: wsDiff.files }, 'websocket');
                // Fetch changed files if manifest format
                if (wsDiff.files) {
                    fetchChangedFiles(wsDiff.files);
                }
                return;
            }
            loadDiff();
        }
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

    type DiffData = { diff?: string; untracked?: Array<{ path: string; lines?: string[]; line_count?: number; file_size?: number; truncated?: boolean; orig_added?: number }>; truncated_files?: string[]; original_file_stats?: Record<string, { added: number; removed: number }>; files?: Record<string, { hash: string; type: string; added?: number; removed?: number; truncated?: boolean; origAdded?: number; origRemoved?: number; line_count?: number; file_size?: number }> };

    function updateDiff(data: DiffData, source: 'http' | 'http-force' | 'websocket' = 'http') {
        const ignoreSwp = (p: string) => !p.split('/').pop()?.match(/\..*\.swp$/);

        // New manifest format: per-file entries
        if (data.files && Object.keys(data.files).length > 0) {
            const newCache: Record<string, DiffFile> = {};
            const newUntracked: Record<string, { fileSize?: number; truncated?: boolean; origAdded?: number; loading?: boolean }> = {};
            for (const [path, entry] of Object.entries(data.files)) {
                if (!ignoreSwp(path) && entry.type === 'untracked') continue;
                if (entry.type === 'tracked') {
                    // Keep existing rows if cached (hash unchanged), update metadata
                    const existing = diffFileCache[path];
                    newCache[path] = {
                        path,
                        rows: existing?.rows || [],
                        added: entry.added ?? 0,
                        removed: entry.removed ?? 0,
                        truncated: !!entry.truncated,
                        origAdded: entry.origAdded,
                        origRemoved: entry.origRemoved,
                    };
                } else if (entry.type === 'untracked') {
                    const existing = diffFileCache[path];
                    newUntracked[path] = { fileSize: entry.file_size, truncated: false, origAdded: entry.line_count, loading: false };
                    newCache[path] = {
                        path,
                        rows: existing?.rows || [],
                        added: entry.line_count ?? 0,
                        removed: 0,
                    };
                }
            }
            diffFileCache = newCache;
            untrackedCache = newUntracked;
            // Store per-file hashes for incremental comparison
            const newHashes: Record<string, string> = {};
            for (const [path, entry] of Object.entries(data.files!)) {
                newHashes[path] = entry.hash;
            }
            diffFileHashes = newHashes;
            return;
        }

        // Legacy format: monolithic diff string + untracked array
        // If cache already has data (from manifest), skip empty legacy updates
        if (!data.diff && !data.untracked && (Object.keys(diffFileCache).length > 0 || Object.keys(untrackedCache).length > 0)) {
            return; // keep existing manifest data
        }
        const truncatedSet = new Set(data.truncated_files || []);
        const origStats = data.original_file_stats || {};
        const newCache: Record<string, DiffFile> = {};
        const newUntracked: Record<string, { fileSize?: number; truncated?: boolean; origAdded?: number; loading?: boolean }> = {};

        parseUnifiedDiff(data.diff || '').filter(f => ignoreSwp(f.path)).forEach(f => {
            const isTruncated = truncatedSet.has(f.path);
            const stat = origStats[f.path];
            newCache[f.path] = {
                ...f,
                truncated: isTruncated,
                origAdded: stat?.added,
                origRemoved: stat?.removed,
            };
        });

        (data.untracked || []).filter(u => ignoreSwp(u.path)).forEach(u => {
            newUntracked[u.path] = { fileSize: u.file_size, truncated: u.truncated, origAdded: u.orig_added, loading: false };
            newCache[u.path] = buildUntrackedDiffFile(u.path, u.lines, u.line_count);
        });

        diffFileCache = newCache;
        untrackedCache = newUntracked;
    }

    let diffLoading = $state(false);

    async function loadDiff() {
        if (!task || task.status === 'backlog' || diffLoading) return;
        diffLoading = true;
        try {
            await Promise.all([loadCommits(), loadDiffForMode()]);
        } finally {
            diffLoading = false;
            // Sync tracker with current store value so a pending WebSocket
            // diffStats event doesn't trigger a redundant re-fetch.
            const stat = diffStatsMap[task.id];
            diffTracker.hash = stat?.hash || null;
        }
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
            const isDefaultMode = !headRef && !baseRef;
            updateDiff(result, isDefaultMode ? 'http' : 'http-force');
            // For manifest format, fetch individual files
            if (result.files && isDefaultMode) {
                await fetchChangedFiles(result.files);
            }
        } catch (e) {
            diffFileCache = {};
            untrackedCache = {};
            diffFileHashes = {};
        }
    }

    /** Fetch individual files whose hash changed (or are new). */
    async function fetchChangedFiles(manifest: Record<string, { hash: string; type: string; added?: number; removed?: number; line_count?: number }>) {
        if (!task) return;
        const updatedDiff: Record<string, DiffFile> = {};
        const updatedUntracked: Record<string, boolean> = {};
        const fetches: Promise<void>[] = [];
        for (const [path, entry] of Object.entries(manifest)) {
            const localHash = diffFileHashes[path];
            const remoteHash = entry.hash;
            // Skip if hash matches (file unchanged) and we already have content
            if (localHash === remoteHash && diffFileCache[path]?.rows.length) continue;
            // Skip blank untracked files (no content to fetch)
            if (entry.type === 'untracked' && (entry.line_count ?? 0) === 0) continue;
            // Fetch this file
            fetches.push((async () => {
                try {
                    const data = await getDiffFile(task.project_id, task.id, path);
                    if (data.rows) {
                        updatedDiff[path] = { path, rows: data.rows, added: entry.added ?? 0, removed: entry.removed ?? 0, truncated: data.truncated, origAdded: data.origAdded, origRemoved: data.origRemoved };
                    } else if (data.lines) {
                        updatedDiff[path] = { path, rows: data.lines.map((line, i) => ({ lineNumber: i + 1, variant: 'added' as const, text: line })), added: data.lines.length, removed: 0 };
                        updatedUntracked[path] = true;
                    }
                } catch (e) {
                    // Silently fail for individual file fetches
                }
            })());
        }
        await Promise.all(fetches);
        if (Object.keys(updatedDiff).length > 0) {
            diffFileCache = { ...diffFileCache, ...updatedDiff };
        }
        if (Object.keys(updatedUntracked).length > 0) {
            const newUntracked = { ...untrackedCache };
            for (const path of Object.keys(updatedUntracked)) {
                newUntracked[path] = { ...(untrackedCache[path] || {}), loading: false };
            }
            untrackedCache = newUntracked;
        }
    }

    /** Fetch only changed files directly (no manifest fetch). Used for delta updates. */
    async function fetchDeltaFiles(changedHashes: Record<string, string>, prevHashes: Record<string, string>) {
        if (!task) return;
        const updatedDiff: Record<string, DiffFile> = {};
        const updatedUntracked: Record<string, { fileSize?: number; truncated?: boolean; origAdded?: number; loading?: boolean }> = {};
        const fetches: Promise<void>[] = [];
        for (const path of Object.keys(changedHashes)) {
            // Skip only if hash unchanged from previous (no actual change)
            if (diffFileCache[path]?.rows.length && prevHashes[path] === changedHashes[path]) continue;
            fetches.push((async () => {
                try {
                    const data = await getDiffFile(task.project_id, task.id, path);
                    if (data.rows) {
                        const added = data.rows.filter((r: any) => r.variant === 'added').length;
                        const removed = data.rows.filter((r: any) => r.variant === 'removed').length;
                        updatedDiff[path] = { path, rows: data.rows, added, removed, truncated: data.truncated, origAdded: data.origAdded, origRemoved: data.origRemoved };
                    } else if (data.lines) {
                        updatedDiff[path] = { path, rows: data.lines.map((line: string, i: number) => ({ lineNumber: i + 1, variant: 'added' as const, text: line })), added: data.lines.length, removed: 0 };
                        updatedUntracked[path] = { loading: false };
                    }
                } catch (e) {
                    // Silently fail for individual file fetches
                }
            })());
        }
        await Promise.all(fetches);
        // Reassign for Svelte 5 reactivity (new keys on $state records need reassignment)
        if (Object.keys(updatedDiff).length > 0) {
            diffFileCache = { ...diffFileCache, ...updatedDiff };
        }
        if (Object.keys(updatedUntracked).length > 0) {
            untrackedCache = { ...untrackedCache, ...updatedUntracked };
        }
    }

    async function handleExpandDiff(path?: string) {
        if (!task || task.status === 'backlog') return;
        try {
            // If a specific file path is provided, fetch only that file's full diff
            if (path) {
                await expandTruncatedFile(path);
                return;
            }
            // Fallback: fetch full diff for all files
            const headRef = diffTo === 'working' ? undefined : diffTo;
            const baseRef = diffFrom || undefined;
            const result = await getTaskDiff(task.project_id, task.id, { headRef, baseRef, full: true });
            const isDefaultMode = !headRef && !baseRef;
            updateDiff(result, isDefaultMode ? 'http' : 'http-force');
        } catch (e) {
            // Silently fail
        }
    }

    /** Fetch full (un-truncated) rows for a single tracked file. */
    async function expandTruncatedFile(path: string) {
        if (!task) return;
        try {
            const data = await getDiffFile(task.project_id, task.id, path, true);
            if (data.rows) {
                const added = data.rows.filter((r: any) => r.variant === 'added').length;
                const removed = data.rows.filter((r: any) => r.variant === 'removed').length;
                diffFileCache = { ...diffFileCache, [path]: { path, rows: data.rows, added, removed, truncated: false, origAdded: data.origAdded, origRemoved: data.origRemoved } };
            }
        } catch (e) {
            // Silently fail
        }
    }

    /** Load content for a single untracked file on-demand. */
    async function loadUntrackedFile(path: string) {
        if (!task) return;
        // Already loaded
        if (diffFileCache[path]?.rows.length) return;
        // Mark as loading
        untrackedCache = { ...untrackedCache, [path]: { ...(untrackedCache[path] || {}), loading: true } };
        try {
            const data = await getDiffFile(task.project_id, task.id, path);
            if (data.lines) {
                diffFileCache = { ...diffFileCache, [path]: {
                    path,
                    rows: data.lines.map((line, i) => ({ lineNumber: i + 1, variant: 'added' as const, text: line })),
                    added: data.lines.length,
                    removed: 0,
                }};
            }
            untrackedCache = { ...untrackedCache, [path]: { ...(untrackedCache[path] || {}), loading: false } };
        } catch (e) {
            untrackedCache = { ...untrackedCache, [path]: { ...(untrackedCache[path] || {}), loading: false } };
        }
    }

    function getStatusBadge(t: Task): { text: string; color: string } {
        const streaming = get(streamingIds).has(t.id);
        const state = $boardStore?.tasks.find(tt => tt.id === t.id)?.status_state ?? t.status_state;
        if (streaming || state === 'running') return { text: 'Running', color: 'var(--color-status-blue)' };
        if (state === 'pulling_image') return { text: 'Pulling image', color: 'var(--color-status-blue)' };
        if (state === 'failed') return { text: 'Failed', color: 'var(--color-status-red)' };
        if (state === 'stopped') return { text: 'Stopped', color: 'var(--color-status-yellow)' };
        return { text: '', color: 'var(--color-text-tertiary)' };
    }


</script>

    <div class="task-detail-page">
        <!-- Header -->
        <TaskHeader
            {workspaceDir}
            taskId={task.id}
            {sandboxEnabled}
            {badge}
            {worktreeExists}
            {onClose}
            onOpenTerminal={() => showTerminal.set(true)}
            onDelete={handleDelete}
        />

        <!-- Mobile Tab Bar -->
        {#if isMobile}
            <MobileTabBar activeTab={activeTab} onTabChange={(tab) => activeTab = tab} />
        {/if}

        <!-- 3-Column Layout -->
        <div bind:this={columnsRef} class="columns"  role="region" aria-label="Task detail panels">
            <!-- Column 1: Board Overview -->
            <div class="column column-context" class:hidden={isMobile && activeTab !== 'context'} style:width={boardWidth}>
                <BoardOverview
                    sections={overviewSections}
                    {allTasks}
                    {consolidatedSubtaskIds}
                    {subtaskIds}
                    {sectionDragState}
                    {collapsedSections}
                    currentTask={task}
                    {childrenMap}
                    {expandedTasks}
                    {onSelectTask}
                    {onRefreshBoard}
                    onToggleSection={toggleSection}
                    onSectionDragOver={handleSectionDragOver}
                    onSectionDragLeave={handleSectionDragLeave}
                    onSectionDrop={handleSectionDrop}
                    onClearDone={handleClearDone}
                    onToggleExpanded={toggleTaskExpanded}
                    onCreateChildTask={(parentId: string) => { creatingChildOf.set(parentId); showCreateTask.set(true); }}
                    onShowCreateTask={() => showCreateTask.set(true)}
                    getColumnColor={getSectionColor}
                />
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
                            onRefreshDiff={(data: DiffData) => updateDiff(data, 'websocket')}
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
                    {#if editingFile}
                        <!-- Inline file editor (lazy-loaded) -->
                        {#if FileEditorComponent}
                            {@const FileEditor = FileEditorComponent}
                            <FileEditor
                                filePath={editingFile.path}
                                lineNumber={editingFile.lineNumber}
                                displayPath={editingFile.displayPath}
                                projectId={task.project_id}
                                taskId={task.id}
                                onClose={closeFileEditor}
                            />
                        {:else}
                            <div class="file-editor-loading">Loading editor...</div>
                        {/if}
                    {:else}
                        {#if worktreeExists || task.branch}
                            <div class="diff-header">
                                <DiffSelect options={fromOptions} value={diffFrom} onChange={onDiffFromChange} ariaLabel="From" />
                                <span class="diff-arrow">→</span>
                                <DiffSelect options={toOptions} value={diffTo} onChange={onDiffToChange} ariaLabel="To" />
                            </div>
                        {/if}
                        <DiffViewer files={diffFiles} untracked={untrackedFiles} onExpandFile={handleExpandDiff} onExpandUntracked={loadUntrackedFile} onLineClick={handleDiffLineClick} />

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
                                    <DiffViewer files={checkpointDiffFiles} untracked={checkpointDiffUntracked} onExpandFile={handleExpandDiff} onExpandUntracked={handleExpandDiff} onLineClick={handleDiffLineClick} />
                                {/if}
                            </div>
                        {/if}
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
    .column-chat {
        min-width: 0;
        border-right: 1px solid var(--color-border);
    }
    .column-diff {
        min-width: 0;
    }
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
    .column.hidden {
        display: none;
    }

    @media (max-width: 768px) {
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
