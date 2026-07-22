<script lang="ts">
    import { onMount, onDestroy } from 'svelte';
    import type { Task, TaskCommit } from '$lib/types';
    import type { DiffFile, DiffRow } from '$lib/types/diff';
    import { appStore, uiStore, dialogStore, updateTaskInBoard, removeTaskFromBoard } from '$lib/stores';
    import { getStreamingContext, getTaskDiffStatsContext, getExpandedTasksContext } from '$lib/context';
    import { moveTask, deleteTask, getTaskDiff, getTaskCommits, getDiffFile, clearDoneTasks, stageFile } from '$lib/api';
    import { parseUnifiedDiff, enrichRowsWithInlineSegments } from '$lib/utils/diff-parser';
    import { clearHighlightCache } from '$lib/highlighter';
    import { useIsMobile } from '$lib/composables/use-is-mobile.svelte';
    import { loadAppSetting, saveAppSetting, loadCollapsedSections, saveCollapsedSections } from '$lib/storage';
    import { useBoardDnd } from '$lib/composables/use-board-dnd.svelte';
    import { useSubtaskHierarchy } from '$lib/composables/use-subtask-hierarchy.svelte';
    import { DiffCache } from '$lib/services/diff-cache.svelte';
    import TaskChat from './Chat/TaskChat.svelte';
    import ResizeHandle from './ResizeHandle.svelte';
    // Lazy-loaded on first diff view
    let DiffViewerComponent: typeof import('./DiffViewer.svelte').default | null = $state(null);
    let diffViewerLoading = $state(false);
    function loadDiffViewer(): void {
        if (DiffViewerComponent || diffViewerLoading) return;
        diffViewerLoading = true;
        import('./DiffViewer.svelte').then((mod) => {
            DiffViewerComponent = mod.default;
            diffViewerLoading = false;
        }).catch((e) => {
            console.error('[diff] Failed to load DiffViewer:', e);
            diffViewerLoading = false;
        });
    }
    import CheckpointOverlay from './CardDetail/CheckpointOverlay.svelte';
    import DiffComparison from './CardDetail/DiffComparison.svelte';
    // Lazy-loaded on first file edit
    let DiffEditorComponent: typeof import('./DiffEditor.svelte').default | null = $state(null);
    let diffEditorLoading = $state(false);
    function loadDiffEditor(): void {
        if (DiffEditorComponent || diffEditorLoading) return;
        diffEditorLoading = true;
        import('./DiffEditor.svelte').then((mod) => {
            DiffEditorComponent = mod.default;
            diffEditorLoading = false;
        }).catch(() => { diffEditorLoading = false; });
    }
    import MobileTabBar from './CardDetail/MobileTabBar.svelte';
    import TaskHeader from './CardDetail/TaskHeader.svelte';
    import BoardOverview from './CardDetail/BoardOverview.svelte';
    import MultiSelectBar from './MultiSelectBar.svelte';
    import { useMultiSelectActions } from '$lib/composables/use-multi-select-actions.svelte';

    let { task, tasks, projectPath, projectName, workspaceDir, sandboxEnabled, onClose, onSelectTask, onRefreshBoard, onOpenTerminal, activeTab: activeTabProp = 'chat', onActiveTabChange }: { task: Task; tasks?: Task[]; projectPath?: string; projectName?: string; workspaceDir?: string; sandboxEnabled?: boolean; onClose: (status?: string) => void; onSelectTask?: (task: Task) => void; onRefreshBoard?: () => void; onOpenTerminal?: () => void; activeTab?: 'context' | 'chat' | 'diff' | 'checkpoint'; onActiveTabChange?: (tab: 'context' | 'chat' | 'diff' | 'checkpoint') => void } = $props();

    let allTasks = $derived(tasks || appStore.board?.tasks || []);

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

    // Board overview drag-and-drop (extracted composable)
    let dnd = useBoardDnd(() => allTasks, () => task.project_id);

    async function handleClearDone(count: number) {
        if (!task.project_id) return;
        const confirmed = await dialogStore.showConfirm({
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
            dialogStore.showToast('Failed to clear done tasks: ' + String(e), 'error');
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

    // Diff state — managed by DiffCache service (reactive via $state())
    let diffCache = new DiffCache({
        getTaskDiff,
        getDiffFile,
        stageFile,
        enrichRows: enrichRowsWithInlineSegments,
    }, () => task.project_id, () => task.id);
    // Derived arrays for DiffViewer (sorted, split by staged status)
    // For done tasks, all diff files are shown as "Merged" (no staging/unstaged concept).
    let isDoneTask = $derived(task.status === 'done');
    let stagedFiles = $derived(isDoneTask ? [] : Object.values(diffCache.state.files).filter(f => f.staged && !diffCache.state.untracked[f.path] && (f.rows?.length > 0 || f.isBinary)));
    let unstagedFiles = $derived(isDoneTask ? [] : Object.values(diffCache.state.files).filter(f => !f.staged && !diffCache.state.untracked[f.path] && (f.rows?.length > 0 || f.isBinary)));
    let committedFiles = $derived(isDoneTask
        ? Object.values(diffCache.state.files).filter(f => !diffCache.state.untracked[f.path] && (f.rows?.length > 0 || f.isBinary))
        : Object.values(diffCache.state.files).filter(f => f.committedRows && f.committedRows.length > 0));
    // Authoritative staged-changes count from DiffCache (source of truth).
    // Passed to TaskChat so the ReviewBar button stays in sync with the diff panel
    // when the user stages/unstages files (optimistic update before WS catch-up).
    let hasStagedChanges = $derived(stagedFiles.length > 0);
    let stagedUntracked = $derived([] as { path: string; fileSize?: number; isBinary?: boolean; truncated?: boolean; origAdded?: number; loading?: boolean; file: DiffFile }[]);
    let unstagedUntracked = $derived(isDoneTask ? [] : Object.entries(diffCache.state.untracked).map(([path, meta]) => ({
        path,
        ...meta,
        file: diffCache.state.files[path] || { path, rows: [], added: 0, removed: 0 },
    })));
    // Terminal - worktree exists when task has a branch (set on start, cleared on delete)
    let worktreeExists = $derived(!!task.branch);

    // Commit history for diff comparison
    let commits: TaskCommit[] = $state([]);
    let baseCommit: TaskCommit | null = $state(null);

    // Checkpoint diff overlay (overlays the diff panel)
    let checkpointDiffOpen = $state(false);
    let checkpointDiffHash = $state(''); // which checkpoint hash is currently shown
    let checkpointDiffFiles: DiffFile[] = $state([]);
    let checkpointDiffUntracked: { path: string; fileSize?: number; file: DiffFile; truncated?: boolean; origAdded?: number }[] = $state([]);
    let checkpointDiffLoading = $state(false);
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
        loadDiffEditor();
        editingFile = { path: fullPath, lineNumber, displayPath: relativePath };
    }

    /** Stage or unstage a single file. */
    async function handleStageFile(path: string, stage: boolean) {
        if (!task) return;
        try {
            await diffCache.stageFile(path, stage, () => { diffCache.revertStageFile(path, stage); });
        } catch (e) {
            dialogStore.showToast(String(e), 'error');
        }
    }

    /** Stage all unstaged tracked and untracked files. */
    async function handleStageAll() {
        if (!task) return;
        await diffCache.stageAll();
    }

    /** Unstage all staged files. */
    async function handleUnstageAll() {
        if (!task) return;
        await diffCache.unstageAll();
    }

    function closeDiffEditor(saved: boolean) {
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

    function onDiffFromChange(value: string) {
        diffFrom = value;
    }

    function onDiffToChange(value: string) {
        diffTo = value;
    }

    /** Handle "View Changes" from a checkpoint divider — opens its own panel.
     * Shows changes between origin and the state RIGHT BEFORE the checkpoint.
     * Backend resolves stash refs to their common ancestor with baseRef.
     * Toggles closed if the same checkpoint is clicked again. */
    async function handleViewChanges(commitHash: string) {
        if (isMobileCtx.isMobile) { activeTab = 'checkpoint'; onActiveTabChange?.('checkpoint'); }
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
        if (isMobileCtx.isMobile && activeTab === 'checkpoint') {
            activeTab = 'diff';
            onActiveTabChange?.('diff');
        }
    }

    // Context stores
    let streamingIdsContext = getStreamingContext();
    let expandedTasksContext = getExpandedTasksContext();
    let taskDiffStats = getTaskDiffStatsContext();
    // Sync streamingIds store to $state for reactive tracking in $derived.by
    // (get() on a Svelte store inside $derived.by does NOT establish a dependency)
    let streamingIds = $state<Set<string>>(new Set());
    $effect(() => {
        const unsub = streamingIdsContext.subscribe(v => { streamingIds = v; });
        return unsub;
    });
    // Sync store to reactive state for $effect tracking
    let diffStatsMap = $state<Record<string, { hash: string; files: number; additions: number; deletions: number; file_hashes?: Record<string, string>; removed_files?: string[]; staged_paths?: string[] }>>({});

    // Reload diff when task changes (switching tasks via task view keeps CardDetail mounted)
    let previousTaskId = $state<string | null>(null);
    $effect(() => {
        const taskId = task.id;
        if (previousTaskId && taskId !== previousTaskId) {
            // Task changed — reload diff and reset state
            diffCache.reset();
            commits = [];
            baseCommit = null;
            diffFrom = '';
            diffTo = 'working';
            clearHighlightCache();
            loadDiff();
        }
        previousTaskId = taskId;
    });

    // Load diff when task transitions from backlog to active (e.g., user starts the task)
    $effect(() => {
        const status = task.status;
        const branch = task.branch;
        if (status !== 'backlog' && branch && !DiffViewerComponent && !diffViewerLoading) {
            loadDiff();
        }
    });



    // Status badge
    let badge = $derived.by(() => {
        const streaming = streamingIds.has(task.id);
        const state = appStore.board?.tasks.find(t => t.id === task.id)?.execution_state ?? task.execution_state;
        if (streaming || state === 'running') return { text: 'Running', color: 'var(--color-status-blue)' };
        if (state === 'idle' && task.status === 'in_progress') return { text: 'Pending', color: 'var(--color-status-orange)' };
        if (state === 'failed') return { text: 'Failed', color: 'var(--color-status-red)' };
        if (state === 'stopped') return { text: 'Stopped', color: 'var(--color-status-yellow)' };
        return { text: 'Idle', color: 'var(--color-text-tertiary)' };
    });

    // Mobile tab — synced with prop from App.svelte
    let activeTab = $state<'context' | 'chat' | 'diff' | 'checkpoint'>('chat');

    $effect(() => {
        activeTab = activeTabProp;
    });
    const isMobileCtx = useIsMobile();

    // Subtask hierarchy (extracted composable)
    let hierarchy = useSubtaskHierarchy(() => allTasks, expandedTasksContext, () => task.parent_task_id);

    // Multi-select actions (wires up floating bar events)
    let multiSelectActions = useMultiSelectActions({
        async onDelete(taskIds, titles) {
            const confirmed = await dialogStore.showConfirm({
                title: 'Delete Tasks',
                message: `Permanently delete ${taskIds.length} task${taskIds.length > 1 ? 's' : ''}? This cannot be undone.`,
                items: titles,
                confirmText: 'Delete',
                cancelText: 'Cancel',
            });
            if (!confirmed) return;
            for (const id of taskIds) {
                try {
                    await import('$lib/api').then(({ deleteTask }) => deleteTask(task.project_id, id));
                    removeTaskFromBoard(appStore, id);
                } catch (err) {
                    dialogStore.showToast('Failed to delete task: ' + String(err), 'error');
                }
            }
            onRefreshBoard?.();
        },
        async onStart(taskIds) {
            let started = 0;
            for (const id of taskIds) {
                try {
                    const t = allTasks.find(t => t.id === id);
                    if (!t) continue;
                    if (t.parent_task_id) {
                        const { startSubtask } = await import('$lib/api');
                        const updated = await startSubtask(task.project_id, id);
                        updateTaskInBoard(appStore, updated.id, updated);
                    } else {
                        const { startTask } = await import('$lib/api');
                        const updated = await startTask(task.project_id, id);
                        updateTaskInBoard(appStore, updated.id, updated);
                    }
                    started++;
                } catch (err) {
                    dialogStore.showToast(`Failed to start task: ${String(err)}`, 'error');
                }
            }
            if (started > 0) {
                dialogStore.showToast(`${started} task${started > 1 ? 's' : ''} started`, 'success');
            }
        },
        async onModelChange(taskIds, providerUrl, modelName, reasoningEffort) {
            let updated = 0;
            for (const id of taskIds) {
                try {
                    const { updateTask } = await import('$lib/api');
                    const taskUpdate: Record<string, unknown> = { llama_server_url: providerUrl };
                    if (modelName) taskUpdate.model_name = modelName;
                    else taskUpdate.model_name = undefined;
                    if (reasoningEffort) taskUpdate.reasoning_effort = reasoningEffort;
                    else taskUpdate.reasoning_effort = undefined;
                    const result = await updateTask(task.project_id, id, taskUpdate);
                    updateTaskInBoard(appStore, result.id, result);
                    updated++;
                } catch (err) {
                    dialogStore.showToast(`Failed to update model: ${String(err)}`, 'error');
                }
            }
            if (updated > 0) {
                dialogStore.showToast(`Model updated for ${updated} task${updated > 1 ? 's' : ''}`, 'success');
            }
        },
    });
    let multiSelect = multiSelectActions.multiSelect;

    // Column resize (inline — matches ExplorerView pattern)
    let columnsRef: HTMLDivElement | null = $state(null);
    const DEFAULT_BOARD_RATIO = 0.22;
    const DEFAULT_CHAT_RATIO = 0.55;
    const MIN_BOARD_RATIO = 0.12;
    const MAX_BOARD_RATIO = 0.40;
    const MIN_CHAT_RATIO = 0.15;
    const MAX_CHAT_RATIO = 0.80;

    let boardRatio = $state(DEFAULT_BOARD_RATIO);
    let chatRatio = $state(DEFAULT_CHAT_RATIO);
    let boardDragStartX = 0;
    let boardDragStartRatio = 0;
    let chatDragStartX = 0;
    let chatDragStartRatio = 0;

    let boardWidth = $derived(`${(boardRatio * 100).toFixed(1)}%`);
    let chatWidth = $derived(`${(chatRatio * (1 - boardRatio) * 100).toFixed(1)}%`);
    // Diff takes full remaining space (checkpoint overlays on top)
    let diffWidth = $derived(`${((1 - chatRatio) * (1 - boardRatio) * 100).toFixed(1)}%`);

    function handleBoardDragStart(ev: MouseEvent | TouchEvent, startX: number) {
        boardDragStartX = startX;
        boardDragStartRatio = boardRatio;
    }
    function handleBoardDragMove(pointerX: number) {
        if (!columnsRef) return;
        const delta = (pointerX - boardDragStartX) / Math.max(columnsRef.offsetWidth, 1);
        boardRatio = Math.max(MIN_BOARD_RATIO, Math.min(MAX_BOARD_RATIO, boardDragStartRatio + delta));
    }
    function handleBoardDragEnd() {
        saveAppSetting('kanban:detail:boardRatio', boardRatio).catch(() => {});
    }

    function handleChatDragStart(ev: MouseEvent | TouchEvent, startX: number) {
        chatDragStartX = startX;
        chatDragStartRatio = chatRatio;
    }
    function handleChatDragMove(pointerX: number) {
        if (!columnsRef) return;
        const remaining = Math.max(columnsRef.offsetWidth * (1 - boardRatio), 1);
        const delta = (pointerX - chatDragStartX) / remaining;
        chatRatio = Math.max(MIN_CHAT_RATIO, Math.min(MAX_CHAT_RATIO, chatDragStartRatio + delta));
    }
    function handleChatDragEnd() {
        saveAppSetting('kanban:detail:chatRatio', chatRatio).catch(() => {});
    }

    // Load saved ratios
    loadAppSetting('kanban:detail:boardRatio', DEFAULT_BOARD_RATIO).then(v => {
        const val = Number.parseFloat(String(v));
        if (!isNaN(val) && val > 0) boardRatio = val;
    }).catch(() => {});
    loadAppSetting('kanban:detail:chatRatio', DEFAULT_CHAT_RATIO).then(v => {
        const val = Number.parseFloat(String(v));
        if (!isNaN(val) && val > 0) chatRatio = val;
    }).catch(() => {});



    let detailEl: HTMLDivElement | null = null;
    let previousFocus: HTMLElement | null = null;

    onMount(() => {
        // Save current focus so we can restore on close
        previousFocus = document.activeElement as HTMLElement;
        // Focus the detail panel for keyboard/screen reader users
        queueMicrotask(() => detailEl?.focus());
        const unsubDiff = taskDiffStats.subscribe(v => { diffStatsMap = v; });
        const unsubsMultiSelect = multiSelectActions.setup();

        loadDiff();

        function handleKey(e: KeyboardEvent) {
            if (e.key === 'Escape') {
                // Don't close when user is typing in an input/textarea
                const tag = (document.activeElement as HTMLElement)?.tagName.toLowerCase();
                if (tag === 'input' || tag === 'textarea' || tag === 'select') return;
                if (diffFullscreen) {
                    diffFullscreen = false;
                } else if (checkpointDiffOpen) {
                    closeCheckpointDiff();
                } else if (multiSelect.hasSelection) {
                    multiSelect.clear();
                } else {
                    onClose();
                }
            }
        }
        window.addEventListener('keydown', handleKey);
        return () => {
            unsubDiff();
            unsubsMultiSelect();
            window.removeEventListener('keydown', handleKey);
        };
    });

    onDestroy(() => {
        // Restore focus to the element that was focused before CardDetail opened
        queueMicrotask(() => previousFocus?.focus());
    });

    // Real-time diff update: delegate to DiffCache service.
    $effect(() => {
        const stat = diffStatsMap[task.id];
        diffCache.processStats(stat, diffLoading);
    });

    async function handleMove(status: string) {
        if (!task) return;
        try {
            const updated = await moveTask(task.project_id, task.id, status);
            task = updated;
            updateTaskInBoard(appStore, task!.id, updated);
        } catch (e) {
            dialogStore.showToast('Failed to move: ' + String(e), 'error');
        }
    }

    async function handleDelete() {
        if (!task) return;
        const confirmed = await dialogStore.showConfirm({
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
            onClose(task.status);
        } catch (e) {
            dialogStore.showToast('Failed to delete task: ' + String(e), 'error');
        }
    }

    let diffLoading = $state(false);

    /** Reload the full diff. Aborts in-flight delta fetches and resets overlay state.
     * Used after thread switch or task change to ensure a clean slate.
     * For done tasks, uses branch_tip to show the diff of committed changes. */
    async function loadDiff() {
        if (!task || task.status === 'backlog' || diffLoading) return;
        // Reset overlay and comparison state
        checkpointDiffOpen = false;
        checkpointDiffHash = '';
        editingFile = null;
        diffFrom = '';
        diffTo = 'working';
        clearHighlightCache();
        diffLoading = true;
        // Preload the diff viewer chunk while fetching diff data
        loadDiffViewer();
        try {
            // For done tasks, fetch the diff of committed changes.
            // Use branch_tip (captured at commit time) if available, otherwise fall back to branch name.
            const diffOptions = task.status === 'done' && (task.branch_tip || task.branch)
                ? { headRef: task.branch_tip || task.branch }
                : {};
            await Promise.all([loadCommits(), diffCache.loadDiff(() => {}, diffOptions)]);
        } finally {
            diffLoading = false;
            // Apply any pending staged_paths from a WebSocket broadcast that
            // arrived during the load (the effect was blocked by diffLoading).
            const stat = diffStatsMap[task.id];
            if (stat) {
                diffCache.applyStagedPaths(stat.staged_paths);
            }
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

    async function handleExpandDiff(path?: string) {
        if (!task || task.status === 'backlog') return;
        try {
            // If a specific file path is provided, fetch only that file's full diff
            if (path) {
                await expandTruncatedFile(path);
                return;
            }
            // Fallback: fetch full diff for all files
            // For done tasks, always use branch_tip (committed diff), never working tree.
            let headRef: string | undefined;
            if (task.status === 'done') {
                headRef = task.branch_tip || task.branch || undefined;
            } else {
                headRef = diffTo === 'working' ? undefined : diffTo;
            }
            const baseRef = diffFrom || undefined;
            const result = await getTaskDiff(task.project_id, task.id, { headRef, baseRef, full: true });
            diffCache.updateDiff(result);
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
                diffCache.setFile(path, { path, rows: data.rows as DiffRow[], added, removed, truncated: false, origAdded: data.origAdded, origRemoved: data.origRemoved });
            }
        } catch (e) {
            // Silently fail
        }
    }

    /** Load content for a single untracked file on-demand. */
    async function loadUntrackedFile(path: string) {
        if (!task) return;
        const meta = diffCache.state.untracked[path];
        const isTruncated = meta?.truncated && diffCache.state.files[path]?.rows.length;
        // Already loaded and not truncated
        if (diffCache.state.files[path]?.rows.length && !isTruncated) return;
        // Mark as loading
        diffCache.setUntrackedMeta(path, { ...(diffCache.state.untracked[path] || {}), loading: true });
        try {
            const data = await getDiffFile(task.project_id, task.id, path, { full: !!isTruncated });
            if (data.lines) {
                diffCache.setFile(path, {
                    path,
                    rows: data.lines.map((line, i) => ({ lineNumber: i + 1, variant: 'added' as const, text: line })),
                    added: data.lines.length,
                    removed: 0,
                    truncated: !!data.truncated,
                    origAdded: data.origAdded,
                });
            } else if (data.isBinary || data.fileSize) {
                diffCache.setFile(path, {
                    path,
                    rows: [],
                    added: 0,
                    removed: 0,
                    fileSize: data.fileSize,
                    isBinary: data.isBinary,
                    truncated: false,
                });
            }
            diffCache.setUntrackedMeta(path, { ...(diffCache.state.untracked[path] || {}), loading: false, truncated: !!data?.truncated, fileSize: data.fileSize ?? meta?.fileSize });
        } catch (e) {
            diffCache.setUntrackedMeta(path, { ...(diffCache.state.untracked[path] || {}), loading: false });
        }
    }

</script>

    <div class="task-detail-page" class:fullscreen={editorFullscreen || diffFullscreen} bind:this={detailEl} tabindex="-1" aria-label="Task detail">
        <!-- Header -->
        <TaskHeader
            {workspaceDir}
            taskId={task.id}
            {sandboxEnabled}
            {badge}
            {worktreeExists}
            {task}
            {onClose}
            onOpenTerminal={onOpenTerminal || (() => uiStore.showTerminal = true)}
            onDelete={handleDelete}
        />

        <!-- Mobile Tab Bar (always rendered, visibility controlled by CSS media query) -->
        <MobileTabBar activeTab={activeTab} showCheckpointTab={checkpointDiffOpen} onTabChange={(tab: 'context' | 'chat' | 'diff' | 'checkpoint') => { activeTab = tab; onActiveTabChange?.(tab); }} />

        <!-- 3-Column Layout -->
        <div bind:this={columnsRef} class="columns"  role="region" aria-label="Task detail panels">
            <!-- Column 1: Board Overview -->
            <div class="column column-context" class:hidden={isMobileCtx.isMobile && activeTab !== 'context'} style:width={boardWidth}>
                <BoardOverview
                    sections={overviewSections}
                    {allTasks}
                    consolidatedSubtaskIds={hierarchy.consolidatedSubtaskIds}
                    sectionDragState={dnd.sectionDragState}
                    {collapsedSections}
                    currentTask={task}
                    childrenMap={hierarchy.childrenMap}
                    expandedTasks={hierarchy.expandedTasks}
                    {onSelectTask}
                    {onRefreshBoard}
                    onToggleSection={toggleSection}
                    onSectionDragOver={dnd.handleSectionDragOver}
                    onSectionDragLeave={dnd.handleSectionDragLeave}
                    onSectionDrop={dnd.handleSectionDrop}
                    onClearDone={handleClearDone}
                    onToggleExpanded={hierarchy.toggleTaskExpanded}
                    onCreateChildTask={(parentId: string) => { uiStore.creatingChildOf = parentId; uiStore.showCreateTask = true; }}
                    onShowCreateTask={() => uiStore.showCreateTask = true}
                    getColumnColor={getSectionColor}
                    {multiSelect}
                />
            </div>

            <!-- Resize handle: Board ↔ Chat -->
            {#if !isMobileCtx.isMobile}
                <ResizeHandle
                    ariaLabel="Resize board and agent panels"
                    onDragStart={handleBoardDragStart}
                    onDragMove={handleBoardDragMove}
                    onDragEnd={handleBoardDragEnd}
                />
            {/if}

            <!-- Column 2: Chat / Agent Interface -->
            <div class="column column-chat" class:hidden={isMobileCtx.isMobile && activeTab !== 'chat'} style:width={chatWidth}>
                <div style="flex:1;min-height:0">
                    {#key task.id}
                        <TaskChat
                            projectId={task.project_id}
                            taskId={task.id}
                            taskPrompt={task.prompt}
                            taskStatus={task.status}
                            taskExecutionState={task.execution_state}
                            taskTitle={task.title}
                            hasStagedChanges={hasStagedChanges}
                            onRefreshDiff={(data) => {
                                // Skip update while loadDiff() is in progress to prevent
                                // WS diff_changed events from overwriting the cache with
                                // empty rows before fetchChangedFiles() completes.
                                if (!diffLoading) diffCache.updateDiff(data);
                            }}
                            onMoveTask={handleMove}
                            onThreadSwitch={() => loadDiff()}
                            onViewChanges={handleViewChanges}
                            onInvalidateDiff={() => {
                                // Clear cache so next WS event triggers a full reload.
                                diffCache.reset();
                            }}
                        />
                    {/key}
                </div>
            </div>

            <!-- Resize handle: Chat ↔ Diff -->
            {#if !isMobileCtx.isMobile}
                <ResizeHandle
                    ariaLabel="Resize agent and changes panels"
                    onDragStart={handleChatDragStart}
                    onDragMove={handleChatDragMove}
                    onDragEnd={handleChatDragEnd}
                />
            {/if}

            <!-- Column 3: Working Changes (Diff) with Checkpoint overlay -->
            <div class="column column-diff" class:hidden={isMobileCtx.isMobile && activeTab !== 'diff' && activeTab !== 'checkpoint'} style:width={diffWidth}>
                <div class="diff-content">
                    {#if editingFile}
                        <!-- Inline file editor (lazy-loaded) -->
                        {#if DiffEditorComponent}
                            {@const DiffEditor = DiffEditorComponent}
                            <DiffEditor
                                filePath={editingFile.path}
                                lineNumber={editingFile.lineNumber}
                                displayPath={editingFile.displayPath}
                                projectId={task.project_id}
                                taskId={task.id}
                                projectPath={projectPath}
                                baseRef={diffCache.state.baseRef}
                                onClose={closeDiffEditor}
                                onToggleFullscreen={toggleEditorFullscreen}
                                mobileFullscreen={editorFullscreen}
                                isMobile={isMobileCtx.isMobile}
                            />
                        {:else}
                            <div class="diff-editor-loading">Loading editor...</div>
                        {/if}
                    {:else}
                        {#if !checkpointDiffOpen}
                            {#if worktreeExists || task.branch}
                                <DiffComparison
                                    commits={commits}
                                    baseCommit={baseCommit}
                                    diffFrom={diffFrom}
                                    diffTo={diffTo}
                                    isDone={task.status === 'done'}
                                    branchTip={task.branch_tip}
                                    branchName={task.branch}
                                    onFromChange={onDiffFromChange}
                                    onToChange={onDiffToChange}
                                    onToggleFullscreen={toggleDiffFullscreen}
                                    isFullscreen={diffFullscreen}
                                />
                            {/if}
                            {#if DiffViewerComponent}
                                {@const DiffViewer = DiffViewerComponent}
                                <DiffViewer committedFiles={committedFiles} stagedFiles={stagedFiles} unstagedFiles={unstagedFiles} stagedUntracked={stagedUntracked} unstagedUntracked={unstagedUntracked} mergedLabel={isDoneTask ? 'Merged' : 'Merged from Subtasks'} onExpandFile={handleExpandDiff} onExpandUntracked={loadUntrackedFile} onLineClick={handleDiffLineClick} onStageFile={handleStageFile} onStageAll={handleStageAll} onUnstageAll={handleUnstageAll} />
                            {:else}
                                <div class="diff-viewer-loading">Loading diff viewer...</div>
                            {/if}
                        {/if}

                        <!-- Checkpoint overlay (overlays entire diff panel) -->
                        {#if checkpointDiffOpen}
                            <CheckpointOverlay
                                files={checkpointDiffFiles}
                                untracked={checkpointDiffUntracked}
                                loading={checkpointDiffLoading}
                                onClose={closeCheckpointDiff}
                                onExpandFile={handleExpandDiff}
                                onLineClick={handleDiffLineClick}
                            />
                        {/if}
                    {/if}
                </div>
            </div>
        </div>

        <MultiSelectBar
            {multiSelect}
            tasks={allTasks}
            projectId={task.project_id}
            onClear={() => multiSelect.clear()}
        />
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
    .diff-content :global(.checkpoint-diff-overlay) {
        position: absolute;
        inset: 0;
        z-index: 5;
    }
    .diff-viewer-loading {
        display: flex;
        align-items: center;
        justify-content: center;
        padding: 48px 24px;
        color: var(--color-text-tertiary);
        font-size: 0.8125rem;
        flex: 1;
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
