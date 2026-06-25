const API_BASE = '/api';

/** CSRF token, set on login or session check. Held in memory only. */
let csrfToken: string = '';

/** Set the CSRF token (called after login or session check). */
export function setCsrfToken(token: string): void {
    csrfToken = token;
}

/** Clear the CSRF token (called on logout). */
export function clearCsrfToken(): void {
    csrfToken = '';
}

/** Lazily fetch CSRF token from session endpoint (for E2E test auth). */
export async function ensureCsrfToken(): Promise<string> {
    // Check module-level token first, then window.__csrfToken (set by E2E tests)
    if (csrfToken) return csrfToken;
    if (typeof window !== 'undefined' && (window as any).__csrfToken) return (window as any).__csrfToken;
    // Auto-fetch from session endpoint (public, no CSRF needed)
    try {
        const res = await fetch('/api/auth/session', { credentials: 'include' });
        const data = await res.json();
        if (data.authenticated && data.csrf_token) {
            csrfToken = data.csrf_token;
            return csrfToken;
        }
    } catch { /* ignore */ }
    return '';
}

/** Wrapper around fetch that includes credentials (cookies) and CSRF token on every request. */
async function apiFetch(url: string, init?: RequestInit): Promise<Response> {
    const headers = new Headers(init?.headers);
    // Skip CSRF for login endpoint (public)
    if (!url.includes('/api/auth/login')) {
        const token = await ensureCsrfToken();
        if (token) headers.set('X-CSRF-Token', token);
    }
    return fetch(url, { ...init, credentials: 'include', headers });
}

/** Returns true if the response is a 401 Unauthorized. */
export function isUnauthorized(res: Response): boolean {
    return res.status === 401;
}

/** Returns true if the response is a 403 Forbidden (e.g., invalid CSRF token). */
export function isForbidden(res: Response): boolean {
    return res.status === 403;
}

/** Extract error message from a failed API response, falling back to a default. */
async function apiError(res: Response, defaultMsg: string): Promise<Error> {
    try {
        const body = await res.json();
        return new Error(body.error || defaultMsg);
    } catch {
        return new Error(defaultMsg);
    }
}

// -- Project endpoints --
export async function getProjects(): Promise<Project[]> {
    const res = await apiFetch(`${API_BASE}/projects`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch projects');
    return res.json();
}

export async function getProjectCounts(): Promise<Record<string, ProjectCounts>> {
    const res = await apiFetch(`${API_BASE}/projects/counts`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch project counts');
    return res.json();
}

export async function getProjectStats(projectId: string): Promise<ProjectStats> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/stats`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch project stats');
    return res.json();
}

export async function resetProjectStats(projectId: string, modelName: string, providerName?: string): Promise<void> {
    const params = new URLSearchParams({ model_name: modelName });
    if (providerName) params.set('provider_name', providerName);
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/stats?${params}`, { method: 'DELETE' });
    if (!res.ok) throw await apiError(res, 'Failed to reset stats');
}

export async function createProject(path: string, sandboxEnabled = false): Promise<Project> {
    const body: Record<string, unknown> = { path, sandbox_enabled: sandboxEnabled };
    if (sandboxEnabled) {
        body.sandbox_network_isolation = true;
    }
    const res = await apiFetch(`${API_BASE}/projects`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
    });
    if (!res.ok) throw await apiError(res, 'Failed to create project');
    return res.json();
}

