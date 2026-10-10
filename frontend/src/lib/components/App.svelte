
<script lang="ts">
    import { onMount, onDestroy, tick, untrack } from 'svelte';
    import { appStore, uiStore, dialogStore, updateTaskInBoard, removeTaskFromBoard, applyResolvedTasksToBoard } from '$lib/stores';
    import { errorText } from '$lib/utils/error-text';
    import { t } from '$lib/i18n';
    import Spinner from './Spinner.svelte';
    import EmptyState from './EmptyState.svelte';
    import { isSandboxMountPath } from '$lib/utils/path';
    import { isAnyModalOpen } from '$lib/utils/modal-state';
    import { connectionStore } from '$lib/stores/connection.svelte';
    import { sandboxStatsStore } from '$lib/stores/sandbox-stats.svelte';
    import { diffStatsStore } from '$lib/stores/diff-stats.svelte';
    import { commitQueueStore } from '$lib/stores/commit-queue.svelte';
    import { taskEventStore } from '$lib/stores/task-events.svelte';
    import { getProjects, getBoard, getConfig, deleteProject, createProject, cloneProject, createAppStream, getProjectCounts, setOfflineStatus, loadQueue, flushQueueNow, onQueueFlushed, waitForFlush, applyQueuedActionsToBoard, isFromCache, initTask, getDroppedActions } from '$lib/api';
    import { reconnectBanner } from '$lib/services/reconnect-banner.svelte';
    import { sessionBoot } from '$lib/services/session-boot.svelte';
    import { repoNameFromGitUrl } from '$lib/utils/repo-name';
    import { dragCarriesFiles } from '$lib/utils/file-drop';
    import { resolveBoardLandingColumn, resolveClosedTaskColumn, mergeTaskUpdateIntoBoard } from '$lib/utils/task-utils';
    import { resolveLandingProject } from '$lib/utils/project-landing';
    import { explorerPathRoots } from '$lib/utils/app-link';
    import { urlRouter } from '$lib/services/url-router.svelte';
    import { reviewNotifier } from '$lib/services/review-notifications.svelte';
    import { getBoardCache, getBoardCacheEntry, boardCacheKey, doneStatsCacheKey, setBoardCache, removeBoardCache, loadAppSetting, saveAppSetting } from '$lib/storage';
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
            reconnectBanner.lastSyncedAt = Date.now();
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
            clearProjectScreen();
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

    /** Open the one app stream this page holds. `mirror` is the offline screen's own: its task handlers
     *  stay silent, because a mirrored board has no live task to follow. */
    function openAppStream(mode: 'live' | 'mirror'): void {
        if (mode === 'mirror') {
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
                ...reconnectBanner.handlers,
                onExtensionChange: handleExtensionChange,
            });
            setAppStreamRef(appStream);
            return;
        }
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
                    reconnectBanner.lastSyncedAt = Date.now();
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
                        reconnectBanner.lastSyncedAt = Date.now();
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
            ...reconnectBanner.handlers,
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
                sessionBoot.servingFromCache = false;
                reportRefusedWrites();
            });
            // Config answers for the server and counts for every project, so neither queues behind a board
            // it has nothing to do with. Both are *delivered* where they were delivered before, after the
            // board landed: the Review ledger reads counts in the order it is told about them, and the
            // board on screen is what makes a rise on this project explainable
            // (`docs/notifications.md → The ledger`).
            const configRead = getConfig().catch(() => null);
            const countsRead = getProjectCounts().catch(() => null);
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
                sessionBoot.servingFromCache = isFromCache(projs);
            }
            // Best-effort each: a server that answers one and not the other still paints what it can.
            const c = await configRead;
            if (c) appStore.config = c;
            const counts = await countsRead;
            if (counts) applyProjectCounts(counts);
        } catch (e) {
            console.error('Failed to load:', e);
        }
        // Mark initial load as done. Reconnects now trigger silentRefresh()
        // to pick up changes that occurred while offline.
        reconnectBanner.initialLoadComplete = true;
        // One release pass per boot, from the one place both boot paths land on; `start()` guards
        // itself, so a path that runs twice costs nothing. Its opt-in question cannot interrupt the
        // loading screen, because it is asked only after the status read and the stamp read behind it.
        // Nothing leaves the process until that question is answered yes.
        void updateCheck.start();
    }

    /** What the boot may ask of this page. It runs before `onMount` opens any stream, so the first
     *  session check already finds its hooks in place. */
    sessionBoot.configure({
        closeStream: closeAppStream,
        openStream: openAppStream,
        loadAppData,
    });

    onMount(() => {
        void sessionBoot.boot();
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

    // Browser network signals. 'offline' queues writes at once; 'online' lifts that and skips the
    // reconnect backoff.
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
        reconnectBanner.destroy();
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
        if (!sessionBoot.isAuthenticated || !appStore.selectedProject) return;
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
        // A cloning placeholder is never subscribed, so this would name an id the server has never seen.
        if (project && !project.cloning && appStream) {
            appStream.unsubscribeProject(project.id);
        }
    }

    /**
     * Take the current project off the screen: no selection, no board, no frames, no task detail. The
     * stream drops first because `disconnectBoardStream` reads the selection this clears; the URL stays
     * with the caller.
     */
    function clearProjectScreen() {
        disconnectBoardStream();
        appStore.selectedProject = null;
        appStore.board = null;
        appStore.selectedTask = null;
        uiStore.showCardDetail = false;
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
        reconnectBanner.lastSyncedAt = entry!.cachedAt;
        // The banner says "this is the newest copy there is", which is only true with no socket to
        // bring the live board in; a slow-but-connected switch must not flash it.
        sessionBoot.servingFromCache = !connectionStore.connected;
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
            sessionBoot.servingFromCache = isFromCache(b);
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
            sessionBoot.servingFromCache = false;
            reconnectBanner.lastSyncedAt = Date.now();
        } catch {
            // Still offline — keep cached data
        }
    }

    /** What the banner may ask of this page: the stream it reconnects, and the re-read a reconnect owes.
     *  It runs before any stream exists — `onMount` opens the first one — and it names this instance's
     *  `appStream` rather than the module ref, which outlives a `closeAppStream()`. */
    reconnectBanner.configure({
        stream: () => appStream,
        refresh: () => {
            void silentRefresh().catch(() => {});
        },
    });

    function clearTaskDetail() {
        // Push URL first so the subscription sees updated URL
        urlRouter.write({ project: appStore.selectedProject?.id }, true, true);
        appStore.selectedTask = null;
        uiStore.showCardDetail = false;
    }

    /** Land the edit dialog's answer on the board, then tell the server. The dialog's own record names
     * the task it was opened for; a task the board does not hold is not edited anywhere. */
    async function saveTaskEdit(
        title: string, model: string, url: string, reasoningEffort: string,
        operatingMode: string, autoMerge: boolean, subtasksAutoMerge: boolean, baseRef: string,
    ): Promise<void> {
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
        // Awaited and its failure reported: a call left running dies with the page when the person reloads
        // straight after saving, taking the queue entry it was about to write with it. The card has already
        // moved, so a refusal puts it back and says why; a write the queue accepted needs no toast, because the
        // offline banner already names what is unreachable.
        try {
            const { updateTask } = await import('$lib/api');
            await updateTask(edit.projectId, edit.taskId, patch);
        } catch (err) {
            updateTaskInBoard(appStore, edit.taskId, task);
            dialogStore.showToast(errorText(err, t('api.updateTask')), 'error');
        }
    }

    async function landingProject(): Promise<Project | null> {
        const last = await loadAppSetting(LAST_PROJECT_KEY, '').catch(() => '');
        return resolveLandingProject(appStore.projects, last);
    }

    /**
     * Make `p` the current project. `push` is false where the URL is not a new navigation — the launch
     * selection, every popstate, and a clone's placeholder taking the real project's id — so no
     * board-less URL is left under the app for the back button to land on. In an installed PWA that entry
     * is the launch entry itself.
     *
     * `board` says this project has never been on screen, so it opens on its own board rather than on the
     * screen the person left.
     */
    async function selectProject(p: Project, push = true, { board = false }: { board?: boolean } = {}) {
        disconnectBoardStream();
        const previous = appStore.selectedProject;
        const wasProjectSwitch = !!previous;
        const wasDifferentProject = !!previous && previous.id !== p.id;
        // Image progress belongs to the project it was sent for.
        if (wasDifferentProject) sandboxImageStatus.clear();
        currentBranch = '';
        appStore.selectedProject = p;
        // A clone's placeholder has no row on the server: no board, mirror, subscription or setting to
        // ask of it.
        const pending = !!p.cloning;
        // The list on screen belongs to the project the user left; the new project's own cached copy
        // replaces it, never the old project's cards.
        if (wasDifferentProject) {
            appStore.board = null;
            if (!pending) void showCachedBoard(p.id);
        }
        if (!pending) saveAppSetting(LAST_PROJECT_KEY, p.id).catch(() => {});
        if (board) {
            fsEventQueue = [];
            urlRouter.write({ project: p.id, task: '', view: null, file: null }, true, push);
            urlRouter.applyView();
            restoreExplorerSidebar();
        } else {
            // The screen on screen is the `view` the new project's address bar has to keep naming.
            const viewParam = urlRouter.currentView();
            // Re-mount explorer view for the new project (only on project switch, not initial load)
            if (wasProjectSwitch && urlRouter.remountExplorers()) fsEventQueue = [];
            // Preserve task param from URL — restoreTaskFromUrl needs it after the board loads.
            urlRouter.write({ project: p.id, task: appStore.selectedTask?.id || urlRouter.task, view: viewParam, file: urlRouter.file || null }, false, push);
        }
        if (pending) return;
        try {
            const b = await getBoard(p.id);
            if (!applyBoard(b, p.id, boardSource(b))) return;
            const boardTasks = appStore.tasks;
            // Mobile: land the board on the column that has work
            if (wasDifferentProject && isMobileCtx.isMobile) {
                boardFocus = { projectId: p.id, column: resolveBoardLandingColumn(boardTasks) };
            }
            sessionBoot.servingFromCache = isFromCache(b);
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
                    sessionBoot.servingFromCache = true;
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

    async function handleSelectProjectPath(path: string, sandboxEnabled: boolean) {
        showFileBrowserVal = false;
        addProjectError = '';
        try {
            const proj = await createProject(path, sandboxEnabled);
            appStore.projects = [...appStore.projects, proj];
            clearTaskDetail();
            selectProject(proj, true, { board: true });
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
        // Navigate now — the progress belongs to the project being cloned.
        clearTaskDetail();
        void selectProject(placeholder, true, { board: true });
        try {
            const proj = await cloneProject(targetDir, gitUrl, sandboxEnabled, (percent) => {
                appStore.projects = appStore.projects.map(x =>
                    x.id === placeholderId && x.cloneProgress !== percent ? { ...x, cloneProgress: percent } : x);
            });
            const list = appStore.projects;
            const stillOnScreen = appStore.selectedProject?.id === placeholderId;
            if (list.some(p => p.id === placeholderId)) {
                appStore.projects = list.map(p => (p.id === placeholderId ? proj : p));
            } else {
                // A project-list refresh replaced the array mid-clone — append.
                appStore.projects = [...list, proj];
            }
            // Someone who opened another project while the clone ran stays where they are.
            if (stillOnScreen) {
                await selectProject(proj, false, { board: true });
            }
        } catch (e) {
            const stillOnScreen = appStore.selectedProject?.id === placeholderId;
            appStore.projects = appStore.projects.filter(p => p.id !== placeholderId);
            addProjectError = String(e);
            showFileBrowserVal = true;
            if (stillOnScreen) {
                // The failed row was on screen, under an address naming a project that does not exist.
                const next = appStore.projects[0];
                if (next) {
                    void selectProject(next, false, { board: true });
                } else {
                    clearProjectScreen();
                    urlRouter.write({ project: '', view: null, file: null }, true, false);
                }
            }
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
            clearTaskDetail();
            if (rest.length > 0) {
                // Auto-select next project (or previous if it was the last)
                selectProject(rest[Math.min(Math.max(index, 0), rest.length - 1)]);
            } else {
                clearProjectScreen();
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

    // Opening a FULL task view replaces the floating task view (if any) — on the opening, not as a standing
    // ban. The board stream replaces `selectedTask` on every frame, so a standing ban destroys the window the
    // Create-Subtask flow opens on purpose above the parent it is talking about.
    let fullViewWasOpen = false;
    $effect(() => {
        const openId = uiStore.showCardDetail ? appStore.selectedTask?.id ?? '' : '';
        if (openId && (!fullViewWasOpen || openId === untrack(() => uiStore.floatingTask?.taskId))) {
            uiStore.floatingTask = null;
        }
        fullViewWasOpen = !!openId;
    });
</script>

<!-- Login gate -->
{#if sessionBoot.isLoadingAuth}
    <div class="loading-screen">
        <img class="loading-icon" src={mikeIcon} width="96" height="96" alt={t('app.alt.loading')} />
        {#if sessionBoot.bootSlow}<p class="loading-slow">{t('app.bootSlow.title')}</p>{/if}
    </div>
{:else if sessionBoot.hasLocalCache}
    <div class="offline-screen">
        <img class="offline-icon" src={mikeIcon} width="96" height="96" alt={t('app.alt.offline')} />
        <p class="offline-text">{sessionBoot.bootSlow ? t('app.bootSlow.title') : t('app.offline.title')}</p>
        <button class="offline-btn" onclick={() => void sessionBoot.useLocalCache()}>
            <svg width="14" height="14" viewBox="0 0 24 24" fill="currentColor" stroke="none" aria-hidden="true"><polygon points="6,3 20,12 6,21"/></svg>
            {t('app.offline.button')}
        </button>
    </div>
{:else if !sessionBoot.isAuthenticated}
    <LoginModal
        hasPasskey={sessionBoot.hasPasskey}
        caDownload={sessionBoot.caDownload}
        deviceName={sessionBoot.deviceName}
        onClose={() => sessionBoot.startAfterLogin()}
    />
{:else}
<!-- Not a live region: the countdown and the age change every second. The region under it
     announces the transitions. -->
{#if !connectionStore.connected}
    <div class="reconnecting-banner">
        <Spinner size={12} decorative />
        {reconnectBanner.status}
        {#if reconnectBanner.staleTimeText}<span class="reconnecting-stale">{t('app.reconnect.dataFrom', { time: reconnectBanner.staleTimeText })}</span>{/if}
        <button class="reconnecting-btn" onclick={() => reconnectBanner.reconnectNow()} type="button" aria-label={t('app.reconnect.connectAria')}>{t('app.reconnect.connect')}</button>
    </div>
{/if}
<div class="sr-only" role="status" aria-live="polite">
    {#if reconnectBanner.notice}{reconnectBanner.notice}{/if}
</div>
<!-- Offline cache banner -->
{#if sessionBoot.servingFromCache}
    <div class="cache-banner" role="status" aria-live="polite">
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M21 15v4a2 2 0 01-2 2H5a2 2 0 01-2-2v-4"/><polyline points="7 10 12 15 17 10"/><line x1="12" y1="15" x2="12" y2="3"/></svg>
        {t('app.cache.banner')}
        {#if connectionStore.connected}
            <button class="reconnecting-btn" onclick={() => reconnectBanner.retryLive()} type="button" aria-label={t('app.reconnect.connectAria')}>{t('app.reconnect.connect')}</button>
        {/if}
    </div>
{/if}
<div class="app" class:collapsed class:task-detail={uiStore.showCardDetail && !!appStore.selectedTask} class:ws-disconnected={!connectionStore.connected} class:cache-visible={sessionBoot.servingFromCache}>
    <!-- Skip navigation link -->
    <a href="#main-content" class="skip-link">{t('app.skipToMain')}</a>

    <!-- Mobile sidebar backdrop -->
    {#if isMobileCtx.isMobile && mobileMenuOpen}
        <button type="button" class="sidebar-backdrop" onclick={() => mobileMenuOpen = false} aria-label={t('app.sidebar.closeMenuAria')} title={t('app.sidebar.closeMenuAria')}></button>
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
                    <img class="loading-icon" src={mikeIcon} width="96" height="96" alt={t('app.alt.loading')} />
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
            <section class="board-container" aria-label={t('app.boardRegionAria')}>
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
            <div class="start-pane">
                <button class="icon-btn" onclick={openSettings} aria-label={t('settings.title')} title={t('settings.title')}>
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                        <circle cx="12" cy="12" r="3"/>
                        <path d="M19.4 15a1.65 1.65 0 00.33 1.82l.06.06a2 2 0 010 2.83 2 2 0 01-2.83 0l-.06-.06a1.65 1.65 0 00-1.82-.33 1.65 1.65 0 00-1 1.51V21a2 2 0 01-2 2 2 2 0 01-2-2v-.09A1.65 1.65 0 009 19.4a1.65 1.65 0 00-1.82.33l-.06.06a2 2 0 01-2.83 0 2 2 0 010-2.83l.06-.06A1.65 1.65 0 004.68 15a1.65 1.65 0 00-1.51-1H3a2 2 0 01-2-2 2 2 0 012-2h.09A1.65 1.65 0 004.6 9a1.65 1.65 0 00-.33-1.82l-.06-.06a2 2 0 010-2.83 2 2 0 012.83 0l.06.06A1.65 1.65 0 009 4.68a1.65 1.65 0 001-1.51V3a2 2 0 012-2 2 2 0 012 2v.09a1.65 1.65 0 001 1.51 1.65 1.65 0 001.82-.33l.06-.06a2 2 0 012.83 0 2 2 0 010 2.83l-.06.06A1.65 1.65 0 0019.4 9a1.65 1.65 0 001.51 1H21a2 2 0 012 2 2 2 0 01-2 2h-.09a1.65 1.65 0 00-1.51 1z"/>
                    </svg>
                    <UpdateDot />
                </button>
                {#if !hasProviders}
                    <EmptyState title={t('app.empty.welcomeTitle')} hint={t('app.empty.welcomeBody')}>
                        {#snippet action()}
                            <button class="btn btn-primary" onclick={() => uiStore.openSettings()}>{t('app.empty.configureProvider')}</button>
                        {/snippet}
                    </EmptyState>
                {:else}
                    <EmptyState title={t('app.empty.noProjectsTitle')} hint={t('app.empty.noProjectsBody')}>
                        {#snippet action()}
                            <button class="btn btn-primary" onclick={openAddProject}>{t('app.empty.addProject')}</button>
                        {/snippet}
                    </EmptyState>
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
                    // `write` leaves the screen flags alone, so an explorer opened from the top bar would keep
                    // the screen: the URL now names the task and the render still asks for the explorer.
                    urlRouter.applyView();
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
            onSignOut={() => sessionBoot.signOut()}
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
    @import '$lib/styles/shared.css';

    /* -- Loading screen -- */
    .loading-screen {
        position: fixed;
        inset: 0;
        z-index: var(--z-max);
        background: var(--color-surface-0);
        display: flex;
        flex-direction: column;
        align-items: center;
        justify-content: center;
        gap: 1rem;
    }

    .loading-slow {
        color: var(--color-text-muted);
        font-size: var(--text-base);
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
        z-index: var(--z-max);
        display: flex;
        flex-direction: column;
        align-items: center;
        justify-content: center;
        gap: 1.5rem;
        background: var(--color-surface-0);
    }

    .offline-text {
        color: var(--color-column-backlog);
        font-size: var(--text-base);
        font-family: inherit;
        margin: 0;
    }

    .offline-btn {
        font-family: inherit;
        font-size: var(--text-base);
        display: flex;
        align-items: center;
        gap: 0.5rem;
        padding: 0.6rem 1.5rem;
        border: 1px solid var(--color-surface-3);
        border-radius: var(--radius-lg);
        background: var(--color-surface-0);
        color: var(--color-text-primary);
        cursor: pointer;
        transition: background var(--dur-base), border-color var(--dur-base);
    }

    .offline-btn svg {
        flex-shrink: 0;
    }

    .offline-btn:hover {
        opacity: 1;
        background: var(--color-surface-2);
        border-color: var(--color-border-bright);
    }

    /* -- Reconnecting banner -- */
    .reconnecting-banner {
        position: fixed;
        top: 0;
        left: 0;
        right: 0;
        z-index: var(--z-banner);
        background: var(--color-alert-bg);
        color: var(--color-alert-text);
        text-align: center;
        padding: 6px 16px;
        font-size: var(--text-sm);
        font-weight: 600;
        display: flex;
        align-items: center;
        justify-content: center;
        gap: 8px;
        box-shadow: var(--shadow-popover);
        animation: slide-down var(--dur-md) ease-out;
    }
    .reconnecting-stale {
        font-weight: 400;
        opacity: 0.75;
        font-size: var(--text-xs);
    }
    .reconnecting-btn {
        background: rgb(from var(--color-alert-text) r g b / 0.15);
        border: 1px solid rgb(from var(--color-alert-text) r g b / 0.3);
        color: var(--color-alert-text);
        padding: 2px 10px;
        line-height: 1;
        border-radius: var(--radius-sm);
        font-size: var(--text-xs);
        font-weight: 600;
        cursor: pointer;
        transition: background var(--dur-base);
    }
    .reconnecting-btn:hover {
        opacity: 1;
        background: rgb(from var(--color-alert-text) r g b / 0.25);
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
        z-index: var(--z-banner);
        background: var(--color-alert-bg);
        color: var(--color-alert-text);
        text-align: center;
        padding: 5px 16px;
        font-size: var(--text-xs);
        font-weight: 500;
        display: flex;
        align-items: center;
        justify-content: center;
        gap: 6px;
        box-shadow: var(--shadow-popover);
        animation: slide-down var(--dur-md) ease-out;
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
            font-size: var(--text-lg) !important;
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

    /* Above the board it skips to; it never needs to beat a dialog, because the focus trap keeps
       it out of the tab order while one is open. */
    .skip-link {
        position: absolute;
        top: -100%;
        left: 16px;
        z-index: var(--z-modal);
        padding: 8px 16px;
        background: var(--color-accent-dark);
        color: var(--color-btn-text);
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
    /* The pane a person lands on with no project: it fills the column, and the settings button
       keeps the corner outside the centred column EmptyState draws. */
    .start-pane {
        position: relative;
        height: 100%;
        display: flex;
        align-items: center;
        justify-content: center;
    }
    .start-pane .icon-btn {
        position: absolute;
        top: 12px;
        right: 12px;
    }
    /* Same tier as Toast.svelte: a failed add/clone has to stay readable above the picker dialog
       it came from. */
    .error-toast {
        position: fixed;
        bottom: 24px;
        left: 50%;
        transform: translateX(-50%);
        background: var(--color-status-red-dark);
        color: var(--color-btn-text);
        padding: 10px 20px;
        border-radius: var(--radius-md);
        font-size: var(--text-base);
        z-index: var(--z-toast);
        box-shadow: var(--shadow-popover);
    }
    /* The drawer's scrim: one tier under the drawer so the drawer stays hittable, and under a
       dialog so a dialog is never dimmed by a drawer left open behind it. */
    .sidebar-backdrop {
        display: none;
    }

    @media (max-width: 768px) {
        .sidebar-backdrop {
            display: block;
            position: fixed;
            inset: 0;
            background: var(--color-backdrop);
            z-index: var(--z-overlay);
        }
    }
</style>