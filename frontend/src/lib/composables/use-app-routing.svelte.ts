import type { Project, Task } from '$lib/types';
import { getProjects, getBoard, getConfig, deleteProject, createProject, getOfflineStatus, waitForFlush, isFromCache } from '$lib/api';
import { projects as projectsStore, selectedProject, board as boardStore, config as configStore, selectedTask } from '$lib/stores/app';
import { showCardDetail } from '$lib/stores/ui';
import { getBoardCache, boardCacheKey } from '$lib/storage';

/** Dependencies the component must provide. */
export interface AppRoutingDeps {
    /** Current selected task (local state, not store). */
    readonly selectedTaskVal: Task | null;
    /** Set the selected task value. */
    setSelectedTaskVal: (task: Task | null) => void;
    /** Current project (local state, not store). */
    readonly currentProject: Project | null;
    /** Set the current project. */
    setCurrentProject: (project: Project | null) => void;
    /** Projects array (local state). */
    readonly projects: Project[];
    /** Set projects array. */
    setProjects: (projects: Project[]) => void;
    /** Tasks array (local state). */
    readonly tasks: Task[];
    /** Set tasks array. */
    setTasks: (tasks: Task[]) => void;
    /** Config (local state). */
    readonly cfg: any;
    /** Set config. */
    setCfg: (cfg: any) => void;
    /** Whether the app stream is available. */
    readonly hasAppStream: boolean;
    /** Whether the app is connected. */
    readonly wsConnected: boolean;
    /** Update last synced timestamp. */
    setLastSyncedAt: (time: number) => void;
    /** Whether serving from cache. */
    readonly servingFromCache: boolean;
    /** Set serving from cache. */
    setServingFromCache: (val: boolean) => void;
    /** Subscribe to a project via app stream. */
    subscribeProject: (projectId: string) => void;
    /** Unsubscribe from a project via app stream. */
    unsubscribeProject: (projectId: string) => void;
    /** Unsubscribe from a task via app stream. */
    unsubscribeTask: (projectId: string, taskId: string) => void;
    /** Subscribe to a task via app stream. */
    subscribeTask: (projectId: string, taskId: string) => void;
}

/**
 * Manages app-level routing and URL state.
 *
 * Handles:
 * - URL parameter management (project, task, view, file, thread)
 * - Browser back/forward (popstate)
 * - External URL change detection (rAF loop)
 * - Task restoration from URL
 * - Project selection with board loading
 * - Board refresh (normal and silent)
 * - Explorer file path tracking
 */
