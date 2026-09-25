
<script lang="ts">
    import { onMount, onDestroy, tick } from 'svelte';
    import { appStore, uiStore, dialogStore, updateTaskInBoard, removeTaskFromBoard, applyResolvedTasksToBoard } from '$lib/stores';
    import { t } from '$lib/i18n';
    import { isSandboxMountPath } from '$lib/utils/path';
    import { isAnyModalOpen } from '$lib/utils/modal-state';
    import { connectionStore } from '$lib/stores/connection.svelte';
    import { sandboxStatsStore } from '$lib/stores/sandbox-stats.svelte';
    import { diffStatsStore } from '$lib/stores/diff-stats.svelte';
    import { commitQueueStore } from '$lib/stores/commit-queue.svelte';
    import { taskEventStore } from '$lib/stores/task-events.svelte';
    import { getProjects, getBoard, getConfig, deleteProject, createProject, cloneProject, createAppStream, getProjectCounts, checkSession, logout, setOfflineStatus, loadQueue, clearQueue, flushQueueNow, onQueueFlushed, waitForFlush, applyQueuedActionsToBoard, isFromCache, initTask, getDroppedActions } from '$lib/api';
    import { repoNameFromGitUrl } from '$lib/utils/repo-name';
    import { dragCarriesFiles } from '$lib/utils/file-drop';
    import { dataAgeText } from '$lib/utils/format-time';
    import { resolveBoardLandingColumn, resolveClosedTaskColumn, mergeTaskUpdateIntoBoard } from '$lib/utils/task-utils';
    import { resolveLandingProject } from '$lib/utils/project-landing';
    import { explorerPathRoots } from '$lib/utils/app-link';
    import { urlRouter } from '$lib/services/url-router.svelte';
    import { reviewNotifier } from '$lib/services/review-notifications.svelte';
    import { getBoardCache, getBoardCacheEntry, boardCacheKey, doneStatsCacheKey, setBoardCache, removeBoardCache, clearAll, loadAppSetting, saveAppSetting } from '$lib/storage';
    import { flushBoardMirror, mirrorBoard } from '$lib/services/board-mirror';
    import { useIsMobile } from '$lib/composables/use-is-mobile.svelte';
    import { useKeyboardShortcuts } from '$lib/composables/use-keyboard-shortcuts.svelte';
    import { useBoardArrowNav } from '$lib/composables/use-board-arrow-nav';
    import { createTabReturn } from '$lib/composables/use-tab-return';
    import mikeIcon from '$lib/assets/icons/mike.svg';
    import type { Project, Task, StreamEvent, SandboxMemoryStats, DiffStatsEntry, Board as BoardData } from '$lib/types';
    import CardDetail from './CardDetail.svelte';
    import { setAppStreamRef } from '$lib/stream-ref';
    import { extensionManager } from '$lib/services/extension-manager.svelte';
    import { localeOffer } from '$lib/services/locale-offer.svelte';
    import { sandboxImageStatus } from '$lib/services/sandbox-image-status.svelte';
    import { updateCheck } from '$lib/services/update-check.svelte';
    import { useLazyComponent } from '$lib/composables/use-lazy-component.svelte';
    import UpdateDot from './UpdateDot.svelte';
    const LAST_PROJECT_KEY = 'lastProjectId';

    import CreateTaskModal from './CreateTaskModal.svelte';
    import EditTaskModal from './Card/EditTaskModal.svelte';
    import ConfigModal from './ConfigModal.svelte';
    import StatsModal from './StatsModal.svelte';
    import Board from './Board.svelte';
    import FileBrowser from './FileBrowser.svelte';
    import Toast from './Toast.svelte';
    import ConfirmationDialog from './ConfirmationDialog.svelte';
    import CommitDialog from './Chat/CommitDialog.svelte';
    import CompressContextDialog from './Chat/CompressContextDialog.svelte';
    import LoginModal from './LoginModal.svelte';
    import SandboxConfigModal from './SandboxConfigModal.svelte';
    import ExplorerView from './ExplorerView.svelte';
    import TaskExplorerView from './TaskExplorerView.svelte';
    import Sidebar from './App/Sidebar.svelte';
    import TopBar from './App/TopBar.svelte';
    import LocaleOffer from './LocaleOffer.svelte';

    const terminalPanel = useLazyComponent(() => import('./TerminalPanel.svelte'));

    /** The reconnect banner both stream connections drive. */
    const reconnectCountdown = () => ({
        onConnected: handleWsConnection,
        onReconnectScheduled: (nextAttemptAt: number) => {
            nextReconnectAt = nextAttemptAt;
            startCountdown();
        },
        onConnectAttempt: () => {
            stopCountdown();
            countdownSeconds = 0;
        },
    });

    /**
     * Open the project terminal (terminal button and open-terminal shortcut).
     * When the task detail is open, initialize that task's worktree first if
     * it does not exist yet.
     */
    async function openTerminal(): Promise<void> {
        terminalPanel.load();
        const task = appStore.selectedTask;
        if (uiStore.showCardDetail && task && !task.worktree_exists && !task.branch) {
            try {
                await initTask(task.project_id, task.id);
            } catch (e) {
                console.error('[terminal] Failed to initialize task:', e);
                return;
            }
        }
        uiStore.showTerminal = true;
    }

    function getProjectAbbr(name: string): string {
        const words = name.split(/[-_\s]+/).filter(Boolean);
        if (words.length >= 2) {
            return (words[0][0] + words[1][0]).toUpperCase();
        }
        return name.slice(0, 2).toUpperCase();
    }

    function computePageTitle(): string {
        const project = appStore.selectedProject;
        if (!project?.name) return t('app.title.default');
        const name = project.name;
        if (urlRouter.terminalOnly) return t('app.title.terminal', { name });
        const reviewCount = projectCounts[project.id]?.review ?? 0;
        if (reviewCount > 0) {
            return t('app.title.review', { name, count: reviewCount });
        }
        for (const [pid, counts] of Object.entries(projectCounts)) {
            if (pid === project.id || !counts.review) continue;
            const other = appStore.projects.find(p => p.id === pid);
            return `${name} | 🟡 ${other?.name || pid}`;
        }
        return name;
    }

    let collapsed = $state(false);
    let appStream: ReturnType<typeof createAppStream> | null = $state(null);
    let initialLoadComplete = $state(false); // tracks whether onMount data load is done
    let lastSyncedAt = $state<number | null>(null); // epoch ms of the last data that actually landed
    let nextReconnectAt = $state(0); // epoch ms of next reconnect attempt
    let countdownSeconds = $state(0);
    let countdownTimer: ReturnType<typeof setInterval> | null = null;
    let staleTimeText = $state(''); // "Data from X ago" text, updated every second while disconnected
    let staleTimer: ReturnType<typeof setInterval> | null = null;
    let visibilityRetryTimer: ReturnType<typeof setTimeout> | null = null; // retry refresh after network stabilizes

    /**
     * Put a board on screen, if the project it was fetched for is still the one being looked at.
     * Each async board path captures the id before its awaits and hands it here, so a response for
     * a project the user has switched away from cannot replace the board they are now on.
     *
     * `'live'` is a board the server answered, so it is mirrored and resets the age line. `'mirror'`
     * keeps the age it carries and gets this browser's queued writes replayed onto it.
     */
    function applyBoard(board: BoardData, projectId: string, source: 'live' | 'mirror'): boolean {
        if (!board || appStore.selectedProject?.id !== projectId) return false;
        if (source === 'live') {
            mirrorBoard(projectId, board);
            lastSyncedAt = Date.now();
            appStore.board = board;
            return true;
        }
        const withQueue = applyQueuedActionsToBoard(board.tasks || [], projectId);
        appStore.board = { ...board, tasks: withQueue };
        return true;
    }

    /** A board the fetch answered from the mirror is a mirror landing: same age rule, same replay. */
    function boardSource(board: BoardData): 'live' | 'mirror' {
        return isFromCache(board) ? 'mirror' : 'live';
    }

    /** The counts frame is the arrival signal for projects whose board is not on screen. */
    function applyProjectCounts(counts: Record<string, { backlog: number; in_progress: number; review: number }>) {
        projectCounts = counts;
        reviewNotifier.observeCounts(counts);
    }

    $effect(() => {
        // The board on screen tells the notifier which Review cards the user can already see.
        const board = appStore.board;
        const projectId = appStore.selectedProject?.id;
        if (!board || !projectId) {
            reviewNotifier.clearBoard();
            return;
        }
        reviewNotifier.observeBoard(projectId, board.tasks || []);
    });

    $effect(() => {
        // What the user opens, and which Review column they are looking at, is them having seen it.
        const openTaskId = appStore.selectedTask?.id || uiStore.floatingTask?.taskId || null;
        reviewNotifier.observeAttention(openTaskId, uiStore.reviewColumnInViewOf);
    });

    /**
     * Every Settings opener goes through here. The update dot decides which tab the modal opens on, so
     * the mark on the control leads to the row that explains it rather than to the provider list.
     */
    function openSettings(): void {
        uiStore.openSettings(updateCheck.dot === 'none' ? null : 'about');
    }

    function startCountdown() {
        stopCountdown();
        updateCountdown();
        countdownTimer = setInterval(updateCountdown, 250);
    }
    function stopCountdown() {
        if (countdownTimer) {
            clearInterval(countdownTimer);
            countdownTimer = null;
        }
    }
    function updateCountdown() {
        countdownSeconds = Math.max(0, Math.ceil((nextReconnectAt - Date.now()) / 1000));
    }
    function startStaleTick() {
        stopStaleTick();
        updateStaleTime();
        staleTimer = setInterval(updateStaleTime, 1000);
    }
    function updateStaleTime() {
        staleTimeText = dataAgeText(lastSyncedAt);
    }

    // The reconnect countdown belongs to the sentence, not to the markup: word
    // order and the unit are the translator's, so the whole line is one message.
    const reconnectStatus = $derived(
        countdownSeconds > 0
            ? t('app.reconnect.statusCountdown', { seconds: countdownSeconds })
            : t('app.reconnect.status'),
    );
    function stopStaleTick() {
        if (staleTimer) {
            clearInterval(staleTimer);
            staleTimer = null;
        }
    }

    let hadConnection = $state(false);
    let everDropped = $state(false);
    /** The countdown and the age line change every second, so they stay out of this announced text. */
    const connectionNotice = $derived(
        !connectionStore.connected
            ? (everDropped ? t('app.reconnect.status') : '')
            : (everDropped && hadConnection ? t('app.reconnect.back') : '')
    );

    function handleWsConnection(connected: boolean): void {
        connectionStore.set(connected);
        setOfflineStatus(!connected);
        if (connected) {
            hadConnection = true;
            stopCountdown();
            stopStaleTick();
            nextReconnectAt = 0;
            countdownSeconds = 0;
            // The first connect has nothing to catch up on.
            if (initialLoadComplete) {
                silentRefresh().catch(() => {});
            }
        } else {
            everDropped = true;
            // The age counts from the last data that landed, not from this socket dropping.
            startStaleTick();
        }
    }

    function tryReconnectNow() {
        if (appStream) {
            appStream.reconnectNow();
            nextReconnectAt = 0;
        }
    }

    function retryLiveNow() {
        tryReconnectNow();
        silentRefresh().catch(() => {});
    }
    let projectCounts: Record<string, { backlog: number; in_progress: number; review: number }> = $state({});
    // Cached done-task diff stats, keyed "<projectId>:<taskId>". Done-task diffs
    // are immutable (pinned BaseCommit..BranchTip), so entries persist in
    // IndexedDB across sessions: on project subscribe they're declared via
    // known_stats and the server skips re-sending the stats frame.
    let doneStatsCache: Record<string, { updated_at: string; stats: DiffStatsEntry }> = $state({});
    let boardRefreshPending = false; // deduplicate rapid refreshBoard calls
    let showFileBrowserVal = $state(false);
    /** The terminal screen's target: the prepared "<projectId>:<taskId>", and whether creating that task's worktree failed. */
    let terminalScreen = $state<{ key: string; failed: boolean }>({ key: '', failed: false });

    /**
     * The terminal-only screen prepares its own target: the project must be on screen, and a task
     * terminal needs the worktree, which the board's terminal button also creates before it opens.
     */
    $effect(() => {
        if (!urlRouter.terminalOnly) {
            terminalScreen = { key: '', failed: false };
            return;
        }
        const project = appStore.selectedProject;
        if (!project) return;
        const wantsTask = urlRouter.task !== '';
        const task = wantsTask ? appStore.selectedTask : null;
        if (wantsTask && !task) return;
        const key = project.id + ':' + (task?.id ?? '');
        if (terminalScreen.key === key) return;
        void openTerminalScreen(key, project.id, task);
    });

    async function openTerminalScreen(key: string, projectId: string, task: Task | null): Promise<void> {
        terminalPanel.load();
        if (task && !task.worktree_exists && !task.branch) {
            try {
                await initTask(projectId, task.id);
            } catch {
                terminalScreen = { key, failed: true };
                return;
            }
        }
        terminalScreen = { key, failed: false };
    }
    let cardDetailActiveTab = $state<'context' | 'chat' | 'diff' | 'checkpoint'>('chat');
