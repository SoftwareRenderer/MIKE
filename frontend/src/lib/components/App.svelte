
<script lang="ts">
    import { onMount, onDestroy } from 'svelte';
    import { projects as projectsStore, selectedProject, board as boardStore, config as configStore, sidebarCollapsed, showCreateTask, creatingChildOf as creatingChildOfStore, showConfig, showTerminal, showStats, showCardDetail, showSandboxConfig, selectedTask, showConfirm, showToast, showEditTask, editTaskData as editTaskDataStore } from '$lib/store';
import { createStreamingContext, createTaskDiffContext, createCommitQueueContext, createExpandedTasksContext, createTaskEventContext } from '$lib/context';
    import { getProjects, getBoard, getConfig, deleteProject, updateProject, createProject, createAppStream, getProjectCounts, setCsrfToken, clearCsrfToken, ensureCsrfToken, setOfflineStatus, loadQueue, flushQueueNow, onQueueFlushed, waitForFlush, applyQueuedActionsToBoard, isFromCache } from '$lib/api';
import { toHomeRelative, getWorktreeDisplayPath } from '$lib/utils/path';
import shieldIcon from '$lib/assets/icons/shield.svg';
import shieldOffIcon from '$lib/assets/icons/shield-off.svg';
import kanbanIcon from '$lib/assets/icons/kanban.svg';
    import type { Project, Task, Config, StreamEvent } from '$lib/types';
    import CardDetail from './CardDetail.svelte';
    import { setAppStreamRef } from '$lib/stream-ref';
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
    import LoginModal from './LoginModal.svelte';
    import SandboxConfigModal from './SandboxConfigModal.svelte';
    import BranchView from './BranchView.svelte';
    import Sidebar from './App/Sidebar.svelte';
    import TopBar from './App/TopBar.svelte';

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
            return `🟡 ${name} - ${reviewCount} task${reviewCount === 1 ? '' : 's'} awaiting review`;
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
    let projectCounts: Record<string, { backlog: number; in_progress: number; review: number }> = $state({});
    let taskDiffStats: Record<string, { hash: string; files: number; additions: number; deletions: number }> = $state({});
    let boardRefreshPending = false; // deduplicate rapid refreshBoard calls
    let showCreateTaskVal = $state(false);
    let showConfigVal = $state(false);
    let showTerminalVal = $state(false);
    let showStatsVal = $state(false);
    let showCardDetailVal = $state(false);
    let showSandboxConfigVal = $state(false);
    let showEditTaskVal = $state(false);
    let editTaskDataVal: import('$lib/store').EditTaskData | null = $state(null);
    let showFileBrowserVal = $state(false);
    let showBranchViewVal = $state(false);
    let cardDetailActiveTab = $state<'context' | 'chat' | 'diff'>('chat');
    let fsEventQueue = $state<Array<{ op: 'create' | 'delete'; path: string }>>([]);
    let addProjectError = $state('');
    let portConflict = $state<{ project_id: string; ports: number[] } | null>(null);
    let currentBranch = $state('');
    let urlFileId = $state(new URLSearchParams(location.search).get('file') || '');
    let selectedTaskVal: Task | null = $state(null);
    let creatingChildOfVal: string | null = $state(null);
    let mobileMenuOpen = $state(false);
    let isMobile = $derived(false);
    let isAuthenticated = $state(false);
    let isLoadingAuth = $state(true);
    let servingFromCache = $state(false);
    let hasLocalCache = $state(false); // true when server unreachable but cache exists
    let hasProviders = $derived(!!(cfg as Config | null)?.providers?.length);
    let pageTitle = $derived(computePageTitle());

    $effect(() => {
        const check = () => isMobile = window.matchMedia('(max-width: 768px)').matches;
        check();
        window.addEventListener('resize', check);
        return () => window.removeEventListener('resize', check);
    });

    // Sync Svelte 4 stores → local $state (single subscription, no onMount needed)
    $effect(() => {
        const unsub = [
            sidebarCollapsed.subscribe(v => collapsed = v),
            showCreateTask.subscribe(v => showCreateTaskVal = v),
            creatingChildOfStore.subscribe(v => creatingChildOfVal = v),
            showConfig.subscribe(v => showConfigVal = v),
            showTerminal.subscribe(v => { showTerminalVal = v; if (v) loadTerminalPanel(); }),
            showStats.subscribe(v => showStatsVal = v),
            showCardDetail.subscribe(v => showCardDetailVal = v),
            showEditTask.subscribe(v => showEditTaskVal = v),
            editTaskDataStore.subscribe(v => editTaskDataVal = v),
            configStore.subscribe(v => { if (v) cfg = v; }),
            selectedTask.subscribe(t => {
                selectedTaskVal = t;
                if (t && currentProject) {
                    // Skip URL update if task is already in the URL (set by pushState from user action)
                    const urlTask = new URLSearchParams(location.search).get('task');
                    if (urlTask !== t.id) {
                        updateUrl({ project: currentProject.id, task: t.id });
                    }
                }
            }),
            taskDiffStatsContext.subscribe(v => taskDiffStats = v),
        ].map(unsub => unsub);
        return () => unsub.forEach(u => u());
    });

    // Manage task-level WebSocket subscription for real-time streaming events.
    // Subscribes when a task is selected, unsubscribes only when switching to a
    // different task (not when closing the task detail — avoids unnecessary
    // unsubscribe/resubscribe when navigating board ↔ task).
    let lastSubKey = '';
    let lastSubStream: ReturnType<typeof createAppStream> | null = null;
    $effect(() => {
        const taskId = selectedTaskVal?.id;
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
            selectedProject.set(null);
            tasks = [];
            boardStore.set(null);
            disconnectBoardStream();
            selectedTask.set(null);
            showCardDetail.set(false);
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
                selectedTask.set(task);
                showCardDetail.set(true);
            }
        } else {
            selectedTask.set(null);
            showCardDetail.set(false);
        }
        // Restore branch view from URL
        const viewParam = params.get('view');
        if (viewParam === 'branch') {
            showBranchViewVal = true;
        } else {
            showBranchViewVal = false;
        }
        // Notify child components (e.g., TaskChat) that the URL changed
        window.dispatchEvent(new CustomEvent('urlchanged'));
    }

    $effect(() => {
        window.addEventListener('popstate', handlePopState);
        return () => window.removeEventListener('popstate', handlePopState);
    });

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
                boardStore.update(b => b ? { ...b, tasks } : b);
                // Clear cache indicator after flush succeeds
                servingFromCache = false;
            });
            const projs = await getProjects();
            projects = projs || [];
            projectsStore.set(projects);
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
                boardStore.update(b => b ? { ...b, tasks } : b);
                // Check if data was served from cache
                servingFromCache = isFromCache(projs);
            }
            // Load config (best-effort — cache fallback inside getConfig)
            try {
                const c = await getConfig();
                cfg = c;
                configStore.set(c);
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
                const { getBoardCache } = await import('../storage');
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
                // Create app-level WebSocket stream
                appStream = createAppStream({
                    onProjectCounts: (counts) => {
                        projectCounts = counts;
                    },
                    onRefresh: () => refreshBoard(),
                    onDiffStats: (task_id, hash, short_stat, files_n, untracked_n, untracked_lines, file_hashes, removed_files, staged_paths) => {
                        const stat = short_stat || '';
                        const insMatch = stat.match(/(\d+) insertion\w*/);
                        const delMatch = stat.match(/(\d+) deletion\w*/);
                        const additions = (insMatch ? parseInt(insMatch[1]) : 0) + (untracked_lines || 0);
                        const deletions = delMatch ? parseInt(delMatch[1]) : 0;
                        taskDiffStatsContext.update(map => {
                            map[task_id] = { hash: hash || '', files: files_n, additions, deletions, file_hashes, removed_files, staged_paths };
                            return { ...map };
                        });
                    },
                    onDiff: (task_id, hash, diff, untracked, files) => {
                        taskDiffContext.update(map => {
                            // Manifest format (files map) or legacy format (diff string + untracked array)
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
                        // Restore branch view from URL when branch becomes available
                        if (branch && !showBranchViewVal) {
                            const params = new URLSearchParams(location.search);
                            if (params.get('view') === 'branch') {
                                showBranchViewVal = true;
                            }
                        }
                    },
                    onFsEvent: (op, path) => {
                        if (showBranchViewVal) {
                            fsEventQueue = [...fsEventQueue, { op, path }];
                        }
                    },
                    onTaskUpdated: (updatedTask: Task) => {
                        const idx = tasks.findIndex(t => t.id === updatedTask.id);
                        if (idx !== -1) {
                            tasks[idx] = updatedTask;
                        } else {
                            // New task from server — replace optimistic (queued) task in-place to avoid flicker
                            const optIdx = tasks.findIndex(t => (t as any).queued);
                            if (optIdx !== -1) {
                                tasks[optIdx] = updatedTask;
                            } else {
                                tasks.push(updatedTask);
                            }
                        }
                        // Spread into a new array so boardStore subscribers (e.g., CardDetail allTasks)
                        // detect the change — Svelte 5 $derived is reference-based.
                        boardStore.update(b => b ? { ...b, tasks: [...tasks] } : b);
                        if (selectedTaskVal && selectedTaskVal.id === updatedTask.id) {
                            selectedTask.set(updatedTask);
                        }
                    },
                    onTaskDeleted: (task_id: string) => {
                        const idx = tasks.findIndex(t => t.id === task_id);
                        if (idx !== -1) {
                            tasks.splice(idx, 1);
                            // Spread into a new array so boardStore subscribers detect the deletion.
                            boardStore.update(b => b ? { ...b, tasks: [...tasks] } : b);
                        }
                        if (selectedTaskVal && selectedTaskVal.id === task_id) {
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
                        // Forward task events to the task event store for TaskStream to consume
                        if (selectedTaskVal && selectedTaskVal.id === task_id) {
                            taskEventContext.set({ task_id, ...event });
                        }
                    },
                    onConnected: (connected) => {
                        wsConnected = connected;
                        setOfflineStatus(!connected);
                        // Auto-refresh cached data on reconnect
                        if (connected && servingFromCache) {
                            silentRefresh().catch(() => {});
                        }
                    },
                    onPortConflict: (project_id, conflicted_ports) => {
                        portConflict = { project_id, ports: conflicted_ports };
                    },
                });
                // Expose appStream so TaskChat can subscribe after handler setup
                setAppStreamRef(appStream);
                // Restore branch view state before loadAppData (which calls selectProject)
                const urlParams = new URLSearchParams(location.search);
                if (urlParams.get('view') === 'branch') {
                    showBranchViewVal = true;
                }
                await loadAppData();
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
                const { getBoardCache } = await import('../storage');
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
    // Check the WebSocket for zombie connections and force reconnect if stale.
    function handleVisibilityChange() {
        if (!document.hidden && appStream) {
            appStream.checkStale();
        }
    }
    window.addEventListener('visibilitychange', handleVisibilityChange);

    onDestroy(() => {
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
                if (connected && servingFromCache) {
                    silentRefresh().catch(() => {});
                }
            },
            onPortConflict: () => {},
        });
        setAppStreamRef(appStream);
        await loadAppData();
        isLoadingAuth = false; // hide loading screen after data is loaded
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
                boardStore.set(b);
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
        selectedTaskVal = task;
        showCardDetailVal = true;
        selectedTask.set(task);
    }

    function disconnectBoardStream() {
        if (currentProject && appStream) {
            appStream.unsubscribeProject(currentProject.id);
        }
        // Task unsubscribe is handled by the $effect cleanup when selectedTaskVal changes
    }

    async function refreshBoard() {
        if (!currentProject || boardRefreshPending) return;
        boardRefreshPending = true;
        try {
            // Wait for pending queue flush
            await waitForFlush();
            const b = await getBoard(currentProject.id);
            tasks = b.tasks || [];
            boardStore.set(b);
            // Update selected task reference to point to fresh object from API
            const task = selectedTaskVal;
            if (task) {
                const fresh = tasks.find(t => t.id === task.id);
                if (fresh) {
                    selectedTaskVal = fresh;
                    selectedTask.set(fresh);
                }
            }
            // Live data received — clear cache indicator
            servingFromCache = false;
        } catch (e) {
            // When offline, sync tasks from boardStore (has optimistic updates)
            const b = (boardStore as any).get();
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
            projectsStore.set(projects);
            if (currentProject) {
                const b = await getBoard(currentProject.id);
                tasks = b.tasks || [];
                boardStore.set(b);
                // Update selected task reference
                if (selectedTaskVal) {
                    const fresh = tasks.find(t => t.id === selectedTaskVal.id);
                    if (fresh) {
                        selectedTaskVal = fresh;
                        selectedTask.set(fresh);
                    }
                }
            }
            const c = await getConfig();
            cfg = c;
            configStore.set(c);
            servingFromCache = false;
        } catch {
            // Still offline — keep cached data
        }
    }

    function clearTaskDetail() {
        // Push URL first so the subscription sees updated URL
        updateUrl({ project: currentProject?.id }, true, true);
        selectedTaskVal = null;
        showCardDetailVal = false;
        selectedTask.set(null);
        showCardDetail.set(false);
    }

    async function selectProject(p: Project) {
        disconnectBoardStream();
        const wasBranchViewOpen = showBranchViewVal;
        const wasProjectSwitch = !!currentProject;
        currentProject = p;
        currentBranch = '';
        selectedProject.set(p);
        // Re-mount branch view for the new project (only on project switch, not initial load)
        if (wasProjectSwitch && wasBranchViewOpen) {
            showBranchViewVal = false;
            fsEventQueue = [];
        }
        // Update URL with project param
        // Preserve task param from URL — restoreTaskFromUrl needs it after board loads
        const urlTaskId = new URLSearchParams(location.search).get('task');
        updateUrl({ project: p.id, task: selectedTaskVal?.id || urlTaskId, view: wasBranchViewOpen ? 'branch' : null, file: urlFileId || null }, false, true);
        if (wasProjectSwitch && wasBranchViewOpen) {
            queueMicrotask(() => { showBranchViewVal = true; });
        }
        try {
            const b = await getBoard(p.id);
            tasks = b.tasks || [];
            boardStore.set(b);
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
                const { getBoardCache, boardCacheKey } = await import('../storage');
                const cachedBoard = await getBoardCache<{ tasks: Task[] }>(boardCacheKey(p.id));
                if (cachedBoard) {
                    tasks = cachedBoard.tasks || [];
                    boardStore.set(cachedBoard);
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
            projectsStore.set(projects);
            clearTaskDetail();
            selectProject(proj);
        } catch (e) {
            addProjectError = String(e);
        }
    }

    function toggleSidebar() {
        collapsed = !collapsed;
        sidebarCollapsed.set(collapsed);
    }

    // Track whether sidebar was auto-collapsed for tablet Explorer (so we can restore it)
    let sidebarAutoCollapsed = $state(false);

    /** Check if viewport is tablet-sized (>768px and <=1024px). */
    function isTablet(): boolean {
        return !isMobile && window.innerWidth <= 1024;
    }

    /** Collapse sidebar on tablet when a file is opened in the Explorer; restore when closed. */
    function handleExplorerFileChange(path: string | null) {
        const isFileOpen = path != null && (path.startsWith(currentProject?.path || '') || path.startsWith('/home/sandbox/'));
        if (isFileOpen) {
            updateUrl({ project: currentProject?.id, view: 'branch', file: path });
            // Auto-collapse sidebar on tablet to give editor more space
            if (isTablet() && !collapsed) {
                collapsed = true;
                sidebarCollapsed.set(true);
                sidebarAutoCollapsed = true;
            }
        } else {
            updateUrl({ project: currentProject?.id, view: 'branch', file: null });
            // Restore sidebar when file is closed
            if (sidebarAutoCollapsed) {
                collapsed = false;
                sidebarCollapsed.set(false);
                sidebarAutoCollapsed = false;
            }
        }
    }

    /** Restore sidebar if it was auto-collapsed for the Explorer. */
    function restoreExplorerSidebar() {
        if (sidebarAutoCollapsed) {
            collapsed = false;
            sidebarCollapsed.set(false);
            sidebarAutoCollapsed = false;
        }
    }

    async function removeProject(p: Project) {
        const confirmed = await showConfirm({
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
            projectsStore.set(projects);
            // Auto-select next project (or previous if it was the last)
            if (projects.length > 0) {
                const nextIndex = Math.min(index, projects.length - 1);
                clearTaskDetail();
                selectProject(projects[nextIndex]);
            } else {
                clearTaskDetail();
                currentProject = null;
                selectedProject.set(null);
                tasks = [];
                boardStore.set(null);
            }
        } catch (e) {
            addProjectError = String(e);
        }
    }

    function openSandboxConfig(p: Project) {
        if ((projectCounts[p.id]?.in_progress ?? 0) > 0) {
            showToast('Cannot change sandbox settings while tasks are In Progress.', 'error');
            return;
        }
        // Clear port conflict when opening modal (user will see/fix it in modal)
        portConflict = null;
        showSandboxConfigVal = true;
    }

    async function handleSandboxSaved() {
        showSandboxConfigVal = false;
        portConflict = null; // Clear port conflict on successful save
        // Refresh projects list to pick up changes
        try {
            const projs = await getProjects();
            projects = projs || [];
            projectsStore.set(projects);
            const proj = currentProject;
            if (proj) {
                const updated = projects.find(p => p.id === proj.id);
                if (updated) {
                    currentProject = updated;
                    selectedProject.set(updated);
                    // Close terminal so it restarts with updated sandbox mode
                    showTerminal.set(false);
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
        <img class="loading-icon" src={kanbanIcon} width="64" height="64" alt="Loading" />
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
        await loadAppData();
    }} />
{:else}
<!-- WebSocket reconnecting banner -->
{#if !wsConnected}
    <div class="reconnecting-banner" role="status" aria-live="polite">
        <span class="reconnecting-spinner"></span>
        Reconnecting to stream...
    </div>
{/if}
<!-- Offline cache banner -->
{#if servingFromCache}
    <div class="cache-banner" role="status" aria-live="polite">
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M21 15v4a2 2 0 01-2 2H5a2 2 0 01-2-2v-4"/><polyline points="7 10 12 15 17 10"/><line x1="12" y1="15" x2="12" y2="3"/></svg>
        Viewing cached data
    </div>
{/if}
<div class="app" class:collapsed class:task-detail={showCardDetailVal && !!selectedTaskVal} class:ws-disconnected={!wsConnected} class:cache-visible={servingFromCache}>
    <!-- Skip navigation link -->
    <a href="#main-content" class="skip-link">Skip to main content</a>

    <!-- Mobile sidebar backdrop -->
    {#if isMobile && mobileMenuOpen}
        <button type="button" class="sidebar-backdrop" onclick={() => mobileMenuOpen = false} aria-label="Close menu" title="Close menu"></button>
    {/if}

    <!-- Sidebar -->
    <Sidebar
        {projects}
        {currentProject}
        {projectCounts}
        {collapsed}
        {isMobile}
        {mobileMenuOpen}
        {getProjectAbbr}
        onSelectProject={(p) => { clearTaskDetail(); selectProject(p); if (isMobile) mobileMenuOpen = false; }}
        onRemoveProject={removeProject}
        onToggleSidebar={toggleSidebar}
        onShowStats={() => { showStats.set(true); if (isMobile) mobileMenuOpen = false; }}
        onShowConfig={() => { showConfig.set(true); if (isMobile) mobileMenuOpen = false; }}
        onAddProject={() => { openAddProject(); if (isMobile) mobileMenuOpen = false; }}
    />

    <!-- Main Content -->
    <main id="main-content" class="main" tabindex="-1">
        {#if currentProject}
            {#if !(showCardDetailVal && selectedTaskVal) && !showBranchViewVal}
            <TopBar
                currentProject={currentProject}
                {currentBranch}
                {isMobile}
                hasPortConflict={portConflict?.project_id === currentProject?.id}
                onOpenMenu={() => mobileMenuOpen = true}
                onOpenSandboxConfig={() => currentProject && openSandboxConfig(currentProject)}
                onOpenBranchView={() => { showBranchViewVal = true; updateUrl({ project: currentProject?.id, view: 'branch' }, false, true); }}
                onOpenTerminal={() => { loadTerminalPanel(); showTerminal.set(true); }}
                onShowStats={() => showStats.set(true)}
                onShowConfig={() => showConfig.set(true)}
            />
            {/if}
            <section class="board-container" aria-label="Kanban board">
                {#if showBranchViewVal && currentProject}
                    <BranchView
                        projectId={currentProject.id}
                        projectPath={currentProject.path}
                        currentBranch={currentBranch}
                        initialFile={urlFileId || ''}
                        sandboxEnabled={currentProject.sandbox_enabled}
                        fsEventQueue={fsEventQueue}
                        onOpenTerminal={() => { loadTerminalPanel(); showTerminal.set(true); }}
                        onUpdateFile={handleExplorerFileChange}
                        onClose={() => { restoreExplorerSidebar(); showBranchViewVal = false; updateUrl({ project: currentProject?.id, view: null, file: null }, false, true); fsEventQueue = []; }}
                    />
                {:else if showCardDetailVal && selectedTaskVal}
                    <CardDetail task={selectedTaskVal} tasks={tasks} projectPath={currentProject?.path} projectName={currentProject?.name} workspaceDir={cfg?.workspace_dir} sandboxEnabled={currentProject?.sandbox_enabled} activeTab={cardDetailActiveTab} onActiveTabChange={(tab: 'context' | 'chat' | 'diff') => cardDetailActiveTab = tab} onClose={() => {
                        showCardDetail.set(false);
                        selectedTask.set(null);
                        updateUrl({ project: currentProject?.id }, true, true);
                    }} onSelectTask={(t: Task) => {
                        // Push URL first so the subscription sees updated URL and skips replaceState
                        updateUrl({ project: currentProject?.id, task: t.id }, true, true);
                        if (isMobile) cardDetailActiveTab = 'chat';
                        selectedTaskVal = t;
                        selectedTask.set(t);
                    }} onRefreshBoard={() => refreshBoard()} />
                {:else}
                    <Board
                        {tasks}
                        projectId={currentProject.id}
                        projectPath={currentProject.path}
                        onAddTask={() => { creatingChildOfStore.set(null); showCreateTask.set(true); }}
                        onCreateChildTask={(parentId: string) => { creatingChildOfStore.set(parentId); showCreateTask.set(true); }}
                        onRefreshBoard={() => refreshBoard()}
                    />
                {/if}
            </section>
        {:else}
            <div class="empty-state">
                <button class="icon-btn" onclick={() => showConfig.set(true)} aria-label="Settings" title="Settings">
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                        <circle cx="12" cy="12" r="3"/>
                        <path d="M19.4 15a1.65 1.65 0 00.33 1.82l.06.06a2 2 0 010 2.83 2 2 0 01-2.83 0l-.06-.06a1.65 1.65 0 00-1.82-.33 1.65 1.65 0 00-1 1.51V21a2 2 0 01-2 2 2 2 0 01-2-2v-.09A1.65 1.65 0 009 19.4a1.65 1.65 0 00-1.82.33l-.06.06a2 2 0 01-2.83 0 2 2 0 010-2.83l.06-.06A1.65 1.65 0 004.68 15a1.65 1.65 0 00-1.51-1H3a2 2 0 01-2-2 2 2 0 012-2h.09A1.65 1.65 0 004.6 9a1.65 1.65 0 00-.33-1.82l-.06-.06a2 2 0 010-2.83 2 2 0 012.83 0l.06.06A1.65 1.65 0 009 4.68a1.65 1.65 0 001-1.51V3a2 2 0 012-2 2 2 0 012 2v.09a1.65 1.65 0 001 1.51 1.65 1.65 0 001.82-.33l.06-.06a2 2 0 012.83 0 2 2 0 010 2.83l-.06.06A1.65 1.65 0 0019.4 9a1.65 1.65 0 001.51 1H21a2 2 0 012 2 2 2 0 01-2 2h-.09a1.65 1.65 0 00-1.51 1z"/>
                    </svg>
                </button>
                {#if !hasProviders}
                    <h2>Welcome to Kanban</h2>
                    <p>Configure an LLM provider to get started. Supports any OpenAI-compatible API.</p>
                    <button class="btn-primary" onclick={() => showConfig.set(true)} title="Configure an LLM provider">Configure Provider</button>
                {:else}
                    <h2>No Projects</h2>
                    <p>Add a project to get started. Projects must be git repositories.</p>
                    <button class="btn-primary" onclick={openAddProject} title="Add a project">Add Project</button>
                {/if}
            </div>
        {/if}
    </main>

    <!-- Modals -->
    {#if showCreateTaskVal}
        <CreateTaskModal
            projectId={currentProject?.id}
            parentId={creatingChildOfVal ?? undefined}
            onClose={() => { showCreateTask.set(false); creatingChildOfStore.set(null); }}
            onTaskCreated={() => { creatingChildOfStore.set(null); }} // board stream pushes update
            onOptimisticTaskCreated={(task: Task) => { tasks = [...tasks, task]; }}
            onReplaceOptimisticTask={(optimisticId: string, realTask: Task) => { const idx = tasks.findIndex(t => t.id === optimisticId); if (idx !== -1) { tasks[idx] = realTask; } }}
        />
    {/if}
    {#if showEditTaskVal && editTaskDataVal}
        <EditTaskModal
            taskTitle={editTaskDataVal.taskTitle}
            taskPrompt={editTaskDataVal.taskPrompt}
            editProviders={editTaskDataVal.editProviders}
            editModel={editTaskDataVal.editModel}
            editUrl={editTaskDataVal.editUrl}
            editReasoningEffort={editTaskDataVal.editReasoningEffort}
            editPlanMode={editTaskDataVal.editPlanMode}
            editAutoMerge={editTaskDataVal.editAutoMerge}
            editSubtasksAutoMerge={editTaskDataVal.editSubtasksAutoMerge}
            taskHasParent={editTaskDataVal.taskHasParent}
            targetBranch={editTaskDataVal.targetBranch}
            isAgentSelected={editTaskDataVal.isAgentSelected}
            onSave={(title: string, model: string, url: string, reasoningEffort: string, planMode: boolean, autoMerge: boolean, subtasksAutoMerge: boolean) => {
                const taskId = editTaskDataVal!.taskId;
                const projectId = editTaskDataVal!.projectId;
                // Find the task in the board and update it
                const task = tasks.find(t => t.id === taskId);
                if (task) {
                    const trimmed = title.trim();
                    const updates: Record<string, unknown> = { title: trimmed };
                    updates.plan_mode = planMode;
                    updates.auto_commit = autoMerge;
                    updates.auto_merge = autoMerge;
                    updates.subtasks_auto_merge = subtasksAutoMerge;
                    if (model) updates.model_name = model;
                    if (url) updates.llama_server_url = url;
                    if (reasoningEffort) updates.reasoning_effort = reasoningEffort;
                    task.title = trimmed;
                    task.plan_mode = planMode;
                    task.auto_commit = autoMerge;
                    task.auto_merge = autoMerge;
                    task.subtasks_auto_merge = subtasksAutoMerge;
                    if (model) task.model_name = model;
                    if (url) task.llama_server_url = url;
                    if (reasoningEffort) task.reasoning_effort = reasoningEffort;
                    boardStore.update(b => {
                        if (!b) return b;
                        return { ...b, tasks: b.tasks.map(t => t.id === task.id ? task : t) };
                    });
                    import('$lib/api').then(({ updateTask }) => updateTask(projectId, taskId, updates)).catch(err => {
                        console.error('Failed to update task:', err);
                    });
                }
                showEditTask.set(false);
                editTaskDataStore.set(null);
            }}
            onCancel={() => {
                showEditTask.set(false);
                editTaskDataStore.set(null);
            }}
        />
    {/if}
    {#if showConfigVal}
        <ConfigModal
            config={cfg}
            onClose={() => showConfig.set(false)}
            onSaved={() => showConfig.set(false)}
            onSignOut={handleLogout}
        />
    {/if}
    {#if showTerminalVal && TerminalPanelComponent}
        {#key currentProject?.id + '-' + (currentProject?.sandbox_enabled ?? '')}
            {@const Terminal = TerminalPanelComponent}
            {#if Terminal}
                <Terminal />
            {/if}
        {/key}
    {/if}
    {#if showStatsVal && currentProject}
        <StatsModal
            projectId={currentProject.id}
            onClose={() => showStats.set(false)}
        />
    {/if}
    {#if showFileBrowserVal}
        <FileBrowser
            onClose={() => showFileBrowserVal = false}
            onSelect={handleSelectProjectPath}
        />
    {/if}
    {#if showSandboxConfigVal && currentProject}
        <SandboxConfigModal
            project={currentProject}
            inProgressCount={projectCounts[currentProject.id]?.in_progress ?? 0}
            onClose={() => showSandboxConfigVal = false}
            onSaved={handleSandboxSaved}
        />
    {/if}
    {#if addProjectError}
        <div class="error-toast">{addProjectError}</div>
    {/if}
    <Toast />
    <ConfirmationDialog />
    <CommitDialog />
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
        background: #000;
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
        background: #000;
    }

    .offline-text {
        color: #99AAB5;
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
        border: 1px solid #333;
        border-radius: 8px;
        background: #1a1a1a;
        color: #d6d3d1;
        cursor: pointer;
        transition: background 0.15s, border-color 0.15s;
    }

    .offline-btn svg {
        flex-shrink: 0;
    }

    .offline-btn:hover {
        background: #252525;
        border-color: #555;
    }

    /* -- Reconnecting banner -- */
    .reconnecting-banner {
        position: fixed;
        top: 0;
        left: 0;
        right: 0;
        z-index: 10001;
        background: #550000;
        color: #d6d3d1;
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
    .reconnecting-spinner {
        display: inline-block;
        width: 12px;
        height: 12px;
        border: 2px solid rgba(214, 211, 209, 0.3);
        border-top-color: #d6d3d1;
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
        background: #550000;
        color: #d6d3d1;
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
        background: var(--color-accent);
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
        background: var(--color-accent);
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
        background: var(--color-status-red);
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
            background: rgba(0,0,0,0.5);
            z-index: 199;
        }
    }
</style>