export function useAppRouting(deps: AppRoutingDeps) {
    let urlFileId = $state(new URLSearchParams(location.search).get('file') || '');
    let showExplorerViewVal = $state(false);
    let boardRefreshPending = false;
    let lastSubKey = '';

    // rAF URL change detection
    let urlRafId: number | null = null;
    let lastUrlCheck = '';
    let lastUrlCheckTime = 0;
    const URL_CHECK_THROTTLE = 250;

    /**
     * Update the URL to reflect the current navigation state.
     * Params are always ordered: project, task, view, file, thread.
     */
    function updateUrl(params: { project?: string | null; task?: string | null; view?: string | null; file?: string | null }, clearThread = false, push = false) {
        const current = new URLSearchParams(location.search);
        const project = params.project;
        const task = params.task;
        const view = params.view !== undefined ? params.view : current.get('view');
        const file = params.file !== undefined ? params.file : current.get('file');
        const thread = clearThread ? null : current.get('thread');
        const parts: string[] = [];
        if (project) parts.push('project=' + encodeURIComponent(project));
        if (task) parts.push('task=' + encodeURIComponent(task));
        if (view) parts.push('view=' + encodeURIComponent(view));
        if (file) parts.push('file=' + file);
        if (thread) parts.push('thread=' + encodeURIComponent(thread));
        const newUrl = location.pathname + (parts.length ? '?' + parts.join('&') : '');
        if (location.pathname + location.search !== newUrl) {
            if (push) {
                history.pushState(null, '', newUrl);
            } else {
                history.replaceState(null, '', newUrl);
            }
            if (params.file !== undefined) {
                urlFileId = params.file || '';
            }
        }
    }

    /** Handle browser back/forward navigation. */
    function handlePopState() {
        const params = new URLSearchParams(location.search);
        const projectId = params.get('project');
        const taskId = params.get('task');
        const fileParam = params.get('file') || '';
        urlFileId = (fileParam && (fileParam.startsWith(deps.currentProject?.path || '') || fileParam.startsWith('/home/sandbox/'))) ? fileParam : '';

        // If URL has no project param, go back to project list
        if (!projectId && deps.currentProject) {
            deps.setCurrentProject(null);
            selectedProject.set(null);
            deps.setTasks([]);
            boardStore.set(null);
            disconnectBoardStream();
            selectedTask.set(null);
            showCardDetail.set(false);
            window.dispatchEvent(new CustomEvent('urlchanged'));
            return;
        }

        // If URL specifies a different project, switch to it
        if (projectId && deps.currentProject && projectId !== deps.currentProject.id) {
            const targetProject = deps.projects.find(p => p.id === projectId);
            if (targetProject) {
                selectProject(targetProject);
                window.dispatchEvent(new CustomEvent('urlchanged'));
                return;
            }
        }

        if (taskId) {
            const task = deps.tasks.find(t => t.id === taskId);
            if (task) {
                selectedTask.set(task);
                showCardDetail.set(true);
            }
        } else {
            selectedTask.set(null);
            showCardDetail.set(false);
        }

        // Restore explorer view from URL
        const viewParam = params.get('view');
        showExplorerViewVal = viewParam === 'explorer';

        window.dispatchEvent(new CustomEvent('urlchanged'));
    }

    /** Restore task from URL after board loads. */
    async function restoreTaskFromUrl() {
        const params = new URLSearchParams(location.search);
        const taskId = params.get('task');
        if (!taskId || !deps.currentProject) return;
        let task = deps.tasks.find(t => t.id === taskId);
        if (!task) {
            try {
                const b = await getBoard(deps.currentProject.id);
                deps.setTasks(b.tasks || []);
                boardStore.set(b);
                task = deps.tasks.find(t => t.id === taskId);
                if (!task) {
                    const url = new URL(location.href);
                    url.searchParams.delete('task');
                    history.replaceState(null, '', location.pathname + url.search);
                    return;
                }
            } catch {
                return;
            }
        }
        deps.setSelectedTaskVal(task);
        showCardDetail.set(true);
        selectedTask.set(task);
    }

    /** Check for external URL changes (rAF throttled). */
    function checkUrlChange() {
        const now = Date.now();
        if (now - lastUrlCheckTime < URL_CHECK_THROTTLE) {
            urlRafId = requestAnimationFrame(checkUrlChange);
            return;
        }
        if (location.search !== lastUrlCheck) {
            lastUrlCheck = location.search;
            lastUrlCheckTime = now;
            if (deps.tasks.length > 0) {
                restoreTaskFromUrl();
            }
        }
        urlRafId = requestAnimationFrame(checkUrlChange);
    }

    /** Clear task detail and update URL. */
    function clearTaskDetail() {
        updateUrl({ project: deps.currentProject?.id }, true, true);
        deps.setSelectedTaskVal(null);
        selectedTask.set(null);
        showCardDetail.set(false);
    }

    /** Disconnect from the current project's board stream. */
    function disconnectBoardStream() {
        if (deps.currentProject && deps.hasAppStream) {
            deps.unsubscribeProject(deps.currentProject.id);
        }
    }

    /** Select a project: load board, update URL, subscribe to stream. */
    async function selectProject(p: Project) {
        disconnectBoardStream();
        const wasExplorerViewOpen = showExplorerViewVal;
        const wasProjectSwitch = !!deps.currentProject;
        deps.setCurrentProject(p);
        selectedProject.set(p);

        if (wasProjectSwitch && wasExplorerViewOpen) {
            showExplorerViewVal = false;
        }

        const urlTaskId = new URLSearchParams(location.search).get('task');
        updateUrl({ project: p.id, task: deps.selectedTaskVal?.id || urlTaskId, view: wasExplorerViewOpen ? 'explorer' : null, file: urlFileId || null }, false, true);

        if (wasProjectSwitch && wasExplorerViewOpen) {
            queueMicrotask(() => { showExplorerViewVal = true; });
        }

        try {
            const b = await getBoard(p.id);
            deps.setTasks(b.tasks || []);
            boardStore.set(b);
            deps.setLastSyncedAt(Date.now());
            await restoreTaskFromUrl();
            if (deps.hasAppStream) {
                deps.subscribeProject(p.id);
            }
        } catch (e) {
            console.error('Failed to load board:', e);
            try {
                const cachedBoard = await getBoardCache<{ tasks: Task[] }>(boardCacheKey(p.id));
                if (cachedBoard) {
                    deps.setTasks(cachedBoard.tasks || []);
                    boardStore.set(cachedBoard);
                    await restoreTaskFromUrl();
                }
            } catch {
                // No cached board available
            }
        }
    }

    /** Refresh the board from the server. */
    async function refreshBoard() {
        if (!deps.currentProject || boardRefreshPending) return;
        boardRefreshPending = true;
        try {
            await waitForFlush();
            const b = await getBoard(deps.currentProject.id);
            deps.setTasks(b.tasks || []);
            boardStore.set(b);
            const task = deps.selectedTaskVal;
            if (task) {
                const fresh = deps.tasks.find(t => t.id === task.id);
                if (fresh) {
                    deps.setSelectedTaskVal(fresh);
                    selectedTask.set(fresh);
                }
            }
            deps.setServingFromCache(false);
            if (deps.wsConnected) deps.setLastSyncedAt(Date.now());
        } catch (e) {
            const b = (boardStore as any).get();
            if (b?.tasks) {
                deps.setTasks(b.tasks);
            }
        } finally {
            boardRefreshPending = false;
        }
    }

    /** Silently refresh all cached data after reconnection. */
    async function silentRefresh() {
        try {
            await waitForFlush();
            const projs = await getProjects();
            deps.setProjects(projs || []);
            projectsStore.set(projs || []);
            if (deps.currentProject) {
                const b = await getBoard(deps.currentProject.id);
                deps.setTasks(b.tasks || []);
                boardStore.set(b);
                if (deps.selectedTaskVal) {
                    const fresh = deps.tasks.find(t => t.id === deps.selectedTaskVal.id);
                    if (fresh) {
                        deps.setSelectedTaskVal(fresh);
                        selectedTask.set(fresh);
                    }
                }
            }
            const c = await getConfig();
            deps.setCfg(c);
            configStore.set(c);
            deps.setServingFromCache(false);
            if (deps.wsConnected) deps.setLastSyncedAt(Date.now());
        } catch {
            // Still offline — keep cached data
        }
    }

    /** Manage task-level WebSocket subscription. */
    function setupTaskSubscription(): () => void {
        // This is called from the component's $effect — returns cleanup
        return () => {
            if (lastSubKey && deps.hasAppStream) {
                const [pid, tid] = lastSubKey.split(':');
                deps.unsubscribeTask(pid, tid);
                lastSubKey = '';
            }
        };
    }

    function updateTaskSubscription() {
        if (!deps.hasAppStream || !deps.currentProject || !deps.selectedTaskVal) return;
        const key = `${deps.currentProject.id}:${deps.selectedTaskVal.id}`;
        if (key !== lastSubKey) {
            if (lastSubKey && deps.hasAppStream) {
                const [pid, tid] = lastSubKey.split(':');
                deps.unsubscribeTask(pid, tid);
            }
            deps.subscribeTask(deps.currentProject.id, deps.selectedTaskVal.id);
            lastSubKey = key;
        }
    }

    function _startUrlWatch() {
        lastUrlCheck = location.search;
        lastUrlCheckTime = Date.now();
        urlRafId = requestAnimationFrame(_checkUrlChangeLoop);
    }
    function _stopUrlWatch() {
        if (urlRafId !== null) cancelAnimationFrame(urlRafId);
    }
    function _checkUrlChangeLoop() {
        const now = Date.now();
        if (now - lastUrlCheckTime < URL_CHECK_THROTTLE) {
            urlRafId = requestAnimationFrame(_checkUrlChangeLoop);
            return;
        }
        if (location.search !== lastUrlCheck) {
            lastUrlCheck = location.search;
            lastUrlCheckTime = now;
            if (deps.tasks.length > 0) {
                restoreTaskFromUrl();
            }
        }
        urlRafId = requestAnimationFrame(_checkUrlChangeLoop);
    }

    return {
        urlFileId,
        showExplorerViewVal,
        updateUrl,
        handlePopState,
        restoreTaskFromUrl,
        checkUrlChange,
        clearTaskDetail,
        disconnectBoardStream,
        selectProject,
        refreshBoard,
        silentRefresh,
        setupTaskSubscription,
        updateTaskSubscription,
        _startUrlWatch,
        _stopUrlWatch,
        _checkUrlChangeLoop,
    };
}
