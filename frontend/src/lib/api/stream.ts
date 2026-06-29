import type { ProjectCounts, Task, StreamEvent } from '$lib/types';
import { API_BASE } from './helpers';

/**
 * App stream - single WebSocket for all real-time events.
 * Subscribes to projects and tasks for real-time updates.
 */

export interface AppStreamCallbacks {
    onProjectCounts?: (counts: Record<string, ProjectCounts>) => void;
    onRefresh?: () => void;
    onDiffStats?: (task_id: string, hash: string, short_stat: string, untracked_n: number, untracked_lines: number, file_hashes?: Record<string, string>) => void;
    onDiff?: (task_id: string, hash: string, diff?: string, untracked?: Array<{ path: string; diff: string; file_size?: number }>, files?: Record<string, { hash: string; type: string; added?: number; removed?: number; line_count?: number; file_size?: number }>) => void;
    onBranch?: (branch: string) => void;
    onFsEvent?: (op: 'create' | 'delete', path: string) => void;
    onTaskUpdated?: (task: Task) => void;
    onTaskDeleted?: (task_id: string) => void;
    onCommitStatus?: (task_id: string, status: 'queued' | 'processing') => void;
    onTaskEvent?: (task_id: string, event: StreamEvent) => void; // task-scoped events (tokens, tool calls, etc.)
    onConnected?: (connected: boolean) => void; // true on connect, false on disconnect
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

    let hasConnectedOnce = false;

    function connect() {
        if (isClosed) return;
        const ws = new WebSocket(`${window.location.origin}${API_BASE}/stream`);
        currentWs = ws;

        ws.onopen = () => {
            reconnectAttempts = 0;
            hasConnectedOnce = true;
            callbacks.onConnected?.(true);
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
                callbacks.onDiffStats(msg.task_id, msg.hash, msg.short_stat, msg.untracked_n, msg.untracked_lines, msg.file_hashes);
            }
            if (msg.type === 'task_diff' && callbacks.onDiff) {
                callbacks.onDiff(msg.task_id, msg.hash || '', msg.diff, msg.untracked || [], msg.files);
            }
            if (msg.type === 'project_branch' && callbacks.onBranch) {
                callbacks.onBranch(msg.branch);
            }
            if (msg.type === 'fs_event' && callbacks.onFsEvent) {
                callbacks.onFsEvent(msg.op, msg.path);
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
            // Signal offline on any disconnect (including initial connection failure)
            callbacks.onConnected?.(false);
            const delay = Math.min(1000 * Math.pow(2, reconnectAttempts), maxDelay);
            reconnectAttempts++;
            reconnectTimer = setTimeout(() => connect(), delay);
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
