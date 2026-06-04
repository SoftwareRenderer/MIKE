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

export async function createProject(path: string): Promise<Project> {
    const res = await fetch(`${API_BASE}/projects`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ path }),
    });
    if (!res.ok) throw await apiError(res, 'Failed to create project');
    return res.json();
}

export async function updateProject(id: string, name: string): Promise<Project> {
    const res = await fetch(`${API_BASE}/projects/${id}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name }),
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
export function createBoardStream(projectId: string, onRefresh: () => void): { ws: WebSocket; close: () => void } {
    const ws = new WebSocket(`${window.location.origin}${API_BASE}/projects/${projectId}/board/stream`);
    ws.onmessage = (e) => {
        const msg = JSON.parse(e.data);
        if (msg.type === 'board_refresh') {
            onRefresh();
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

export function createTaskStream(projectId: string, taskId: string, onEvent: (event: StreamEvent) => void): { ws: WebSocket; send: (msg: string) => void; close: () => void } {
    const ws = new WebSocket(`${window.location.origin}${API_BASE}/projects/${projectId}/tasks/${taskId}/stream`);
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
        close: () => ws.close()
    };
}

export async function getTaskMessages(projectId: string, taskId: string): Promise<TaskMessage[]> {
    const res = await fetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/messages`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch messages');
    return res.json();
}

export async function deleteTaskMessagesAfter(projectId: string, taskId: string, messageId: string): Promise<void> {
    const res = await fetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/messages`, {
        method: 'DELETE',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ message_id: messageId }),
    });
    if (!res.ok) throw await apiError(res, 'Failed to delete messages');
}

export async function updateTaskMessage(projectId: string, taskId: string, messageId: string, content: string): Promise<void> {
    const res = await fetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/messages/${messageId}`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ content }),
    });
    if (!res.ok) throw await apiError(res, 'Failed to update message');
}

export async function getTaskDiff(projectId: string, taskId: string): Promise<string> {
    const res = await fetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/diff`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch diff');
    return res.text();
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

// -- Terminal --
export function createTerminal(projectId: string, term: import('@xterm/xterm').Terminal): WebSocket {
    const fitAddon = new (import('@xterm/addon-fit').FitAddon)();
    const ws = new WebSocket(`${window.location.origin}${API_BASE}/projects/${projectId}/terminal`);
    ws.onopen = () => {
        term.loadAddon(fitAddon);
        fitAddon.fit();
        term.onData((data) => ws.send(JSON.stringify({ type: 'input', data })));
        term.onResize(({ cols, rows }) => ws.send(JSON.stringify({ type: 'resize', cols, rows })));
    };
    ws.onmessage = (e) => {
        const msg = JSON.parse(e.data);
        if (msg.type === 'output') term.write(msg.data);
        if (msg.type === 'title') term.title = msg.data;
    };
    return ws;
}