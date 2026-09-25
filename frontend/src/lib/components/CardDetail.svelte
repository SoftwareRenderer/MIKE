<script lang="ts">
    import { onMount, onDestroy } from 'svelte';
    import type { Task, TaskCommit } from '$lib/types';
    import type { DiffFile, DiffImageSource } from '$lib/types/diff';
    import { appStore, uiStore, dialogStore, updateTaskInBoard, removeTaskFromBoard } from '$lib/stores';
    import { getStreamingContext, getTaskDiffStatsContext, getExpandedTasksContext } from '$lib/context';
    import { moveTask, deleteTask, getTaskDiff, getTaskCommits, getDiffFile, clearDoneTasks, stageFile, toDiffFiles } from '$lib/api';
    import { enrichRowsWithInlineSegments } from '$lib/utils/diff-parser';
    import { shouldAutoFullscreenDiffEditor } from '$lib/utils/diff-editor-layout';
    import { chatBlurState, chatCollapseState, chatFocusState, chatManualToggleState, chatSendState, type ChatAutoMaximizeRules, type ChatMaximizeState } from '$lib/utils/chat-auto-maximize';
    import { filterTopLevelTaskIds, willConsolidateIntoParent, taskIsInFlight } from '$lib/utils/task-utils';
    import { createStatusLabel } from '$lib/utils/subtask-status';
    import { sandboxMountPath } from '$lib/utils/path';
    import { isVimSwapFile } from '$lib/utils/file-suffix';
    import { isAnyModalOpen } from '$lib/utils/modal-state';
    import { clearHighlightCache } from '$lib/highlighter';
    import { getAppStreamRef } from '$lib/stream-ref';
    import { createTabReturn } from '$lib/composables/use-tab-return';
    import { useIsMobile } from '$lib/composables/use-is-mobile.svelte';
    import { loadAppSetting, saveAppSetting, loadCollapsedSections, saveCollapsedSections } from '$lib/storage';
    import { useBoardDnd } from '$lib/composables/use-board-dnd.svelte';
    import { useSubtaskHierarchy } from '$lib/composables/use-subtask-hierarchy.svelte';
    import { resolveOverviewArrowTarget } from '$lib/composables/use-board-arrow-nav';
    import { canSwitchDetailColumn, resolveDetailColumn, resolveDiffFileTarget, type DetailColumnId } from '$lib/composables/use-task-detail-column-nav';
    import { effectiveToggleFullscreenKey, isEditableTarget, matchShortcut } from '$lib/utils/keyboard';
    import { DiffCache } from '$lib/services/diff-cache.svelte';
    import { t, type MessageKey } from '$lib/i18n';
    import TaskChat from './Chat/TaskChat.svelte';
    import ResizeHandle from './ResizeHandle.svelte';
    import CheckpointOverlay from './CardDetail/CheckpointOverlay.svelte';
    import DiffComparison from './CardDetail/DiffComparison.svelte';
    import MobileTabBar from './CardDetail/MobileTabBar.svelte';
    import { useMemoryAlert } from '$lib/composables/use-memory-alert.svelte';
    import { useLazyComponent } from '$lib/composables/use-lazy-component.svelte';
    import { memoryAlertAction, memoryAlertSentences, projectMemoryLimitMB } from '$lib/utils/memory-stats';
    import TaskHeader from './CardDetail/TaskHeader.svelte';
    import BoardOverview from './CardDetail/BoardOverview.svelte';
    import MultiSelectBar from './MultiSelectBar.svelte';
    import { useMultiSelectActions } from '$lib/composables/use-multi-select-actions.svelte';

    const diffViewer = useLazyComponent(() => import('./DiffViewer.svelte'));
    const diffEditor = useLazyComponent(() => import('./FileEditor.svelte'));

    let { task, tasks, projectPath, projectName, workspaceDir, sandboxEnabled, fsEventQueue, onClose, onSelectTask, onRefreshBoard, onOpenTerminal, onOpenExplorer, onOpenTask, activeTab: activeTabProp = 'chat', onActiveTabChange, floating = false }: { task: Task; tasks?: Task[]; projectPath?: string; projectName?: string; workspaceDir?: string; sandboxEnabled?: boolean; fsEventQueue?: { op: 'create' | 'delete'; path: string }[]; onClose: (status?: string) => void; onSelectTask?: (task: Task) => void; onRefreshBoard?: () => void; onOpenTerminal?: () => void; onOpenExplorer?: () => void; onOpenTask?: () => void; activeTab?: 'context' | 'chat' | 'diff' | 'checkpoint'; onActiveTabChange?: (tab: 'context' | 'chat' | 'diff' | 'checkpoint') => void; /** Floating task view: narrow mobile-style window (Chat only) fixed at
     *  bottom-right over the current view — no navigation, app stays behind. */
    floating?: boolean } = $props();

    let allTasks = $derived(tasks || appStore.board?.tasks || []);

    const memLimitMB = $derived(projectMemoryLimitMB(appStore.projects.find(p => p.id === task.project_id)));
    const memory = useMemoryAlert(() => task, () => memLimitMB);
    const memAlert = $derived(memory.alert);
    const memAlertAction = $derived(memAlert ? memoryAlertAction(memAlert.alert) : null);

    /**
     * Section `id`s are status data (compared, sent to the API); `title` is the English
     * name behind the `Expand/Collapse …` tooltip (`title` text is never translated) and
     * `labelKey` is the visible, translatable section label.
     */
    const overviewSections: { id: string; title: string; labelKey: MessageKey }[] = [
        { id: 'backlog', title: 'Backlog', labelKey: 'board.column.backlog' },
        { id: 'in_progress', title: 'In Progress', labelKey: 'board.column.inProgress' },
        { id: 'review', title: 'Review', labelKey: 'board.column.review' },
        { id: 'done', title: 'Done', labelKey: 'board.column.done' },
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
            title: t('board.clearDone.title'),
            message: t('board.clearDone.message', { count }),
            confirmText: t('board.confirm.delete'),
            cancelText: t('board.confirm.cancel'),
        });
        if (!confirmed) return;
        try {
            await clearDoneTasks(task.project_id);
            // Board stream WebSocket will push the update
        } catch (e) {
            dialogStore.showToast(t('board.clearDone.failed', { error: String(e) }), 'error');
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

    /** The column's label in the active language. */
    const statusLabel = createStatusLabel(overviewSections);

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
    // Includes both staged index changes and committed-but-unmerged changes.
    // When commits are already merged into the target branch, committed files
    // don't count — only staged (index) changes matter.
    // Note: diffStatsMap is declared later (line ~297) but $derived.by evaluates lazily.
    let hasStagedChanges = $derived.by(() => {
        const alreadyMerged = diffStatsMap[task.id]?.already_merged ?? false;
        return stagedFiles.length > 0 || (!alreadyMerged && committedFiles.length > 0);
    });
    let stagedUntracked = $derived([] as { path: string; fileSize?: number; isBinary?: boolean; truncated?: boolean; origAdded?: number; loading?: boolean; file: DiffFile }[]);
    let unstagedUntracked = $derived(isDoneTask ? [] : Object.entries(diffCache.state.untracked).map(([path, meta]) => ({
        path,
        ...meta,
        file: diffCache.state.files[path] || { path, rows: [], added: 0, removed: 0 },
    })));
    // Terminal - worktree exists when task is initialized (branch created via init or start)
    let worktreeExists = $derived(task.worktree_exists || !!task.branch);
    // Note: opening the terminal (button and shortcut) initializes the task
    // worktree first if needed — that logic lives in App.svelte's openTerminal().

    // Commit history for diff comparison
    let commits: TaskCommit[] = $state([]);
    let baseCommit: TaskCommit | null = $state(null);

    // Checkpoint diff overlay (overlays the diff panel)
    let checkpointDiffOpen = $state(false);
    let checkpointDiffHash = $state(''); // which checkpoint hash is currently shown
    let checkpointDiffBaseRef = $state(''); // what that checkpoint was compared against
    let checkpointDiffFiles: DiffFile[] = $state([]);
    let checkpointDiffUntracked: { path: string; fileSize?: number; file: DiffFile; truncated?: boolean; origAdded?: number }[] = $state([]);
    let checkpointDiffLoading = $state(false);
    // Two-dropdown diff: '' means base ref (original branch), 'working' means working tree
    let diffFrom = $state('');
    let diffTo = $state('working');

    // File editor overlay
    let editingFile = $state<{ path: string; lineNumber: number; displayPath: string } | null>(null);
    let editingFilePath = $derived(editingFile?.path ?? '');
    let editingFileLine = $derived(editingFile?.lineNumber ?? 0);
    let editingFileDisplayPath = $derived(editingFile?.displayPath ?? '');
    let editingTaskId = $derived(task?.id ?? '');
    let editingTaskProjectId = $derived(task?.project_id ?? '');
    let editingBaseRef = $derived(diffCache.state.baseRef ?? '');
    let editorFullscreen = $state(false);
    // Diff viewer fullscreen
    let diffFullscreen = $state(false);

    // What each half of the diff panel is comparing. The panel can name a binary file but not what it
    // changed *from*, and two identical sides in the viewer would be a lie.
    let diffImageSource = $derived<DiffImageSource>({
        project: task.project_id,
        task: task.id,
        working: { old: 'HEAD' },
        committed: isDoneTask
            ? { old: task.base_commit || undefined, new: task.branch_tip || task.branch || undefined }
            : { old: diffFrom || diffCache.state.baseRef || undefined, new: diffTo === 'working' ? 'HEAD' : diffTo },
    });
    // A checkpoint's tree against the baseline it was compared with.
    let checkpointImageSource = $derived<DiffImageSource>({
        project: task.project_id,
        task: task.id,
        working: { old: checkpointDiffBaseRef || 'HEAD', new: checkpointDiffHash || 'HEAD' },
    });
    let chatMaximize = $state<ChatMaximizeState>({ fullscreen: false, autoMaximized: false });

    function toggleEditorFullscreen() {
        editorFullscreen = !editorFullscreen;
    }

    function toggleDiffFullscreen() {
        diffFullscreen = !diffFullscreen;
    }

    function toggleChatFullscreen() {
        chatMaximize = chatManualToggleState(chatMaximize);
    }

    function resolveFilePath(relativePath: string): string | null {
        if (!task.id) return null;
        if (sandboxEnabled) {
            if (!projectName) return null;
            return `${sandboxMountPath(projectName)}/${relativePath}`;
        }
        if (!workspaceDir) return null;

        return `${workspaceDir.replace(/\/+$/, '')}/${task.project_id}/${task.id}/${relativePath}`;
    }

    // Consume fs events from the shared queue
    let pendingFsEvent = $state<{ op: 'create' | 'delete'; path: string } | null>(null);
    let lastFsQueueLen = $state(0);
    $effect(() => {
        const len = (fsEventQueue || []).length;
        if (len === 0) {
            lastFsQueueLen = 0;
            return;
        }
        if (len > lastFsQueueLen) {
            pendingFsEvent = (fsEventQueue || [])[len - 1];
            lastFsQueueLen = len;
        }
    });

    // Read on click: the column's rendered width decides, not the window's.
    let diffColEl: HTMLDivElement | null = $state(null);

    function handleDiffLineClick(relativePath: string, lineNumber: number) {
        const fullPath = resolveFilePath(relativePath);
        if (!fullPath) return;
        diffEditor.load();

        if (shouldAutoFullscreenDiffEditor(diffColEl?.offsetWidth ?? 0, mobile)) {
            editorFullscreen = true;
        }
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

    function closeDiffEditor(_saved: boolean) {
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
     * Shows changes between origin and the state AT the checkpoint
     * (all accumulated changes up to and including this checkpoint).
     * Toggles closed if the same checkpoint is clicked again. */
    async function handleViewChanges(commitHash: string, checkpointIndex?: number) {
        if (mobile) { activeTab = 'checkpoint'; onActiveTabChange?.('checkpoint'); }
        // Toggle: if same checkpoint is already open, close it
        if (checkpointDiffOpen && checkpointDiffHash === commitHash) {
            closeCheckpointDiff();
            return;
        }
        checkpointDiffOpen = true;
        checkpointDiffHash = commitHash;
        checkpointDiffLoading = true;
        checkpointDiffBaseRef = '';
        checkpointDiffFiles = [];
        checkpointDiffUntracked = [];
        try {
            const headRef = commitHash || undefined;
            const result = await getTaskDiff(task.project_id, task.id, { headRef, checkpointIndex, parsed: true });
            checkpointDiffBaseRef = result.base_ref || '';
            const truncatedSet = new Set(result.truncated_files || []);
            const origStats = result.original_file_stats || {};
            checkpointDiffFiles = toDiffFiles(result.parsed_files ?? []).filter(f => !isVimSwapFile(f.path)).map(f => ({
                ...f,
                truncated: truncatedSet.has(f.path),
                origAdded: origStats[f.path]?.added,
                origRemoved: origStats[f.path]?.removed,
            }));
            checkpointDiffUntracked = (result.untracked || []).filter(u => !isVimSwapFile(u.path)).map(u => ({
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
        checkpointDiffBaseRef = '';
        if (mobile && activeTab === 'checkpoint') {
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
    let diffStatsMap = $state<Record<string, { hash: string; files: number; additions: number; deletions: number; file_hashes?: Record<string, string>; removed_files?: string[]; staged_paths?: string[]; already_merged?: boolean }>>({});

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
        if (status !== 'backlog' && branch && !diffViewer.component && !diffViewer.loading) {
            loadDiff();
        }
    });

    // Status badge
    let badge = $derived.by(() => {
        const streaming = streamingIds.has(task.id);
        const state = appStore.board?.tasks.find(t => t.id === task.id)?.execution_state ?? task.execution_state;
        if (streaming || state === 'running') return { text: t('board.badge.running'), color: 'var(--color-status-blue)' };
        if (state === 'pulling_image') return { text: t('board.badge.pullingImage'), color: 'var(--color-status-blue)' };
        if (state === 'paused') return { text: t('board.badge.paused'), color: 'var(--color-status-yellow)' };
        if (state === 'idle' && task.status === 'in_progress') return { text: t('board.badge.pending'), color: 'var(--color-status-orange)' };
        if (state === 'failed') return { text: t('board.badge.failed'), color: 'var(--color-status-red)' };
        return { text: t('board.card.idle'), color: 'var(--color-text-tertiary)' };
    });

    // Mobile tab — synced with prop from App.svelte
    let activeTab = $state<'context' | 'chat' | 'diff' | 'checkpoint'>('chat');

    $effect(() => {
        activeTab = activeTabProp;
    });
    const isMobileCtx = useIsMobile();
    // Floating mode reuses the mobile single-column layout.
    const mobile = $derived(floating || isMobileCtx.isMobile);

    // The floating window floats over the board, so it keeps its own size even though
    // it shares the mobile layout.
    const chatAutoMaximizeRules = $derived<ChatAutoMaximizeRules>({
        enabled: appStore.config?.auto_maximize_chat_mobile !== false,
        isMobile: isMobileCtx.isMobile,
        floating,
    });

    function handleChatInputFocus() {
        chatMaximize = chatFocusState(chatAutoMaximizeRules, chatMaximize);
    }

    function handleChatInputBlur() {
        chatMaximize = chatBlurState(chatMaximize);
    }

    function handleChatMessageSent() {
        chatMaximize = chatSendState(chatMaximize);
    }

    // Subtask hierarchy (extracted composable)
    let hierarchy = useSubtaskHierarchy(() => allTasks, expandedTasksContext, () => task.parent_task_id);

    // True when this task actually has child subtasks (only then is the
    // committed-files section legitimately "Merged from Subtasks").
    let hasMergedSubtasks = $derived((hierarchy.childrenMap.get(task.id)?.length ?? 0) > 0);

    // Multi-select actions (wires up floating bar events)
    let multiSelectActions = useMultiSelectActions({
        async onDelete(taskIds, titles) {
            const confirmed = await dialogStore.showConfirm({
                title: t('board.deleteTasks.title'),
                message: t('board.deleteTasks.message', { count: taskIds.length }),
                items: titles,
                confirmText: t('board.confirm.delete'),
                cancelText: t('board.confirm.cancel'),
            });
            if (!confirmed) return;
            // Only send DELETE for top-level tasks, the server recursively deletes descendants
            const topLevelIds = filterTopLevelTaskIds(taskIds, allTasks);
            for (const id of topLevelIds) {
                try {
                    await import('$lib/api').then(({ deleteTask }) => deleteTask(task.project_id, id));
                    removeTaskFromBoard(appStore, id);
                } catch (err) {
                    dialogStore.showToast(t('board.toast.deleteFailed', { error: String(err) }), 'error');
                }
            }
            onRefreshBoard?.();
        },
        async onStart(taskIds) {
            let started = 0;
            for (const id of taskIds) {
                try {
                    // (named `selected`, not `t` — `t` is the i18n function here)
                    const selected = allTasks.find(t => t.id === id);
                    if (!selected) continue;
                    if (selected.parent_task_id) {
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
                    dialogStore.showToast(t('board.toast.startFailed', { error: String(err) }), 'error');
                }
            }
            if (started > 0) {
                dialogStore.showToast(t('board.toast.started', { count: started }), 'success');
            }
        },
        async onMove(taskIds, status) {
            let moved = 0;
            for (const id of taskIds) {
                const selected = allTasks.find(t => t.id === id);
                if (!selected) continue;
                try {
                    const updated = await moveTask(task.project_id, id, status);
                    if (!willConsolidateIntoParent(selected, allTasks, status)) {
                        updateTaskInBoard(appStore, id, updated);
                    }
                    moved++;
                } catch (err) {
                    dialogStore.showToast(t('board.toast.moveFailed', { error: String(err) }), 'error');
                }
            }
            if (moved > 0) {
                dialogStore.showToast(t('board.toast.movedCount', { count: moved, status: statusLabel(status) }), 'success');
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
                    dialogStore.showToast(t('board.toast.modelUpdateFailed', { error: String(err) }), 'error');
                }
            }
            if (updated > 0) {
                dialogStore.showToast(t('board.toast.modelUpdated', { count: updated }), 'success');
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

    function handleBoardDragStart(_ev: MouseEvent | TouchEvent, startX: number) {
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

    function handleChatDragStart(_ev: MouseEvent | TouchEvent, startX: number) {
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
                // A floating task view owns Escape while it's open — a background
                // full task view must not close at the same time.
                if (!floating && uiStore.floatingTask) return;
                // Don't close when user is typing in an input/textarea
                const tag = (document.activeElement as HTMLElement)?.tagName.toLowerCase();
                if (tag === 'input' || tag === 'textarea' || tag === 'select') return;
                if (diffFullscreen) {
                    diffFullscreen = false;
                } else if (chatMaximize.fullscreen) {
                    chatMaximize = chatCollapseState();
                } else if (checkpointDiffOpen) {
                    closeCheckpointDiff();
                } else if (multiSelect.hasSelection) {
                    multiSelect.clear();
                } else {
                    onClose();
                }
                return;
            }
            // Toggle maximize/minimize (default Ctrl+Up) for the focused column:
            // chat -> chat fullscreen; diff -> editor while a file is being
            // edited, else diff. No maximize button in the checkpoint overlay
            // or no-branch/worktree state -> no-op.
            if (e.key === 'ArrowUp' && !e.repeat && !e.defaultPrevented) {
                const key = effectiveToggleFullscreenKey(appStore.config);
                if (key && matchShortcut(key, e)) {
                    const active = document.activeElement;
                    if (active instanceof HTMLElement && detailEl) {
                        const chatCol = detailEl.querySelector<HTMLElement>('.column-chat');
                        if (chatCol?.contains(active)) {
                            e.preventDefault();
                            toggleChatFullscreen();
                            return;
                        }
                        const diffCol = detailEl.querySelector<HTMLElement>('.column-diff');
                        if (diffCol?.contains(active)) {
                            if (editingFile) {
                                e.preventDefault();
                                toggleEditorFullscreen();
                            } else if (!checkpointDiffOpen && (worktreeExists || task.branch)) {
                                e.preventDefault();
                                toggleDiffFullscreen();
                            }
                        }
                    }
                    return;
                }
            }
            if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
                // Column-aware navigation: Board -> overview cards, Chat ->
                // page-scroll the messages, Diff -> diff file headers. Focus
                // outside all columns anchors to the board overview.
                if (e.altKey || e.ctrlKey || e.metaKey || e.shiftKey) return;
                const active = document.activeElement;
                if (active instanceof HTMLElement && isEditableTarget(active)) return;
                if (isAnyModalOpen()) return;
                if (mobile && activeTab !== 'context') return;
                if (!detailEl) return;
                const chatCol = detailEl.querySelector<HTMLElement>('.column-chat');
                if (active instanceof HTMLElement && chatCol?.contains(active)) {
                    const scroller = chatCol.querySelector<HTMLElement>('.chat-messages');
                    if (!scroller) return;
                    e.preventDefault();
                    scroller.scrollBy({ top: e.key === 'ArrowDown' ? scroller.clientHeight : -scroller.clientHeight, behavior: 'smooth' });
                    return;
                }
                const diffCol = detailEl.querySelector<HTMLElement>('.column-diff');
                if (!editingFile && active instanceof HTMLElement && diffCol?.contains(active)) {
                    const sections = Array.from(diffCol.querySelectorAll<HTMLElement>('.diff-file-section'));
                    const headers = sections
                        .map(s => s.querySelector<HTMLElement>('button.diff-file-header'))
                        .filter((h): h is HTMLElement => !!h);
                    const target = resolveDiffFileTarget({ sections, headers, activeElement: active, key: e.key });
                    if (!target) return;
                    e.preventDefault();
                    target.focus();
                    target.scrollIntoView({ block: 'nearest', inline: 'nearest' });
                    return;
                }
                const cards = Array.from(detailEl.querySelectorAll<HTMLElement>('.task-list .task-wrapper > .card'));
                const target = resolveOverviewArrowTarget({ cards, activeElement: active, key: e.key });
                if (!target) return;
                e.preventDefault();
                target.focus();
                target.scrollIntoView({ block: 'nearest', inline: 'nearest' });
            }
            if (e.key === 'ArrowLeft' || e.key === 'ArrowRight') {
                // Cyclic column focus switching: Board -> Chat -> Diff -> Board.
                // Plain arrows keep native text-cursor movement in editable
                // focus (incl. the chat input); Ctrl+Alt+arrows switch columns
                // from any focus
                const active = document.activeElement;
                if (!canSwitchDetailColumn({
                    ctrlKey: e.ctrlKey,
                    altKey: e.altKey,
                    metaKey: e.metaKey,
                    shiftKey: e.shiftKey,
                    defaultPrevented: e.defaultPrevented,
                    isMobile: mobile,
                    blocked: isAnyModalOpen(),
                    editingFile: !!editingFile,
                    diffFullscreen,
                    chatFullscreen: chatMaximize.fullscreen,
                    isEditable: active instanceof HTMLElement && isEditableTarget(active),
                })) return;
                if (!detailEl) return;
                const boardCol = detailEl.querySelector<HTMLElement>('.column-context');
                const chatCol: HTMLElement | null = detailEl.querySelector('.column-chat');
                const diffCol = detailEl.querySelector<HTMLElement>('.column-diff');
                const current: DetailColumnId | null = active instanceof HTMLElement
                    ? (boardCol?.contains(active) ? 'board' : chatCol?.contains(active) ? 'chat' : diffCol?.contains(active) ? 'diff' : null)
                    : null;
                const next = resolveDetailColumn(['board', 'chat', 'diff'], current, e.key);
                if (!next) return;
                let target: HTMLElement | null = null;
                if (next === 'board') {
                    // Current card, else first overview card, else the column container.
                    target =
                        (active instanceof HTMLElement && boardCol?.contains(active) ? (active.closest('.task-list .task-wrapper > .card') as HTMLElement | null) : null) ||
                        detailEl.querySelector<HTMLElement>('.task-list .task-wrapper > .card') ||
                        boardCol;
                } else if (next === 'chat') {
                    target = chatCol?.querySelector<HTMLElement>('textarea') || chatCol?.querySelector<HTMLElement>('.chat-messages') || chatCol;
                } else {
                    target = diffCol?.querySelector<HTMLElement>('button.diff-file-header') || diffCol;
                }
                if (!target) return;
                e.preventDefault();
                target.focus();
                target.scrollIntoView({ block: 'nearest', inline: 'nearest' });
            }
        }
        window.addEventListener('keydown', handleKey);

        // The diff is mirrored by `task_diff_stats` on the project channel, so this screen can answer for
        // itself. A reload is not free either: it closes the open file and the checkpoint overlay.
        const returnWatcher = createTabReturn({
            mayBeBehind: () => taskIsInFlight(task.execution_state) || streamingIds.has(task.id),
            msSinceFrame: () => getAppStreamRef()?.msSinceFrame() ?? null,
            msSinceUnpainted: () => getAppStreamRef()?.msSinceUnpainted() ?? null,
            onReturn: () => { void loadDiff(); },
        });

        return () => {
            unsubDiff();
            unsubsMultiSelect();
            returnWatcher.destroy();
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
            if (status === 'done') closeOnDone();
        } catch (e) {
            dialogStore.showToast(t('task.toast.moveFailed', { error: String(e) }), 'error');
        }
    }

    function closeOnDone(): void {
        onClose('done');
    }

    async function handleDelete() {
        if (!task) return;
        const confirmed = await dialogStore.showConfirm({
            title: t('board.deleteTask.title'),
            message: t('board.deleteTask.message'),
            confirmText: t('board.confirm.delete'),
            cancelText: t('board.confirm.cancel'),
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
            dialogStore.showToast(t('board.toast.deleteFailed', { error: String(e) }), 'error');
        }
    }

    let diffLoading = $state(false);
    // Set when loadDiff() is requested while another load is in flight. The current
    // load finishes, then a reload runs so a thread switch during a slow diff load is
    // not dropped (otherwise the panel stays stale until a manual refresh).
    let diffReloadQueued = $state(false);

    /** Reload the full diff. Aborts in-flight delta fetches and resets overlay state.
     * Used after thread switch or task change to ensure a clean slate.
     * For done tasks, uses branch_tip to show the diff of committed changes. */
    async function loadDiff() {
        // A backlog task has no worktree unless it was sent back from active
        if (!task || (task.status === 'backlog' && !worktreeExists)) return;
        if (diffLoading) {
            // A load is in progress; a newer thread switch/task change supersedes it.
            diffReloadQueued = true;
            return;
        }
        // Reset overlay and comparison state
        checkpointDiffOpen = false;
        checkpointDiffHash = '';
        editingFile = null;
        diffFrom = '';
        diffTo = 'working';
        clearHighlightCache();
        diffLoading = true;
        diffReloadQueued = false;
        // Preload the diff viewer chunk while fetching diff data
        diffViewer.load();
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
            // If a newer request arrived during the load, run it now so the panel
            // reflects the latest thread/task instead of staying stale.
            if (diffReloadQueued) {
                diffReloadQueued = false;
                void loadDiff();
            }
        }
    }

    async function loadCommits() {
        if (!task || (task.status === 'backlog' && !worktreeExists)) return;
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
        if (!task || (task.status === 'backlog' && !worktreeExists)) return;
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
            await diffCache.expandFile(path);
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
                diffCache.patchFile(path, {
                    rows: data.lines.map((line, i) => ({ lineNumber: i + 1, variant: 'added' as const, text: line })),
                    added: data.lines.length,
                    removed: 0,
                    truncated: !!data.truncated,
                    origAdded: data.origAdded,
                });
            } else if (data.isBinary || data.fileSize) {
                diffCache.patchFile(path, {
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

    <div class="task-detail-page" class:floating={floating} class:fullscreen={editorFullscreen || diffFullscreen} class:chat-fullscreen={chatMaximize.fullscreen} bind:this={detailEl} tabindex="-1" aria-label="Task detail">
        <!-- Header -->
        <TaskHeader
            {workspaceDir}
            taskId={task.id}
            {sandboxEnabled}
            {badge}
            {task}
            {onClose}
            onOpenTerminal={onOpenTerminal}
            onOpenExplorer={onOpenExplorer}
            onOpenTask={onOpenTask}
            {floating}
            onDelete={handleDelete}
        />

        <!-- Stated once above every tab, and it stays until the user acknowledges it. -->
        {#if memAlert}
            <div class="memory-alert-bar" role="alert">
                <span class="memory-alert-text">
                    <span class="memory-alert-title">{t('memory.alert.title')}</span>
                    {#each memoryAlertSentences(memAlert.alert) as line}<span class="memory-alert-line">{line}</span>{/each}
                    {#if memAlertAction}<span class="memory-alert-line">{memAlertAction}</span>{/if}
                </span>
                <button class="memory-alert-dismiss" onclick={() => { void memory.dismiss(); }}>
                    {t('memory.alert.dismiss')}
                </button>
            </div>
        {/if}

        <!-- Mobile Tab Bar (visibility controlled by CSS media query; omitted in
             the floating view — it is chat-only, no column switching) -->
        {#if !floating}
        <MobileTabBar activeTab={activeTab} showCheckpointTab={checkpointDiffOpen} onTabChange={(tab: 'context' | 'chat' | 'diff' | 'checkpoint') => { activeTab = tab; onActiveTabChange?.(tab); }} />
        {/if}

        <!-- 3-Column Layout -->
        <div bind:this={columnsRef} class="columns"  role="region" aria-label="Task detail panels">
            <!-- Column 1: Board Overview -->
            <div class="column column-context" tabindex="-1" class:hidden={floating || (mobile && activeTab !== 'context')} style:width={boardWidth}>
                <BoardOverview
                    sections={overviewSections.map(section => ({ ...section, label: t(section.labelKey) }))}
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
            {#if !mobile}
                <ResizeHandle
                    ariaLabel="Resize board and agent panels"
                    onDragStart={handleBoardDragStart}
                    onDragMove={handleBoardDragMove}
                    onDragEnd={handleBoardDragEnd}
                />
            {/if}

            <!-- Column 2: Chat / Agent Interface -->
            <div class="column column-chat" class:hidden={mobile && activeTab !== 'chat'} style:width={chatWidth}>
                <div style="flex:1;min-height:0">
                    {#key task.id}
                        <TaskChat
                            projectId={task.project_id}
                            taskId={task.id}
                            taskPrompt={task.prompt}
                            taskStatus={task.status}
                            taskExecutionState={task.execution_state}
                            taskSummarizing={task.summarizing}
                            hasStagedChanges={hasStagedChanges}
                            onToggleFullscreen={toggleChatFullscreen}
                            isChatFullscreen={chatMaximize.fullscreen}
                            onChatInputFocus={handleChatInputFocus}
                            onChatInputBlur={handleChatInputBlur}
                            onMessageSent={handleChatMessageSent}
                            onRefreshDiff={(data) => {
                                // Skip update while loadDiff() is in progress to prevent
                                // WS diff_changed events from overwriting the cache with
                                // empty rows before fetchChangedFiles() completes.
                                if (!diffLoading) diffCache.updateDiff(data);
                            }}
                            onMoveTask={handleMove}
                            onTaskDone={closeOnDone}
                            onThreadSwitch={() => {
                                // Reset the diff cache synchronously so stale rows from
                                // the previous thread can't leak into the new thread's view
                                diffCache.reset();
                                loadDiff();
                            }}
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
            {#if !mobile}
                <ResizeHandle
                    ariaLabel="Resize agent and changes panels"
                    onDragStart={handleChatDragStart}
                    onDragMove={handleChatDragMove}
                    onDragEnd={handleChatDragEnd}
                />
            {/if}

            <!-- Column 3: Working Changes (Diff) with Checkpoint overlay -->
            <div class="column column-diff" tabindex="-1" class:hidden={floating || (mobile && (activeTab !== 'diff' && activeTab !== 'checkpoint'))} style:width={diffWidth} bind:this={diffColEl}>
                <div class="diff-content">
                    {#if editingFile}
                        <!-- Inline file editor (lazy-loaded) -->
                        {#if diffEditor.component}
                            {@const FileEditor = diffEditor.component}
                            <FileEditor
                                mode="diff"
                                filePath={editingFilePath}
                                targetLine={editingFileLine}
                                displayPath={editingFileDisplayPath}
                                projectId={editingTaskProjectId}
                                taskId={editingTaskId}
                                projectPath={projectPath}
                                workspaceDir={workspaceDir}
                                baseRef={editingBaseRef}
                                pendingFsEvent={pendingFsEvent}
                                onClose={closeDiffEditor}
                                onToggleFullscreen={toggleEditorFullscreen}
                                mobileFullscreen={editorFullscreen}
                                closeOnEscape={!mobile}
                            />
                        {:else}
                            <div class="diff-editor-loading">{t('task.loadingEditor')}</div>
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
                                {#if diffViewer.component}
                                    {@const DiffViewer = diffViewer.component}
                                    <DiffViewer committedFiles={committedFiles} stagedFiles={stagedFiles} unstagedFiles={unstagedFiles} stagedUntracked={stagedUntracked} unstagedUntracked={unstagedUntracked} loading={diffLoading} mergedLabel={isDoneTask ? t('task.diff.merged') : hasMergedSubtasks ? t('task.diff.mergedFromSubtasks') : t('task.diff.committedInTask')} onExpandFile={handleExpandDiff} onExpandUntracked={loadUntrackedFile} onLineClick={handleDiffLineClick} onStageFile={handleStageFile} onStageAll={handleStageAll} onUnstageAll={handleUnstageAll} imageSource={diffImageSource} />
                                {:else}
                                    <div class="diff-viewer-loading">{t('task.loadingDiffViewer')}</div>
                                {/if}
                            {:else}
                                <div class="diff-viewer-empty">{t('task.noChanges')}</div>
                            {/if}
                        {/if}

                        <!-- Checkpoint overlay (overlays entire diff panel) -->
                        {#if checkpointDiffOpen}
                            <CheckpointOverlay
                                files={checkpointDiffFiles}
                                untracked={checkpointDiffUntracked}
                                loading={checkpointDiffLoading}
                                imageSource={checkpointImageSource}
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
    .memory-alert-bar {
        display: flex;
        align-items: center;
        justify-content: space-between;
        gap: 12px;
        padding: 8px 14px;
        background: rgba(248, 81, 73, 0.1);
        border-bottom: 1px solid rgba(248, 81, 73, 0.3);
    }
    .memory-alert-text {
        display: flex;
        flex-wrap: wrap;
        gap: 2px 10px;
        min-width: 0;
    }
    .memory-alert-title {
        color: var(--color-status-red);
        font-size: 0.8125rem;
        font-weight: 600;
    }
    .memory-alert-line {
        color: var(--color-text-secondary);
        font-size: 0.75rem;
    }
    .memory-alert-dismiss {
        flex-shrink: 0;
        padding: 4px 10px;
        font-size: 0.75rem;
        color: var(--color-text-primary);
        background: var(--color-surface-3);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        cursor: pointer;
    }
    .memory-alert-dismiss:hover {
        border-color: var(--color-text-tertiary);
    }
    /* Floating task view: narrow mobile-style window over the current view.
       bottom: 44px sits above the editor footer (≈28px) + gap. */
    .task-detail-page.floating {
        position: fixed;
        top: auto;
        right: 16px;
        bottom: 44px;
        width: 480px;
        height: 72vh;
        max-width: calc(100vw - 32px);
        z-index: 900;
        flex: none;
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-lg);
        box-shadow: 0 12px 40px rgba(0, 0, 0, 0.35);
    }
    @media (max-width: 768px) {
        .task-detail-page.floating {
            inset: 0;
            width: 100vw;
            max-width: none;
            height: 100dvh;
            border: none;
            border-radius: 0;
            box-shadow: none;
        }
    }
    /* Chat-only: the visible column must fill the window. Its inline width is
       a 3-column share (e.g. 35%) — a flex item with an explicit width does
       not stretch, and the ≤768px media query below doesn't apply on desktop. */
    .task-detail-page.floating .column:not(.hidden) {
        flex: 0 0 100% !important;
        width: 100% !important;
        min-width: 0;
        border-right: none;
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
    .diff-viewer-empty {
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

    /* Inset focus ring — the global ring's 2px offset is clipped by .columns. */
    .column-context:focus-visible,
    .column-diff:focus-visible {
        outline-offset: -2px;
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
    .task-detail-page.fullscreen :global(.resize-handle) {
        display: none !important;
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

    /* Chat fullscreen mode */
    .task-detail-page.chat-fullscreen {
        position: fixed;
        top: 0;
        left: 0;
        right: 0;
        bottom: 0;
        z-index: 1000;
        margin: 0;
    }
    .task-detail-page.chat-fullscreen :global(.header),
    .task-detail-page.chat-fullscreen :global(.mobile-tab-bar) {
        display: none !important;
    }
    .task-detail-page.chat-fullscreen .columns {
        height: 100%;
    }
    .task-detail-page.chat-fullscreen :global(.resize-handle) {
        display: none !important;
    }
    .task-detail-page.chat-fullscreen :global(.chat-review-bar) {
        display: none !important;
    }
    .task-detail-page.chat-fullscreen :global(.model-selector) {
        display: none !important;
    }
    .task-detail-page.chat-fullscreen .column-context,
    .task-detail-page.chat-fullscreen .column-diff {
        display: none !important;
    }
    .task-detail-page.chat-fullscreen .column-chat {
        width: 100% !important;
        flex: 1 !important;
        min-width: 100% !important;
    }
</style>
