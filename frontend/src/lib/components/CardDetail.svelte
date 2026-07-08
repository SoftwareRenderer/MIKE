<script lang="ts">
    import { onMount } from 'svelte';
    import { get } from 'svelte/store';
    import type { Task, Provider, TaskCommit } from '$lib/types';
    import type { DiffFile } from '$lib/types/diff';
    import { board as boardStore, config as configStore, selectedTask, showTerminal, showConfirm, showToast, showCreateTask, creatingChildOf, persistCollapsedTasks, cleanupCollapsedTasks, initExpandedTasks } from '$lib/store';
    import { getStreamingContext, getTaskDiffStatsContext, getExpandedTasksContext } from '$lib/context';
    import { moveTask, deleteTask, getTaskDiff, getTaskCommits, getDiffFile, updateTask, clearDoneTasks, stageFile } from '$lib/api';
    import { parseUnifiedDiff, enrichRowsWithInlineSegments } from '$lib/utils/diff-parser';
    import { loadCollapsedSections, saveCollapsedSections, loadAppSetting, saveAppSetting } from '$lib/storage';
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

    let { task, tasks: columnTasks, projectPath, projectName, workspaceDir, sandboxEnabled, onClose, onSelectTask, onRefreshBoard, activeTab: activeTabProp = 'chat', onActiveTabChange }: { task: Task; tasks: Task[]; projectPath?: string; projectName?: string; workspaceDir?: string; sandboxEnabled?: boolean; onClose: () => void; onSelectTask?: (task: Task) => void; onRefreshBoard?: () => void; activeTab?: 'context' | 'chat' | 'diff'; onActiveTabChange?: (tab: 'context' | 'chat' | 'diff') => void } = $props();

    let allTasks: Task[] = $state([]);

    const overviewSections = [
        { id: 'backlog', title: 'Backlog' },
        { id: 'in_progress', title: 'In Progress' },
        { id: 'review', title: 'Review' },
        { id: 'done', title: 'Done' },
    ];

    // Collapsible sections
    let collapsedSections: Set<string> = $state(new Set());

    // Load collapsed sections from IndexedDB on init
    loadCollapsedSections().then(ids => { collapsedSections = ids; }).catch(() => {});

    function toggleSection(sectionId: string) {
        const next = new Set(collapsedSections);
        if (next.has(sectionId)) {
            next.delete(sectionId);
        } else {
            next.add(sectionId);
        }
        collapsedSections = next;
        saveCollapsedSections(next).catch(() => {});
    }

    // --- Drag-and-drop for board overview ---
    let sectionDragState = $state<Map<string, { isOver: boolean; isRejected: boolean }>>(new Map());

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
    let diffCacheVersion = $state(0); // forces $derived reactivity when staged status changes
    let untrackedCache = $state<Record<string, { fileSize?: number; isBinary?: boolean; truncated?: boolean; origAdded?: number; loading?: boolean }>>({}); // untracked: path → metadata
    // Derived arrays for DiffViewer (sorted, split by staged status)
    let stagedFiles = $derived.by(() => {
            const _v = diffCacheVersion; // depend on version for reactivity
            return Object.values(diffFileCache).filter(f => f.staged && !untrackedCache[f.path]);
        });
    let unstagedFiles = $derived.by(() => {
            const _v = diffCacheVersion; // depend on version for reactivity
            return Object.values(diffFileCache).filter(f => !f.staged && !untrackedCache[f.path]);
        });
    let stagedUntracked = $derived([] as typeof untrackedFiles); // untracked files are never staged
    let unstagedUntracked = $derived(Object.entries(untrackedCache).map(([path, meta]) => ({
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
    let editorFullscreen = $state(false);
    // Diff viewer fullscreen
    let diffFullscreen = $state(false);

    function toggleEditorFullscreen() {
        editorFullscreen = !editorFullscreen;
    }

    function toggleDiffFullscreen() {
        diffFullscreen = !diffFullscreen;
    }

    function resolveFilePath(relativePath: string): string | null {
        if (!task.id) return null;
        if (sandboxEnabled) {
            if (!projectName) return null;
            return `/home/sandbox/${projectName}/${relativePath}`;
        }
        if (!workspaceDir) return null;
        return `${workspaceDir.replace(/\/+$/, '')}/${task.id}/${relativePath}`;
    }

    function handleDiffLineClick(relativePath: string, lineNumber: number) {
        const fullPath = resolveFilePath(relativePath);
        if (!fullPath) return;
        loadFileEditor();
        editingFile = { path: fullPath, lineNumber, displayPath: relativePath };
    }

    /** Stage or unstage a single file. Updates staged flag optimistically, then re-loads from server for reliable reconciliation. */
    async function handleStageFile(path: string, stage: boolean) {
        if (!task) return;
        // Optimistic update — flip staged flag in-place for instant UI feedback
        if (diffFileCache[path]) {
            diffFileCache = { ...diffFileCache, [path]: { ...diffFileCache[path], staged: stage } };
            diffCacheVersion++;
        }
        try {
            await stageFile(task.project_id, task.id, path, stage);
        } catch (e) {
            // Revert optimistic update on failure
            if (diffFileCache[path]) {
                diffFileCache = { ...diffFileCache, [path]: { ...diffFileCache[path], staged: !stage } };
                diffCacheVersion++;
            }
            showToast(String(e), 'error');
        }
    }

    /** Stage all unstaged tracked and untracked files. */
    async function handleStageAll() {
        if (!task) return;
        const paths = [...Object.keys(diffFileCache).filter(p => !diffFileCache[p].staged && !untrackedCache[p]), ...Object.keys(untrackedCache)];
        for (const path of paths) {
            try { await stageFile(task.project_id, task.id, path, true); } catch { /* skip */ }
        }
    }

    /** Unstage all staged files. */
    async function handleUnstageAll() {
        if (!task) return;
        const paths = Object.keys(diffFileCache).filter(p => diffFileCache[p].staged);
        for (const path of paths) {
            try { await stageFile(task.project_id, task.id, path, false); } catch { /* skip */ }
        }
    }

    function closeFileEditor(saved: boolean) {
        editorFullscreen = false;
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
    }

    function onDiffToChange(value: string) {
        diffTo = value;
    }

    /** Set diff to compare base ref vs a specific checkpoint commit. */
    function setDiffToCommit(commitHash: string) {
        diffFrom = '';
        diffTo = commitHash;
    }

    /** Handle "View Changes" from a checkpoint divider — opens an overlay in the diff panel.
     * Shows changes between origin and the state RIGHT BEFORE the checkpoint.
     * Backend resolves stash refs to their common ancestor with baseRef.
     * Toggles closed if the same checkpoint is clicked again. */
    async function handleViewChanges(commitHash: string) {
        if (isMobile) { activeTab = 'diff'; onActiveTabChange?.('diff'); }
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

    // Context stores
    let streamingIds = getStreamingContext();
    let expandedTasksContext = getExpandedTasksContext();
    let taskDiffStats = getTaskDiffStatsContext();
    // Sync store to reactive state for $effect tracking
    let diffStatsMap = $state<Record<string, { hash: string; files: number; additions: number; deletions: number; file_hashes?: Record<string, string>; removed_files?: string[]; staged_paths?: string[] }>>({});

    // Reload diff when task changes (switching tasks via task view keeps CardDetail mounted)
    let previousTaskId = $state<string | null>(null);
    $effect(() => {
        const taskId = task.id;
        if (previousTaskId && taskId !== previousTaskId) {
            // Task changed — reload diff and reset state
            diffTracker.hash = null;
            diffTracker.stagedPathsKey = '';
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
        const state = $boardStore?.tasks.find(t => t.id === task.id)?.execution_state ?? task.execution_state;
        if (streaming || state === 'running') return { text: 'Running', color: 'var(--color-status-blue)' };
        if (state === 'failed') return { text: 'Failed', color: 'var(--color-status-red)' };
        if (state === 'stopped') return { text: 'Stopped', color: 'var(--color-status-yellow)' };
        return { text: 'Idle', color: 'var(--color-text-tertiary)' };
    });

    // Mobile tab — synced with prop from App.svelte
    let activeTab = $state<'context' | 'chat' | 'diff'>(activeTabProp);

    $effect(() => {
        activeTab = activeTabProp;
    });
    let isMobile = $state(false);

    $effect(() => {
        const check = () => isMobile = window.matchMedia('(max-width: 768px)').matches;
        check();
        window.addEventListener('resize', check);
        return () => window.removeEventListener('resize', check);
    });

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

    // Initialize expanded tasks from IndexedDB (default: all expanded) — run once
    let expandedInitialized = $state(false);
    $effect(() => {
        if (expandedInitialized) return;
        if (parentTaskIds.size > 0) {
            expandedInitialized = true;
            (async () => {
                const allIds = new Set(allTasks.map(t => t.id));
                await cleanupCollapsedTasks(allIds);
                expandedTasksContext.set(await initExpandedTasks(parentTaskIds));
            })().catch(() => {});
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
        persistCollapsedTasks(next, parentTaskIds).catch(() => {});
        cleanupCollapsedTasks(allIds).catch(() => {});
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

    let boardRatio = $state(DEFAULT_BOARD_RATIO);
    let chatRatio = $state(DEFAULT_CHAT_RATIO);

    /** Load a ratio value from IndexedDB. */
    async function loadRatio(key: string, defaultVal: number): Promise<number> {
        const stored = await loadAppSetting(key, defaultVal);
        const val = Number.parseFloat(String(stored));
        if (!isNaN(val) && val > 0) return val;
        return defaultVal;
    }

    /** Save a ratio value to IndexedDB. */
    async function saveRatio(key: string, value: number): Promise<void> {
        await saveAppSetting(key, value);
    }

    // Load saved ratios on init
    loadRatio(BOARD_RATIO_KEY, DEFAULT_BOARD_RATIO).then(v => { boardRatio = v; }).catch(() => {});
    loadRatio(CHAT_RATIO_KEY, DEFAULT_CHAT_RATIO).then(v => { chatRatio = v; }).catch(() => {});

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
        saveRatio(BOARD_RATIO_KEY, boardRatio).catch(() => {});
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
        saveRatio(CHAT_RATIO_KEY, chatRatio).catch(() => {});
    }



    onMount(() => {
        const unsubBoard = boardStore.subscribe(b => {
            if (b) {
                allTasks = b.tasks;
            } else {
                allTasks = [];
            }
        });
        const unsubDiff = taskDiffStats.subscribe(v => { diffStatsMap = v; });

        loadDiff();

        function handleKey(e: KeyboardEvent) {
            if (e.key === 'Escape') {
                if (diffFullscreen) {
                    diffFullscreen = false;
                } else if (checkpointDiffOpen) {
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

    // Real-time diff update: re-fetch when the diff hash changes.
    // Two phases: initial load (full manifest + all files), then delta updates (WS-pushed).
    // Delta events are only processed after the initial load completes to prevent
    // stale fetchChangedFiles responses from overwriting fresh delta data.
    // Plain object (not $state) to avoid re-triggering $effect on writes.
    let diffTracker = { hash: null as string | null, stagedPathsKey: '' };
    // Per-file hash tracker for incremental updates
    let diffFileHashes = $state<Record<string, string>>({});
    // Gate: delta processing only starts after the initial load is fully done.
    let diffInitialLoaded = $state(false);
    // Generation counter — incremented on each full loadDiff(). Delta fetches capture
    // the current generation and skip cache writes if a newer load has started.
    let diffLoadGen = $state(0);

    /** Serialize staged_paths for deterministic change detection. */
    function stagedPathsKey(paths: string[] | null | undefined): string {
        return paths ? [...paths].sort().join('\n') : '';
    }

    /** Update staged flags on every cached file to match the given staged_paths list. */
    function applyStagedPaths(paths: string[] | null | undefined) {
        if (Object.keys(diffFileCache).length === 0) return;
        const stagedSet = paths ? new Set(paths) : new Set();
        const updated: Record<string, DiffFile> = {};
        for (const [path, entry] of Object.entries(diffFileCache)) {
            updated[path] = { ...entry, staged: stagedSet.has(path) };
        }
        diffFileCache = updated as Record<string, DiffFile>;
        diffCacheVersion++;
    }

    $effect(() => {
        const stat = diffStatsMap[task.id];
        const currentHash = stat?.hash || null;
        const stagedPaths = stat?.staged_paths;
        // Tracked separately from hash because staged_paths can change without
        // the diff hash changing (e.g., git add then git reset of same content).
        const stagedKey = stagedPathsKey(stagedPaths);
        const hashChanged = currentHash !== diffTracker.hash;
        const stagedChanged = stagedKey !== diffTracker.stagedPathsKey;

        // Skip processing while loadDiff() is in progress. The full reload will
        // provide authoritative data; delta events during a reload would corrupt
        // the cache by replacing fetched rows with empty metadata.
        if (diffLoading) return;

        if (hashChanged) {
            diffTracker.hash = currentHash;
            const fileHashes = stat?.file_hashes;
            const removedFiles = stat?.removed_files || [];

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

            // Empty diff — clear cache (WS already told us the state)
            if (!currentHash) {
                diffFileCache = {};
                untrackedCache = {};
                diffFileHashes = {};
                diffInitialLoaded = false;
                diffTracker.stagedPathsKey = '';
                return;
            }

            // Update staged status (stagedPaths can be null — means no files staged).
            applyStagedPaths(stagedPaths);
            diffTracker.stagedPathsKey = stagedKey;

            if (!fileHashes || Object.keys(fileHashes).length === 0) {
                return;
            }

            // Delta fetch for changed files
            if (fileHashes && !diffLoading) {
                const oldHashes = { ...diffFileHashes };
                diffFileHashes = { ...diffFileHashes, ...fileHashes };
                const stagedSet = stagedPaths ? new Set(stagedPaths) : null;
                fetchDeltaFiles(fileHashes, oldHashes, stagedSet);
            }
        } else if (stagedChanged) {
            // staged_paths changed without hash change — update staged flags only.
            applyStagedPaths(stagedPaths);
            diffTracker.stagedPathsKey = stagedKey;
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

    type DiffData = { files?: Record<string, { hash: string; type: string; added?: number; removed?: number; truncated?: boolean; origAdded?: number; origRemoved?: number; line_count?: number; file_size?: number; is_binary?: boolean; staged?: boolean }>; short_stat?: string };

    function updateDiff(data: DiffData) {
        if (!data.files || Object.keys(data.files).length === 0) {
            // Empty diff — clear all caches so stale files from a previous
            // thread don't persist when the current thread has no changes.
            diffFileCache = {};
            untrackedCache = {};
            diffFileHashes = {};
            return;
        }
        const ignoreSwp = (p: string) => !p.split('/').pop()?.match(/\..*\.swp$/);
        const newCache: Record<string, DiffFile> = {};
        const newUntracked: Record<string, { fileSize?: number; truncated?: boolean; origAdded?: number; loading?: boolean }> = {};
        for (const [path, entry] of Object.entries(data.files)) {
            if (!ignoreSwp(path) && entry.type === 'untracked') continue;
            if (entry.type === 'tracked') {
                const existing = diffFileCache[path];
                newCache[path] = {
                    path,
                    rows: existing?.rows || [],
                    added: entry.added ?? 0,
                    removed: entry.removed ?? 0,
                    fileSize: entry.file_size,
                    isBinary: entry.is_binary,
                    truncated: !!entry.truncated,
                    origAdded: entry.origAdded,
                    origRemoved: entry.origRemoved,
                    staged: entry.staged,
                };
            } else if (entry.type === 'untracked') {
                const existing = diffFileCache[path];
                newUntracked[path] = { fileSize: entry.file_size, isBinary: entry.is_binary, truncated: !!entry.truncated, origAdded: entry.origAdded ?? entry.line_count, loading: false };
                newCache[path] = {
                    path,
                    rows: existing?.rows || [],
                    added: entry.line_count ?? 0,
                    removed: 0,
                    staged: false,
                };
            }
        }
        diffFileCache = newCache;
        untrackedCache = newUntracked;
        const newHashes: Record<string, string> = {};
        for (const [path, entry] of Object.entries(data.files)) {
            newHashes[path] = entry.hash;
        }
        diffFileHashes = newHashes;
    }

    let diffLoading = $state(false);

    /** Reload the full diff. Aborts in-flight delta fetches and resets overlay state.
     * Used after thread switch or task change to ensure a clean slate. */
    async function loadDiff() {
        if (!task || task.status === 'backlog' || diffLoading) return;
        // Abort any in-flight delta fetches so they don't overwrite the fresh load
        for (const controller of Object.values(diffAbortControllers)) {
            controller.abort();
        }
        diffAbortControllers = {};
        // Clear caches so stale files from a previous thread don't persist
        // when the new thread has an empty or smaller diff.
        // updateDiff returns early on empty manifest, leaving old entries.
        diffFileCache = {};
        untrackedCache = {};
        diffFileHashes = {};
        diffInitialLoaded = false;
        // Bump generation so any delta fetches that already started will discard results
        diffLoadGen++;
        // Reset tracker so stale WebSocket events for the old thread don't
        // block processing of the new thread's diff stats.
        // Without this, diffTracker.hash stays at the old thread's hash,
        // and the next WS event with the SAME hash (from worktree watcher
        // catching up) is silently skipped.
        diffTracker.hash = null;
        diffTracker.stagedPathsKey = '';
        // Clear stale diff stats for this task so the $effect processes
        // the next WS event as a fresh hash change.
        const newStats = { ...diffStatsMap };
        delete newStats[task.id];
        taskDiffStats.set(newStats);
        // Reset overlay and comparison state
        checkpointDiffOpen = false;
        checkpointDiffHash = '';
        editingFile = null;
        diffFrom = '';
        diffTo = 'working';
        diffLoading = true;
        try {
            await Promise.all([loadCommits(), loadDiffForMode()]);
        } finally {
            diffLoading = false;
            // Mark initial load complete — delta processing can now start.
            diffInitialLoaded = true;
            // Apply any pending staged_paths from a WebSocket broadcast that
            // arrived during the load (the effect was blocked by diffLoading).
            // This must happen BEFORE syncing the tracker, otherwise the effect
            // would skip processing (currentHash === diffTracker.hash).
            const stat = diffStatsMap[task.id];
            if (stat) {
                applyStagedPaths(stat.staged_paths);
            }
            // Sync tracker with current store value so a pending WebSocket
            // diffStats event doesn't trigger a redundant re-fetch.
            diffTracker.hash = stat?.hash || null;
            diffTracker.stagedPathsKey = stagedPathsKey(stat?.staged_paths);
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
            // Save old hashes before updateDiff overwrites them
            const oldHashes = { ...diffFileHashes };
            updateDiff(result);
            // For manifest format, fetch individual files (skip unchanged)
            if (result.files && isDefaultMode) {
                await fetchChangedFiles(result.files, oldHashes);
            }
        } catch (e) {
            diffFileCache = {};
            untrackedCache = {};
            diffFileHashes = {};
        }
    }

    /** Fetch individual files from the manifest. Skips files whose hash is unchanged and already cached. */
    async function fetchChangedFiles(manifest: Record<string, { hash: string; type: string; added?: number; removed?: number; line_count?: number; file_size?: number; is_binary?: boolean; staged?: boolean }>, oldHashes?: Record<string, string>) {
        if (!task) return;
        const updatedDiff: Record<string, DiffFile> = {};
        const updatedUntracked: Record<string, boolean> = {};
        const fetches: Promise<void>[] = [];
        for (const [path, entry] of Object.entries(manifest)) {
            // Skip blank untracked files (no content to fetch)
            if (entry.type === 'untracked' && (entry.line_count ?? 0) === 0) continue;
            // Skip if hash unchanged, rows already cached, and staged status matches.
            // Staged status can change without content changing (git add/reset).
            const cachedEntry = diffFileCache[path];
            const stagedMatch = cachedEntry?.staged === (entry.staged ?? false);
            if (oldHashes && oldHashes[path] === entry.hash && cachedEntry?.rows && cachedEntry.rows.length > 0 && stagedMatch) continue;
            // Skip if file transitioned from untracked → tracked with same content.
            // Content is identical (all additions), so convert cached lines to rows locally.
            if (entry.type === 'tracked' && untrackedCache[path] && diffFileCache[path]?.rows && diffFileCache[path].rows.length > 0) {
                const cachedRows = diffFileCache[path].rows;
                if (cachedRows.length === (entry.added ?? 0) && cachedRows.every((r: any) => r.variant === 'added')) {
                    // Content matches — reuse cached rows, just update metadata
                    updatedDiff[path] = { ...diffFileCache[path], added: entry.added ?? 0, removed: entry.removed ?? 0, truncated: !!entry.truncated, origAdded: entry.origAdded, origRemoved: entry.origRemoved, staged: entry.staged };
                    // Remove from untracked cache (now tracked)
                    const newUntracked = { ...untrackedCache };
                    delete newUntracked[path];
                    untrackedCache = newUntracked;
                    continue;
                }
            }
            // Fetch this file — server says it exists, render it
            fetches.push((async () => {
                try {
                    const data = await getDiffFile(task.project_id, task.id, path);
                    if (data.rows) {
                        updatedDiff[path] = { path, rows: enrichRowsWithInlineSegments(data.rows), added: entry.added ?? 0, removed: entry.removed ?? 0, truncated: data.truncated, origAdded: data.origAdded, origRemoved: data.origRemoved, staged: entry.staged };
                    } else if (data.lines) {
                        updatedDiff[path] = { path, rows: data.lines.map((line, i) => ({ lineNumber: i + 1, variant: 'added' as const, text: line })), added: data.lines.length, removed: 0, truncated: !!data.truncated, origAdded: data.origAdded, staged: entry.staged ?? false };
                        updatedUntracked[path] = true;
                    } else if (data.isBinary || data.fileSize) {
                        // Binary/large file — no rows but has metadata
                        updatedDiff[path] = { path, rows: [], added: entry.added ?? 0, removed: entry.removed ?? 0, fileSize: data.fileSize ?? entry.file_size, isBinary: data.isBinary ?? entry.is_binary, truncated: false, staged: entry.staged };
                        if (entry.type === 'untracked') {
                            updatedUntracked[path] = true;
                        }
                    }
                } catch (e) {
                    console.error(`[diff] Failed to fetch file ${path}:`, e);
                }
            })());
        }
        await Promise.all(fetches);
        diffFileCache = { ...diffFileCache, ...updatedDiff };
        if (Object.keys(updatedUntracked).length > 0) {
            const newUntracked = { ...untrackedCache };
            for (const path of Object.keys(updatedUntracked)) {
                newUntracked[path] = { ...(untrackedCache[path] || {}), loading: false };
            }
            untrackedCache = newUntracked;
        }
    }

    /** Per-file AbortControllers to cancel stale in-flight fetches. */
    let diffAbortControllers: Record<string, AbortController> = {};

    /** Fetch changed files directly (no manifest fetch). Used for delta updates. */
    async function fetchDeltaFiles(changedHashes: Record<string, string>, oldHashes?: Record<string, string>, stagedSet?: Set<string>) {
        if (!task) return;
        // Capture current generation — if a full loadDiff() starts during this delta,
        // the generation will bump and we'll discard our results to avoid stale overwrites.
        const gen = diffLoadGen;
        const updatedDiff: Record<string, DiffFile> = {};
        const updatedUntracked: Record<string, { fileSize?: number; truncated?: boolean; origAdded?: number; loading?: boolean }> = {};
        const fetches: Promise<void>[] = [];
        for (const path of Object.keys(changedHashes)) {
            // Skip if hash unchanged and rows already cached
            if (oldHashes && oldHashes[path] === changedHashes[path] && diffFileCache[path]?.rows && diffFileCache[path].rows.length > 0) continue;
            // Skip if file transitioned from untracked → tracked (all rows are additions).
            if (untrackedCache[path] && diffFileCache[path]?.rows && diffFileCache[path].rows.length > 0) {
                const cachedRows = diffFileCache[path].rows;
                // Check: all rows are additions (untracked file content)
                if (cachedRows.every((r: any) => r.variant === 'added')) {
                    const existing = diffFileCache[path];
                    updatedDiff[path] = { ...existing, staged: existing?.staged };
                    const newUntracked = { ...untrackedCache };
                    delete newUntracked[path];
                    untrackedCache = newUntracked;
                    continue;
                }
            }
            // Cancel any previous in-flight fetch for this file
            if (diffAbortControllers[path]) {
                diffAbortControllers[path].abort();
            }
            const controller = new AbortController();
            diffAbortControllers[path] = controller;
            fetches.push((async () => {
                try {
                    const existing = diffFileCache[path];
                    const data = await getDiffFile(task.project_id, task.id, path, { signal: controller.signal });
                    const isStaged = stagedSet?.has(path) ?? existing?.staged ?? false;
                    if (data.rows) {
                        const rows = enrichRowsWithInlineSegments(data.rows);
                        updatedDiff[path] = { path, rows, added: rows.filter((r: any) => r.variant === 'added').length, removed: rows.filter((r: any) => r.variant === 'removed').length, truncated: data.truncated, origAdded: data.origAdded, origRemoved: data.origRemoved, staged: isStaged };
                    } else if (data.lines) {
                        updatedDiff[path] = { path, rows: data.lines.map((line: string, i: number) => ({ lineNumber: i + 1, variant: 'added' as const, text: line })), added: data.lines.length, removed: 0, truncated: !!data.truncated, origAdded: data.origAdded, staged: isStaged };
                        updatedUntracked[path] = { ...(untrackedCache[path] || {}), loading: false };
                    } else if (data.isBinary || data.fileSize) {
                        updatedDiff[path] = { path, rows: [], added: existing?.added ?? 0, removed: existing?.removed ?? 0, fileSize: data.fileSize, isBinary: data.isBinary, truncated: false, staged: isStaged };
                        if (untrackedCache[path]) {
                            updatedUntracked[path] = { ...(untrackedCache[path] || {}), loading: false, fileSize: data.fileSize };
                        }
                    }
                } catch (e) {
                    if (e instanceof DOMException && e.name === 'AbortError') return;
                    console.error(`[diff] Failed to fetch file ${path}:`, e);
                }
            })());
        }
        await Promise.all(fetches);
        // Discard results if a full loadDiff() started while we were fetching.
        // The generation counter was bumped by loadDiff(), so our data is stale.
        if (gen !== diffLoadGen) return;
        // Merge, preserving any newer staged values that arrived while fetches were in-flight.
        const currentStaged = new Map(Object.entries(diffFileCache).map(([p, f]) => [p, f.staged]));
        diffFileCache = { ...diffFileCache, ...updatedDiff };
        for (const path of Object.keys(updatedDiff)) {
            const staged = currentStaged.get(path);
            if (staged !== undefined) {
                diffFileCache[path] = { ...diffFileCache[path], staged };
            }
        }
        for (const path of Object.keys(changedHashes)) {
            delete diffAbortControllers[path];
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
            updateDiff(result);
        } catch (e) {
            // Silently fail
        }
    }

    /** Fetch full (un-truncated) rows for a single tracked file. */
    async function expandTruncatedFile(path: string) {
        if (!task) return;
        try {
            const data = await getDiffFile(task.project_id, task.id, path, { full: true });
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
        const meta = untrackedCache[path];
        const isTruncated = meta?.truncated && diffFileCache[path]?.rows.length;
        // Already loaded and not truncated
        if (diffFileCache[path]?.rows.length && !isTruncated) return;
        // Mark as loading
        untrackedCache = { ...untrackedCache, [path]: { ...(untrackedCache[path] || {}), loading: true } };
        try {
            const data = await getDiffFile(task.project_id, task.id, path, { full: isTruncated });
            if (data.lines) {
                diffFileCache = { ...diffFileCache, [path]: {
                    path,
                    rows: data.lines.map((line, i) => ({ lineNumber: i + 1, variant: 'added' as const, text: line })),
                    added: data.lines.length,
                    removed: 0,
                    truncated: !!data.truncated,
                    origAdded: data.origAdded,
                }};
            } else if (data.isBinary || data.fileSize) {
                // Binary/large file — no lines but has metadata
                diffFileCache = { ...diffFileCache, [path]: {
                    path,
                    rows: [],
                    added: 0,
                    removed: 0,
                    fileSize: data.fileSize,
                    isBinary: data.isBinary,
                    truncated: false,
                }};
            }
            untrackedCache = { ...untrackedCache, [path]: { ...(untrackedCache[path] || {}), loading: false, truncated: !!data?.truncated, fileSize: data.fileSize ?? meta?.fileSize } };
        } catch (e) {
            untrackedCache = { ...untrackedCache, [path]: { ...(untrackedCache[path] || {}), loading: false } };
        }
    }

    function getStatusBadge(t: Task): { text: string; color: string } {
        const streaming = get(streamingIds).has(t.id);
        const state = $boardStore?.tasks.find(tt => tt.id === t.id)?.execution_state ?? t.execution_state;
        if (streaming || state === 'running') return { text: 'Running', color: 'var(--color-status-blue)' };
        if (state === 'pulling_image') return { text: 'Pulling image', color: 'var(--color-status-blue)' };
        if (state === 'failed') return { text: 'Failed', color: 'var(--color-status-red)' };
        if (state === 'stopped') return { text: 'Stopped', color: 'var(--color-status-yellow)' };
        return { text: '', color: 'var(--color-text-tertiary)' };
    }


</script>

    <div class="task-detail-page" class:fullscreen={editorFullscreen || diffFullscreen}>
        <!-- Header -->
        <TaskHeader
            {workspaceDir}
            taskId={task.id}
            {sandboxEnabled}
            {badge}
            {worktreeExists}
            {task}
            {onClose}
            onOpenTerminal={() => showTerminal.set(true)}
            onDelete={handleDelete}
        />

        <!-- Mobile Tab Bar (always rendered, visibility controlled by CSS media query) -->
        <MobileTabBar activeTab={activeTab} onTabChange={(tab: 'context' | 'chat' | 'diff') => { activeTab = tab; onActiveTabChange?.(tab); }} />

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
                            onRefreshDiff={(data: DiffData) => {
                                // Skip update while loadDiff() is in progress to prevent
                                // WS diff_changed events from overwriting the cache with
                                // empty rows before fetchChangedFiles() completes.
                                if (!diffLoading) updateDiff(data);
                            }}
                            onMoveTask={handleMove}
                            onThreadSwitch={() => loadDiff()}
                            onViewChanges={handleViewChanges}
                            onInvalidateDiff={() => {
                                // Clear cache so next WS event triggers a full reload.
                                diffFileCache = {};
                                untrackedCache = {};
                                diffFileHashes = {};
                                diffInitialLoaded = false;
                            }}
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
                                onToggleFullscreen={toggleEditorFullscreen}
                                mobileFullscreen={editorFullscreen}
                                isMobile={isMobile}
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
                                <button type="button" class="diff-fullscreen-btn" onclick={toggleDiffFullscreen} title="Toggle fullscreen">
                                    {#if diffFullscreen}
                                        <svg width="14" height="14" viewBox="0 0 16 16" fill="none"><path d="M10 2V6H14M6 14V10H2" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
                                    {:else}
                                        <svg width="14" height="14" viewBox="0 0 16 16" fill="none"><path d="M10 2h4v4M6 14H2v-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
                                    {/if}
                                </button>
                            </div>
                        {/if}
                        <DiffViewer stagedFiles={stagedFiles} unstagedFiles={unstagedFiles} stagedUntracked={stagedUntracked} unstagedUntracked={unstagedUntracked} onExpandFile={handleExpandDiff} onExpandUntracked={loadUntrackedFile} onLineClick={handleDiffLineClick} onStageFile={handleStageFile} onStageAll={handleStageAll} onUnstageAll={handleUnstageAll} />

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
                                    <DiffViewer stagedFiles={[]} unstagedFiles={checkpointDiffFiles} stagedUntracked={[]} unstagedUntracked={checkpointDiffUntracked} onExpandFile={handleExpandDiff} onExpandUntracked={handleExpandDiff} onLineClick={handleDiffLineClick} />
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
    .diff-fullscreen-btn {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 28px;
        height: 28px;
        border: none;
        background: transparent;
        color: var(--color-text-tertiary);
        cursor: pointer;
        border-radius: 4px;
        flex-shrink: 0;
        margin-left: auto;
    }
    .diff-fullscreen-btn:hover {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }
    .column.hidden {
        display: none;
    }

    /* Mobile tab bar — hidden on desktop, shown on mobile via media query */
    :global(.mobile-tab-bar) {
        display: none !important;
    }

    @media (max-width: 768px) {
        :global(.mobile-tab-bar) {
            display: flex !important;
        }
        /* On mobile: hide all columns by default, only show the non-hidden (active) one */
        .column {
            display: none;
        }
        .column:not(.hidden) {
            display: flex;
            flex: 0 0 100% !important;
            min-width: 100% !important;
            width: 100% !important;
        }
        .column-context:not(.hidden) {
            display: block;
            border-right: none;
        }
        .column-chat {
            border-right: none;
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

    /* File editor fullscreen mode (mobile) */
    .task-detail-page.fullscreen {
        position: fixed;
        top: 0;
        left: 0;
        right: 0;
        bottom: 0;
        z-index: 1000;
        margin: 0;
    }
    .task-detail-page.fullscreen :global(.header),
    .task-detail-page.fullscreen :global(.mobile-tab-bar) {
        display: none !important;
    }
    .task-detail-page.fullscreen .columns {
        height: 100%;
    }
    .task-detail-page.fullscreen .column-context,
    .task-detail-page.fullscreen .column-chat {
        display: none !important;
    }
    .task-detail-page.fullscreen .column-diff {
        width: 100% !important;
        flex: 1 !important;
        min-width: 100% !important;
    }
</style>
