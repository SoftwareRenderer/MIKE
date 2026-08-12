
<script lang="ts">
    import { onMount, onDestroy } from 'svelte';
    import { get } from 'svelte/store';
    import { appStore, uiStore, dialogStore, updateTaskInBoard } from '$lib/stores';
import { createStreamingContext, createTaskDiffContext, createCommitQueueContext, createExpandedTasksContext, createTaskEventContext } from '$lib/context';
    import { getProjects, getBoard, getConfig, deleteProject, updateProject, createProject, createAppStream, getProjectCounts, setCsrfToken, clearCsrfToken, ensureCsrfToken, setOfflineStatus, loadQueue, flushQueueNow, onQueueFlushed, waitForFlush, applyQueuedActionsToBoard, isFromCache } from '$lib/api';
import { toHomeRelative, getWorktreeDisplayPath } from '$lib/utils/path';
import { getBoardCache, boardCacheKey } from '$lib/storage';
import { useIsMobile } from '$lib/composables/use-is-mobile.svelte';
import kanbanIcon from '$lib/assets/icons/kanban.svg';
    import type { Project, Task, Config, StreamEvent } from '$lib/types';
    import CardDetail from './CardDetail.svelte';
    import { setAppStreamRef } from '$lib/stream-ref';
    import { extensionManager } from '$lib/services/extension-manager.svelte';
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
    import SWUpdateBanner from './SWUpdateBanner.svelte';

    function loadTerminalPanel(): void {
        if (TerminalPanelComponent || terminalLoading) return;
        terminalLoading = true;
        import('./TerminalPanel.svelte').then((mod) => {
            TerminalPanelComponent = mod.default;
            terminalLoading = false;
        });
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
        if (!name || !currentProject) return 'Kanban Board';
        const reviewCount = projectCounts[currentProject.id]?.review ?? 0;
        // If current project has reviews, show it with count
        if (reviewCount > 0) {
            return `🟡 ${name} - ${reviewCount} task${reviewCount === 1 ? '' : 's'} for review`;
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
    let lastSyncedAt = $state(Infinity); // epoch ms of last successful data sync
    let nextReconnectAt = $state(0); // epoch ms of next reconnect attempt
    let countdownSeconds = $state(0);
    let countdownTimer: ReturnType<typeof setInterval> | null = null;
    let staleTimeText = $state(''); // "Data from X ago" text, updated every second while disconnected
    let staleTimer: ReturnType<typeof setInterval> | null = null;
    let visibilityRetryTimer: ReturnType<typeof setTimeout> | null = null; // retry refresh after network stabilizes

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
        staleTimeText = lastSyncedAgo();
    }
    function stopStaleTick() {
        if (staleTimer) {
            clearInterval(staleTimer);
            staleTimer = null;
        }
    }

    /** Format seconds as a human-readable relative time string. */
    function timeAgo(seconds: number): string {
        if (seconds < 60) return `just now`;
        const mins = Math.floor(seconds / 60);
        if (mins < 60) return `${mins}m ago`;
        const hrs = Math.floor(mins / 60);
        return `${hrs}h ago`;
    }

    /** Get relative time since last data sync. */
    function lastSyncedAgo(): string {
        if (lastSyncedAt === Infinity) return 'just now';
        return timeAgo(Math.floor((Date.now() - lastSyncedAt) / 1000));
    }

    function tryReconnectNow() {
        if (appStream) {
            appStream.reconnectNow();
            nextReconnectAt = 0;
        }
    }
    let projectCounts: Record<string, { backlog: number; in_progress: number; review: number }> = $state({});
    let taskDiffStats: Record<string, { hash: string; files: number; additions: number; deletions: number }> = $state({});
    let boardRefreshPending = false; // deduplicate rapid refreshBoard calls
    let showFileBrowserVal = $state(false);
    let showExplorerViewVal = $state(false);
    let showTaskExplorerVal = $state(false);
    let cardDetailActiveTab = $state<'context' | 'chat' | 'diff' | 'checkpoint'>('chat');
let scrollToColumn = $state<string | null>(null); // column to scroll to on mobile after task delete
    let fsEventQueue = $state<Array<{ op: 'create' | 'delete'; path: string }>>([]);
    let addProjectError = $state('');
    let portConflict = $state<{ project_id: string; ports: number[] } | null>(null);
    let currentBranch = $state('');
    let urlFileId = $state(new URLSearchParams(location.search).get('file') || '');
    let mobileMenuOpen = $state(false);
    let isAuthenticated = $state(false);
    let isLoadingAuth = $state(true);
    let servingFromCache = $state(false);
    let hasLocalCache = $state(false); // true when server unreachable but cache exists
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
    $effect(() => { if (taskDiffStatsContext) taskDiffStats = get(taskDiffStatsContext); });

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
    function handlePopState() {
        const params = new URLSearchParams(location.search);
        const projectId = params.get('project');
        const taskId = params.get('task');
        const fileParam = params.get('file') || '';
        urlFileId = (fileParam && (fileParam.startsWith(currentProject?.path || '') || fileParam.startsWith('/home/sandbox/'))) ? fileParam : '';

        // If URL has no project param, go back to project list
        if (!projectId && currentProject) {
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
                selectProject(targetProject);
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
        // Restore explorer view from URL
        const viewParam = params.get('view');
        if (viewParam === 'explorer') {
            showExplorerViewVal = true;
            showTaskExplorerVal = false;
        } else if (viewParam === 'task-explorer') {
            showExplorerViewVal = false;
            showTaskExplorerVal = true;
        } else {
            showExplorerViewVal = false;
            showTaskExplorerVal = false;
        }
        // Notify child components (e.g., TaskChat) that the URL changed
        window.dispatchEvent(new CustomEvent('urlchanged'));
    }

    $effect(() => {
        window.addEventListener('popstate', handlePopState);
        return () => window.removeEventListener('popstate', handlePopState);
    });

    async function initializeAuthSession() {
        appStream = createAppStream({
            onProjectCounts: (counts) => {
                projectCounts = counts;
            },
            onRefresh: () => refreshBoard(),
            onDiffStats: (task_id, hash, short_stat, files_n, untracked_n, untracked_lines, tracked_add, tracked_del, file_hashes, removed_files, staged_paths, has_committed_changes, already_merged) => {
                const additions = tracked_add + (untracked_lines || 0);
                const deletions = tracked_del;
                taskDiffStatsContext.update(map => {
                    map[task_id] = { hash: hash || '', files: files_n, additions, deletions, untracked_n: untracked_n || 0, file_hashes, removed_files, staged_paths, has_committed_changes, already_merged };
                    return { ...map };
                });
            },
            onDiff: (task_id, hash, diff, untracked, files) => {
                taskDiffContext.update(map => {
                    const hasManifest = files && Object.keys(files).length > 0;
                    const hasLegacy = diff || (untracked?.length ?? 0) > 0;
                    if (hasManifest || hasLegacy) {
                        map[task_id] = { hash, diff, untracked, files, updated_at: Date.now() };
                    } else {
                        delete map[task_id];
                    }
                    return { ...map };
                });
            },
            onBranch: (branch) => {
                currentBranch = branch;
                if (branch && !showExplorerViewVal) {
                    const params = new URLSearchParams(location.search);
                    if (params.get('view') === 'explorer') {
                        showExplorerViewVal = true;
                    }
                }
            },
            onFsEvent: (op, path) => {
                if (showExplorerViewVal || showTaskExplorerVal) {
                    fsEventQueue = [...fsEventQueue, { op, path }];
                }
            },
            onTaskUpdated: (updatedTask: Task) => {
                const idx = tasks.findIndex(t => t.id === updatedTask.id);
                if (idx >= 0) {
                    tasks = tasks.map((t, i) => i === idx ? updatedTask : t);
                } else {
                    tasks = [...tasks.filter(t => t.id !== updatedTask.id), updatedTask];
                }
                appStore.board = { ...appStore.board!, tasks };
                if (appStore.selectedTask?.id === updatedTask.id) {
                    appStore.selectedTask = updatedTask;
                }
            },
            onTaskDeleted: (task_id: string) => {
                tasks = tasks.filter(t => t.id !== task_id);
                appStore.board = { ...appStore.board!, tasks };
                if (appStore.selectedTask && appStore.selectedTask.id === task_id) {
                    clearTaskDetail();
                }
            },
            onCommitStatus: (task_id: string, status: 'queued' | 'processing') => {
                const proj = currentProject;
                if (proj) {
                    commitQueueStatusContext.update(map => ({ ...map, [proj.id]: { taskID: task_id, status } }));
                }
            },
            onTaskEvent: (task_id: string, event: StreamEvent) => {
                if (appStore.selectedTask && appStore.selectedTask.id === task_id) {
                    taskEventContext.set({ task_id, ...event });
                }
            },
            onConnected: (connected) => {
                wsConnected = connected;
                setOfflineStatus(!connected);
                if (!connected) {
                    lastSyncedAt = Date.now();
                    startStaleTick();
                } else {
                    stopCountdown();
                    stopStaleTick();
                    nextReconnectAt = 0;
                    countdownSeconds = 0;
                    if (initialLoadComplete) {
                        silentRefresh().catch(() => {});
                    }
                }
            },
            onReconnectScheduled: (nextAttemptAt) => {
                nextReconnectAt = nextAttemptAt;
                startCountdown();
            },
            onConnectAttempt: () => {
                stopCountdown();
                countdownSeconds = 0;
            },
            onPortConflict: (project_id, conflicted_ports) => {
                portConflict = { project_id, ports: conflicted_ports };
            },
            onProjectPorts: (ports) => {
                appStore.projectTerminalPorts = ports;
            },
            onExtensionChange: () => {
                extensionManager.loadExtensions().catch(() => {});
            },
        });
        setAppStreamRef(appStream);
        const urlParams = new URLSearchParams(location.search);
        const viewParam = urlParams.get('view');
        if (viewParam === 'explorer') {
            showExplorerViewVal = true;
            showTaskExplorerVal = false;
        } else if (viewParam === 'task-explorer') {
            showExplorerViewVal = false;
            showTaskExplorerVal = true;
        }
        await loadAppData();
        extensionManager.loadExtensions().catch(() => {});
    }

    async function loadAppData() {
        try {
            // Restore offline action queue from IndexedDB
            await loadQueue();
            // Flush queued actions via HTTP immediately (don't wait for WebSocket to connect)
            flushQueueNow();
            // After offline queue is flushed, replace optimistic tasks with real ones from HTTP responses
            onQueueFlushed((optimisticIds, resolvedTasks) => {
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
            });
            const projs = await getProjects();
            projects = projs || [];
            appStore.projects = projects;
            portConflict = null; // Clear stale port conflict on reload
            if (projects.length > 0) {
                // Check URL for project param to restore previous project
                const urlParams = new URLSearchParams(location.search);
                const urlProjectId = urlParams.get('project');
                const targetProject = urlProjectId
                    ? projects.find(p => p.id === urlProjectId)
                    : null;
                await selectProject(targetProject || projects[0]);
                // Replay queued actions onto cached board so UI reflects user's last actions
                tasks = applyQueuedActionsToBoard(tasks);
                appStore.board = { ...appStore.board!, tasks };
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
    }

    // Initialize context providers
    const { taskDiffStats: taskDiffStatsContext, taskDiff: taskDiffContext } = createTaskDiffContext();
    const commitQueueStatusContext = createCommitQueueContext();
    const expandedTasksContext = createExpandedTasksContext();
    const taskEventContext = createTaskEventContext();
    const streamingIdsContext = createStreamingContext();

    onMount(async () => {
        // Check auth status first. If this fails, we're offline — load from cache.
        // Show offline screen after 2s if no response yet (but don't abort the request).
        let authResolved = false;
        const offlineTimer = setTimeout(async () => {
            if (authResolved) return;
            setOfflineStatus(true);
            try {
                const cachedProjects = await getBoardCache<Project[]>('projects');
                if (cachedProjects && cachedProjects.length > 0) {
                    hasLocalCache = true;
                }
            } catch { /* no cache */ }
            isLoadingAuth = false;
        }, 2000);
        try {
            const authRes = await fetch('/api/auth/session', { credentials: 'include' });
            authResolved = true;
            clearTimeout(offlineTimer);
            const authData = await authRes.json();
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
            // Check if we have cached data to offer the user
            try {
                const cachedProjects = await getBoardCache<Project[]>('projects');
                if (cachedProjects && cachedProjects.length > 0) {
                    hasLocalCache = true;
                }
            } catch (cacheErr) {
                console.error('Failed to check cached data:', cacheErr);
            }
            isLoadingAuth = false;
        }
    });

    // Detect when the tab becomes visible again (e.g. phone wakes from sleep).
    // Check the WebSocket for zombie connections, force reconnect if stale,
    // try immediate reconnect if disconnected, and always refresh board data.
    // Always refresh on wake because the per-connection event buffer is lost
    // when the WebSocket drops — any task_updated/project_counts events during
    // the disconnect window are missed and must be recovered via HTTP fetch.
    // If the first refresh fails (network still recovering from suspension),
    // retry once after a short delay to give the network stack time to stabilize.
    function handleVisibilityChange() {
        if (document.hidden || !appStream) return;
        appStream.checkStale();
        if (!wsConnected) {
            appStream.reconnectNow();
        }
        // Always refresh on tab-visible — WebSocket events may have been lost
        // during sleep/disconnect (per-connection buffer is evicted on reconnect).
        refreshBoard().catch(() => {
            // First refresh failed — network may still be recovering.
            // Retry once after a short delay.
            if (visibilityRetryTimer) return; // already retrying
            visibilityRetryTimer = setTimeout(() => {
                visibilityRetryTimer = null;
                refreshBoard().catch(() => {}); // give up after second attempt
            }, 1000);
        });
    }
    window.addEventListener('visibilitychange', handleVisibilityChange);

    onDestroy(() => {
        stopCountdown();
        if (visibilityRetryTimer) {
            clearTimeout(visibilityRetryTimer);
            visibilityRetryTimer = null;
        }
        window.removeEventListener('visibilitychange', handleVisibilityChange);
        disconnectBoardStream();
        if (appStream) {
            appStream.close();
            appStream = null;
        }
        if (urlRafId !== null) cancelAnimationFrame(urlRafId);
    });

    /** Load the board from local cache after the user clicks "Load Local Cache". */
    async function loadFromCache() {
        hasLocalCache = false; // hide the offline screen
        // Keep isLoadingAuth = true so the loading screen bridges the gap
        // while cached data is read from IndexedDB (avoids flash of empty state)
        isAuthenticated = true;
        appStream = createAppStream({
            onProjectCounts: (counts) => { projectCounts = counts; },
            onRefresh: () => refreshBoard(),
            onDiffStats: () => {},
            onDiff: () => {},
            onBranch: (branch) => { currentBranch = branch; },
            onFsEvent: () => {},
            onTaskUpdated: () => {},
            onTaskDeleted: () => {},
            onCommitStatus: () => {},
            onTaskEvent: () => {},
            onConnected: (connected) => {
                wsConnected = connected;
                setOfflineStatus(!connected);
                if (!connected) {
                    // Record the disconnect time so the stale banner counts from now
                    lastSyncedAt = Date.now();
                    startStaleTick();
                } else {
                    stopCountdown();
                    stopStaleTick();
                    nextReconnectAt = 0;
                    countdownSeconds = 0;
                    // Skip refresh on first connect — data was just loaded in onMount.
                    if (initialLoadComplete) {
                        silentRefresh().catch(() => {});
                    }
                }
            },
            onReconnectScheduled: (nextAttemptAt) => {
                nextReconnectAt = nextAttemptAt;
                startCountdown();
            },
            onConnectAttempt: () => {
                stopCountdown();
                countdownSeconds = 0;
            },
            onPortConflict: () => {},
            onExtensionChange: () => {
                extensionManager.loadExtensions().catch(() => {});
            },
        });
        setAppStreamRef(appStream);
        await loadAppData();
        initialLoadComplete = true; // mark initial data load as done
        isLoadingAuth = false; // hide loading screen after data is loaded

        // Load installed extensions for LSP discovery
        extensionManager.loadExtensions().catch(() => {});
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
        let task = tasks.find(t => t.id === taskId);
        if (!task) {
            try {
                // Refresh board to pick up tasks created via API
                const b = await getBoard(currentProject.id);
                tasks = b.tasks || [];
                appStore.board = b;
                task = tasks.find(t => t.id === taskId);
                if (!task) {
                    // Task truly doesn't exist — clear task param
                    const url = new URL(location.href);
                    url.searchParams.delete('task');
                    history.replaceState(null, '', location.pathname + url.search);
                    return;
                }
            } catch {
                return;
            }
        }
        appStore.selectedTask = task;
        // Restore view state from URL
        const viewParam = params.get('view');
        if (viewParam === 'task-explorer') {
            showTaskExplorerVal = true;
            showExplorerViewVal = false;
            uiStore.showCardDetail = false;
        } else if (viewParam === 'explorer') {
            showExplorerViewVal = true;
            showTaskExplorerVal = false;
            uiStore.showCardDetail = false;
        } else {
            uiStore.showCardDetail = true;
        }
    }

    function disconnectBoardStream() {
        if (currentProject && appStream) {
            appStream.unsubscribeProject(currentProject.id);
        }
        // Task unsubscribe is handled by the $effect cleanup when appStore.selectedTask changes
    }

    async function refreshBoard() {
        if (!currentProject || boardRefreshPending) return;
        boardRefreshPending = true;
        try {
            // Wait for pending queue flush
            await waitForFlush();
            const b = await getBoard(currentProject.id);
            tasks = b.tasks || [];
            appStore.board = b;
            // Update selected task reference to point to fresh object from API
            const task = appStore.selectedTask;
            if (task) {
                const fresh = tasks.find(t => t.id === task.id);
                if (fresh) {
                    appStore.selectedTask = fresh;
                }
            }
            // Live data received — clear cache indicator
            servingFromCache = false;
            if (wsConnected) lastSyncedAt = Date.now();
        } catch (e) {
            // When offline, sync tasks from appStore (has optimistic updates)
            const b = appStore.board;
            if (b?.tasks) {
                tasks = b.tasks;
            }
        } finally {
            boardRefreshPending = false;
        }
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
                const b = await getBoard(currentProject.id);
                tasks = b.tasks || [];
                appStore.board = b;
                // Update selected task reference
                { const sel = appStore.selectedTask; if (sel) {
                    const fresh = tasks.find(t => t.id === sel.id);
                    if (fresh) {
                        appStore.selectedTask = fresh;
                    }
                }}
            }
            const c = await getConfig();
            cfg = c;
            appStore.config = c;
            servingFromCache = false;
            if (wsConnected) lastSyncedAt = Date.now();
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

    async function selectProject(p: Project) {
        disconnectBoardStream();
        const wasExplorerViewOpen = showExplorerViewVal;
        const wasTaskExplorerOpen = showTaskExplorerVal;
        const wasProjectSwitch = !!currentProject;
        currentProject = p;
        currentBranch = '';
        appStore.selectedProject = p;
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
        const viewParam = wasExplorerViewOpen ? 'explorer' : (wasTaskExplorerOpen ? 'task-explorer' : null);
        updateUrl({ project: p.id, task: appStore.selectedTask?.id || urlTaskId, view: viewParam, file: urlFileId || null }, false, true);
        if (wasProjectSwitch && wasExplorerViewOpen) {
            queueMicrotask(() => { showExplorerViewVal = true; });
        }
        if (wasProjectSwitch && wasTaskExplorerOpen) {
            queueMicrotask(() => { showTaskExplorerVal = true; });
        }
        try {
            const b = await getBoard(p.id);
            tasks = b.tasks || [];
            appStore.board = b;
            lastSyncedAt = Date.now();
            // Restore task from URL after board loads (await so task detail renders before return)
            await restoreTaskFromUrl();
            // Subscribe to project for real-time updates
            if (appStream) {
                appStream.subscribeProject(p.id);
            }
        } catch (e) {
            console.error('Failed to load board:', e);
            // Fallback: load board from cache if available
            try {
                const cachedBoard = await getBoardCache<{ tasks: Task[] }>(boardCacheKey(p.id));
                if (cachedBoard) {
                    tasks = cachedBoard.tasks || [];
                    appStore.board = cachedBoard;
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
        try {
            const proj = await createProject(targetDir, sandboxEnabled, gitUrl);
            projects.push(proj);
            appStore.projects = projects;
            clearTaskDetail();
            selectProject(proj);
        } catch (e) {
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
                // Focus the collapse toggle button
                (document.querySelector('.desktop-collapse-btn') as HTMLElement | null)?.focus();
            } else {
                // Focus the first project item
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
            updateUrl({ project: currentProject?.id, view: 'explorer', file: path });
            // Auto-collapse sidebar on tablet to give editor more space
            if (isTablet() && !collapsed) {
                collapsed = true;
                appStore.sidebarCollapsed = collapsed;
                sidebarAutoCollapsed = true;
            }
        } else {
            updateUrl({ project: currentProject?.id, view: 'explorer', file: null });
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
            title: 'Remove Project',
            message: `Remove "${p.name}" from the board?\n\nThis will delete the project's Kanban board, all tasks, messages, and their worktree directories.\nThe project folder itself will NOT be deleted.`,
            confirmText: 'Remove',
            cancelText: 'Cancel',
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

    function openSandboxConfig(p: Project) {
        // Clear port conflict when opening modal (user will see/fix it in modal)
        portConflict = null;
        uiStore.showSandboxConfig = true;
    }

    async function handleSandboxSaved() {
        uiStore.showSandboxConfig = false;
        portConflict = null; // Clear port conflict on successful save
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
</script>

<!-- Login gate -->
{#if isLoadingAuth}
    <div class="loading-screen">
        <img class="loading-icon" src={kanbanIcon} width="96" height="96" alt="Loading" />
    </div>
{:else if hasLocalCache}
    <div class="offline-screen">
        <img class="offline-icon" src={kanbanIcon} width="96" height="96" alt="Offline" />
        <p class="offline-text">Server unreachable</p>
        <button class="offline-btn" onclick={loadFromCache}>
            <svg width="14" height="14" viewBox="0 0 24 24" fill="currentColor" stroke="none" aria-hidden="true"><polygon points="6,3 20,12 6,21"/></svg>
            Offline Mode
        </button>
    </div>
{:else if !isAuthenticated}
    <LoginModal onClose={async () => {
        isAuthenticated = true;
        await initializeAuthSession();
    }} />
{:else}
<!-- WebSocket reconnecting banner -->
{#if !wsConnected}
    <div class="reconnecting-banner" role="status" aria-live="polite">
        <span class="reconnecting-spinner"></span>
        Reconnecting {#if countdownSeconds > 0}in {countdownSeconds}s {/if}...
        {#if staleTimeText}<span class="reconnecting-stale">Data from {staleTimeText}</span>{/if}
        <button class="reconnecting-btn" onclick={tryReconnectNow} type="button" aria-label="Try connecting now">Connect</button>
    </div>
{/if}
<!-- Offline cache banner -->
{#if servingFromCache}
    <div class="cache-banner" role="status" aria-live="polite">
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M21 15v4a2 2 0 01-2 2H5a2 2 0 01-2-2v-4"/><polyline points="7 10 12 15 17 10"/><line x1="12" y1="15" x2="12" y2="3"/></svg>
        Viewing cached data
    </div>
{/if}
<div class="app" class:collapsed class:task-detail={uiStore.showCardDetail && !!appStore.selectedTask} class:ws-disconnected={!wsConnected} class:cache-visible={servingFromCache}>
    <!-- Skip navigation link -->
    <a href="#main-content" class="skip-link">Skip to main content</a>

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
                isMobile={isMobileCtx.isMobile}
                hasPortConflict={portConflict?.project_id === currentProject?.id}
                {hasReview}
                onOpenMenu={() => mobileMenuOpen = true}
                onOpenSandboxConfig={() => currentProject && openSandboxConfig(currentProject)}
                onOpenExplorerView={() => { showExplorerViewVal = true; updateUrl({ project: currentProject?.id, view: 'explorer' }, false, true); }}
                onOpenTerminal={() => { loadTerminalPanel(); uiStore.showTerminal = true; }}
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
                        onOpenTerminal={() => { loadTerminalPanel(); uiStore.showTerminal = true; }}
                        onUpdateFile={handleExplorerFileChange}
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
                        currentBranch={currentBranch}
                        sandboxEnabled={currentProject.sandbox_enabled ?? false}
                        fsEventQueue={fsEventQueue}
                        onOpenTerminal={() => { loadTerminalPanel(); uiStore.showTerminal = true; }}
                        onUpdateFile={(path: string | null) => { if (path) updateUrl({ project: currentProject?.id, task: appStore.selectedTask?.id, view: 'task-explorer', file: path }, false, true); }}
                        onClose={() => { showTaskExplorerVal = false; updateUrl({ project: currentProject?.id, task: appStore.selectedTask?.id, view: null, file: null }, false, true); fsEventQueue = []; }}
                    />
                {:else if uiStore.showCardDetail && appStore.selectedTask}
                    <CardDetail task={appStore.selectedTask} tasks={tasks} projectPath={currentProject?.path} projectName={currentProject?.name} workspaceDir={cfg?.workspace_dir} sandboxEnabled={currentProject?.sandbox_enabled} activeTab={cardDetailActiveTab} onActiveTabChange={(tab: 'context' | 'chat' | 'diff' | 'checkpoint') => cardDetailActiveTab = tab} onClose={(status?: string) => {
                        if (status) scrollToColumn = status;
                        uiStore.showCardDetail = false;
                        appStore.selectedTask = null;
                        updateUrl({ project: currentProject?.id }, true, true);
                    }} onSelectTask={(t: Task) => {
                        // Push URL first so the subscription sees updated URL and skips replaceState
                        updateUrl({ project: currentProject?.id, task: t.id }, true, true);
                        if (isMobileCtx.isMobile) cardDetailActiveTab = 'chat';
                        appStore.selectedTask = t;
                    }} onRefreshBoard={() => refreshBoard()} onOpenTerminal={() => { loadTerminalPanel(); uiStore.showTerminal = true; }} onOpenExplorer={() => { showTaskExplorerVal = true; updateUrl({ project: currentProject?.id, task: appStore.selectedTask?.id, view: 'task-explorer' }, false, true); }} />
                {:else}
                    <Board
                        {tasks}
                        projectId={currentProject.id}
                        projectPath={currentProject.path}
                        scrollToColumn={scrollToColumn}
                        onAddTask={() => { uiStore.creatingChildOf = null; uiStore.showCreateTask = true; }}
                        onCreateChildTask={(parentId: string) => { uiStore.creatingChildOf = parentId; uiStore.showCreateTask = true; }}
                        onRefreshBoard={() => refreshBoard()}
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
                    <h2>Welcome to Kanban</h2>
                    <p>Configure an LLM provider to get started. Supports any OpenAI-compatible API.</p>
                    <button class="btn-primary" onclick={() => uiStore.showConfig = true} title="Configure an LLM provider">Configure Provider</button>
                {:else}
                    <h2>No Projects</h2>
                    <p>Add a project to get started. Projects must be git repositories.</p>
                    <button class="btn-primary" onclick={openAddProject} title="Add a project">Add Project</button>
                {/if}
            </div>
        {/if}
    </main>

    <!-- Modals -->
    {#if uiStore.showCreateTask}
        <CreateTaskModal
            projectId={currentProject?.id}
            parentId={uiStore.creatingChildOf ?? undefined}
            currentBranch={currentBranch}
            onClose={() => { uiStore.showCreateTask = false; uiStore.creatingChildOf = null; }}
            onTaskCreated={() => { uiStore.creatingChildOf = null; }} // board stream pushes update
            onOptimisticTaskCreated={(task: Task) => { tasks = [...tasks, task]; }}
            onReplaceOptimisticTask={(optimisticId: string, realTask: Task) => { tasks = tasks.map(t => t.id === optimisticId ? realTask : t); }}
        />
    {/if}
    {#if appStore.showEditTask && appStore.editTaskData}
        <EditTaskModal
            taskId={appStore.editTaskData.taskId}
            projectId={appStore.editTaskData.projectId}
            taskTitle={appStore.editTaskData.taskTitle}
            taskPrompt={appStore.editTaskData.taskPrompt}
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
            isAgentSelected={appStore.editTaskData.isAgentSelected}
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
    <SWUpdateBanner />
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
        align-items: center;
        justify-content: center;
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