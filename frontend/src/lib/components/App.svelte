
<script lang="ts">
    import { onMount, onDestroy, tick } from 'svelte';
    import { get } from 'svelte/store';
    import { appStore, uiStore, dialogStore, updateTaskInBoard } from '$lib/stores';
    import { t } from '$lib/i18n';
    import { wsConnection } from '$lib/stores/connection';
    import { sandboxStatsStore } from '$lib/stores/sandbox-stats.svelte';
    import { createStreamingContext, createTaskDiffContext, createCommitQueueContext, createExpandedTasksContext, createTaskEventContext } from '$lib/context';
    import { getProjects, getBoard, getConfig, deleteProject, createProject, cloneProject, createAppStream, getProjectCounts, setCsrfToken, clearCsrfToken, ensureCsrfToken, setOfflineStatus, loadQueue, clearQueue, flushQueueNow, onQueueFlushed, waitForFlush, applyQueuedActionsToBoard, isFromCache, initTask, getDroppedActions } from '$lib/api';
    import { repoNameFromGitUrl } from '$lib/utils/repo-name';
    import { dragCarriesFiles } from '$lib/utils/file-drop';
    import { dataAgeText } from '$lib/utils/format-time';
    import { resolveBoardLandingColumn, mergeTaskUpdateIntoBoard } from '$lib/utils/task-utils';
    import { resolveLandingProject } from '$lib/utils/project-landing';
    import { getBoardCache, getBoardCacheEntry, boardCacheKey, doneStatsCacheKey, setBoardCache, removeBoardCache, clearAll, loadAppSetting, saveAppSetting } from '$lib/storage';
    import { useIsMobile } from '$lib/composables/use-is-mobile.svelte';
    import { useKeyboardShortcuts } from '$lib/composables/use-keyboard-shortcuts.svelte';
    import { useBoardArrowNav } from '$lib/composables/use-board-arrow-nav';
    import { createTabReturn } from '$lib/composables/use-tab-return';
    import kanbanIcon from '$lib/assets/icons/kanban.svg';
    import type { Project, Task, Config, StreamEvent, SandboxMemoryStats, DiffStatsEntry, Board as BoardData } from '$lib/types';
    import CardDetail from './CardDetail.svelte';
    import { setAppStreamRef } from '$lib/stream-ref';
    import { extensionManager } from '$lib/services/extension-manager.svelte';
    import { localeOffer } from '$lib/services/locale-offer.svelte';
    import { reportChunkFailure } from '$lib/services/chunk-failure';
    import { sandboxImageStatus } from '$lib/services/sandbox-image-status.svelte';
    const LAST_PROJECT_KEY = 'lastProjectId';

    let TerminalPanelComponent: typeof import('./TerminalPanel.svelte').default | null = $state(null);
    let terminalLoading = $state(false);
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

    function loadTerminalPanel(): void {
        if (TerminalPanelComponent || terminalLoading) return;
        terminalLoading = true;
        import('./TerminalPanel.svelte').then((mod) => {
            TerminalPanelComponent = mod.default;
            terminalLoading = false;
        }).catch((e) => {
            // No latch: the next click asks again.
            reportChunkFailure(e);
            terminalLoading = false;
        });
    }

    /**
     * Open the project terminal (terminal button and open-terminal shortcut).
     * When the task detail is open, initialize that task's worktree first if
     * it does not exist yet.
     */
    async function openTerminal(): Promise<void> {
        loadTerminalPanel();
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
        const name = currentProject?.name;
        if (!name || !currentProject) return t('app.title.default');
        const reviewCount = projectCounts[currentProject.id]?.review ?? 0;
        // If current project has reviews, show it with count
        if (reviewCount > 0) {
            return t('app.title.review', { name, count: reviewCount });
        }
        // Check if any other project has reviews
        for (const [pid, counts] of Object.entries(projectCounts)) {
            if (pid === currentProject.id || !counts.review) continue;
            const project = projects.find(p => p.id === pid);
            const pName = project?.name || pid;
            return `${name} | 🟡 ${pName}`;
        }
        return name;
    }

    /**
     * Update the URL to reflect the current navigation state.
     * Params are always ordered: project, task, view, thread.
     * - project/task: must be explicitly provided; omitted = cleared
     * - view: preserved if omitted; null = cleared
     * - thread: preserved unless clearThread=true
     * @param clearThread Also remove the `thread` param (use when leaving task detail).
     * @param push If true, uses pushState (creates history entry for back button). Default: replaceState.
     */
    function updateUrl(params: { project?: string | null; task?: string | null; view?: string | null; file?: string | null }, clearThread = false, push = false) {
        const current = new URLSearchParams(location.search);
        // project/task: explicitly provided or cleared; view: preserve if omitted
        const project = params.project;
        const task = params.task;
        const view = params.view !== undefined ? params.view : current.get('view');
        const file = params.file !== undefined ? params.file : current.get('file');
        const thread = clearThread ? null : current.get('thread');
        // Keep the Review Changes panel state in sync
        if (params.view !== undefined) {
            urlChangesOpen = params.view === 'review' || params.view === 'task-review';
        }
        // Build query in canonical order: project, task, view, file, thread
        // Use manual encoding to keep / unencoded in file paths
        const parts: string[] = [];
        if (project) parts.push('project=' + encodeURIComponent(project));
        if (task) parts.push('task=' + encodeURIComponent(task));
        if (view) parts.push('view=' + encodeURIComponent(view));
        if (file) parts.push('file=' + file); // keep / unencoded
        if (thread) parts.push('thread=' + encodeURIComponent(thread));
        const newUrl = location.pathname + (parts.length ? '?' + parts.join('&') : '');
        if (location.pathname + location.search !== newUrl) {
            if (push) {
                history.pushState(null, '', newUrl);
            } else {
                history.replaceState(null, '', newUrl);
            }
            // Keep urlFileId in sync — replaceState doesn't fire popstate
            if (params.file !== undefined) {
                urlFileId = params.file || '';
            }
        }
    }

    let projects: Project[] = $state([]);
    let currentProject: Project | null = $state(null);
    let tasks: Task[] = $state([]);
    let cfg: Config | null = $state(null);
    let collapsed = $state(false);
    let appStream: ReturnType<typeof createAppStream> | null = $state(null);
    let wsConnected = $state(false);
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
     */
    function applyBoard(board: BoardData, projectId: string): boolean {
        if (!board || currentProject?.id !== projectId) return false;
        tasks = board.tasks || [];
        appStore.board = board;
        return true;
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
        !wsConnected
            ? (everDropped ? t('app.reconnect.status') : '')
            : (everDropped && hadConnection ? t('app.reconnect.back') : '')
    );

    function handleWsConnection(connected: boolean): void {
        wsConnected = connected;
        wsConnection.set(connected);
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
    let projectCounts: Record<string, { backlog: number; in_progress: number; review: number }> = $state({});
    // Cached done-task diff stats, keyed "<projectId>:<taskId>". Done-task diffs
    // are immutable (pinned BaseCommit..BranchTip), so entries persist in
    // IndexedDB across sessions: on project subscribe they're declared via
    // known_stats and the server skips re-sending the stats frame.
    let doneStatsCache: Record<string, { updated_at: string; stats: DiffStatsEntry }> = $state({});
    let boardRefreshPending = false; // deduplicate rapid refreshBoard calls
    let showFileBrowserVal = $state(false);
    let showExplorerViewVal = $state(false);
    let showTaskExplorerVal = $state(false);
    // Review Changes panel state (URL: view=review / view=task-review)
    let urlChangesOpen = $state(initialChangesOpen());

    /** True when the initial URL opened the Review Changes panel. */
    function initialChangesOpen(): boolean {
        const v = new URLSearchParams(location.search).get('view');
        return v === 'review' || v === 'task-review';
    }

    /** Restore explorer + changes-panel state from the URL's view param. */
    function applyUrlView(viewParam: string | null) {
        if (viewParam === 'explorer' || viewParam === 'review') {
            showExplorerViewVal = true;
            showTaskExplorerVal = false;
            urlChangesOpen = viewParam === 'review';
        } else if (viewParam === 'task-explorer' || viewParam === 'task-review') {
            showExplorerViewVal = false;
            showTaskExplorerVal = true;
            urlChangesOpen = viewParam === 'task-review';
        } else {
            showExplorerViewVal = false;
            showTaskExplorerVal = false;
            urlChangesOpen = false;
        }
    }
    let cardDetailActiveTab = $state<'context' | 'chat' | 'diff' | 'checkpoint'>('chat');
let scrollToColumn = $state<string | null>(null); // column to scroll to on mobile after task delete
    // Mobile: column the board jumps to after switching project, keyed by project id.
    let boardFocus = $state<{ projectId: string; column: string } | null>(null);
    let fsEventQueue = $state<Array<{ op: 'create' | 'delete'; path: string }>>([]);
    let addProjectError = $state('');
    let currentBranch = $state('');
    let urlFileId = $state(new URLSearchParams(location.search).get('file') || '');
    let mobileMenuOpen = $state(false);

    // No modal/overlay is open (shared gate for the keyboard shortcuts below).
    function noModalOpen(): boolean {
        return (
            !uiStore.showCreateTask &&
            !uiStore.showConfig &&
            !uiStore.showTerminal &&
            !uiStore.showStats &&
            !uiStore.showSandboxConfig &&
            !appStore.showEditTask &&
            !dialogStore.confirmation &&
            !dialogStore.commitDialog &&
            !dialogStore.compressContextDialog
        );
    }
    // Keyboard shortcuts (Settings → Preferences): the create-task shortcut is
    // active only on the visible board; the open-terminal shortcut is active
    // on every page with a terminal button (board, task detail, explorers).
    function boardShortcutsEnabled(): boolean {
        return (
            !!currentProject &&
            !(uiStore.showCardDetail && appStore.selectedTask) &&
            !showExplorerViewVal &&
            !showTaskExplorerVal &&
            noModalOpen()
        );
    }
    function terminalShortcutEnabled(): boolean {
        return !!currentProject && noModalOpen();
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
    let hasProviders = $derived(!!(cfg as Config | null)?.providers?.length);
    let pageTitle = $derived(computePageTitle());
    let hasReview = $derived(!!(currentProject && projectCounts[(currentProject as unknown as Project).id]?.review > 0));

    const isMobileCtx = useIsMobile();

    // Sync reactive stores → local $state
    $effect(() => {
        collapsed = appStore.sidebarCollapsed;
        appStore.persistSidebarCollapsed();
    });
    $effect(() => { const v = appStore.config; if (v) cfg = v; });

    // Manage task-level WebSocket subscription for real-time streaming events.
    // Subscribes when a task is selected, unsubscribes only when switching to a
    // different task (not when closing the task detail — avoids unnecessary
    // unsubscribe/resubscribe when navigating board ↔ task).
    let lastSubKey = '';
    let lastSubStream: ReturnType<typeof createAppStream> | null = null;
    $effect(() => {
        const taskId = appStore.selectedTask?.id;
        const project = currentProject;
        const stream = appStream;
        if (!stream || !project || !taskId) return;
        const key = `${project.id}:${taskId}`;
        if (key !== lastSubKey) {
            if (lastSubKey) {
                lastSubStream?.unsubscribeTask(lastSubKey.split(':')[0], lastSubKey.split(':')[1]);
            }
            stream.subscribeTask(project.id, taskId);
            lastSubKey = key;
            lastSubStream = stream;
        }
    });

    // Handle browser back/forward
    async function handlePopState() {
        const params = new URLSearchParams(location.search);
        const projectId = params.get('project');
        const taskId = params.get('task');
        const fileParam = params.get('file') || '';
        const fileProject = (projectId ? projects.find(p => p.id === projectId) : null) ?? currentProject;
        const fileProjectPath = fileProject?.path ?? '';
        const fileProjectBase = fileProjectPath.split('/').filter(Boolean).pop() ?? '';
        const fileSandboxMount = fileProject?.sandbox_enabled && fileProjectBase
            ? '/home/sandbox/' + fileProjectBase
            : '';
        const fileInBounds = (fileProjectPath && fileParam.startsWith(fileProjectPath)) ||
            (fileSandboxMount && fileParam.startsWith(fileSandboxMount));
        urlFileId = fileParam && fileInBounds ? fileParam : '';

        if (!projectId && currentProject) {
            const landing = await landingProject();
            if (landing) {
                await selectProject(landing, false);
                window.dispatchEvent(new CustomEvent('urlchanged'));
                return;
            }
            currentProject = null;
            appStore.selectedProject = null;
            tasks = [];
            appStore.board = null;
            disconnectBoardStream();
            appStore.selectedTask = null;
            uiStore.showCardDetail = false;
            window.dispatchEvent(new CustomEvent('urlchanged'));
            return;
        }

        // If URL specifies a different project, switch to it
        if (projectId && currentProject && projectId !== currentProject.id) {
            const targetProject = projects.find(p => p.id === projectId);
            if (targetProject) {
                await selectProject(targetProject, false);
                // After board loads, restoreTaskFromUrl will handle the task
                window.dispatchEvent(new CustomEvent('urlchanged'));
                return;
            }
        }

        if (taskId) {
            const task = tasks.find(t => t.id === taskId);
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
        applyUrlView(params.get('view'));
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
        showExplorerViewVal = false;
        showTaskExplorerVal = true;
        updateUrl({ project: currentProject?.id, task: appStore.selectedTask?.id, view: 'task-explorer' }, false, true);
    }

    // Route a go-to-definition jump
    async function routeDefinitionJump(event: Event): Promise<void> {
        const detail = (event as CustomEvent<{ path: string; line?: number; taskId?: string }>).detail;
        // Project jumps are heard by the project explorer, which owns the only
        // editor that can issue one.
        if (!detail?.taskId) return;
        // Not the selected task — nothing here knows how to open that file.
        if (detail.taskId !== appStore.selectedTask?.id) return;
        // The task explorer is showing: its own listener already has this event.
        if (showTaskExplorerVal) return;
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
                projectCounts = counts;
            },
            onRefresh: () => refreshBoard(),
            onDiffStats: (task_id, hash, _short_stat, files_n, untracked_n, untracked_lines, tracked_add, tracked_del, file_hashes, removed_files, staged_paths, has_committed_changes, already_merged) => {
                const additions = tracked_add + (untracked_lines || 0);
                const deletions = tracked_del;
                const stats: DiffStatsEntry = { hash: hash || '', files: files_n, additions, deletions, untracked_n: untracked_n || 0, file_hashes, removed_files, staged_paths, has_committed_changes, already_merged };
                taskDiffStatsContext.update(map => ({ ...map, [task_id]: stats }));
                // persist dont-task stats
                const t = tasks.find(x => x.id === task_id);
                if (t && t.status === 'done' && currentProject && t.updated_at) {
                    const key = `${currentProject.id}:${task_id}`;
                    doneStatsCache[key] = { updated_at: t.updated_at, stats };
                    setBoardCache(doneStatsCacheKey(currentProject.id, task_id), doneStatsCache[key]).catch(() => {});
                }
            },
            onBranch: (branch) => {
                currentBranch = branch;
                if (branch && !showExplorerViewVal) {
                    const view = new URLSearchParams(location.search).get('view');
                    if (view === 'explorer' || view === 'review') {
                        applyUrlView(view);
                    }
                }
            },
            onFsEvent: (op, path) => {
                if (showExplorerViewVal || showTaskExplorerVal || (uiStore.showCardDetail && appStore.selectedTask)) {
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
                const merged = mergeTaskUpdateIntoBoard(tasks, updatedTask, currentProject?.id ?? '');
                if (!merged) return;
                tasks = merged;
                appStore.board = { ...appStore.board!, tasks };
                if (appStore.selectedTask?.id === updatedTask.id) {
                    appStore.selectedTask = updatedTask;
                }
            },
            onTaskDeleted: (task_id: string) => {
                const remaining = tasks.filter(t => t.id !== task_id);
                // A frame naming a task this board never held belongs to another project; there is
                // nothing here to take away.
                if (remaining.length !== tasks.length) {
                    tasks = remaining;
                    appStore.board = { ...appStore.board!, tasks };
                }
                sandboxStatsStore.remove(task_id);
                if (appStore.selectedTask && appStore.selectedTask.id === task_id) {
                    clearTaskDetail();
                }
                taskDiffStatsContext.update(map => {
                    if (!(task_id in map)) return map;
                    const next = { ...map };
                    delete next[task_id];
                    return next;
                });
                if (currentProject) {
                    const key = `${currentProject.id}:${task_id}`;
                    if (doneStatsCache[key]) {
                        const next = { ...doneStatsCache };
                        delete next[key];
                        doneStatsCache = next;
                        removeBoardCache(doneStatsCacheKey(currentProject.id, task_id)).catch(() => {});
                    }
                }
            },
            onCommitStatus: (task_id: string, status: 'queued' | 'processing') => {
                const proj = currentProject;
                // The frame names no project, so the board on screen decides whether the task is
                // ours — the slot is one per project, and another project's task would occupy it.
                if (proj && tasks.some(t => t.id === task_id)) {
                    commitQueueStatusContext.update(map => ({ ...map, [proj.id]: { taskID: task_id, status } }));
                }
            },
            onTaskEvent: (task_id: string, event: StreamEvent) => {
                if (event.type === 'sandbox_stats') {
                    // Sandbox memory stats live in their own store (keyed by
                    // task) — not in the single-slot task event context.
                    sandboxStatsStore.set(task_id, event.data as SandboxMemoryStats);
                    return;
                }
                // Dispatch to the task's TaskChat
                const floatingId = uiStore.floatingTask?.taskId;
                if ((appStore.selectedTask && appStore.selectedTask.id === task_id) ||
                    (floatingId && floatingId === task_id)) {
                    taskEventContext.set({ task_id, ...event });
                }
            },
            onConnected: handleWsConnection,
            onReconnectScheduled: (nextAttemptAt) => {
                nextReconnectAt = nextAttemptAt;
                startCountdown();
            },
            onConnectAttempt: () => {
                stopCountdown();
                countdownSeconds = 0;
            },
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
        applyUrlView(new URLSearchParams(location.search).get('view'));
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
                tasks = tasks.map(t => {
                    // First: optimisticId → real task from createTask
                    const created = resolvedTasks.get(t.id);
                    if (!created) return t;
                    // Then: realId → latest state from subsequent actions (start, move, etc.)
                    const latest = resolvedTasks.get(created.id);
                    return latest || created;
                });
                appStore.board = { ...appStore.board!, tasks };
                // Clear cache indicator after flush succeeds
                servingFromCache = false;
                reportRefusedWrites();
            });
            const projs = await getProjects();
            projects = projs || [];
            appStore.projects = projects;
            if (projects.length > 0) {
                const urlParams = new URLSearchParams(location.search);
                const urlProjectId = urlParams.get('project');
                const targetProject = (urlProjectId ? projects.find(p => p.id === urlProjectId) : null)
                    ?? await landingProject();
                if (targetProject) {
                    await selectProject(targetProject, false);
                }
                // Replay queued actions onto cached board so UI reflects user's last actions —
                // this project's actions only; the queue holds every project's writes.
                if (currentProject) {
                    tasks = applyQueuedActionsToBoard(tasks, currentProject.id);
                    appStore.board = { ...appStore.board!, tasks };
                }
                // Check if data was served from cache
                servingFromCache = isFromCache(projs);
            }
            // Load config (best-effort — cache fallback inside getConfig)
            try {
                const c = await getConfig();
                cfg = c;
                appStore.config = c;
            } catch {
                // No cached config — proceed without it
            }
            // Fetch initial project counts (best-effort, no cache fallback)
            try {
                const counts = await getProjectCounts();
                projectCounts = counts;
            } catch {
                // Use defaults — counts are non-critical
            }
        } catch (e) {
            console.error('Failed to load:', e);
        }
        // Mark initial load as done. Reconnects now trigger silentRefresh()
        // to pick up changes that occurred while offline.
        initialLoadComplete = true;
    }

    // Initialize context providers
    const { taskDiffStats: taskDiffStatsContext } = createTaskDiffContext();
    const commitQueueStatusContext = createCommitQueueContext();
    createExpandedTasksContext();
    const taskEventContext = createTaskEventContext();
    createStreamingContext();

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
            const authRes = await fetch('/api/auth/session', { credentials: 'include' });
            authResolved = true;
            clearTimeout(offlineTimer);
            const authData = await authRes.json();
            bootSlow = false;
            // The mirror screen outranks the app, so only this takes it down.
            hasLocalCache = false;
            isAuthenticated = authData.authenticated;
            if (isAuthenticated) {
                // Extract CSRF token from session response
                if (authData.csrf_token) setCsrfToken(authData.csrf_token);
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
            onProjectCounts: (counts) => { projectCounts = counts; },
            onRefresh: () => refreshBoard(),
            onDiffStats: () => {},
            onBranch: (branch) => { currentBranch = branch; },
            onFsEvent: () => {},
            onTaskUpdated: () => {},
            onTaskDeleted: () => {},
            onCommitStatus: () => {},
            onTaskEvent: () => {},
            onConnected: handleWsConnection,
            onReconnectScheduled: (nextAttemptAt) => {
                nextReconnectAt = nextAttemptAt;
                startCountdown();
            },
            onConnectAttempt: () => {
                stopCountdown();
                countdownSeconds = 0;
            },
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
    let lastUrlCheck = '';
    let lastUrlCheckTime = 0;
    const URL_CHECK_THROTTLE = 250; // ms between checks

    function checkUrlChange() {
        const now = Date.now();
        if (now - lastUrlCheckTime < URL_CHECK_THROTTLE) {
            urlRafId = requestAnimationFrame(checkUrlChange);
            return;
        }
        if (location.search !== lastUrlCheck) {
            lastUrlCheck = location.search;
            lastUrlCheckTime = now;
            if (tasks.length > 0) {
                restoreTaskFromUrl();
            }
        }
        urlRafId = requestAnimationFrame(checkUrlChange);
    }

    $effect(() => {
        if (!isAuthenticated || !currentProject) return;
        lastUrlCheck = location.search;
        lastUrlCheckTime = Date.now();
        urlRafId = requestAnimationFrame(checkUrlChange);
        return () => { if (urlRafId !== null) cancelAnimationFrame(urlRafId); };
    });

    // Function to restore task from URL after board loads.
    // When task is not in local tasks array (e.g., created via API, board stream
    // hasn't pushed yet), refresh board from server to pick up new tasks.
    async function restoreTaskFromUrl() {
        const params = new URLSearchParams(location.search);
        const taskId = params.get('task');
        if (!taskId || !currentProject) return;
        const projectId = currentProject.id;
        let task = tasks.find(t => t.id === taskId);
        if (!task) {
            try {
                // Refresh board to pick up tasks created via API
                const b = await getBoard(projectId);
                if (!applyBoard(b, projectId)) return;
                task = tasks.find(t => t.id === taskId);
                if (!task) {
                    // The task belongs to another project, so the detail on screen is not the URL's:
                    // clearing the param alone leaves it rendered under the project the URL names.
                    const url = new URL(location.href);
                    url.searchParams.delete('task');
                    history.replaceState(null, '', location.pathname + url.search);
                    appStore.selectedTask = null;
                    uiStore.showCardDetail = false;
                    return;
                }
            } catch {
                return;
            }
        }
        appStore.selectedTask = task;
        // Restore view state from URL
        const viewParam = params.get('view');
        if (viewParam === 'explorer' || viewParam === 'review' || viewParam === 'task-explorer' || viewParam === 'task-review') {
            applyUrlView(viewParam);
            uiStore.showCardDetail = false;
        } else {
            applyUrlView(null);
            uiStore.showCardDetail = true;
        }
    }

    function disconnectBoardStream() {
        if (currentProject && appStream) {
            appStream.unsubscribeProject(currentProject.id);
        }
        // The tracked task subscription belongs to the project being left, so it goes with it.
        if (lastSubKey) {
            const [subProjectId, subTaskId] = lastSubKey.split(':');
            // The floating chat subscribed the same key itself and nothing re-subscribes after an
            // unsubscribe, so a chat still open on screen keeps its stream.
            if (uiStore.floatingTask?.taskId !== subTaskId) {
                lastSubStream?.unsubscribeTask(subProjectId, subTaskId);
                lastSubKey = '';
            }
        }
        // Task unsubscribe on closing the detail is handled by the subscription effect.
    }

    /** Hydrate cached done-task stats (IndexedDB) into the in-memory cache and
     * the diff-stats context so cards render immediately and the next
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
        // Mirror into the context only for tasks without live stats already.
        const current = get(taskDiffStatsContext);
        const toAdd: Record<string, DiffStatsEntry> = {};
        for (const { id, entry } of hits) {
            if (!(id in current)) toAdd[id] = entry.stats;
        }
        if (Object.keys(toAdd).length > 0) {
            taskDiffStatsContext.update(map => ({ ...map, ...toAdd }));
        }
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
        if (!cached || appStore.board || currentProject?.id !== projectId) return;
        if (!applyBoard(cached, projectId)) return;
        lastSyncedAt = entry!.cachedAt;
        // The banner says "this is the newest copy there is", which is only true with no socket to
        // bring the live board in; a slow-but-connected switch must not flash it.
        servingFromCache = !wsConnected;
    }

    async function refreshBoard() {
        if (!currentProject || boardRefreshPending) return;
        const projectId = currentProject.id;
        boardRefreshPending = true;
        try {
            // Wait for pending queue flush
            await waitForFlush();
            const b = await getBoard(projectId);
            if (!applyBoard(b, projectId)) return;
            void hydrateDoneTaskStats(projectId, tasks);
            // Update selected task reference to point to fresh object from API
            const task = appStore.selectedTask;
            if (task) {
                const fresh = tasks.find(t => t.id === task.id);
                if (fresh) {
                    appStore.selectedTask = fresh;
                }
            }
            // Live data received — clear cache indicator
            servingFromCache = isFromCache(b);
            // A mirror board is not data that just arrived; it keeps the age it had.
            if (!isFromCache(b)) lastSyncedAt = Date.now();
        } catch (e) {
            if (currentProject?.id !== projectId) return;
            // When offline, sync tasks from appStore (has optimistic updates)
            const b = appStore.board;
            if (b?.tasks) {
                tasks = b.tasks;
            }
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
            projects = projs || [];
            appStore.projects = projects;
            if (currentProject) {
                const projectId = currentProject.id;
                const b = await getBoard(projectId);
                if (applyBoard(b, projectId)) {
                    // Update selected task reference
                    { const sel = appStore.selectedTask; if (sel) {
                        const fresh = tasks.find(t => t.id === sel.id);
                        if (fresh) {
                            appStore.selectedTask = fresh;
                        }
                    }}
                }
            }
            const c = await getConfig();
            cfg = c;
            appStore.config = c;
            servingFromCache = false;
            lastSyncedAt = Date.now();
        } catch {
            // Still offline — keep cached data
        }
    }

    function clearTaskDetail() {
        // Push URL first so the subscription sees updated URL
        updateUrl({ project: currentProject?.id }, true, true);
        appStore.selectedTask = null;
        uiStore.showCardDetail = false;
    }

    async function landingProject(): Promise<Project | null> {
        const last = await loadAppSetting(LAST_PROJECT_KEY, '').catch(() => '');
        return resolveLandingProject(projects, last);
    }

    /**
     * Make `p` the current project. `push` is false where the URL is not a new navigation — the
     * launch selection and every popstate — so no board-less URL is left under the app for the back
     * button to land on. In an installed PWA that entry is the launch entry itself.
     */
    async function selectProject(p: Project, push = true) {
        disconnectBoardStream();
        const wasExplorerViewOpen = showExplorerViewVal;
        const wasTaskExplorerOpen = showTaskExplorerVal;
        const wasProjectSwitch = !!currentProject;
        const wasDifferentProject = !!currentProject && currentProject.id !== p.id;
        currentProject = p;
        // Image progress belongs to the project it was sent for.
        if (wasDifferentProject) sandboxImageStatus.clear();
        currentBranch = '';
        appStore.selectedProject = p;
        // The list on screen belongs to the project the user left; the new project's own cached copy
        // replaces it, never the old project's cards.
        if (wasDifferentProject) {
            tasks = [];
            appStore.board = null;
            void showCachedBoard(p.id);
        }
        saveAppSetting(LAST_PROJECT_KEY, p.id).catch(() => {});
        // Re-mount explorer view for the new project (only on project switch, not initial load)
        if (wasProjectSwitch && wasExplorerViewOpen) {
            showExplorerViewVal = false;
            fsEventQueue = [];
        }
        if (wasProjectSwitch && wasTaskExplorerOpen) {
            showTaskExplorerVal = false;
            fsEventQueue = [];
        }
        // Update URL with project param
        // Preserve task param from URL — restoreTaskFromUrl needs it after board loads
        const urlTaskId = new URLSearchParams(location.search).get('task');
        const viewParam = wasExplorerViewOpen ? (urlChangesOpen ? 'review' : 'explorer')
            : (wasTaskExplorerOpen ? (urlChangesOpen ? 'task-review' : 'task-explorer') : null);
        updateUrl({ project: p.id, task: appStore.selectedTask?.id || urlTaskId, view: viewParam, file: urlFileId || null }, false, push);
        if (wasProjectSwitch && wasExplorerViewOpen) {
            queueMicrotask(() => { showExplorerViewVal = true; });
        }
        if (wasProjectSwitch && wasTaskExplorerOpen) {
            queueMicrotask(() => { showTaskExplorerVal = true; });
        }
        try {
            const b = await getBoard(p.id);
            if (!applyBoard(b, p.id)) return;
            const boardTasks = tasks;
            // Mobile: land the board on the column that has work
            if (wasDifferentProject && isMobileCtx.isMobile) {
                boardFocus = { projectId: p.id, column: resolveBoardLandingColumn(boardTasks) };
            }
            servingFromCache = isFromCache(b);
            if (!servingFromCache) lastSyncedAt = Date.now();
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
            if (currentProject?.id !== p.id) return;
            // Fallback: load board from cache if available
            try {
                const cachedBoard = await getBoardCache<BoardData>(boardCacheKey(p.id));
                if (cachedBoard && applyBoard(cachedBoard, p.id)) {
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
        const csrfToken = await ensureCsrfToken();
        await fetch('/api/auth/logout', {
            method: 'POST',
            credentials: 'include',
            headers: csrfToken ? { 'X-CSRF-Token': csrfToken } : {},
        });
        clearCsrfToken();
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
            projects.push(proj);
            appStore.projects = projects;
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
        projects.push(placeholder);
        appStore.projects = projects;
        try {
            const proj = await cloneProject(targetDir, gitUrl, sandboxEnabled, (percent) => {
                const p = projects.find(x => x.id === placeholderId);
                if (p && p.cloneProgress !== percent) {
                    p.cloneProgress = percent;
                }
            });
            const idx = projects.findIndex(p => p.id === placeholderId);
            if (idx >= 0) {
                projects.splice(idx, 1, proj);
            } else {
                // A project-list refresh replaced the array mid-clone — append.
                projects.push(proj);
            }
            appStore.projects = projects;
            clearTaskDetail();
            selectProject(proj);
        } catch (e) {
            const idx = projects.findIndex(p => p.id === placeholderId);
            if (idx >= 0) {
                projects.splice(idx, 1);
            }
            appStore.projects = projects;
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
        const isFileOpen = path != null && (path.startsWith(currentProject?.path || '') || path.startsWith('/home/sandbox/'));
        if (isFileOpen) {
            updateUrl({ project: currentProject?.id, view: urlChangesOpen ? 'review' : 'explorer', file: path });
            // Auto-collapse sidebar on tablet to give editor more space
            if (isTablet() && !collapsed) {
                collapsed = true;
                appStore.sidebarCollapsed = collapsed;
                sidebarAutoCollapsed = true;
            }
        } else {
            updateUrl({ project: currentProject?.id, view: urlChangesOpen ? 'review' : 'explorer', file: null });
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
            const index = projects.indexOf(p);
            projects.splice(index, 1);
            appStore.projects = projects;
            // Auto-select next project (or previous if it was the last)
            if (projects.length > 0) {
                const nextIndex = Math.min(index, projects.length - 1);
                clearTaskDetail();
                selectProject(projects[nextIndex]);
            } else {
                clearTaskDetail();
                currentProject = null;
                appStore.selectedProject = null;
                tasks = [];
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
            projects = projs || [];
            appStore.projects = projects;
            const proj = currentProject;
            if (proj) {
                const updated = projects.find(p => p.id === proj.id);
                if (updated) {
                    currentProject = updated;
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
        <img class="loading-icon" src={kanbanIcon} width="96" height="96" alt="Loading" />
        {#if bootSlow}<p class="loading-slow">{t('app.bootSlow.title')}</p>{/if}
    </div>
{:else if hasLocalCache}
    <div class="offline-screen">
        <img class="offline-icon" src={kanbanIcon} width="96" height="96" alt="Offline" />
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
{#if !wsConnected}
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
    </div>
{/if}
<div class="app" class:collapsed class:task-detail={uiStore.showCardDetail && !!appStore.selectedTask} class:ws-disconnected={!wsConnected} class:cache-visible={servingFromCache}>
    <!-- Skip navigation link -->
    <a href="#main-content" class="skip-link">{t('app.skipToMain')}</a>

    <!-- Mobile sidebar backdrop -->
    {#if isMobileCtx.isMobile && mobileMenuOpen}
        <button type="button" class="sidebar-backdrop" onclick={() => mobileMenuOpen = false} aria-label="Close menu" title="Close menu"></button>
    {/if}

    <!-- Sidebar -->
    <Sidebar
        {projects}
        {currentProject}
        {projectCounts}
        {collapsed}
        isMobile={isMobileCtx.isMobile}
        {mobileMenuOpen}
        {getProjectAbbr}
        onSelectProject={(p) => { clearTaskDetail(); selectProject(p); if (isMobileCtx.isMobile) mobileMenuOpen = false; }}
        onRemoveProject={removeProject}
        onToggleSidebar={toggleSidebar}
        onShowStats={() => { uiStore.showStats = true; if (isMobileCtx.isMobile) mobileMenuOpen = false; }}
        onShowConfig={() => { uiStore.showConfig = true; if (isMobileCtx.isMobile) mobileMenuOpen = false; }}
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

    <!-- Main Content -->
    <main id="main-content" class="main" tabindex="-1">
        {#if currentProject}
            {#if !(uiStore.showCardDetail && appStore.selectedTask) && !showExplorerViewVal && !showTaskExplorerVal}
            <TopBar
                currentProject={currentProject}
                {currentBranch}
                {hasReview}
                onOpenMenu={() => mobileMenuOpen = true}
                onOpenSandboxConfig={() => currentProject && openSandboxConfig(currentProject)}
                onOpenExplorerView={() => { showExplorerViewVal = true; updateUrl({ project: currentProject?.id, view: 'explorer' }, false, true); }}
                onOpenTerminal={openTerminal}
                onShowStats={() => uiStore.showStats = true}
                onShowConfig={() => uiStore.showConfig = true}
            />
            {/if}
            <section class="board-container" aria-label="Kanban board">
                {#if showExplorerViewVal && currentProject}
                    <ExplorerView
                        projectId={currentProject.id}
                        projectPath={currentProject.path}
                        currentBranch={currentBranch}
                        initialFile={urlFileId || ''}
                        sandboxEnabled={currentProject.sandbox_enabled ?? false}
                        fsEventQueue={fsEventQueue}
                        onOpenTerminal={openTerminal}
                        onUpdateFile={handleExplorerFileChange}
                        changesOpen={urlChangesOpen}
                        onChangesOpen={(open: boolean) => updateUrl({ project: currentProject?.id, view: open ? 'review' : 'explorer' }, false, open)}
                        onClose={() => { restoreExplorerSidebar(); showExplorerViewVal = false; updateUrl({ project: currentProject?.id, view: null, file: null }, false, true); fsEventQueue = []; }}
                    />
                {:else if showTaskExplorerVal && currentProject && appStore.selectedTask}
                    <TaskExplorerView
                        projectId={currentProject.id}
                        projectName={currentProject.name}
                        taskId={appStore.selectedTask.id}
                        taskPath={cfg?.workspace_dir ? `${cfg.workspace_dir}/${currentProject.id}/${appStore.selectedTask.id}` : ''}
                        taskBranch={appStore.selectedTask.branch || ''}
                        taskStatus={appStore.selectedTask.status || ''}
                        sandboxEnabled={currentProject.sandbox_enabled ?? false}
                        fsEventQueue={fsEventQueue}
                        onOpenTerminal={openTerminal}
                        onUpdateFile={(path: string | null) => { if (path) updateUrl({ project: currentProject?.id, task: appStore.selectedTask?.id, view: urlChangesOpen ? 'task-review' : 'task-explorer', file: path }, false, true); }}
                        changesOpen={urlChangesOpen}
                        onChangesOpen={(open: boolean) => updateUrl({ project: currentProject?.id, task: appStore.selectedTask?.id, view: open ? 'task-review' : 'task-explorer' }, false, open)}
                        onClose={() => { showTaskExplorerVal = false; updateUrl({ project: currentProject?.id, task: appStore.selectedTask?.id, view: null, file: null }, false, true); fsEventQueue = []; }}
                    />
                {:else if uiStore.showCardDetail && appStore.selectedTask}
                    <CardDetail task={appStore.selectedTask} tasks={tasks} projectPath={currentProject?.path} projectName={currentProject?.name} workspaceDir={cfg?.workspace_dir} sandboxEnabled={currentProject?.sandbox_enabled} fsEventQueue={fsEventQueue} activeTab={cardDetailActiveTab} onActiveTabChange={(tab: 'context' | 'chat' | 'diff' | 'checkpoint') => cardDetailActiveTab = tab} onClose={(status?: string) => {
                        if (status) scrollToColumn = status;
                        uiStore.showCardDetail = false;
                        appStore.selectedTask = null;
                        fsEventQueue = [];
                        updateUrl({ project: currentProject?.id }, true, true);
                    }} onSelectTask={(t: Task) => {
                        // Push URL first so the subscription sees updated URL and skips replaceState
                        updateUrl({ project: currentProject?.id, task: t.id }, true, true);
                        if (isMobileCtx.isMobile) cardDetailActiveTab = 'chat';
                        appStore.selectedTask = t;
                    }} onRefreshBoard={() => refreshBoard()} onOpenTerminal={openTerminal} onOpenExplorer={openTaskExplorer} />
                {:else}
                    <Board
                        {tasks}
                        projectId={currentProject.id}
                        projectPath={currentProject.path}
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
                <button class="icon-btn" onclick={() => uiStore.showConfig = true} aria-label="Settings" title="Settings">
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                        <circle cx="12" cy="12" r="3"/>
                        <path d="M19.4 15a1.65 1.65 0 00.33 1.82l.06.06a2 2 0 010 2.83 2 2 0 01-2.83 0l-.06-.06a1.65 1.65 0 00-1.82-.33 1.65 1.65 0 00-1 1.51V21a2 2 0 01-2 2 2 2 0 01-2-2v-.09A1.65 1.65 0 009 19.4a1.65 1.65 0 00-1.82.33l-.06.06a2 2 0 01-2.83 0 2 2 0 010-2.83l.06-.06A1.65 1.65 0 004.68 15a1.65 1.65 0 00-1.51-1H3a2 2 0 01-2-2 2 2 0 012-2h.09A1.65 1.65 0 004.6 9a1.65 1.65 0 00-.33-1.82l-.06-.06a2 2 0 010-2.83 2 2 0 012.83 0l.06.06A1.65 1.65 0 009 4.68a1.65 1.65 0 001-1.51V3a2 2 0 012-2 2 2 0 012 2v.09a1.65 1.65 0 001 1.51 1.65 1.65 0 001.82-.33l.06-.06a2 2 0 012.83 0 2 2 0 010 2.83l-.06.06A1.65 1.65 0 0019.4 9a1.65 1.65 0 001.51 1H21a2 2 0 012 2 2 2 0 01-2 2h-.09a1.65 1.65 0 00-1.51 1z"/>
                    </svg>
                </button>
                {#if !hasProviders}
                    <h2>{t('app.empty.welcomeTitle')}</h2>
                    <p>{t('app.empty.welcomeBody')}</p>
                    <button class="btn-primary" onclick={() => uiStore.showConfig = true} title="Configure an LLM provider">{t('app.empty.configureProvider')}</button>
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
        {@const floatingTaskData = tasks.find((t) => t.id === uiStore.floatingTask!.taskId) ?? uiStore.floatingTask!.task}
        {#if floatingTaskData}
            <CardDetail
                task={floatingTaskData}
                tasks={tasks}
                projectPath={currentProject?.path}
                projectName={currentProject?.name}
                workspaceDir={cfg?.workspace_dir}
                sandboxEnabled={currentProject?.sandbox_enabled}
                fsEventQueue={fsEventQueue}
                activeTab="chat"
                floating
                onClose={() => { uiStore.floatingTask = null; }}
                onSelectTask={(t: Task) => { uiStore.floatingTask = { projectId: currentProject?.id as string, taskId: t.id, task: t }; }}
                onRefreshBoard={() => refreshBoard()}
                onOpenTask={() => {
                    // Navigate to the full task view and a new history entry
                    const ft = uiStore.floatingTask;
                    if (!ft) return;
                    uiStore.floatingTask = null;
                    const task = tasks.find((t) => t.id === ft.taskId) ?? ft.task;
                    appStore.selectedTask = task;
                    uiStore.showCardDetail = true;
                    updateUrl({ project: ft.projectId, task: ft.taskId, view: null, file: null }, false, true);
                }}
            />
        {/if}
    {/if}

    <!-- Modals -->
    {#if uiStore.showCreateTask}
        <CreateTaskModal
            projectId={currentProject?.id}
            parentId={uiStore.creatingChildOf ?? undefined}
            currentBranch={currentBranch}
            initialPrompt={uiStore.createTaskLineSelection?.prompt}
            lineTask={!!uiStore.createTaskLineSelection}
            onOpenTask={(task: Task) => {
                // Floating task view over the current view
                uiStore.floatingTask = { projectId: currentProject?.id as string, taskId: task.id, task };
            }}
            onClose={() => { uiStore.showCreateTask = false; uiStore.creatingChildOf = null; uiStore.createTaskLineSelection = null; }}
            onTaskCreated={() => { uiStore.creatingChildOf = null; }} // board stream pushes update
            onOptimisticTaskCreated={(task: Task) => { tasks = [...tasks, task]; }}
            onReplaceOptimisticTask={(optimisticId: string, realTask: Task) => { tasks = tasks.map(t => t.id === optimisticId ? realTask : t); }}
            onRemoveOptimisticTask={(optimisticId: string) => { tasks = tasks.filter(t => t.id !== optimisticId); }}
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
            onSave={(title: string, model: string, url: string, reasoningEffort: string, operatingMode: string, autoMerge: boolean, subtasksAutoMerge: boolean, baseRef: string) => {
                const taskId = appStore.editTaskData!.taskId;
                const projectId = appStore.editTaskData!.projectId;
                // Find the task in the board and update it
                const task = tasks.find(t => t.id === taskId);
                if (task) {
                    const trimmed = title.trim();
                    const updates: Record<string, unknown> = { title: trimmed };
                    updates.operating_mode = operatingMode;
                    updates.auto_commit = autoMerge;
                    updates.auto_merge = autoMerge;
                    updates.subtasks_auto_merge = subtasksAutoMerge;
                    if (baseRef) updates.base_ref = baseRef;
                    if (model) updates.model_name = model;
                    if (url) updates.llama_server_url = url;
                    if (reasoningEffort) updates.reasoning_effort = reasoningEffort;
                    task.title = trimmed;
                    task.operating_mode = operatingMode;
                    task.auto_commit = autoMerge;
                    task.auto_merge = autoMerge;
                    task.subtasks_auto_merge = subtasksAutoMerge;
                    if (baseRef) task.base_ref = baseRef;
                    if (model) task.model_name = model;
                    if (url) task.llama_server_url = url;
                    if (reasoningEffort) task.reasoning_effort = reasoningEffort;
                    updateTaskInBoard(appStore, task.id, task);
                    import('$lib/api').then(({ updateTask }) => updateTask(projectId, taskId, updates)).catch(err => {
                        console.error('Failed to update task:', err);
                    });
                }
                appStore.showEditTask = false;
                appStore.editTaskData = null;
            }}
            onCancel={() => {
                appStore.showEditTask = false;
                appStore.editTaskData = null;
            }}
        />
    {/if}
    {#if uiStore.showConfig}
        <ConfigModal
            config={cfg}
            onClose={() => uiStore.showConfig = false}
            onSaved={() => uiStore.showConfig = false}
            onSignOut={handleLogout}
        />
    {/if}
    {#if uiStore.showTerminal && TerminalPanelComponent}
        {#key currentProject?.id + '-' + (currentProject?.sandbox_enabled ?? '')}
            {@const Terminal = TerminalPanelComponent}
            {#if Terminal}
                <Terminal />
            {/if}
        {/key}
    {/if}
    {#if uiStore.showStats && currentProject}
        <StatsModal
            projectId={currentProject.id}
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
    {#if uiStore.showSandboxConfig && currentProject}
        <SandboxConfigModal
            project={currentProject}
            inProgressCount={projectCounts[currentProject.id]?.in_progress ?? 0}
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
    @keyframes spin {
        to {
            transform: rotate(360deg);
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