export async function updateProject(id: string, updates: { name?: string; sandbox_enabled?: boolean; sandbox_network_isolation?: boolean; sandbox_image?: string; sandbox_dockerfile_path?: string; sandbox_memory_limit?: number }): Promise<Project> {
    const res = await apiFetch(`${API_BASE}/projects/${id}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(updates),
    });
    if (!res.ok) throw await apiError(res, 'Failed to update project');
    return res.json();
}

export async function buildSandboxImage(id: string): Promise<{ status: string; image: string; project: string }> {
    const res = await apiFetch(`${API_BASE}/projects/${id}/sandbox/build`, {
        method: 'POST',
    });
    if (!res.ok) throw await apiError(res, 'Failed to build sandbox image');
    return res.json();
}

export async function deleteProject(id: string): Promise<void> {
    const res = await apiFetch(`${API_BASE}/projects/${id}`, { method: 'DELETE' });
    if (!res.ok) throw await apiError(res, 'Failed to delete project');
}

// -- Board endpoints --
export async function getBoard(projectId: string): Promise<Board> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/board`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch board');
    return res.json();
}

export async function reorderTasks(projectId: string, reorders: ReorderRequest[]): Promise<void> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/reorder`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(reorders),
    });
    if (!res.ok) throw await apiError(res, 'Failed to reorder tasks');
}

// -- Task CRUD endpoints --
export async function createTask(projectId: string, task: Partial<Task>): Promise<Task> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(task),
    });
    if (!res.ok) throw await apiError(res, 'Failed to create task');
    return res.json();
}

export async function getTask(projectId: string, taskId: string): Promise<Task> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}`);
    if (!res.ok) throw await apiError(res, 'Task not found');
    return res.json();
}

export async function updateTask(projectId: string, taskId: string, task: Partial<Task>): Promise<Task> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(task),
    });
    if (!res.ok) throw await apiError(res, 'Failed to update task');
    return res.json();
}

export async function deleteTask(projectId: string, taskId: string): Promise<void> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}`, { method: 'DELETE' });
    if (!res.ok) throw await apiError(res, 'Failed to delete task');
}

export async function moveTask(projectId: string, taskId: string, status: string): Promise<Task> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/move`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ status }),
    });
    if (!res.ok) throw await apiError(res, 'Failed to move task');
    return res.json();
}

export async function commitAndDone(projectId: string, taskId: string): Promise<Task> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/commit`, {
        method: 'POST',
    });
    if (!res.ok) throw await apiError(res, 'Failed to commit and mark as done');
    return res.json();
}

export async function clearDoneTasks(projectId: string): Promise<{ deleted: number }> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/done`, { method: 'DELETE' });
    if (!res.ok) throw await apiError(res, 'Failed to clear done tasks');
    return res.json();
}

// -- Task execution endpoints --
export async function startTask(projectId: string, taskId: string): Promise<Task> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/start`, { method: 'POST' });
    if (!res.ok) throw await apiError(res, 'Failed to start task');
    return res.json();
}

export async function startSubtask(projectId: string, taskId: string): Promise<Task> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/start-subtask`, { method: 'POST' });
    if (!res.ok) throw await apiError(res, 'Failed to start subtask');
    return res.json();
}

export async function stopTask(projectId: string, taskId: string): Promise<Task> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/stop`, { method: 'POST' });
    if (!res.ok) throw await apiError(res, 'Failed to stop task');
    return res.json();
}

export async function retryTask(projectId: string, taskId: string): Promise<Task> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/retry`, { method: 'POST' });
    if (!res.ok) throw await apiError(res, 'Failed to retry task');
    return res.json();
}