let scrollToColumn = $state<string | null>(null); // column to scroll to on mobile after task delete
    // Mobile: column the board jumps to after switching project, keyed by project id.
    let boardFocus = $state<{ projectId: string; column: string } | null>(null);
    let fsEventQueue = $state<Array<{ op: 'create' | 'delete'; path: string }>>([]);
    let addProjectError = $state('');
    let currentBranch = $state('');
    let mobileMenuOpen = $state(false);

    // Keyboard shortcuts (Settings → Preferences): the create-task shortcut is
    // active only on the visible board; the open-terminal shortcut is active
    // on every page with a terminal button (board, task detail, explorers).
    function boardShortcutsEnabled(): boolean {
        return (
            !!appStore.selectedProject &&
            !urlRouter.terminalOnly &&
            !(uiStore.showCardDetail && appStore.selectedTask) &&
            !urlRouter.explorerOpen &&
            !urlRouter.taskExplorerOpen &&
            !isAnyModalOpen()
        );
    }
    function terminalShortcutEnabled(): boolean {
        return !!appStore.selectedProject && !urlRouter.terminalOnly && !isAnyModalOpen();
    }
    useKeyboardShortcuts({
        isEnabled: boardShortcutsEnabled,
        onCreateTask: () => {
            uiStore.creatingChildOf = null;
            uiStore.showCreateTask = true;
        },
        isTerminalEnabled: terminalShortcutEnabled,
        onOpenTerminal: () => { void openTerminal(); },
    });
    // Arrow-key navigation between board cards (same gate as the shortcuts above).
    useBoardArrowNav({ isEnabled: boardShortcutsEnabled });

    let isAuthenticated = $state(false);
    let isLoadingAuth = $state(true);
    let servingFromCache = $state(false);
    let hasLocalCache = $state(false); // the server did not answer and the mirror can be offered
    let bootSlow = $state(false);      // the session check has not answered; nothing is known yet
    let hasProviders = $derived(!!appStore.config?.providers?.length);
    let pageTitle = $derived(computePageTitle());
    let hasReview = $derived.by(() => {
        const project = appStore.selectedProject;
        return !!project && (projectCounts[project.id]?.review ?? 0) > 0;
    });

    const isMobileCtx = useIsMobile();

    // Sync reactive stores → local $state
    $effect(() => {
        collapsed = appStore.sidebarCollapsed;
        appStore.persistSidebarCollapsed();
    });

    // A task's frames belong to the chat that reads it: `TaskChat` takes and releases the key. `docs/reconnection.md`

    // Handle browser back/forward
    async function handlePopState() {
        const url = urlRouter.read();
        const projectId = url.project;
        const taskId = url.task;
        const fileProject = (projectId ? appStore.projects.find(p => p.id === projectId) : null) ?? appStore.selectedProject;
        const fileRoots = fileProject
            ? explorerPathRoots({
                projectPath: fileProject.path, projectName: fileProject.name,
                sandboxEnabled: !!fileProject.sandbox_enabled, workspaceDir: appStore.config?.workspace_dir,
                projectId: fileProject.id, taskId: taskId || undefined,
            })
            : [];
        urlRouter.acceptFile(url.file, fileRoots);

        if (!projectId && appStore.selectedProject) {
            const landing = await landingProject();
            if (landing) {
                await selectProject(landing, false);
                window.dispatchEvent(new CustomEvent('urlchanged'));
                return;
            }
            appStore.selectedProject = null;
            appStore.board = null;
            disconnectBoardStream();
            appStore.selectedTask = null;
            uiStore.showCardDetail = false;
            window.dispatchEvent(new CustomEvent('urlchanged'));
            return;
        }

        // If URL specifies a different project, switch to it
        if (projectId && appStore.selectedProject && projectId !== appStore.selectedProject.id) {
            const targetProject = appStore.projects.find(p => p.id === projectId);
            if (targetProject) {
                await selectProject(targetProject, false);
                // After board loads, restoreTaskFromUrl will handle the task
                window.dispatchEvent(new CustomEvent('urlchanged'));
                return;
            }
        }

        if (taskId) {
            const task = appStore.tasks.find(t => t.id === taskId);
            if (task) {
                appStore.selectedTask = task;
                uiStore.showCardDetail = true;
            }
        } else {
            // Scroll to the task's column when going back via browser back button
            const prevTask = appStore.selectedTask;
            if (prevTask) scrollToColumn = prevTask.status;
            appStore.selectedTask = null;
            uiStore.showCardDetail = false;
        }
        // Restore explorer view (and Review Changes panel) from URL
        urlRouter.applyView();
        // Notify child components (e.g., TaskChat) that the URL changed
        window.dispatchEvent(new CustomEvent('urlchanged'));
    }

    $effect(() => {
        window.addEventListener('popstate', handlePopState);
        return () => window.removeEventListener('popstate', handlePopState);
    });

    /**
     * Put the task's explorer in the shared slot (the task view and both
     * explorers occupy one place) and mirror it in the URL.
     */
    function openTaskExplorer(): void {
        const project = appStore.selectedProject;
        const task = appStore.selectedTask;
        if (!project || !task) return;
        urlRouter.openTaskExplorer(project.id, task.id);
    }

    async function routeDefinitionJump(event: Event): Promise<void> {
        const detail = (event as CustomEvent<{ path: string; line?: number; taskId?: string }>).detail;
        // Project jumps are heard by the project explorer, which owns the only
        // editor that can issue one.
        if (!detail?.taskId) return;
        // Not the selected task — nothing here knows how to open that file.
        if (detail.taskId !== appStore.selectedTask?.id) return;
        // The task explorer is showing: its own listener already has this event.
        if (urlRouter.taskExplorerOpen) return;
        openTaskExplorer();
        await tick();
        window.dispatchEvent(new CustomEvent('open-file', { detail }));
    }

    $effect(() => {
        const handler = (event: Event) => void routeDefinitionJump(event);
        window.addEventListener('open-file', handler);
        return () => window.removeEventListener('open-file', handler);
    });

    /** One app stream per page: a second one leaves the first open and unreachable to close. */
    function closeAppStream(): void {
        if (!appStream) return;
        appStream.close();
        appStream = null;
    }

    async function initializeAuthSession() {
        closeAppStream();
        appStream = createAppStream({
            onProjectCounts: (counts) => {
                applyProjectCounts(counts);
            },
            onRefresh: () => refreshBoard(),
            onDiffStats: (task_id, hash, _short_stat, files_n, untracked_n, untracked_lines, tracked_add, tracked_del, file_hashes, removed_files, staged_paths, has_committed_changes, already_merged) => {
                const additions = tracked_add + (untracked_lines || 0);
                const deletions = tracked_del;
                const stats: DiffStatsEntry = { hash: hash || '', files: files_n, additions, deletions, untracked_n: untracked_n || 0, file_hashes, removed_files, staged_paths, has_committed_changes, already_merged };
                diffStatsStore.set(task_id, stats);
                // persist dont-task stats
                const t = appStore.tasks.find(x => x.id === task_id);
                const project = appStore.selectedProject;
                if (t && t.status === 'done' && project && t.updated_at) {
                    const key = `${project.id}:${task_id}`;
                    doneStatsCache[key] = { updated_at: t.updated_at, stats };
                    setBoardCache(doneStatsCacheKey(project.id, task_id), doneStatsCache[key]).catch(() => {});
                }
            },
            onBranch: (branch) => {
                currentBranch = branch;
                if (branch && !urlRouter.explorerOpen) urlRouter.restoreProjectExplorer();
            },
            onFsEvent: (op, path) => {
                if (urlRouter.explorerOpen || urlRouter.taskExplorerOpen || (uiStore.showCardDetail && appStore.selectedTask)) {
                    fsEventQueue = [...fsEventQueue, { op, path }];
                }
            },
            onTaskUpdated: (updatedTask: Task) => {
                // Leaving Done invalidates the cached done-diff stats (the task
                // is about to accumulate new work) — whichever project is on screen,
                // since the cache is keyed by project and task.
                if (updatedTask.status !== 'done' && updatedTask.project_id) {
                    const key = `${updatedTask.project_id}:${updatedTask.id}`;
                    if (doneStatsCache[key]) {
                        const next = { ...doneStatsCache };
                        delete next[key];
                        doneStatsCache = next;
                        removeBoardCache(doneStatsCacheKey(updatedTask.project_id, updatedTask.id)).catch(() => {});
                    }
                }
                const board = appStore.board;
                if (!board) return;
                const merged = mergeTaskUpdateIntoBoard(board.tasks || [], updatedTask, appStore.selectedProject?.id ?? '');
                if (!merged) return;
                appStore.board = { ...board, tasks: merged };
                // A frame this connection was sent is server-answered state, so the mirror and the
                // age line follow it.
                const project = appStore.selectedProject;
                if (project) {
                    mirrorBoard(project.id, { tasks: merged });
                    lastSyncedAt = Date.now();
                }
                if (appStore.selectedTask?.id === updatedTask.id) {
                    appStore.selectedTask = updatedTask;
                }
            },
            onTaskDeleted: (task_id: string) => {
                // A frame naming a task this board never held belongs to another project; there is
                // nothing here to take away.
                if (appStore.tasks.some(t => t.id === task_id)) {
                    removeTaskFromBoard(appStore, task_id);
                    const project = appStore.selectedProject;
                    if (project) {
                        mirrorBoard(project.id, { tasks: appStore.tasks });
                        lastSyncedAt = Date.now();
                    }
                }
                sandboxStatsStore.remove(task_id);
                if (appStore.selectedTask && appStore.selectedTask.id === task_id) {
                    clearTaskDetail();
                }
                diffStatsStore.remove(task_id);
                const project = appStore.selectedProject;
                if (project) {
                    const key = `${project.id}:${task_id}`;
                    if (doneStatsCache[key]) {
                        const next = { ...doneStatsCache };
                        delete next[key];
                        doneStatsCache = next;
                        removeBoardCache(doneStatsCacheKey(project.id, task_id)).catch(() => {});
                    }
                }
            },
            onCommitStatus: (task_id: string, status: 'queued' | 'processing') => {
                const project = appStore.selectedProject;
                // The frame names no project, so the board on screen decides whether the task is
                // ours — the slot is one per project, and another project's task would occupy it.
                if (project && appStore.tasks.some(t => t.id === task_id)) {
                    commitQueueStore.set(project.id, { taskID: task_id, status });
                }
            },
            onTaskEvent: (task_id: string, event: StreamEvent) => {
                if (event.type === 'sandbox_stats') {
                    // Sandbox memory stats live in their own store (keyed by
                    // task) — not in the single-slot task event store.
                    sandboxStatsStore.set(task_id, event.data as SandboxMemoryStats);
                    return;
                }
                // Dispatch to the task's TaskChat
                const floatingId = uiStore.floatingTask?.taskId;
                if ((appStore.selectedTask && appStore.selectedTask.id === task_id) ||
                    (floatingId && floatingId === task_id)) {
                    taskEventStore.publish({ task_id, ...event });
                }
            },
            ...reconnectCountdown(),
            onProjectPorts: (ports) => {
                appStore.projectTerminalPorts = ports;
            },
            onSandboxImageStatus: (status) => {
                sandboxImageStatus.update(status);
            },
            onExtensionChange: handleExtensionChange,
        }, {
            // Declare which done-task stats we already have, so the server
            // can skip re-sending them on (re)subscribe.
            getKnownStats: (projectId: string) => {
                const prefix = projectId + ':';
                const known: Record<string, string> = {};
                for (const [key, entry] of Object.entries(doneStatsCache)) {
                    if (key.startsWith(prefix)) {
                        known[key.slice(prefix.length)] = entry.updated_at;
                    }
                }
                return known;
            },
        });
        setAppStreamRef(appStream);
        urlRouter.read();
        urlRouter.applyView();
        await loadAppData();
        // The language offer runs **after** the extension list resolves: its first
        // rule is "no i18n pack is enabled", and before this resolves that answer
        // is the previous session's, which offers a pack that is already on.
        extensionManager.loadExtensions()
            .then(() => localeOffer.detect())
            .catch(() => {});
    }

    function reportRefusedWrites(): void {
        const count = getDroppedActions().length;
        if (count === 0) return;
        dialogStore.showToast(t('api.queueRejected', { count }), 'error');
    }

    async function loadAppData() {
        try {
            // Restore offline action queue from IndexedDB
            await loadQueue();
            // Flush queued actions via HTTP immediately (don't wait for WebSocket to connect)
            flushQueueNow();
            // After offline queue is flushed, replace optimistic tasks with real ones from HTTP responses
            onQueueFlushed((_optimisticIds, resolvedTasks) => {
                // Replace optimistic tasks with their latest real state
                // resolvedTasks has: optimisticId → createTask result, AND realId → latest update (start/move/etc.)
                applyResolvedTasksToBoard(appStore, resolvedTasks);
                // Clear cache indicator after flush succeeds
                servingFromCache = false;
                reportRefusedWrites();
            });
            const projs = await getProjects();
            appStore.projects = projs || [];
            if (appStore.projects.length > 0) {
                const targetProject = (urlRouter.project ? appStore.projects.find(p => p.id === urlRouter.project) : null)
                    ?? await landingProject();
                if (targetProject) {
                    await selectProject(targetProject, false);
                }
                // The landing replayed this project's queued writes already; a second pass opens a
                // second card.
                // Check if data was served from cache
                servingFromCache = isFromCache(projs);
            }
            // Load config (best-effort — cache fallback inside getConfig)
            try {
                const c = await getConfig();
                appStore.config = c;
            } catch {
                // No cached config — proceed without it
            }
            // Fetch initial project counts (best-effort, no cache fallback)
            try {
                const counts = await getProjectCounts();
                applyProjectCounts(counts);
            } catch {
                // Use defaults — counts are non-critical
            }
        } catch (e) {
            console.error('Failed to load:', e);
        }
        // Mark initial load as done. Reconnects now trigger silentRefresh()
        // to pick up changes that occurred while offline.
        initialLoadComplete = true;
        // One release pass per boot, from the one place both boot paths land on; `start()` guards
        // itself, so a path that runs twice costs nothing. Its opt-in question cannot interrupt the
        // loading screen, because it is asked only after the status read and the stamp read behind it.
        // Nothing leaves the process until that question is answered yes.
        void updateCheck.start();
    }

    async function hasCachedProjects(): Promise<boolean> {
        try {
            const cached = await getBoardCache<Project[]>('projects');
            return !!cached && cached.length > 0;
        } catch (e) {
            console.error('Failed to check cached data:', e);
            return false;
        }
    }

    onMount(async () => {
        // A slow session answer is not an unreachable server: the 2 s timer only offers the offline
        // mirror, and the answer that arrives later takes the offer back.
        let authResolved = false;
        const offlineTimer = setTimeout(async () => {
            if (authResolved) return;
            bootSlow = true;
            setOfflineStatus(true);
            // With no mirror to offer there is nothing to show but the wait.
            if (await hasCachedProjects()) {
                hasLocalCache = true;
                isLoadingAuth = false;
            }
        }, 2000);
        try {
            isAuthenticated = await checkSession();
            authResolved = true;
            clearTimeout(offlineTimer);
            bootSlow = false;
            // The mirror screen outranks the app, so only this takes it down.
            hasLocalCache = false;
            if (isAuthenticated) {
                await initializeAuthSession();
            }
            isLoadingAuth = false;
        } catch (e) {
            authResolved = true;
            clearTimeout(offlineTimer);
            console.error('Failed to check auth:', e);
            // Mark as offline so API calls skip network and use cache immediately
            setOfflineStatus(true);
            hasLocalCache = await hasCachedProjects();
            isLoadingAuth = false;
        }
    });

    // What this window missed comes back over HTTP: the per-connection event buffer is gone once the
    // socket drops.
    function reconcileAfterAbsence(): void {
        appStream?.reconnectNow();
        refreshBoard().catch(() => {
            if (visibilityRetryTimer) return; // already retrying
            visibilityRetryTimer = setTimeout(() => {
                visibilityRetryTimer = null;
                refreshBoard().catch(() => {}); // give up after second attempt
            }, 1000);
        });
    }

    const returnWatcher = createTabReturn({
        mayBeBehind: () => true,
        msSinceFrame: () => appStream?.msSinceFrame() ?? null,
        msSinceUnpainted: () => appStream?.msSinceUnpainted() ?? null,
        onReturn: reconcileAfterAbsence,
    });

    // Browser network signals. 'offline' queues writes immediately;
    // 'online' flushes the queue and skips the reconnect backoff.
    function handleBrowserOffline() {
        setOfflineStatus(true);
    }
    function handleBrowserOnline() {
        setOfflineStatus(false);
        appStream?.reconnectNow();
    }
    window.addEventListener('online', handleBrowserOnline);
    window.addEventListener('offline', handleBrowserOffline);

    // A snapshot still in the coalescing window goes with the page unless it is written here.
    function flushMirrorOnPageHide() {
        void flushBoardMirror();
    }
    window.addEventListener('pagehide', flushMirrorOnPageHide);

    function rejectStrayFileDrop(e: DragEvent) {
        if (!dragCarriesFiles(e.dataTransfer)) return;
        e.preventDefault();
    }
    window.addEventListener('dragover', rejectStrayFileDrop);
    window.addEventListener('drop', rejectStrayFileDrop);

    onDestroy(() => {
        stopCountdown();
        if (visibilityRetryTimer) {
            clearTimeout(visibilityRetryTimer);
            visibilityRetryTimer = null;
        }
        returnWatcher.destroy();
        window.removeEventListener('online', handleBrowserOnline);
        window.removeEventListener('offline', handleBrowserOffline);
        window.removeEventListener('pagehide', flushMirrorOnPageHide);
        void flushBoardMirror();
        window.removeEventListener('dragover', rejectStrayFileDrop);
        window.removeEventListener('drop', rejectStrayFileDrop);
        disconnectBoardStream();
        closeAppStream();
        if (urlRafId !== null) cancelAnimationFrame(urlRafId);
    });

    /** Load the board from local cache after the user clicks "Load Local Cache". */
    async function loadFromCache() {
        hasLocalCache = false; // hide the offline screen
        closeAppStream(); // the mirror's stream must not outlive the mirror
        // Keep isLoadingAuth = true so the loading screen bridges the gap
        // while cached data is read from IndexedDB (avoids flash of empty state)
        isAuthenticated = true;
        appStream = createAppStream({
            onProjectCounts: (counts) => { applyProjectCounts(counts); },
            onRefresh: () => refreshBoard(),
            onDiffStats: () => {},
            onBranch: (branch) => { currentBranch = branch; },
            onFsEvent: () => {},
            onTaskUpdated: () => {},
            onTaskDeleted: () => {},
            onCommitStatus: () => {},
            onTaskEvent: () => {},
            ...reconnectCountdown(),
            onExtensionChange: handleExtensionChange,
        });
        setAppStreamRef(appStream);
        const mirror = await getBoardCacheEntry('projects').catch(() => null);
        if (mirror) lastSyncedAt = mirror.cachedAt;
        await loadAppData(); // sets initialLoadComplete on completion
        isLoadingAuth = false; // hide loading screen after data is loaded

        // Load installed extensions for LSP discovery, then ask (once, silently if
        // the answer is no) whether this browser wants a translation pack.
        extensionManager.loadExtensions()
            .then(() => localeOffer.detect())
            .catch(() => {});
    }

    // Detect external URL changes (e.g., page.goto() in Playwright, direct URL navigation).
    // Uses requestAnimationFrame with throttling — only checks when frame fires,
    // skips if URL hasn't changed. ~10-60ms latency vs 500ms setInterval.
    let urlRafId: number | null = null;

    function checkUrlChange() {
        if (urlRouter.refresh() && appStore.tasks.length > 0) {
            void restoreTaskFromUrl();
        }
        urlRafId = requestAnimationFrame(checkUrlChange);
    }

    $effect(() => {
        if (!isAuthenticated || !appStore.selectedProject) return;
        urlRouter.markSeen();
        urlRafId = requestAnimationFrame(checkUrlChange);
        return () => { if (urlRafId !== null) cancelAnimationFrame(urlRafId); };
    });

    // Function to restore task from URL after board loads.
    // When task is not on the board (e.g., created via API, board stream
    // hasn't pushed yet), refresh board from server to pick up new tasks.
    async function restoreTaskFromUrl() {
        const taskId = urlRouter.task;
        if (!taskId || !appStore.selectedProject) return;
        const projectId = appStore.selectedProject.id;
        let task = appStore.tasks.find(t => t.id === taskId);
        if (!task) {
            try {
                // Refresh board to pick up tasks created via API
                const b = await getBoard(projectId);
                if (!applyBoard(b, projectId, boardSource(b))) return;
                task = appStore.tasks.find(t => t.id === taskId);
                if (!task) {
                    // The task belongs to another project, so the detail on screen is not the URL's:
                    // clearing the param alone leaves it rendered under the project the URL names.
                    urlRouter.write({ project: projectId });
                    appStore.selectedTask = null;
                    uiStore.showCardDetail = false;
                    return;
                }
            } catch {
                return;
            }
        }
        appStore.selectedTask = task;
        // Restore view state from URL: a `view` that names a screen of its own keeps the detail closed.
        uiStore.showCardDetail = !urlRouter.applyView();
    }

    function disconnectBoardStream() {
        const project = appStore.selectedProject;
        if (project && appStream) {
            appStream.unsubscribeProject(project.id);
        }
    }

    /** Hydrate cached done-task stats (IndexedDB) into the in-memory cache and the
     * diff-stats store so cards render immediately and the next
     * subscribe can declare them via known_stats. A cache miss just means the
     * server sends the stats as usual. */
    async function hydrateDoneTaskStats(projectId: string, projectTasks: Task[]) {
        const pending = projectTasks.filter(t => t.status === 'done' && !doneStatsCache[`${projectId}:${t.id}`]);
        if (pending.length === 0) return;
        type Hit = { id: string; entry: { updated_at: string; stats: DiffStatsEntry } };
        const loaded = await Promise.all(pending.map(async t => {
            try {
                const cached = await getBoardCache<{ updated_at: string; stats: DiffStatsEntry }>(doneStatsCacheKey(projectId, t.id));
                return cached ? { id: t.id, entry: cached } as Hit : null;
            } catch {
                return null;
            }
        }));
        const hits = loaded.filter((x): x is Hit => x !== null);
        if (hits.length === 0) return;
        const next = { ...doneStatsCache };
        for (const { id, entry } of hits) {
            next[`${projectId}:${id}`] = entry;
        }
        doneStatsCache = next;
        const toAdd: Record<string, DiffStatsEntry> = {};
        for (const { id, entry } of hits) {
            toAdd[id] = entry.stats;
        }
        diffStatsStore.fillAbsent(toAdd);
    }

    /** The board of the project just opened, as IndexedDB last saw it, applied while its live board
     * is in flight. Dropped if that project is no longer the one on screen, or if a live board for
     * it has landed in the meantime (`appStore.board` is set by every board write). */
    async function showCachedBoard(projectId: string): Promise<void> {
        let entry: { data: unknown; cachedAt: number } | null = null;
        try {
            entry = await getBoardCacheEntry(boardCacheKey(projectId));
        } catch {
            return; // no cache to show — the live board is the first paint
        }
        const cached = entry?.data as BoardData | undefined;
        if (!cached || appStore.board || appStore.selectedProject?.id !== projectId) return;
        if (!applyBoard(cached, projectId, 'mirror')) return;
        lastSyncedAt = entry!.cachedAt;
        // The banner says "this is the newest copy there is", which is only true with no socket to
        // bring the live board in; a slow-but-connected switch must not flash it.
        servingFromCache = !connectionStore.connected;
    }

    async function refreshBoard() {
        if (!appStore.selectedProject || boardRefreshPending) return;
        const projectId = appStore.selectedProject.id;
        boardRefreshPending = true;
        try {
            // Wait for pending queue flush
            await waitForFlush();
            const b = await getBoard(projectId);
            if (!applyBoard(b, projectId, boardSource(b))) return;
            void hydrateDoneTaskStats(projectId, appStore.tasks);
            // Update selected task reference to point to fresh object from API
            const task = appStore.selectedTask;
            if (task) {
                const fresh = appStore.tasks.find(t => t.id === task.id);
                if (fresh) {
                    appStore.selectedTask = fresh;
                }
            }
            // Live data received — clear cache indicator
            servingFromCache = isFromCache(b);
        } catch {
            // A board that did not answer leaves the one on screen untouched; the age line counts
            // from whenever data last landed.
        } finally {
            boardRefreshPending = false;
        }
    }

    function handleExtensionChange() {
        extensionManager.loadExtensions().catch(() => {});
        appStore.refreshConfig();
    }

    /** Silently refresh all cached data after reconnection. No flicker, no URL changes. */
    async function silentRefresh() {
        try {
            // Wait for pending queue flush
            await waitForFlush();
            const projs = await getProjects();
            appStore.projects = projs || [];
            const project = appStore.selectedProject;
            if (project) {
                const projectId = project.id;
                const b = await getBoard(projectId);
                if (applyBoard(b, projectId, boardSource(b))) {
                    // Update selected task reference
                    { const sel = appStore.selectedTask; if (sel) {
                        const fresh = appStore.tasks.find(t => t.id === sel.id);
                        if (fresh) {
                            appStore.selectedTask = fresh;
                        }
                    }}
                }
            }
            const c = await getConfig();
            appStore.config = c;
            servingFromCache = false;
            lastSyncedAt = Date.now();
        } catch {
            // Still offline — keep cached data
        }
    }

    function clearTaskDetail() {
        // Push URL first so the subscription sees updated URL
        urlRouter.write({ project: appStore.selectedProject?.id }, true, true);
        appStore.selectedTask = null;
        uiStore.showCardDetail = false;
    }

    /** Land the edit dialog's answer on the board, then tell the server. The dialog's own record names
     * the task it was opened for; a task the board does not hold is not edited anywhere. */
    function saveTaskEdit(
        title: string, model: string, url: string, reasoningEffort: string,
        operatingMode: string, autoMerge: boolean, subtasksAutoMerge: boolean, baseRef: string,
    ): void {
        const edit = appStore.editTaskData;
        appStore.showEditTask = false;
        appStore.editTaskData = null;
        if (!edit) return;
        const patch: Partial<Task> = {
            title: title.trim(),
            operating_mode: operatingMode,
            auto_commit: autoMerge,
            auto_merge: autoMerge,
            subtasks_auto_merge: subtasksAutoMerge,
        };
        if (baseRef) patch.base_ref = baseRef;
        if (model) patch.model_name = model;
        if (url) patch.llama_server_url = url;
        if (reasoningEffort) patch.reasoning_effort = reasoningEffort;
        const task = appStore.tasks.find(t => t.id === edit.taskId);
        if (!task) return;
        updateTaskInBoard(appStore, edit.taskId, { ...task, ...patch });
        import('$lib/api').then(({ updateTask }) => updateTask(edit.projectId, edit.taskId, patch)).catch(err => {
            console.error('Failed to update task:', err);
        });
    }

    async function landingProject(): Promise<Project | null> {
        const last = await loadAppSetting(LAST_PROJECT_KEY, '').catch(() => '');
        return resolveLandingProject(appStore.projects, last);
    }

    /**
     * Make `p` the current project. `push` is false where the URL is not a new navigation — the
     * launch selection and every popstate — so no board-less URL is left under the app for the back
     * button to land on. In an installed PWA that entry is the launch entry itself.
     */
    async function selectProject(p: Project, push = true) {
        disconnectBoardStream();
        const previous = appStore.selectedProject;
        // The screen on screen is the `view` the new project's address bar has to keep naming.
        const viewParam = urlRouter.currentView();
        const wasProjectSwitch = !!previous;
        const wasDifferentProject = !!previous && previous.id !== p.id;
        // Image progress belongs to the project it was sent for.
        if (wasDifferentProject) sandboxImageStatus.clear();
        currentBranch = '';
        appStore.selectedProject = p;
        // The list on screen belongs to the project the user left; the new project's own cached copy
        // replaces it, never the old project's cards.
        if (wasDifferentProject) {
            appStore.board = null;
            void showCachedBoard(p.id);
        }
        saveAppSetting(LAST_PROJECT_KEY, p.id).catch(() => {});
        // Re-mount explorer view for the new project (only on project switch, not initial load)
        if (wasProjectSwitch && urlRouter.remountExplorers()) fsEventQueue = [];
        // Update URL with project param
        // Preserve task param from URL — restoreTaskFromUrl needs it after board loads
        urlRouter.write({ project: p.id, task: appStore.selectedTask?.id || urlRouter.task, view: viewParam, file: urlRouter.file || null }, false, push);
        try {
            const b = await getBoard(p.id);
            if (!applyBoard(b, p.id, boardSource(b))) return;
            const boardTasks = appStore.tasks;
            // Mobile: land the board on the column that has work
            if (wasDifferentProject && isMobileCtx.isMobile) {
                boardFocus = { projectId: p.id, column: resolveBoardLandingColumn(boardTasks) };
            }
            servingFromCache = isFromCache(b);
            // Restore task from URL after board loads (await so task detail renders before return)
            await restoreTaskFromUrl();
            // Hydrate cached done-task stats BEFORE subscribing so the
            // known_stats handshake is in place when the server builds the
            // initial payload (skips re-sending stats we already have).
            await hydrateDoneTaskStats(p.id, boardTasks);
            // Subscribe to project for real-time updates
            if (appStream) {
                appStream.subscribeProject(p.id);
            }
        } catch (e) {
            console.error('Failed to load board:', e);
            if (appStore.selectedProject?.id !== p.id) return;
            // Fallback: load board from cache if available
            try {
                const cachedBoard = await getBoardCache<BoardData>(boardCacheKey(p.id));
                if (cachedBoard && applyBoard(cachedBoard, p.id, 'mirror')) {
                    servingFromCache = true;
                    await restoreTaskFromUrl();
                }
            } catch {
                // No cached board available
            }
        }
    }

    function openAddProject() {
        showFileBrowserVal = true;
        addProjectError = '';
    }

    async function handleLogout() {
        await logout();
        // Signing out means this browser keeps nothing: the stores hold this user's
        // messages, diffs, drafts and queued writes, and gc() only prunes tasks
        // that no longer exist. The in-memory queue goes with them.
        clearQueue();
        await clearAll();
        isAuthenticated = false;
    }

    async function handleSelectProjectPath(path: string, sandboxEnabled: boolean) {
        showFileBrowserVal = false;
        addProjectError = '';
        try {
            const proj = await createProject(path, sandboxEnabled);
            appStore.projects = [...appStore.projects, proj];
            clearTaskDetail();
            selectProject(proj);
        } catch (e) {
            addProjectError = String(e);
        }
    }

    async function handleCloneProject(gitUrl: string, targetDir: string, sandboxEnabled: boolean) {
        showFileBrowserVal = false;
        addProjectError = '';
        // Optimistic placeholder so the sidebar shows a clone progress bar in
        // the same tick as the click — the real project only exists on the
        // server after the (potentially long) git clone finishes.
        const placeholderId = `clone-pending-${Date.now()}`;
        const placeholder: Project = {
            id: placeholderId,
            name: repoNameFromGitUrl(gitUrl),
            path: '',
            cloning: true,
            cloneProgress: 0,
            sandbox_enabled: sandboxEnabled,
            created_at: new Date().toISOString(),
            updated_at: new Date().toISOString(),
        };
        appStore.projects = [...appStore.projects, placeholder];
        try {
            const proj = await cloneProject(targetDir, gitUrl, sandboxEnabled, (percent) => {
                appStore.projects = appStore.projects.map(x =>
                    x.id === placeholderId && x.cloneProgress !== percent ? { ...x, cloneProgress: percent } : x);
            });
            const list = appStore.projects;
            if (list.some(p => p.id === placeholderId)) {
                appStore.projects = list.map(p => (p.id === placeholderId ? proj : p));
            } else {
                // A project-list refresh replaced the array mid-clone — append.
                appStore.projects = [...list, proj];
            }
            clearTaskDetail();
            selectProject(proj);
        } catch (e) {
            appStore.projects = appStore.projects.filter(p => p.id !== placeholderId);
            addProjectError = String(e);
            showFileBrowserVal = true;
        }
    }

    function toggleSidebar() {
        collapsed = !collapsed;
        appStore.sidebarCollapsed = collapsed;
        // Manage focus: when collapsing, focus the toggle button; when expanding, focus the first project
        queueMicrotask(() => {
            if (collapsed) {
                (document.querySelector('.desktop-collapse-btn') as HTMLElement | null)?.focus();
            } else {
                (document.querySelector('.project-item') as HTMLElement | null)?.focus();
            }
        });
    }

    // Track whether sidebar was auto-collapsed for tablet Explorer (so we can restore it)
    let sidebarAutoCollapsed = $state(false);

    /** Check if viewport is tablet-sized (>768px and <=1024px). */
    function isTablet(): boolean {
        return !isMobileCtx.isMobile && window.innerWidth <= 1024;
    }

    /** Collapse sidebar on tablet when a file is opened in the Explorer; restore when closed. */
    function handleExplorerFileChange(path: string | null) {
        const isFileOpen = path != null && (path.startsWith(appStore.selectedProject?.path || '') || isSandboxMountPath(path));
        if (isFileOpen) {
            urlRouter.write({ project: appStore.selectedProject?.id, view: urlRouter.changesOpen ? 'review' : 'explorer', file: path });
            // Auto-collapse sidebar on tablet to give editor more space
            if (isTablet() && !collapsed) {
                collapsed = true;
                appStore.sidebarCollapsed = collapsed;
                sidebarAutoCollapsed = true;
            }
        } else {
            urlRouter.write({ project: appStore.selectedProject?.id, view: urlRouter.changesOpen ? 'review' : 'explorer', file: null });
            // Restore sidebar when file is closed
            if (sidebarAutoCollapsed) {
                collapsed = false;
                appStore.sidebarCollapsed = collapsed;
                sidebarAutoCollapsed = false;
            }
        }
    }

    /** Restore sidebar if it was auto-collapsed for the Explorer. */
    function restoreExplorerSidebar() {
        if (sidebarAutoCollapsed) {
            collapsed = false;
            appStore.sidebarCollapsed = collapsed;
            sidebarAutoCollapsed = false;
        }
    }

    async function removeProject(p: Project) {
        const confirmed = await dialogStore.showConfirm({
            title: t('app.project.removeTitle'),
            message: t('app.project.removeMessage', { name: p.name }),
            confirmText: t('app.project.remove'),
            cancelText: t('app.cancel'),
        });
        if (!confirmed) return;
        try {
            await deleteProject(p.id);
            const index = appStore.projects.findIndex(x => x.id === p.id);
            appStore.projects = appStore.projects.filter(x => x.id !== p.id);
            const rest = appStore.projects;
            // Auto-select next project (or previous if it was the last)
            if (rest.length > 0) {
                clearTaskDetail();
                selectProject(rest[Math.min(Math.max(index, 0), rest.length - 1)]);
            } else {
                clearTaskDetail();
                appStore.selectedProject = null;
                appStore.board = null;
            }
        } catch (e) {
            addProjectError = String(e);
        }
    }

    function openSandboxConfig(_p: Project) {
        uiStore.showSandboxConfig = true;
    }

    async function handleSandboxSaved() {
        uiStore.showSandboxConfig = false;
        // Refresh projects list to pick up changes
        try {
            const projs = await getProjects();
            appStore.projects = projs || [];
            const proj = appStore.selectedProject;
            if (proj) {
                const updated = appStore.projects.find(p => p.id === proj.id);
                if (updated) {
                    appStore.selectedProject = updated;
                    // Close terminal so it restarts with updated sandbox mode
                    uiStore.showTerminal = false;
                }
            }
        } catch (e) {
            console.error('Failed to refresh projects:', e);
        }
    }

    // Opening a FULL task view replaces the floating task view (if any).
    $effect(() => {
        if (uiStore.showCardDetail && appStore.selectedTask) uiStore.floatingTask = null;
    });
