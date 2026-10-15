<script lang="ts">
    import { onMount, onDestroy } from 'svelte';
    import type { Task, TaskCommit } from '$lib/types';
    import type { DiffFile, DiffImageSource } from '$lib/types/diff';
    import { appStore, uiStore, dialogStore, updateTaskInBoard, removeTaskFromBoard } from '$lib/stores';
    import { streamingTaskStore } from '$lib/stores/streaming-tasks.svelte';
    import { diffStatsStore } from '$lib/stores/diff-stats.svelte';
    import { moveTask, deleteTask, getTaskDiff, getTaskCommits, getDiffFile, clearDoneTasks, stageTaskPaths, toDiffFiles, getTask } from '$lib/api';
    import type { GitBatchResult } from '$lib/api';
    import { fetchTaskSync } from '$lib/services/task-sync';
    import { resyncPlan, type HeldRow } from '$lib/utils/task-sync';
    import { diffBaseMoved, diffHeadRef } from '$lib/utils/diff-base';
    import { enrichRowsWithInlineSegments } from '$lib/utils/diff-parser';
    import { shouldAutoFullscreenDiffEditor } from '$lib/utils/diff-editor-layout';
    import { chatBlurState, chatCollapseState, chatFocusState, chatManualToggleState, chatSendState, type ChatAutoMaximizeRules, type ChatMaximizeState } from '$lib/utils/chat-auto-maximize';
    import { willConsolidateIntoParent } from '$lib/utils/task-utils';
    import { createStatusLabel } from '$lib/utils/subtask-status';
    import { createBulkTaskActions } from '$lib/utils/bulk-task-actions';
    import { sandboxMountPath } from '$lib/utils/path';
    import { isVimSwapFile } from '$lib/utils/file-suffix';
    import { isAnyModalOpen } from '$lib/utils/modal-state';
    import { clearHighlightCache } from '$lib/utils/highlight-cache';
    import { createTabReturn } from '$lib/composables/use-tab-return';
    import { useIsMobile } from '$lib/composables/use-is-mobile.svelte';
    import { loadAppSetting, saveAppSetting, loadCollapsedSections, saveCollapsedSections } from '$lib/storage';
    import { useBoardDnd } from '$lib/composables/use-board-dnd.svelte';
    import { useSubtaskHierarchy } from '$lib/composables/use-subtask-hierarchy.svelte';
    import { resolveOverviewArrowTarget } from '$lib/composables/use-board-arrow-nav';
    import { asChatScrollKey, canSwitchDetailColumn, chatScrollDelta, resolveDetailColumn, resolveDiffFileTarget, type DetailColumnId } from '$lib/composables/use-task-detail-column-nav';
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
    import EmptyState from './EmptyState.svelte';
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
     * Section `id`s are status data (compared, sent to the API); `labelKey` is both the visible
     * section label and the name in the `Expand/Collapse …` tooltip.
     */
    const overviewSections: { id: string; labelKey: MessageKey }[] = [
        { id: 'backlog', labelKey: 'board.column.backlog' },
        { id: 'in_progress', labelKey: 'board.column.inProgress' },
        { id: 'review', labelKey: 'board.column.review' },
        { id: 'done', labelKey: 'board.column.done' },
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
        stageTaskPaths,
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
    // when the user stages/unstages files.
    // Includes both staged index changes and committed-but-unmerged changes.
    // When commits are already merged into the target branch, committed files
    // don't count — only staged (index) changes matter.
    let hasStagedChanges = $derived.by(() => {
        const alreadyMerged = diffStatsStore.get(task.id)?.already_merged ?? false;
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
    let checkpointDiffLabel = $state(''); // the checkpoint's own words, from the chat band that opened it
    let checkpointDiffFiles: DiffFile[] = $state([]);
    let checkpointDiffLoading = $state(false);
    let checkpointDiffError = $state('');
    /** The overlay's header names both sides, so the panel never shows a comparison it cannot identify. */
    let checkpointDiffRefs = $derived(
        `${shortRef(checkpointDiffBaseRef)} → ${shortRef(checkpointDiffHash)}`,
    );
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

        if (shouldAutoFullscreenDiffEditor(diffColEl?.offsetWidth ?? 0, mobile, uiStore.autoMaximizeCodeViewer)) {
            editorFullscreen = true;
        }
        editingFile = { path: fullPath, lineNumber, displayPath: relativePath };
    }

    /** The section whose whole-section action is running, so only that header spins. */
    let busySection = $state<'staged' | 'unstaged' | null>(null);
    let pendingPaths = $derived(new Set(diffCache.state.pending));
    let stageBusyLabel = $derived(diffCache.state.pending.length > 0 || busySection ? t('diff.stagingBusy') : '');

    /** Reports what git refused. A batch that moved some paths is a result, not an exception. */
    async function reportStageBatch(run: () => Promise<GitBatchResult | null>, stage: boolean) {
        try {
            const answer = await run();
            if (answer && answer.failed.length > 0) {
                dialogStore.showToast(t(stage ? 'git.stageFailed' : 'git.unstageFailed', { count: answer.failed.length, error: answer.failed[0].error }), 'error');
            }
        } catch (e) {
            dialogStore.showToast(String(e), 'error');
        }
    }

    /** Stage or unstage a single file. */
    async function handleStageFile(path: string, stage: boolean) {
        if (!task) return;
        await reportStageBatch(() => diffCache.stagePaths([path], stage), stage);
    }

    /** Stage every tracked file the index does not hold. */
    async function handleStageAll() {
        if (!task || busySection) return;
        busySection = 'unstaged';
        try {
            await reportStageBatch(() => diffCache.stageAll(), true);
        } finally {
            busySection = null;
        }
    }

    /** Unstage every tracked file the index holds. */
    async function handleUnstageAll() {
        if (!task || busySection) return;
        busySection = 'staged';
        try {
            await reportStageBatch(() => diffCache.unstageAll(), false);
        } finally {
            busySection = null;
        }
    }

    function closeDiffEditor(_saved: boolean) {
        editorFullscreen = false;
        editingFile = null;
    }

    /** A ref as the panel names it: the words the reader can compare, not a 40-character object id. */
    function shortRef(ref: string): string {
        if (!ref) return t('task.diff.originalBranch');
        return ref.length > 12 ? ref.slice(0, 12) : ref;
    }

    function onDiffFromChange(value: string) {
        diffFrom = value;
    }

    function onDiffToChange(value: string) {
        diffTo = value;
    }

    /**
     * "View changes" from a checkpoint band: what this thread had changed by that point, measured from the
     * baseline the Changes panel uses. Cumulative by contract — the snapshot is a tree, so it answers the
     * state as of the band and never the work that came after it. Clicking the same band again closes it.
     */
    async function loadCheckpointDiff(commitHash: string, commitLabel: string) {
        checkpointDiffOpen = true;
        checkpointDiffHash = commitHash;
        checkpointDiffLabel = commitLabel;
        checkpointDiffLoading = true;
        checkpointDiffError = '';
        checkpointDiffBaseRef = '';
        checkpointDiffFiles = [];
        try {
            const result = await getTaskDiff(task.project_id, task.id, { headRef: commitHash || undefined, parsed: true });
            checkpointDiffBaseRef = result.base_ref || '';
            const truncatedSet = new Set(result.truncated_files || []);
            const origStats = result.original_file_stats || {};
            checkpointDiffFiles = toDiffFiles(result.parsed_files ?? []).filter(f => !isVimSwapFile(f.path)).map(f => ({
                ...f,
                truncated: truncatedSet.has(f.path),
                origAdded: origStats[f.path]?.added,
                origRemoved: origStats[f.path]?.removed,
            }));
        } catch (e) {
            // A snapshot that will not read is not a checkpoint with no changes: say which of the two this is.
            checkpointDiffError = String(e);
            checkpointDiffFiles = [];
        }
        checkpointDiffLoading = false;
    }

    async function handleViewChanges(commitHash: string, commitLabel?: string) {
        if (mobile) { activeTab = 'checkpoint'; onActiveTabChange?.('checkpoint'); }
        if (checkpointDiffOpen && checkpointDiffHash === commitHash) {
            closeCheckpointDiff();
            return;
        }
        await loadCheckpointDiff(commitHash, commitLabel || '');
    }

    /**
     * Expand one truncated file inside the checkpoint overlay. The answer comes from the two trees that
     * comparison names — `/diff/file` without refs answers the working tree, and writing those rows here
     * would show later work under the checkpoint's own heading.
     */
    async function expandCheckpointFile(path: string) {
        if (!checkpointDiffHash) return;
        try {
            const data = await getDiffFile(task.project_id, task.id, path, {
                full: true,
                headRef: checkpointDiffHash,
                baseRef: checkpointDiffBaseRef || undefined,
            });
            const rows = data.rows ? enrichRowsWithInlineSegments(data.rows) : [];
            checkpointDiffFiles = checkpointDiffFiles.map(f => f.path === path ? {
                ...f,
                rows,
                added: rows.filter(r => r.variant === 'added').length,
                removed: rows.filter(r => r.variant === 'removed').length,
                truncated: !!data.truncated,
                origAdded: data.origAdded ?? f.origAdded,
                origRemoved: data.origRemoved ?? f.origRemoved,
            } : f);
        } catch {
            // The truncated rows stay on screen; they already say they are partial.
        }
    }

    /** Every piece of the checkpoint view's state. The overlay renders only while `checkpointDiffOpen` is
     * set, and a half-cleared view would keep naming the comparison it no longer shows. */
    function resetCheckpointDiff() {
        checkpointDiffOpen = false;
        checkpointDiffHash = '';
        checkpointDiffLabel = '';
        checkpointDiffError = '';
        checkpointDiffBaseRef = '';
        checkpointDiffFiles = [];
    }

    function closeCheckpointDiff() {
        resetCheckpointDiff();
        if (mobile && activeTab === 'checkpoint') {
            activeTab = 'diff';
            onActiveTabChange?.('diff');
        }
    }

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
        const streaming = streamingTaskStore.has(task.id);
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
    let hierarchy = useSubtaskHierarchy(() => allTasks, () => task.parent_task_id);

    // True when this task actually has child subtasks (only then is the
    // committed-files section legitimately "Merged from Subtasks").
    let hasMergedSubtasks = $derived((hierarchy.childrenMap.get(task.id)?.length ?? 0) > 0);

    // Multi-select actions (wires up floating bar events)
    let multiSelectActions = useMultiSelectActions(createBulkTaskActions({
        tasks: () => allTasks,
        projectId: () => task.project_id,
        statusLabel,
        api: {
            deleteTask: async (proj, taskId) => (await import('$lib/api')).deleteTask(proj, taskId),
            startTask: async (proj, taskId) => (await import('$lib/api')).startTask(proj, taskId),
            startSubtask: async (proj, taskId) => (await import('$lib/api')).startSubtask(proj, taskId),
            updateTask: async (proj, taskId, update) => (await import('$lib/api')).updateTask(proj, taskId, update),
        },
        board: {
            update: (updated) => updateTaskInBoard(appStore, updated.id, updated),
            remove: (taskId) => removeTaskFromBoard(appStore, taskId),
        },
        dialog: dialogStore,
        moveTask: applyMove,
        onRefreshBoard: () => onRefreshBoard?.(),
    }));

    /** Send one move and land it on the board overview. */
    async function applyMove(selected: Task, targetStatus: string): Promise<boolean> {
        try {
            const updated = await moveTask(task.project_id, selected.id, targetStatus);
            if (!willConsolidateIntoParent(selected, allTasks, targetStatus)) {
                updateTaskInBoard(appStore, selected.id, updated);
            }
            return true;
        } catch (err) {
            dialogStore.showToast(t('board.toast.moveFailed', { error: String(err) }), 'error');
            return false;
        }
    }

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
            const scrollKey = asChatScrollKey(e.key);
            if (scrollKey) {
                // Column-aware navigation: Chat -> page-scroll the messages, Diff ->
                // diff file headers, Board -> overview cards. Focus outside all columns
                // anchors to the board overview.
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
                    scroller.scrollBy({ top: chatScrollDelta(scrollKey, scroller.clientHeight), behavior: 'smooth' });
                    return;
                }
                // The other two columns are a ring of focusable rows: a page is not a step on it.
                if (scrollKey === 'PageDown' || scrollKey === 'PageUp') return;
                const diffCol = detailEl.querySelector<HTMLElement>('.column-diff');
                if (!editingFile && active instanceof HTMLElement && diffCol?.contains(active)) {
                    const sections = Array.from(diffCol.querySelectorAll<HTMLElement>('.diff-file-section'));
                    const headers = sections
                        .map(s => s.querySelector<HTMLElement>('button.diff-file-header'))
                        .filter((h): h is HTMLElement => !!h);
                    const target = resolveDiffFileTarget({ sections, headers, activeElement: active, key: scrollKey });
                    if (!target) return;
                    e.preventDefault();
                    target.focus();
                    target.scrollIntoView({ block: 'nearest', inline: 'nearest' });
                    return;
                }
                const cards = Array.from(detailEl.querySelectorAll<HTMLElement>('.task-list .task-wrapper > .card'));
                const target = resolveOverviewArrowTarget({ cards, activeElement: active, key: scrollKey });
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

        // `task_diff_stats` mirrors the diff only while this window keeps running and reading, so a return
        // asks the server what moved.
        const returnWatcher = createTabReturn({
            revalidateOnReturn: true,
            onReturn: () => { void reconcileDiff(); },
        });

        return () => {
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
        diffCache.processStats(diffStatsStore.get(task.id) ?? undefined, diffLoading);
    });

    // A file the person deletes outside a turn is a diff change the stream may never announce: the
    // worktree watcher throttles, and a create with its delete inside one window computes as no change
    // against what the watcher last computed, so it broadcasts nothing. The fs_event names the path, and
    // a row this panel holds for it came from a manifest read, so this is where the panel can learn the
    // file is gone.
    $effect(() => {
        const event = pendingFsEvent;
        if (!event) return;
        const headRef = diffHeadRef(task);
        void diffCache.reconcileDeletion(event, headRef ? { headRef } : {});
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
    /** The base the diff on screen was drawn against, '' for the working tree. Two bases cannot share one
     * view, so a moved base resets the panel. */
    let loadedHeadRef = '';

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
        resetCheckpointDiff();
        editingFile = null;
        diffFrom = '';
        diffTo = 'working';
        clearHighlightCache();
        diffLoading = true;
        diffReloadQueued = false;
        // Preload the diff viewer chunk while fetching diff data
        diffViewer.load();
        try {
            // For done tasks, fetch the diff of committed changes: branch_tip (captured at commit time) if
            // there is one, else the branch. `diffHeadRef` owns that rule, and the return path compares
            // against the value written here.
            loadedHeadRef = diffHeadRef(task);
            const diffOptions = loadedHeadRef ? { headRef: loadedHeadRef } : {};
            await Promise.all([loadCommits(), diffCache.loadDiff(() => {}, diffOptions)]);
        } finally {
            diffLoading = false;
            // Apply any pending staged_paths from a WebSocket broadcast that
            // arrived during the load (the effect was blocked by diffLoading).
            const stat = diffStatsStore.get(task.id);
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

    /**
     * What this window does about having been elsewhere: ask which part of the view moved, then bring back
     * that part. `taskIsInFlight` read at return time answers "no" in exactly the case that matters — a run
     * that started and finished while the window was elsewhere.
     */
    async function reconcileDiff(): Promise<void> {
        const digest = await fetchTaskSync(task.project_id, task.id);
        // No answer says nothing; the project channel still carries the stats frames.
        if (!digest) return;
        const heldRow: HeldRow = {
            status: task.status,
            executionState: task.execution_state,
            updatedAt: task.updated_at,
            branchTip: task.branch_tip ?? '',
            summarizing: task.summarizing,
            mergeConflict: task.has_merge_conflict,
        };
        const verdict = resyncPlan({
            row: heldRow,
            diff: {
                hash: diffCache.tracker.hash ?? '',
                staged: diffStatsStore.get(task.id)?.staged_paths?.length ?? 0,
            },
        }, digest);
        if (!verdict.row && !verdict.diff) return;
        let current = task;
        if (verdict.row) {
            try {
                current = await getTask(task.project_id, task.id);
                updateTaskInBoard(appStore, current.id, current);
            } catch {
                // Unreachable: compare against the row this screen already holds.
            }
        }
        if (diffBaseMoved(loadedHeadRef, current)) {
            void loadDiff();
            return;
        }
        if (verdict.diff) {
            const headRef = diffHeadRef(current);
            await diffCache.refreshManifest(headRef ? { headRef } : {});
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

    <div class="task-detail-page" class:floating={floating} class:fullscreen={editorFullscreen || diffFullscreen} class:chat-fullscreen={chatMaximize.fullscreen} bind:this={detailEl} tabindex="-1" aria-label={t('task.detailAria')}>
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
        <div bind:this={columnsRef} class="columns"  role="region" aria-label={t('task.detailPanelsAria')}>
            <!-- Column 1: Board Overview -->
            <div class="column column-context" tabindex="-1" class:hidden={floating || (mobile && activeTab !== 'context')} style:width={boardWidth}>
                <BoardOverview
                    sections={overviewSections.map(section => ({ id: section.id, label: t(section.labelKey) }))}
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
                    ariaLabel={t('task.resizeBoardAgentAria')}
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
                            onViewChanges={handleViewChanges}
                            openCheckpointHash={checkpointDiffOpen ? checkpointDiffHash : ''}
                            onReloadDiff={() => {
                                // The chat moved the files under this panel: a thread switch is a different
                                // comparison, a deletion rewinds the worktree, an edit-and-send retires a
                                // fork, and a rewind is not what the worktree watcher reports. Reload it — the
                                // rows held describe a state that no comparison answers, and a cleared panel
                                // stays empty until some unrelated frame arrives.
                                diffCache.reset();
                                void loadDiff();
                            }}
                        />
                    {/key}
                </div>
            </div>

            <!-- Resize handle: Chat ↔ Diff -->
            {#if !mobile}
                <ResizeHandle
                    ariaLabel={t('task.resizeAgentChangesAria')}
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
                                isMobile={mobile}
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
                                    <DiffViewer committedFiles={committedFiles} stagedFiles={stagedFiles} unstagedFiles={unstagedFiles} stagedUntracked={stagedUntracked} unstagedUntracked={unstagedUntracked} loading={diffLoading} pendingPaths={pendingPaths} busySection={busySection} busyLabel={stageBusyLabel} mergedLabel={isDoneTask ? t('task.diff.merged') : hasMergedSubtasks ? t('task.diff.mergedFromSubtasks') : t('task.diff.committedInTask')} onExpandFile={handleExpandDiff} onExpandUntracked={loadUntrackedFile} onLineClick={handleDiffLineClick} onStageFile={handleStageFile} onStageAll={handleStageAll} onUnstageAll={handleUnstageAll} imageSource={diffImageSource} />
                                {:else}
                                    <div class="diff-viewer-loading">{t('task.loadingDiffViewer')}</div>
                                {/if}
                            {:else}
                                <EmptyState class="pane-fill" title={t('task.noChanges')} />
                            {/if}
                        {/if}

                        <!-- Checkpoint overlay (overlays entire diff panel) -->
                        {#if checkpointDiffOpen}
                            <CheckpointOverlay
                                files={checkpointDiffFiles}
                                loading={checkpointDiffLoading}
                                error={checkpointDiffError}
                                label={checkpointDiffLabel}
                                refs={checkpointDiffRefs}
                                imageSource={checkpointImageSource}
                                onClose={closeCheckpointDiff}
                                onExpandFile={expandCheckpointFile}
                                onRetry={() => { void loadCheckpointDiff(checkpointDiffHash, checkpointDiffLabel); }}
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
        background: rgb(from var(--color-status-red) r g b / 0.1);
        border-bottom: 1px solid rgb(from var(--color-status-red) r g b / 0.3);
    }
    .memory-alert-text {
        display: flex;
        flex-wrap: wrap;
        gap: 2px 10px;
        min-width: 0;
    }
    .memory-alert-title {
        color: var(--color-status-red);
        font-size: var(--text-sm);
        font-weight: 600;
    }
    .memory-alert-line {
        color: var(--color-text-secondary);
        font-size: var(--text-xs);
    }
    .memory-alert-dismiss {
        flex-shrink: 0;
        padding: 4px 10px;
        font-size: var(--text-xs);
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
        z-index: var(--z-overlay);
        flex: none;
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-lg);
        box-shadow: var(--shadow-float);
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
        z-index: var(--z-content);
    }
    .diff-viewer-loading {
        display: flex;
        align-items: center;
        justify-content: center;
        padding: 48px 24px;
        color: var(--color-text-tertiary);
        font-size: var(--text-sm);
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
        z-index: var(--z-modal);
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
        z-index: var(--z-modal);
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