export async function resumeTask(projectId: string, taskId: string): Promise<Task> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/resume`, { method: 'POST' });
    if (!res.ok) throw await apiError(res, 'Failed to resume task');
    return res.json();
}

// App stream - single WebSocket for all real-time events
export interface AppStreamCallbacks {
    onProjectCounts?: (counts: Record<string, ProjectCounts>) => void;
    onRefresh?: () => void;
    onDiffStats?: (task_id: string, short_stat: string, untracked_n: number, untracked_lines: number) => void;
    onDiff?: (task_id: string, diff: string, untracked: Array<{ path: string; diff: string; file_size?: number }>) => void;
    onBranch?: (branch: string) => void;
    onTaskUpdated?: (task: Task) => void;
    onTaskDeleted?: (task_id: string) => void;
    onCommitStatus?: (task_id: string, status: 'queued' | 'processing') => void;
    onTaskEvent?: (task_id: string, event: StreamEvent) => void; // task-scoped events (tokens, tool calls, etc.)
}

export interface AppStream {
    close: () => void;
    subscribeProject: (projectId: string) => void;
    unsubscribeProject: (projectId: string) => void;
    subscribeTask: (projectId: string, taskId: string) => void;
    unsubscribeTask: (projectId: string, taskId: string) => void;
}

export function createAppStream(callbacks: AppStreamCallbacks): AppStream {
    let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
    let reconnectAttempts = 0;
    let isClosed = false;
    const maxDelay = 30_000;
    let currentWs: WebSocket | null = null;
    // Track active subscriptions for re-subscription on reconnect
    const subscribedProjects = new Set<string>();
    const subscribedTasks = new Set<string>(); // "projectId:taskId"
    // Pending messages queued before WebSocket is open
    const pendingMessages: Array<{ type: string; data: Record<string, any> }> = [];

    function sendMsg(type: string, data: Record<string, any>) {
        if (currentWs && currentWs.readyState === WebSocket.OPEN) {
            currentWs.send(JSON.stringify({ type, data }));
        } else {
            // Queue message for when WebSocket opens
            pendingMessages.push({ type, data });
        }
    }

    function flushPending() {
        for (const msg of pendingMessages) {
            if (currentWs && currentWs.readyState === WebSocket.OPEN) {
                currentWs.send(JSON.stringify(msg));
            }
        }
        pendingMessages.length = 0;
    }

    function connect(sync = false) {
        if (isClosed) return;
        const syncParam = sync ? '?sync=1' : '';
        const ws = new WebSocket(`${window.location.origin}${API_BASE}/stream${syncParam}`);
        currentWs = ws;

        ws.onopen = () => {
            reconnectAttempts = 0;
            // Flush pending messages first
            flushPending();
            // Re-subscribe to all previously subscribed projects/tasks
            for (const projectId of subscribedProjects) {
                sendMsg('subscribe_project', { project_id: projectId });
            }
            for (const key of subscribedTasks) {
                const [projectId, taskId] = key.split(':');
                sendMsg('subscribe_task', { project_id: projectId, task_id: taskId });
            }
        };

        ws.onmessage = (e) => {
            const msg = JSON.parse(e.data);
            // Handle task_event (server-side multiplexed task events)
            if (msg.type === 'task_event' && callbacks.onTaskEvent) {
                callbacks.onTaskEvent(msg.task_id, {
                    type: msg.event,
                    data: msg.data,
                });
                return;
            }
            // Handle board-level events
            if (msg.type === 'board_refresh' && callbacks.onRefresh) {
                callbacks.onRefresh();
            }
            if (msg.type === 'project_counts' && callbacks.onProjectCounts) {
                callbacks.onProjectCounts(msg.data);
            }
            if (msg.type === 'task_diff_stats' && callbacks.onDiffStats) {
                callbacks.onDiffStats(msg.task_id, msg.short_stat, msg.untracked_n, msg.untracked_lines);
            }
            if (msg.type === 'task_diff' && callbacks.onDiff) {
                callbacks.onDiff(msg.task_id, msg.diff, msg.untracked || []);
            }
            if (msg.type === 'project_branch' && callbacks.onBranch) {
                callbacks.onBranch(msg.branch);
            }
            if (msg.type === 'task_updated' && callbacks.onTaskUpdated) {
                callbacks.onTaskUpdated(msg.task);
            }
            if (msg.type === 'task_deleted' && callbacks.onTaskDeleted) {
                callbacks.onTaskDeleted(msg.task_id);
            }
            if (msg.type === 'commit_status' && callbacks.onCommitStatus) {
                callbacks.onCommitStatus(msg.task_id, msg.status);
            }
        };

        ws.onclose = () => {
            currentWs = null;
            if (isClosed) return;
            const delay = Math.min(1000 * Math.pow(2, reconnectAttempts), maxDelay);
            reconnectAttempts++;
            reconnectTimer = setTimeout(() => connect(true), delay);
        };

        ws.onerror = () => {
            // Connection errors trigger onclose, which handles reconnect
        };
    }

    connect();

    return {
        close: () => {
            isClosed = true;
            if (reconnectTimer) {
                clearTimeout(reconnectTimer);
                reconnectTimer = null;
            }
            if (currentWs) {
                currentWs.close();
                currentWs = null;
            }
        },
        subscribeProject: (projectId: string) => {
            subscribedProjects.add(projectId);
            sendMsg('subscribe_project', { project_id: projectId });
        },
        unsubscribeProject: (projectId: string) => {
            subscribedProjects.delete(projectId);
            sendMsg('unsubscribe_project', { project_id: projectId });
        },
        subscribeTask: (projectId: string, taskId: string) => {
            subscribedTasks.add(`${projectId}:${taskId}`);
            sendMsg('subscribe_task', { project_id: projectId, task_id: taskId });
        },
        unsubscribeTask: (projectId: string, taskId: string) => {
            subscribedTasks.delete(`${projectId}:${taskId}`);
            sendMsg('unsubscribe_task', { project_id: projectId, task_id: taskId });
        },
    };
}

/** Send a user message via HTTP POST. Triggers executor and returns the created message. */
export async function sendTaskMessage(
    projectId: string,
    taskId: string,
    content: string,
    modelName?: string,
    serverUrl?: string,
    attachments?: Array<{ mimeType: string; data: string }>
): Promise<{ id: string; role: string; content: string; thread_id?: string }> {
    const body: Record<string, any> = { content };
    if (modelName) body.model_name = modelName;
    if (serverUrl) body.llama_server_url = serverUrl;
    if (attachments && attachments.length > 0) {
        body.attachments = JSON.stringify(attachments);
    }
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/send`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
    });
    if (!res.ok) throw await apiError(res, 'Failed to send message');
    return res.json();
}

