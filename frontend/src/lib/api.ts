const API_BASE = '/api';

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
    const res = await fetch(`${API_BASE}/projects`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch projects');
    return res.json();
}

export async function getProjectCounts(): Promise<Record<string, ProjectCounts>> {
    const res = await fetch(`${API_BASE}/projects/counts`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch project counts');
    return res.json();
}

export async function getProjectStats(projectId: string): Promise<ProjectStats> {
    const res = await fetch(`${API_BASE}/projects/${projectId}/stats`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch project stats');
    return res.json();
}

export async function resetProjectStats(projectId: string, modelName: string, providerName?: string): Promise<void> {
    const params = new URLSearchParams({ model_name: modelName });
    if (providerName) params.set('provider_name', providerName);
    const res = await fetch(`${API_BASE}/projects/${projectId}/stats?${params}`, { method: 'DELETE' });
    if (!res.ok) throw await apiError(res, 'Failed to reset stats');
}

export async function createProject(path: string): Promise<Project> {
    const res = await fetch(`${API_BASE}/projects`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ path }),
    });
    if (!res.ok) throw await apiError(res, 'Failed to create project');
    return res.json();
}

export async function updateProject(id: string, updates: { name?: string; sandbox_enabled?: boolean }): Promise<Project> {
    const res = await fetch(`${API_BASE}/projects/${id}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(updates),
    });
    if (!res.ok) throw await apiError(res, 'Failed to update project');
    return res.json();
}

export async function deleteProject(id: string): Promise<void> {
    const res = await fetch(`${API_BASE}/projects/${id}`, { method: 'DELETE' });
    if (!res.ok) throw await apiError(res, 'Failed to delete project');
}

// -- Board endpoints --
export async function getBoard(projectId: string): Promise<Board> {
    const res = await fetch(`${API_BASE}/projects/${projectId}/board`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch board');
    return res.json();
}

export async function reorderTasks(projectId: string, reorders: ReorderRequest[]): Promise<void> {
    const res = await fetch(`${API_BASE}/projects/${projectId}/tasks/reorder`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(reorders),
    });
    if (!res.ok) throw await apiError(res, 'Failed to reorder tasks');
}

// -- Task CRUD endpoints --
export async function createTask(projectId: string, task: Partial<Task>): Promise<Task> {
    const res = await fetch(`${API_BASE}/projects/${projectId}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(task),
    });
    if (!res.ok) throw await apiError(res, 'Failed to create task');
    return res.json();
}

export async function getTask(projectId: string, taskId: string): Promise<Task> {
    const res = await fetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}`);
    if (!res.ok) throw await apiError(res, 'Task not found');
    return res.json();
}

export async function updateTask(projectId: string, taskId: string, task: Partial<Task>): Promise<Task> {
    const res = await fetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(task),
    });
    if (!res.ok) throw await apiError(res, 'Failed to update task');
    return res.json();
}

export async function deleteTask(projectId: string, taskId: string): Promise<void> {
    const res = await fetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}`, { method: 'DELETE' });
    if (!res.ok) throw await apiError(res, 'Failed to delete task');
}

export async function moveTask(projectId: string, taskId: string, status: string): Promise<Task> {
    const res = await fetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/move`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ status }),
    });
    if (!res.ok) throw await apiError(res, 'Failed to move task');
    return res.json();
}

export async function commitAndDone(projectId: string, taskId: string): Promise<Task> {
    const res = await fetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/commit`, {
        method: 'POST',
    });
    if (!res.ok) throw await apiError(res, 'Failed to commit and mark as done');
    return res.json();
}

export async function clearDoneTasks(projectId: string): Promise<{ deleted: number }> {
    const res = await fetch(`${API_BASE}/projects/${projectId}/tasks/done`, { method: 'DELETE' });
    if (!res.ok) throw await apiError(res, 'Failed to clear done tasks');
    return res.json();
}

// -- Task execution endpoints --
export async function startTask(projectId: string, taskId: string): Promise<Task> {
    const res = await fetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/start`, { method: 'POST' });
    if (!res.ok) throw await apiError(res, 'Failed to start task');
    return res.json();
}

export async function stopTask(projectId: string, taskId: string): Promise<Task> {
    const res = await fetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/stop`, { method: 'POST' });
    if (!res.ok) throw await apiError(res, 'Failed to stop task');
    return res.json();
}

export async function retryTask(projectId: string, taskId: string): Promise<Task> {
    const res = await fetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/retry`, { method: 'POST' });
    if (!res.ok) throw await apiError(res, 'Failed to retry task');
    return res.json();
}

export async function resumeTask(projectId: string, taskId: string): Promise<Task> {
    const res = await fetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/resume`, { method: 'POST' });
    if (!res.ok) throw await apiError(res, 'Failed to resume task');
    return res.json();
}