</script>

<!-- Login gate -->
{#if isLoadingAuth}
    <div class="loading-screen">
        <img class="loading-icon" src={mikeIcon} width="96" height="96" alt="Loading" />
        {#if bootSlow}<p class="loading-slow">{t('app.bootSlow.title')}</p>{/if}
    </div>
{:else if hasLocalCache}
    <div class="offline-screen">
        <img class="offline-icon" src={mikeIcon} width="96" height="96" alt="Offline" />
        <p class="offline-text">{bootSlow ? t('app.bootSlow.title') : t('app.offline.title')}</p>
        <button class="offline-btn" onclick={loadFromCache}>
            <svg width="14" height="14" viewBox="0 0 24 24" fill="currentColor" stroke="none" aria-hidden="true"><polygon points="6,3 20,12 6,21"/></svg>
            {t('app.offline.button')}
        </button>
    </div>
{:else if !isAuthenticated}
    <LoginModal onClose={async () => {
        isAuthenticated = true;
        await initializeAuthSession();
    }} />
{:else}
<!-- Not a live region: the countdown and the age change every second. The region under it
     announces the transitions. -->
{#if !connectionStore.connected}
    <div class="reconnecting-banner">
        <span class="reconnecting-spinner"></span>
        {reconnectStatus}
        {#if staleTimeText}<span class="reconnecting-stale">{t('app.reconnect.dataFrom', { time: staleTimeText })}</span>{/if}
        <button class="reconnecting-btn" onclick={tryReconnectNow} type="button" aria-label="Try connecting now">{t('app.reconnect.connect')}</button>
    </div>
{/if}
<div class="sr-only" role="status" aria-live="polite">
    {#if connectionNotice}{connectionNotice}{/if}
</div>
<!-- Offline cache banner -->
{#if servingFromCache}
    <div class="cache-banner" role="status" aria-live="polite">
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M21 15v4a2 2 0 01-2 2H5a2 2 0 01-2-2v-4"/><polyline points="7 10 12 15 17 10"/><line x1="12" y1="15" x2="12" y2="3"/></svg>
        {t('app.cache.banner')}
        {#if connectionStore.connected}
            <button class="reconnecting-btn" onclick={retryLiveNow} type="button" aria-label="Try connecting now">{t('app.reconnect.connect')}</button>
        {/if}
    </div>
{/if}
<div class="app" class:collapsed class:task-detail={uiStore.showCardDetail && !!appStore.selectedTask} class:ws-disconnected={!connectionStore.connected} class:cache-visible={servingFromCache}>
    <!-- Skip navigation link -->
    <a href="#main-content" class="skip-link">{t('app.skipToMain')}</a>

    <!-- Mobile sidebar backdrop -->
    {#if isMobileCtx.isMobile && mobileMenuOpen}
        <button type="button" class="sidebar-backdrop" onclick={() => mobileMenuOpen = false} aria-label="Close menu" title="Close menu"></button>
    {/if}

    <!-- Sidebar -->
    {#if !urlRouter.terminalOnly}
        <Sidebar
            projects={appStore.projects}
            currentProject={appStore.selectedProject}
            {projectCounts}
            {collapsed}
            isMobile={isMobileCtx.isMobile}
            {mobileMenuOpen}
            {getProjectAbbr}
            onSelectProject={(p) => { clearTaskDetail(); selectProject(p); if (isMobileCtx.isMobile) mobileMenuOpen = false; }}
            onRemoveProject={removeProject}
            onToggleSidebar={toggleSidebar}
            onShowStats={() => { uiStore.showStats = true; if (isMobileCtx.isMobile) mobileMenuOpen = false; }}
            onShowConfig={() => { openSettings(); if (isMobileCtx.isMobile) mobileMenuOpen = false; }}
            onToggleFullScreen={() => {
                if (document.fullscreenElement) {
                    document.exitFullscreen().catch(() => {});
                } else {
                    document.documentElement.requestFullscreen().catch(() => {});
                }
                if (isMobileCtx.isMobile) mobileMenuOpen = false;
            }}
            onAddProject={() => { openAddProject(); if (isMobileCtx.isMobile) mobileMenuOpen = false; }}
        />
    {/if}

    <!-- Main Content -->
    <main id="main-content" class="main" tabindex="-1">
        {#if urlRouter.terminalOnly}
            {#if terminalScreen.key !== '' && !terminalScreen.failed && terminalPanel.component}
                {@const Terminal = terminalPanel.component}
                {#if Terminal}
                    <Terminal standalone />
                {/if}
            {:else if terminalScreen.failed}
                <div class="loading-screen">
                    <p class="loading-slow">{t('api.initializeTask')}</p>
                </div>
            {:else}
                <div class="loading-screen">
                    <img class="loading-icon" src={mikeIcon} width="96" height="96" alt="Loading" />
                </div>
            {/if}
        {:else if appStore.selectedProject}
            {#if !(uiStore.showCardDetail && appStore.selectedTask) && !urlRouter.explorerOpen && !urlRouter.taskExplorerOpen}
            <TopBar
                currentProject={appStore.selectedProject}
                {currentBranch}
                {hasReview}
                onOpenMenu={() => mobileMenuOpen = true}
                onOpenSandboxConfig={() => appStore.selectedProject && openSandboxConfig(appStore.selectedProject)}
                onOpenExplorerView={() => urlRouter.openProjectExplorer(appStore.selectedProject?.id ?? '')}
                onOpenTerminal={openTerminal}
                onShowStats={() => uiStore.showStats = true}
                onShowConfig={openSettings}
            />
            {/if}
            <section class="board-container" aria-label="Kanban board">
                {#if urlRouter.explorerOpen && appStore.selectedProject}
                    <ExplorerView
                        projectId={appStore.selectedProject.id}
                        projectPath={appStore.selectedProject.path}
                        currentBranch={currentBranch}
                        initialFile={urlRouter.file}
                        sandboxEnabled={appStore.selectedProject.sandbox_enabled ?? false}
                        fsEventQueue={fsEventQueue}
                        onOpenTerminal={openTerminal}
                        onUpdateFile={handleExplorerFileChange}
                        changesOpen={urlRouter.changesOpen}
                        onChangesOpen={(open: boolean) => urlRouter.write({ project: appStore.selectedProject?.id, view: open ? 'review' : 'explorer' }, false, open)}
                        onClose={() => { restoreExplorerSidebar(); urlRouter.write({ project: appStore.selectedProject?.id, view: null, file: null }, false, true); urlRouter.applyView(); fsEventQueue = []; }}
                    />
                {:else if urlRouter.taskExplorerOpen && appStore.selectedProject && appStore.selectedTask}
                    <TaskExplorerView
                        projectId={appStore.selectedProject.id}
                        projectName={appStore.selectedProject.name}
                        taskId={appStore.selectedTask.id}
                        taskPath={appStore.config?.workspace_dir ? `${appStore.config.workspace_dir}/${appStore.selectedProject.id}/${appStore.selectedTask.id}` : ''}
                        taskBranch={appStore.selectedTask.branch || ''}
                        taskStatus={appStore.selectedTask.status || ''}
                        initialFile={urlRouter.file}
                        sandboxEnabled={appStore.selectedProject.sandbox_enabled ?? false}
                        fsEventQueue={fsEventQueue}
                        onOpenTerminal={openTerminal}
                        onUpdateFile={(path: string | null) => urlRouter.write({ project: appStore.selectedProject?.id, task: appStore.selectedTask?.id, view: urlRouter.changesOpen ? 'task-review' : 'task-explorer', file: path }, false, !!path)}
                        changesOpen={urlRouter.changesOpen}
                        onChangesOpen={(open: boolean) => urlRouter.write({ project: appStore.selectedProject?.id, task: appStore.selectedTask?.id, view: open ? 'task-review' : 'task-explorer' }, false, open)}
                        onClose={() => { urlRouter.write({ project: appStore.selectedProject?.id, task: appStore.selectedTask?.id, view: null, file: null }, false, true); urlRouter.applyView(); fsEventQueue = []; }}
                    />
                {:else if uiStore.showCardDetail && appStore.selectedTask}
                    <CardDetail task={appStore.selectedTask} tasks={appStore.tasks} projectPath={appStore.selectedProject?.path} projectName={appStore.selectedProject?.name} workspaceDir={appStore.config?.workspace_dir} sandboxEnabled={appStore.selectedProject?.sandbox_enabled} fsEventQueue={fsEventQueue} activeTab={cardDetailActiveTab} onActiveTabChange={(tab: 'context' | 'chat' | 'diff' | 'checkpoint') => cardDetailActiveTab = tab} onClose={(status?: string) => {
                        if (status) scrollToColumn = resolveClosedTaskColumn(status, appStore.tasks, appStore.selectedTask?.id ?? '');
                        uiStore.showCardDetail = false;
                        appStore.selectedTask = null;
                        fsEventQueue = [];
                        urlRouter.write({ project: appStore.selectedProject?.id }, true, true);
                    }} onSelectTask={(t: Task) => {
                        // Push URL first so the subscription sees updated URL and skips replaceState
                        urlRouter.write({ project: appStore.selectedProject?.id, task: t.id }, true, true);
                        if (isMobileCtx.isMobile) cardDetailActiveTab = 'chat';
                        appStore.selectedTask = t;
                    }} onRefreshBoard={() => refreshBoard()} onOpenTerminal={openTerminal} onOpenExplorer={openTaskExplorer} />
                {:else}
                    <Board
                        tasks={appStore.tasks}
                        projectId={appStore.selectedProject.id}
                        projectPath={appStore.selectedProject.path}
                        scrollToColumn={scrollToColumn}
                        {boardFocus}
                        onAddTask={() => { uiStore.creatingChildOf = null; uiStore.showCreateTask = true; }}
                        onCreateChildTask={(parentId: string) => { uiStore.creatingChildOf = parentId; uiStore.showCreateTask = true; }}
                        onRefreshBoard={() => refreshBoard()}
                        onSelectTask={() => { if (isMobileCtx.isMobile) cardDetailActiveTab = 'chat'; }}
                    />
                {/if}
            </section>
        {:else}
            <div class="empty-state">
                <button class="icon-btn" onclick={openSettings} aria-label="Settings" title="Settings">
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                        <circle cx="12" cy="12" r="3"/>
                        <path d="M19.4 15a1.65 1.65 0 00.33 1.82l.06.06a2 2 0 010 2.83 2 2 0 01-2.83 0l-.06-.06a1.65 1.65 0 00-1.82-.33 1.65 1.65 0 00-1 1.51V21a2 2 0 01-2 2 2 2 0 01-2-2v-.09A1.65 1.65 0 009 19.4a1.65 1.65 0 00-1.82.33l-.06.06a2 2 0 01-2.83 0 2 2 0 010-2.83l.06-.06A1.65 1.65 0 004.68 15a1.65 1.65 0 00-1.51-1H3a2 2 0 01-2-2 2 2 0 012-2h.09A1.65 1.65 0 004.6 9a1.65 1.65 0 00-.33-1.82l-.06-.06a2 2 0 010-2.83 2 2 0 012.83 0l.06.06A1.65 1.65 0 009 4.68a1.65 1.65 0 001-1.51V3a2 2 0 012-2 2 2 0 012 2v.09a1.65 1.65 0 001 1.51 1.65 1.65 0 001.82-.33l.06-.06a2 2 0 012.83 0 2 2 0 010 2.83l-.06.06A1.65 1.65 0 0019.4 9a1.65 1.65 0 001.51 1H21a2 2 0 012 2 2 2 0 01-2 2h-.09a1.65 1.65 0 00-1.51 1z"/>
                    </svg>
                    <UpdateDot />
                </button>
                {#if !hasProviders}
                    <h2>{t('app.empty.welcomeTitle')}</h2>
                    <p>{t('app.empty.welcomeBody')}</p>
                    <button class="btn-primary" onclick={() => uiStore.openSettings()} title="Configure an LLM provider">{t('app.empty.configureProvider')}</button>
                {:else}
                    <h2>{t('app.empty.noProjectsTitle')}</h2>
                    <p>{t('app.empty.noProjectsBody')}</p>
                    <button class="btn-primary" onclick={openAddProject} title="Add a project">{t('app.empty.addProject')}</button>
                {/if}
            </div>
        {/if}
    </main>

    <!-- Editor: Floating task view (Chat only) -->
    {#if uiStore.floatingTask}
        {@const floatingTaskData = appStore.tasks.find((t) => t.id === uiStore.floatingTask!.taskId) ?? uiStore.floatingTask!.task}
        {#if floatingTaskData}
            <CardDetail
                task={floatingTaskData}
                tasks={appStore.tasks}
                projectPath={appStore.selectedProject?.path}
                projectName={appStore.selectedProject?.name}
                workspaceDir={appStore.config?.workspace_dir}
                sandboxEnabled={appStore.selectedProject?.sandbox_enabled}
                fsEventQueue={fsEventQueue}
                activeTab="chat"
                floating
                onClose={() => { uiStore.floatingTask = null; }}
                onSelectTask={(t: Task) => { uiStore.floatingTask = { projectId: appStore.selectedProject?.id as string, taskId: t.id, task: t }; }}
                onRefreshBoard={() => refreshBoard()}
                onOpenTask={() => {
                    // Navigate to the full task view and a new history entry
                    const ft = uiStore.floatingTask;
                    if (!ft) return;
                    uiStore.floatingTask = null;
                    const task = appStore.tasks.find((t) => t.id === ft.taskId) ?? ft.task;
                    appStore.selectedTask = task;
                    uiStore.showCardDetail = true;
                    urlRouter.write({ project: ft.projectId, task: ft.taskId, view: null, file: null }, false, true);
                }}
            />
        {/if}
    {/if}

    <!-- Modals -->
    {#if uiStore.showCreateTask}
        <CreateTaskModal
            projectId={appStore.selectedProject?.id}
            parentId={uiStore.creatingChildOf ?? undefined}
            currentBranch={currentBranch}
            initialPrompt={uiStore.createTaskLineSelection?.prompt}
            lineTask={!!uiStore.createTaskLineSelection}
            onOpenTask={(task: Task) => {
                // Floating task view over the current view
                uiStore.floatingTask = { projectId: appStore.selectedProject?.id as string, taskId: task.id, task };
            }}
            onClose={() => { uiStore.showCreateTask = false; uiStore.creatingChildOf = null; uiStore.createTaskLineSelection = null; }}
            onTaskCreated={() => { uiStore.creatingChildOf = null; }} // board stream pushes update
        />
    {/if}
    {#if appStore.showEditTask && appStore.editTaskData}
        <EditTaskModal
            taskId={appStore.editTaskData.taskId}
            projectId={appStore.editTaskData.projectId}
            taskTitle={appStore.editTaskData.taskTitle}
            editProviders={appStore.editTaskData.editProviders}
            editModel={appStore.editTaskData.editModel}
            editUrl={appStore.editTaskData.editUrl}
            editReasoningEffort={appStore.editTaskData.editReasoningEffort}
            editOperatingMode={appStore.editTaskData.editOperatingMode}
            editAutoMerge={appStore.editTaskData.editAutoMerge}
            editSubtasksAutoMerge={appStore.editTaskData.editSubtasksAutoMerge}
            taskHasParent={appStore.editTaskData.taskHasParent}
            targetBranch={appStore.editTaskData.targetBranch}
            baseRef={appStore.editTaskData.baseRef}
            onSave={saveTaskEdit}
            onCancel={() => {
                appStore.showEditTask = false;
                appStore.editTaskData = null;
            }}
        />
    {/if}
    {#if uiStore.showConfig}
        <ConfigModal
            config={appStore.config}
            onClose={() => uiStore.showConfig = false}
            onSaved={() => uiStore.showConfig = false}
            onSignOut={handleLogout}
        />
    {/if}
    {#if uiStore.showTerminal && terminalPanel.component}
        {#key appStore.selectedProject?.id + '-' + (appStore.selectedProject?.sandbox_enabled ?? '')}
            {@const Terminal = terminalPanel.component}
            {#if Terminal}
                <Terminal />
            {/if}
        {/key}
    {/if}
    {#if uiStore.showStats && appStore.selectedProject}
        <StatsModal
            projectId={appStore.selectedProject.id}
            onClose={() => uiStore.showStats = false}
        />
    {/if}
    {#if showFileBrowserVal}
        <FileBrowser
            onClose={() => showFileBrowserVal = false}
            onSelect={handleSelectProjectPath}
            onClone={handleCloneProject}
        />
    {/if}
    {#if uiStore.showSandboxConfig && appStore.selectedProject}
        <SandboxConfigModal
            project={appStore.selectedProject}
            inProgressCount={projectCounts[appStore.selectedProject.id]?.in_progress ?? 0}
            onClose={() => uiStore.showSandboxConfig = false}
            onSaved={handleSandboxSaved}
        />
    {/if}
    {#if addProjectError}
        <div class="error-toast">{addProjectError}</div>
    {/if}
    <Toast />
    <ConfirmationDialog />
    <CommitDialog />
    <CompressContextDialog />
    <LocaleOffer />
</div>
{/if}

<svelte:head>
    <title>{pageTitle}</title>
</svelte:head>

<style>
    /* -- Loading screen -- */
    .loading-screen {
        position: fixed;
        inset: 0;
        z-index: 99999;
        background: var(--color-surface-0);
        display: flex;
        flex-direction: column;
        align-items: center;
        justify-content: center;
        gap: 1rem;
    }

    .loading-slow {
        color: var(--color-text-muted);
        font-size: 0.875rem;
        margin: 0;
    }
    .loading-icon,
    .offline-icon {
        opacity: 0.8;
    }

    /* -- Offline screen -- */
    .offline-screen {
        position: fixed;
        inset: 0;
        z-index: 99999;
        display: flex;
        flex-direction: column;
        align-items: center;
        justify-content: center;
        gap: 1.5rem;
        background: var(--color-surface-0);
    }

    .offline-text {
        color: var(--color-column-backlog);
        font-size: 0.875rem;
        font-family: inherit;
        margin: 0;
    }

    .offline-btn {
        font-family: inherit;
        font-size: 0.875rem;
        display: flex;
        align-items: center;
        gap: 0.5rem;
        padding: 0.6rem 1.5rem;
        border: 1px solid var(--color-surface-3);
        border-radius: 8px;
        background: var(--color-surface-0);
        color: var(--color-text-primary);
        cursor: pointer;
        transition: background 0.15s, border-color 0.15s;
    }

    .offline-btn svg {
        flex-shrink: 0;
    }

    .offline-btn:hover {
        background: var(--color-surface-2);
        border-color: var(--color-border-bright);
    }

    /* -- Reconnecting banner -- */
    .reconnecting-banner {
        position: fixed;
        top: 0;
        left: 0;
        right: 0;
        z-index: 10001;
        background: var(--color-alert-bg);
        color: var(--color-alert-text);
        text-align: center;
        padding: 6px 16px;
        font-size: 0.8125rem;
        font-weight: 600;
        display: flex;
        align-items: center;
        justify-content: center;
        gap: 8px;
        box-shadow: 0 2px 8px rgba(0, 0, 0, 0.2);
        animation: slide-down 0.2s ease-out;
    }
    .reconnecting-stale {
        font-weight: 400;
        opacity: 0.75;
        font-size: 0.75rem;
    }
    .reconnecting-btn {
        background: rgba(214, 211, 209, 0.15);
        border: 1px solid rgba(214, 211, 209, 0.3);
        color: var(--color-alert-text);
        padding: 2px 10px;
        line-height: 1;
        border-radius: 4px;
        font-size: 0.75rem;
        font-weight: 600;
        cursor: pointer;
        transition: background 0.15s;
    }
    .reconnecting-btn:hover {
        background: rgba(214, 211, 209, 0.25);
    }
    .reconnecting-spinner {
        display: inline-block;
        width: 12px;
        height: 12px;
        border: 2px solid rgba(214, 211, 209, 0.3);
        border-top-color: var(--color-alert-text);
        border-radius: 50%;
        animation: spin 0.6s linear infinite;
    }
    @keyframes slide-down {
        from {
            transform: translateY(-100%);
        }
        to {
            transform: translateY(0);
        }
    }
    /* Shift app content down when banner is visible */
    .app.ws-disconnected {
        padding-top: 30px;
    }

    /* -- Cache banner (offline mirror) -- */
    .cache-banner {
        position: fixed;
        top: 0;
        left: 0;
        right: 0;
        z-index: 10001;
        background: var(--color-alert-bg);
        color: var(--color-alert-text);
        text-align: center;
        padding: 5px 16px;
        font-size: 0.75rem;
        font-weight: 500;
        display: flex;
        align-items: center;
        justify-content: center;
        gap: 6px;
        box-shadow: 0 2px 8px rgba(0, 0, 0, 0.2);
        animation: slide-down 0.2s ease-out;
    }
    .cache-banner svg {
        opacity: 0.6;
    }
    .app.cache-visible {
        padding-top: 28px;
    }

    /* When both banners are visible, stack them */
    .app.ws-disconnected.cache-visible {
        padding-top: 58px;
    }

    /* -- Global mobile styles -- */
    @media (max-width: 768px) {
        :global(input),
        :global(textarea),
        :global(select) {
            font-size: 1rem !important;
        }

        :global(*) {
            -webkit-tap-highlight-color: transparent;
        }

        :global(button),
        :global(a),
        :global([role="button"]) {
            touch-action: manipulation;
        }

        :global(.action-btn),
        :global(.icon-btn),
        :global(.toggle-btn),
        :global(.close-btn),
        :global(.btn-back),
        :global(.btn-send),
        :global(.btn-stop) {
            min-width: 44px;
            min-height: 44px;
        }
    }

    .skip-link {
        position: absolute;
        top: -100%;
        left: 16px;
        z-index: 10000;
        padding: 8px 16px;
        background: var(--color-accent-dark);
        color: white;
        font-weight: 600;
        border-radius: var(--radius-md);
        text-decoration: none;
        white-space: nowrap;
    }
    .skip-link:focus {
        top: 16px;
    }

    .app {
        display: flex;
        height: 100dvh;
        overflow: hidden;
    }
    /* Fullscreen: add top margin for camera cutout on phones */
    :global(html:fullscreen) .app {
        padding-top: env(safe-area-inset-top, 0px);
    }
    .app.task-detail :global(aside#sidebar) {
        display: none;
    }

    .main {
        flex: 1;
        display: flex;
        flex-direction: column;
        overflow: hidden;
    }
    .board-container {
        flex: 1;
        overflow: hidden;
    }
    .empty-state {
        display: flex;
        flex-direction: column;
        align-items: center;
        justify-content: center;
        height: 100%;
        gap: 12px;
        color: var(--color-text-secondary);
        text-align: center;
        position: relative;
    }
    .empty-state .icon-btn {
        position: absolute;
        top: 12px;
        right: 12px;
    }
    .empty-state h2 {
        font-size: 1.25rem;
        color: var(--color-text-primary);
    }
    .btn-primary {
        padding: 8px 20px;
        border-radius: var(--radius-md);
        background: var(--color-accent-dark);
        color: white;
        font-weight: 500;
    }
    .btn-primary:hover {
        background: var(--color-accent-hover);
    }
    .error-toast {
        position: fixed;
        bottom: 24px;
        left: 50%;
        transform: translateX(-50%);
        background: var(--color-status-red-dark);
        color: white;
        padding: 10px 20px;
        border-radius: var(--radius-md);
        font-size: 0.875rem;
        z-index: 400;
        box-shadow: 0 4px 12px rgba(0,0,0,0.3);
    }
    .sidebar-backdrop {
        display: none;
    }

    @media (max-width: 768px) {
        .sidebar-backdrop {
            display: block;
            position: fixed;
            inset: 0;
            background: var(--color-backdrop);
            z-index: 199;
        }
    }
</style>