export async function getTaskMessages(projectId: string, taskId: string): Promise<TaskMessage[]> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/messages`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch messages');
    return res.json();
}

export async function createTaskMessage(projectId: string, taskId: string, role: string, content: string): Promise<TaskMessage> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/messages`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ role, content }),
    });
    if (!res.ok) throw await apiError(res, 'Failed to create message');
    return res.json();
}

export async function deleteTaskMessagesAfter(projectId: string, taskId: string, messageId: string, regenerate = false): Promise<void> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/messages`, {
        method: 'DELETE',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ message_id: messageId, regenerate }),
    });
    if (!res.ok) throw await apiError(res, 'Failed to delete messages');
}

/** Delete a single message by ID (e.g., dismissing an error). Does not delete messages after it. */
export async function deleteTaskMessage(projectId: string, taskId: string, messageId: string): Promise<void> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/messages`, {
        method: 'DELETE',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ message_id: messageId, delete_single: true }),
    });
    if (!res.ok) throw await apiError(res, 'Failed to delete message');
}

/** Regenerate from a message: deletes it and all messages after it. Alias for deleteTaskMessagesAfter. */
export const regenerateTaskMessages = deleteTaskMessagesAfter;

/** Check if deleting a message would also delete a fork branch. */
export async function checkMessageDelete(projectId: string, taskId: string, messageId: string): Promise<{ will_delete_branch: boolean; branch_name: string }> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/messages/${messageId}`);
    if (!res.ok) throw await apiError(res, 'Failed to check message delete');
    return res.json();
}

export async function updateTaskMessage(projectId: string, taskId: string, messageId: string, content: string): Promise<void> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/messages/${messageId}`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ content }),
    });
    if (!res.ok) throw await apiError(res, 'Failed to update message');
}

export async function getTaskDiff(projectId: string, taskId: string, options?: { headRef?: string; baseRef?: string }): Promise<{ diff: string; untracked: Array<{ path: string; diff: string; file_size?: number }>; short_stat: string }> {
    const params = new URLSearchParams();
    if (options?.headRef) params.set('head', options.headRef);
    if (options?.baseRef) params.set('base', options.baseRef);
    const url = params.toString() ? `${API_BASE}/projects/${projectId}/tasks/${taskId}/diff?${params}` : `${API_BASE}/projects/${projectId}/tasks/${taskId}/diff`;
    const res = await apiFetch(url);
    if (!res.ok) throw await apiError(res, 'Failed to fetch diff');
    return res.json();
}

export async function getTaskCommits(projectId: string, taskId: string): Promise<{ base_commit?: TaskCommit; commits: TaskCommit[] }> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/commits`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch commits');
    return res.json();
}

export async function resetWorktree(projectId: string, taskId: string, commitHash: string): Promise<{ status: string }> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/reset-worktree`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ commit_hash: commitHash }),
    });
    if (!res.ok) throw await apiError(res, 'Failed to reset worktree');
    return res.json();
}