// Board stream - WebSocket for real-time board updates
export function createBoardStream(projectId: string, onRefresh: () => void, onProjectCounts?: (counts: Record<string, ProjectCounts>) => void, onDiffStats?: (task_id: string, short_stat: string, untracked_n: number, untracked_lines: number) => void, onBranch?: (branch: string) => void, onTaskUpdated?: (task: Task) => void): { ws: WebSocket; close: () => void } {
    const ws = new WebSocket(`${window.location.origin}${API_BASE}/projects/${projectId}/board/stream`);
    ws.onmessage = (e) => {
        const msg = JSON.parse(e.data);
        if (msg.type === 'board_refresh') {
            onRefresh();
        }
        if (msg.type === 'project_counts' && onProjectCounts) {
            onProjectCounts(msg.data);
        }
        if (msg.type === 'task_diff_stats' && onDiffStats) {
            onDiffStats(msg.task_id, msg.short_stat, msg.untracked_n, msg.untracked_lines);
        }
        if (msg.type === 'project_branch' && onBranch) {
            onBranch(msg.branch);
        }
        if (msg.type === 'task_updated' && onTaskUpdated) {
            onTaskUpdated(msg.task);
        }
    };
    ws.onclose = () => {
        // Silent reconnect handled by caller
    };
    ws.onerror = () => {
        // Connection errors are handled via onclose
    };
    return { ws, close: () => ws.close() };
}

export function createTaskStream(projectId: string, taskId: string, onEvent: (event: StreamEvent) => void, syncBuffer = true): { ws: WebSocket; send: (msg: string) => void; sendWithModel: (msg: string, modelName: string, serverUrl: string) => void; close: () => void } {
    const syncParam = syncBuffer ? '?sync=1' : '';
    const ws = new WebSocket(`${window.location.origin}${API_BASE}/projects/${projectId}/tasks/${taskId}/stream${syncParam}`);
    ws.onmessage = (e) => {
        const msg = JSON.parse(e.data);
        // msg.data may already be an object (json.RawMessage) or a string
        const data = typeof msg.data === 'string' ? JSON.parse(msg.data) : msg.data;
        onEvent({ type: msg.type, data });
    };
    ws.onerror = () => onEvent({ type: 'error', data: { message: 'WebSocket error', code: 'ws_error' } });
    ws.onclose = () => onEvent({ type: 'status', data: { state: 'disconnected', activity: 'Stream closed' } });
    return {
        ws,
        send: (msg: string) => {
            if (ws.readyState === WebSocket.OPEN) {
                ws.send(JSON.stringify({ type: 'user_message', data: { content: msg } }));
            }
        },
        sendWithModel: (msg: string, modelName: string, serverUrl: string) => {
            if (ws.readyState === WebSocket.OPEN) {
                const data: Record<string, string> = { content: msg };
                if (modelName) data.model_name = modelName;
                if (serverUrl) data.llama_server_url = serverUrl;
                ws.send(JSON.stringify({ type: 'user_message', data }));
            }
        },
        close: () => ws.close()
    };
}

export async function getTaskMessages(projectId: string, taskId: string): Promise<TaskMessage[]> {
    const res = await fetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/messages`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch messages');
    return res.json();
}

export async function createTaskMessage(projectId: string, taskId: string, role: string, content: string): Promise<TaskMessage> {
    const res = await fetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/messages`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ role, content }),
    });
    if (!res.ok) throw await apiError(res, 'Failed to create message');
    return res.json();
}

export async function deleteTaskMessagesAfter(projectId: string, taskId: string, messageId: string, regenerate = false): Promise<void> {
    const res = await fetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/messages`, {
        method: 'DELETE',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ message_id: messageId, regenerate }),
    });
    if (!res.ok) throw await apiError(res, 'Failed to delete messages');
}

/** Delete a single message by ID (e.g., dismissing an error). Does not delete messages after it. */
export async function deleteTaskMessage(projectId: string, taskId: string, messageId: string): Promise<void> {
    const res = await fetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/messages`, {
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
    const res = await fetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/messages/${messageId}`);
    if (!res.ok) throw await apiError(res, 'Failed to check message delete');
    return res.json();
}

export async function updateTaskMessage(projectId: string, taskId: string, messageId: string, content: string): Promise<void> {
    const res = await fetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/messages/${messageId}`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ content }),
    });
    if (!res.ok) throw await apiError(res, 'Failed to update message');
}

