import type { ProjectCounts, Task, StreamEvent } from '$lib/types';
import { API_BASE } from './helpers';
import { STALE_THRESHOLD_MS, HEARTBEAT_INTERVAL_MS, RECONNECT_MAX_DELAY_MS } from '$lib/constants';

/**
 * App stream - single WebSocket for all real-time events.
 * Subscribes to projects and tasks for real-time updates.
 */

export interface AppStreamCallbacks {
    onProjectCounts?: (counts: Record<string, ProjectCounts>) => void;
    onRefresh?: () => void;
    onDiffStats?: (task_id: string, hash: string, short_stat: string, files_n: number, untracked_n: number, untracked_lines: number, file_hashes?: Record<string, string>, removed_files?: string[], staged_paths?: string[]) => void;
    onDiff?: (task_id: string, hash: string, diff?: string, untracked?: Array<{ path: string; diff: string; file_size?: number }>, files?: Record<string, { hash: string; type: string; added?: number; removed?: number; line_count?: number; file_size?: number }>) => void;
    onBranch?: (branch: string) => void;
    onFsEvent?: (op: 'create' | 'delete', path: string) => void;
    onTaskUpdated?: (task: Task) => void;
    onTaskDeleted?: (task_id: string) => void;
    onCommitStatus?: (task_id: string, status: 'queued' | 'processing') => void;
    onTaskEvent?: (task_id: string, event: StreamEvent) => void; // task-scoped events (tokens, tool calls, etc.)
    onPortConflict?: (project_id: string, conflicted_ports: number[]) => void; // host ports already in use
    onProjectPorts?: (ports: Array<{ host_port: number; container_port: number }>) => void; // project-level terminal port mappings
    onConnected?: (connected: boolean) => void; // true on connect, false on disconnect
    onReconnectScheduled?: (nextAttemptAt: number) => void; // epoch ms of next reconnect attempt
    onConnectAttempt?: () => void; // fired when a connection attempt begins
}

export interface AppStream {
    close: () => void;
    checkStale: () => void;
    reconnectNow: () => void;
    subscribeProject: (projectId: string) => void;
    unsubscribeProject: (projectId: string) => void;
    subscribeTask: (projectId: string, taskId: string) => void;
    unsubscribeTask: (projectId: string, taskId: string) => void;
}

export function createAppStream(callbacks: AppStreamCallbacks): AppStream {
    let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
    let heartbeatTimer: ReturnType<typeof setInterval> | null = null;
    let reconnectAttempts = 0;
    let isClosed = false;
    const maxDelay = RECONNECT_MAX_DELAY_MS;
    let currentWs: WebSocket | null = null;
    // Client-side heartbeat — tracks last activity to detect zombie connections
    // (e.g. phone sleep: WebSocket appears OPEN but server closed its end)
    let lastActivityAt = Date.now();
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

    function startHeartbeat() {
        stopHeartbeat();
        heartbeatTimer = setInterval(() => {
            if (currentWs && currentWs.readyState === WebSocket.OPEN) {
                checkStale();
            }
        }, HEARTBEAT_INTERVAL_MS);
    }

    function stopHeartbeat() {
        if (heartbeatTimer) {
            clearInterval(heartbeatTimer);
            heartbeatTimer = null;
        }
    }

    /** Check if the connection is stale (zombie) and force-close if so. */
    function checkStale() {
        if (currentWs && currentWs.readyState === WebSocket.OPEN) {
            const idle = Date.now() - lastActivityAt;
            if (idle > STALE_THRESHOLD_MS) {
                // Connection appears open but hasn't received data in too long.
                // Force-close to trigger the reconnect logic.
                currentWs.close();
            }
        }
    }

    let hasConnectedOnce = false;
    let connectGen = 0; // incremented each connect() to stale out old onclose handlers

    function connect() {
        if (isClosed) return;
        // Close any in-flight WebSocket to prevent overlapping connections
        // (e.g. reconnect timer + user clicking "Connect" at the same time)
        if (currentWs) {
            currentWs.close();
        }
        stopHeartbeat(); // prevent leak when stale onclose is skipped
        const gen = ++connectGen;
        callbacks.onConnectAttempt?.();
        // Use ?sync=1 on reconnect so the server sends the global event buffer
        const syncParam = hasConnectedOnce ? '?sync=1' : '';
        const ws = new WebSocket(`${window.location.origin}${API_BASE}/stream${syncParam}`);
        currentWs = ws;

        ws.onopen = () => {
            if (gen !== connectGen) return; // stale
            reconnectAttempts = 0;
            hasConnectedOnce = true;
            lastActivityAt = Date.now();
            callbacks.onConnected?.(true);
            startHeartbeat();
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
            lastActivityAt = Date.now();
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
                callbacks.onDiffStats(msg.task_id, msg.hash, msg.short_stat, msg.files_n || 0, msg.untracked_n, msg.untracked_lines, msg.file_hashes, msg.removed_files, msg.staged_paths);
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
            if (msg.type === 'port_conflict' && callbacks.onPortConflict) {
                callbacks.onPortConflict(msg.data.project_id, msg.data.conflicted_ports);
            }
            if (msg.type === 'project_ports' && callbacks.onProjectPorts) {
                callbacks.onProjectPorts(msg.ports);
            }
        };

        ws.onclose = () => {
            if (gen !== connectGen) return; // stale connection, skip
            stopHeartbeat();
            currentWs = null;
            if (isClosed) return;
            // Signal offline on any disconnect (including initial connection failure)
            callbacks.onConnected?.(false);
            const delay = Math.min(1000 * Math.pow(2, reconnectAttempts), maxDelay);
            reconnectAttempts++;
            callbacks.onReconnectScheduled?.(Date.now() + delay);
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
            stopHeartbeat();
            if (reconnectTimer) {
                clearTimeout(reconnectTimer);
                reconnectTimer = null;
            }
            if (currentWs) {
                currentWs.close();
                currentWs = null;
            }
        },
        reconnectNow: () => {
            if (reconnectTimer) {
                clearTimeout(reconnectTimer);
                reconnectTimer = null;
            }
            connect();
        },
        checkStale,
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