// -- Prompt endpoints --
export async function reloadPrompts(): Promise<{ status: string; message: string; files: string[] }> {
    const res = await apiFetch(`${API_BASE}/prompts/reload`, {
        method: 'POST',
    });
    if (!res.ok) throw await apiError(res, 'Failed to reload prompts');
    return res.json();
}

// -- Configuration endpoints --
export async function getConfig(): Promise<Config> {
    const res = await apiFetch(`${API_BASE}/config`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch config');
    return res.json();
}

export async function updateConfig(config: Config): Promise<Config> {
    const res = await apiFetch(`${API_BASE}/config`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(config),
    });
    if (!res.ok) throw await apiError(res, 'Failed to update config');
    return res.json();
}

export async function testConnection(llamaServerUrl: string, modelName: string, apiKey?: string): Promise<{ success: boolean; message: string }> {
    const res = await apiFetch(`${API_BASE}/config/test-connection`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ llama_server_url: llamaServerUrl, model_name: modelName, api_key: apiKey }),
    });
    if (!res.ok) throw await apiError(res, 'Failed to test connection');
    return res.json();
}

export async function fetchModels(llamaServerUrl: string, apiKey?: string): Promise<{ models: Array<{ name: string; context_window?: number }>; error?: string }> {
    const res = await apiFetch(`${API_BASE}/config/fetch-models`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ llama_server_url: llamaServerUrl, api_key: apiKey }),
    });
    if (!res.ok) throw await apiError(res, 'Failed to fetch models');
    return res.json();
}

/** Check if Pi is available on the server. */
export async function checkPiAvailable(): Promise<{ available: boolean; version?: string }> {
    const res = await apiFetch(`${API_BASE}/config/pi-available`, {
        method: 'GET',
    });
    if (!res.ok) throw await apiError(res, 'Failed to check Pi availability');
    return res.json();
}

/** Test Pi connection (runs `pi --version`). */
export async function testPiConnection(): Promise<{ success: boolean; message: string }> {
    const res = await apiFetch(`${API_BASE}/config/test-pi-connection`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({}),
    });
    if (!res.ok) throw await apiError(res, 'Failed to test Pi connection');
    return res.json();
}

/** Resolve a provider+model selection to the actual server URL and model name. */
export function resolveProviderModel(
    providers: { name: string; url: string; models: Array<{ name: string; reasoning_effort?: string }> }[],
    providerName: string,
    modelName: string
): { llama_server_url: string; model_name: string } | null {
    const provider = providers.find(p => p.name === providerName);
    if (!provider) return null;
    return { llama_server_url: provider.url, model_name: modelName };
}

// -- Dependency endpoints --
export async function getDependencies(projectId: string, taskId: string): Promise<TaskDependency[]> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/dependencies`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch dependencies');
    return res.json();
}

export async function createDependency(projectId: string, taskId: string, childTaskId: string, autoStart: boolean): Promise<TaskDependency> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/dependencies`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ child_task_id: childTaskId, auto_start: autoStart }),
    });
    if (!res.ok) throw await apiError(res, 'Failed to create dependency');
    return res.json();
}

// -- Review comment endpoints --
export async function getReviewComments(projectId: string, taskId: string): Promise<ReviewComment[]> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/review-comments`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch review comments');
    return res.json();
}

export async function createReviewComment(projectId: string, taskId: string, filePath: string, lineNum: number, comment: string): Promise<ReviewComment> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/review-comments`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ file_path: filePath, line_num: lineNum, comment }),
    });
    if (!res.ok) throw await apiError(res, 'Failed to create review comment');
    return res.json();
}

// -- Git endpoints --
export async function getGitLog(projectId: string, branch?: string): Promise<GitCommit[]> {
    const url = branch ? `${API_BASE}/projects/${projectId}/git/log?branch=${branch}` : `${API_BASE}/projects/${projectId}/git/log`;
    const res = await apiFetch(url);
    if (!res.ok) throw await apiError(res, 'Failed to fetch git log');
    return res.json();
}