export async function getTaskDiff(projectId: string, taskId: string): Promise<{ diff: string; untracked: Array<{ path: string; diff: string }>; short_stat: string }> {
    const res = await fetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/diff`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch diff');
    return res.json();
}

// -- Prompt endpoints --
export async function reloadPrompts(): Promise<{ status: string; message: string; files: string[] }> {
    const res = await fetch(`${API_BASE}/prompts/reload`, {
        method: 'POST',
    });
    if (!res.ok) throw await apiError(res, 'Failed to reload prompts');
    return res.json();
}

// -- Configuration endpoints --
export async function getConfig(): Promise<Config> {
    const res = await fetch(`${API_BASE}/config`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch config');
    return res.json();
}

export async function updateConfig(config: Config): Promise<Config> {
    const res = await fetch(`${API_BASE}/config`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(config),
    });
    if (!res.ok) throw await apiError(res, 'Failed to update config');
    return res.json();
}

export async function testConnection(llamaServerUrl: string, modelName: string): Promise<{ success: boolean; message: string }> {
    const res = await fetch(`${API_BASE}/config/test-connection`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ llama_server_url: llamaServerUrl, model_name: modelName }),
    });
    if (!res.ok) throw await apiError(res, 'Failed to test connection');
    return res.json();
}

export async function fetchModels(llamaServerUrl: string): Promise<{ models: Array<{ name: string; context_window?: number }>; error?: string }> {
    const res = await fetch(`${API_BASE}/config/fetch-models`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ llama_server_url: llamaServerUrl }),
    });
    if (!res.ok) throw await apiError(res, 'Failed to fetch models');
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
    const res = await fetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/dependencies`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch dependencies');
    return res.json();
}

export async function createDependency(projectId: string, taskId: string, childTaskId: string, autoStart: boolean): Promise<TaskDependency> {
    const res = await fetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/dependencies`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ child_task_id: childTaskId, auto_start: autoStart }),
    });
    if (!res.ok) throw await apiError(res, 'Failed to create dependency');
    return res.json();
}

// -- Review comment endpoints --
export async function getReviewComments(projectId: string, taskId: string): Promise<ReviewComment[]> {
    const res = await fetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/review-comments`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch review comments');
    return res.json();
}

export async function createReviewComment(projectId: string, taskId: string, filePath: string, lineNum: number, comment: string): Promise<ReviewComment> {
    const res = await fetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/review-comments`, {
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
    const res = await fetch(url);
    if (!res.ok) throw await apiError(res, 'Failed to fetch git log');
    return res.json();
}

export async function getGitStatus(projectId: string): Promise<GitStatus> {
    const res = await fetch(`${API_BASE}/projects/${projectId}/git/status`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch git status');
    return res.json();
}

// -- Home agent endpoints --
export async function getHomeAgentMessages(projectId: string): Promise<HomeAgentMessage[]> {
    const res = await fetch(`${API_BASE}/projects/${projectId}/home-agent/messages`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch messages');
    return res.json();
}

// -- Thread endpoints --
export async function getTaskThreads(projectId: string, taskId: string): Promise<Thread[]> {
    const res = await fetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/threads`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch threads');
    return res.json();
}

export async function switchTaskThread(projectId: string, taskId: string, threadId: string): Promise<Thread> {
    const res = await fetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/threads/switch`, {
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
    const res = await fetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/threads/fork`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
    });
    if (!res.ok) throw await apiError(res, 'Failed to fork thread');
    return res.json();
}

export async function deleteTaskThread(projectId: string, taskId: string, threadId: string): Promise<{ status: string }> {
    const res = await fetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/threads`, {
        method: 'DELETE',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ thread_id: threadId }),
    });
    if (!res.ok) throw await apiError(res, 'Failed to delete thread');
    return res.json();
}

// -- Worktree --
export async function checkWorktreeExists(projectId: string, taskId: string): Promise<{ exists: boolean }> {
    const res = await fetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/worktree-exists`);
    if (!res.ok) throw await apiError(res, 'Failed to check worktree');
    return res.json();
}

// -- Terminal --
export function createTerminal(projectId: string, taskId: string, term: import('@xterm/xterm').Terminal): WebSocket {
    return createTerminalWS(`${window.location.origin}${API_BASE}/projects/${projectId}/tasks/${taskId}/terminal`, term);
}

// -- Project Terminal (shell in project root) --
export function createProjectTerminal(projectId: string, term: import('@xterm/xterm').Terminal): WebSocket {
    return createTerminalWS(`${window.location.origin}${API_BASE}/projects/${projectId}/terminal`, term);
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
function createTerminalWS(url: string, term: import('@xterm/xterm').Terminal): WebSocket {
    const ws = new WebSocket(url);
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
    };
    return ws;
}