export async function getGitStatus(projectId: string): Promise<GitStatus> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/git/status`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch git status');
    return res.json();
}

// -- Thread endpoints --
export async function getTaskThreads(projectId: string, taskId: string): Promise<Thread[]> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/threads`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch threads');
    return res.json();
}

export async function switchTaskThread(projectId: string, taskId: string, threadId: string): Promise<Thread> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/threads/switch`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ thread_id: threadId }),
    });
    if (!res.ok) throw await apiError(res, 'Failed to switch thread');
    return res.json();
}

export async function forkTaskThread(projectId: string, taskId: string, fromThreadId: string, fromMessageId: string, newContent?: string): Promise<Thread> {
    const body: Record<string, string> = { from_thread_id: fromThreadId, from_message_id: fromMessageId };
    if (newContent) body.new_content = newContent;
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/threads/fork`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
    });
    if (!res.ok) throw await apiError(res, 'Failed to fork thread');
    return res.json();
}

export async function deleteTaskThread(projectId: string, taskId: string, threadId: string): Promise<{ status: string }> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/threads`, {
        method: 'DELETE',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ thread_id: threadId }),
    });
    if (!res.ok) throw await apiError(res, 'Failed to delete thread');
    return res.json();
}

// -- Terminal --
export function createTerminal(projectId: string, taskId: string, term: import('@xterm/xterm').Terminal, onClosed?: () => void): WebSocket {
    return createTerminalWS(`${window.location.origin}${API_BASE}/projects/${projectId}/tasks/${taskId}/terminal`, term, onClosed);
}

// -- Project Terminal (shell in project root) --
export function createProjectTerminal(projectId: string, term: import('@xterm/xterm').Terminal, onClosed?: () => void): WebSocket {
    return createTerminalWS(`${window.location.origin}${API_BASE}/projects/${projectId}/terminal`, term, onClosed);
}

// Filter xterm.js init queries that the PTY echoes back as visible garbage.
// These are terminal capability probes sent automatically on terminal creation.
function isTerminalQuery(data: string): boolean {
    if (!data.startsWith('\x1b')) return false;
    const second = data[1];
    // DCS sequence (e.g. charset detection)
    if (second === 'P') return true;
    // OSC queries: title (]0;?), background color (]10;?), foreground (]11;?), cursor (]12;?)
    if (second === ']') {
        return /^\x1b\](0|10|11|12);\?\x07?$/.test(data);
    }
    // DSR queries: device attributes (c), secondary device attributes ($y)
    if (second === '[') {
        return data === '\x1b[c' || data.startsWith('\x1b[$y');
    }
    return false;
}

// Shared WebSocket setup for terminal connections
// Returns the WebSocket and sets ws.onClose handler to call onClosed callback.
function createTerminalWS(url: string, term: import('@xterm/xterm').Terminal, onClosed?: () => void): WebSocket {
    // Show status immediately — backend will send its own messages when ready
    term.write('\x1b[2mConnecting...\x1b[0m');
    const ws = new WebSocket(url);
    ws.onopen = () => {
        // Replace connecting message: go to row 1 col 1, clear line
        term.write('\x1b[1H\x1b[2K');
    };
    term.onData((data) => {
        // Filter out xterm.js init queries that the PTY echoes back as garbage text.
        // These are terminal capability queries (DA1, OSC color/title, DCS) sent on mount.
        if (isTerminalQuery(data)) return;
        if (ws.readyState === WebSocket.OPEN) {
            ws.send(JSON.stringify({ type: 'input', data }));
        }
    });
    term.onResize(({ cols, rows }) => {
        if (ws.readyState === WebSocket.OPEN) {
            ws.send(JSON.stringify({ type: 'resize', cols, rows }));
        }
    });
    ws.onmessage = (e) => {
        const msg = JSON.parse(e.data);
        if (msg.type === 'output') term.write(msg.data);
        if (msg.type === 'title') term.title = msg.data;
        if (msg.type === 'status') term.write(msg.data);
        if (msg.type === 'closed' && onClosed) onClosed();
    };
    ws.onclose = () => {
        if (onClosed) onClosed();
    };
    return ws;
}