import type { ProjectCounts, Task, StreamEvent } from '$lib/types';
import type { AppStreamFrame } from './events';
import { API_BASE, parseStreamFrame } from './helpers';
import { STALE_THRESHOLD_MS, HEARTBEAT_INTERVAL_MS, RECONNECT_BASE_DELAY_MS, RECONNECT_MAX_DELAY_MS, SUSPENDED_GAP_MS } from '$lib/constants';
import { WAKE_FRAME_GAP_MS } from '$lib/utils/tab-return';

/**
 * App stream - single WebSocket for all real-time events.
 * Subscribes to projects and tasks for real-time updates.
 */

export interface AppStreamCallbacks {
    onProjectCounts?: (counts: Record<string, ProjectCounts>) => void;
    onRefresh?: () => void;
    onDiffStats?: (task_id: string, hash: string, short_stat: string, files_n: number, untracked_n: number, untracked_lines: number, tracked_add: number, tracked_del: number, file_hashes?: Record<string, string>, removed_files?: string[], staged_paths?: string[], has_committed_changes?: boolean, already_merged?: boolean) => void;
    onBranch?: (branch: string) => void;
    onFsEvent?: (op: 'create' | 'delete', path: string) => void;
    onTaskUpdated?: (task: Task) => void;
    onTaskDeleted?: (task_id: string) => void;
    onCommitStatus?: (task_id: string, status: 'queued' | 'processing') => void;
    onTaskEvent?: (task_id: string, event: StreamEvent) => void; // task-scoped events (tokens, tool calls, etc.)
    onProjectPorts?: (ports: Array<{ host_port: number; container_port: number }>) => void; // project-level terminal port mappings
    onSandboxImageStatus?: (status: { phase?: string; image?: string; line?: string; error?: string }) => void; // building/pulling the project's sandbox image
    onExtensionChange?: () => void; // extension enabled/disabled/installed/removed
    onConnected?: (connected: boolean) => void; // true on connect, false on disconnect
    onReconnectScheduled?: (nextAttemptAt: number) => void; // epoch ms of next reconnect attempt
    onConnectAttempt?: () => void; // fired when a connection attempt begins
}

export interface AppStream {
    close: () => void;
    /** ms since a frame arrived, counting from the last close when that was more recent. */
    msSinceFrame: () => number;
    reconnectNow: () => void;
    subscribeProject: (projectId: string) => void;
    unsubscribeProject: (projectId: string) => void;
    subscribeTask: (projectId: string, taskId: string) => void;
    unsubscribeTask: (projectId: string, taskId: string) => void;
}

/** Options for createAppStream. */
export interface AppStreamOptions {
    /**
     * Provider of the client's cached done-task diff stats, called on every
     * project (re)subscribe. Returns task_id -> task updated_at for done tasks
     * whose stats the client already has. The server skips re-sending the
     * task_diff_stats frame when the declared updated_at matches — the
     * done-task diff is immutable, so identical updated_at means identical
     * stats.
     */
    getKnownStats?: (projectId: string) => Record<string, string> | Promise<Record<string, string>>;
}

export function createAppStream(callbacks: AppStreamCallbacks, options: AppStreamOptions = {}): AppStream {
    let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
    let heartbeatTimer: ReturnType<typeof setInterval> | null = null;
    let reconnectAttempts = 0;
    let isClosed = false;
    const maxDelay = RECONNECT_MAX_DELAY_MS;
    let currentWs: WebSocket | null = null;
    // Client-side heartbeat — tracks last activity to detect zombie connections
    // (e.g. phone sleep: WebSocket appears OPEN but server closed its end)
    let lastActivityAt = Date.now();
    let lastHeartbeatAt = Date.now();
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

    /** Subscribe (or re-subscribe) to a project, declaring any cached done-task stats.
     * The provider is async (IndexedDB-backed), so the subscribe message goes out
     * once it resolves — before the server builds the initial payload for this
     * connection, keeping the skip-if-known handshake in place on reconnects. */
    async function sendSubscribeProject(projectId: string) {
        let knownStats: Record<string, string> | undefined;
        try {
            knownStats = await options.getKnownStats?.(projectId);
        } catch {
            knownStats = undefined; // provider failure must never block subscribing
        }
        const data: Record<string, any> = { project_id: projectId };
        if (knownStats && Object.keys(knownStats).length > 0) {
            data.known_stats = knownStats;
        }
        sendMsg('subscribe_project', data);
    }

    function startHeartbeat() {
        stopHeartbeat();
        lastHeartbeatAt = Date.now();
        heartbeatTimer = setInterval(() => {
            const now = Date.now();
            const tickGap = now - lastHeartbeatAt;
            lastHeartbeatAt = now;
            dropIfSilent(tickGap > HEARTBEAT_INTERVAL_MS + SUSPENDED_GAP_MS ? WAKE_FRAME_GAP_MS : STALE_THRESHOLD_MS);
        }, HEARTBEAT_INTERVAL_MS);
    }

    function stopHeartbeat() {
        if (heartbeatTimer) {
            clearInterval(heartbeatTimer);
            heartbeatTimer = null;
        }
    }

    /**
     * Close a socket that appears OPEN but has framed nothing for `maxSilentMs`: a peer that merely
     * vanished never fires `onclose` by itself, so silence is the only thing that retires it.
     */
    function dropIfSilent(maxSilentMs: number): boolean {
        if (!currentWs || currentWs.readyState !== WebSocket.OPEN) return false;
        if (Date.now() - lastActivityAt <= maxSilentMs) return false;
        currentWs.close();
        return true;
    }

    let hasConnectedOnce = false;
    let connectGen = 0; // incremented each connect() to stale out old onclose handlers

    /** Wait out the retry ladder, then attempt. */
    function scheduleReconnect(): void {
        const delay = Math.min(RECONNECT_BASE_DELAY_MS * Math.pow(2, reconnectAttempts), maxDelay);
        const dueAt = Date.now() + delay;
        reconnectAttempts++;
        callbacks.onReconnectScheduled?.(dueAt);
        reconnectTimer = setTimeout(() => {
            reconnectTimer = null;
            if (Date.now() - dueAt > SUSPENDED_GAP_MS) reconnectAttempts = 0;
            connect();
        }, delay);
    }

    function connect() {
        if (isClosed) return;
        const gen = ++connectGen;
        // Close any in-flight WebSocket to prevent overlapping connections
        // (e.g. reconnect timer + user clicking "Connect" at the same time)
        if (currentWs) {
            currentWs.close();
        }

        if (reconnectTimer) {
            clearTimeout(reconnectTimer);
            reconnectTimer = null;
        }
        stopHeartbeat(); // prevent leak when stale onclose is skipped
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
            for (const projectId of subscribedProjects) {
                void sendSubscribeProject(projectId);
            }
            for (const key of subscribedTasks) {
                const [projectId, taskId] = key.split(':');
                sendMsg('subscribe_task', { project_id: projectId, task_id: taskId });
            }
        };

        ws.onmessage = (e) => {
            lastActivityAt = Date.now();
            // Typed as this socket's vocabulary: a comparison against a name the server does not send is a
            // branch that can never run, and svelte-check is the only thing that would ever notice.
            const msg = parseStreamFrame(e.data, 'stream') as AppStreamFrame | null;
            if (!msg) return; // an unreadable frame is dropped; the connection stays up
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
                callbacks.onDiffStats(msg.task_id, msg.hash, msg.short_stat, msg.files_n || 0, msg.untracked_n, msg.untracked_lines, msg.tracked_add || 0, msg.tracked_del || 0, msg.file_hashes, msg.removed_files, msg.staged_paths, msg.has_committed_changes, msg.already_merged);
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
            if (msg.type === 'project_ports' && callbacks.onProjectPorts) {
                callbacks.onProjectPorts(msg.ports);
            }
            // Sent while the project's sandbox image is built or pulled. The request that needs the
            // image is blocked until it exists, so this channel is the only way to see progress.
            if (msg.type === 'sandbox_image_status' && callbacks.onSandboxImageStatus) {
                callbacks.onSandboxImageStatus(msg.data || {});
            }
            if (msg.type === 'extension_change' && callbacks.onExtensionChange) {
                callbacks.onExtensionChange();
            }
        };

        ws.onclose = () => {
            if (gen !== connectGen) return; // stale connection, skip
            stopHeartbeat();
            // No frames can arrive after this, so the silence clock starts here.
            lastActivityAt = Date.now();
            currentWs = null;
            if (isClosed) return;
            // Signal offline on any disconnect (including initial connection failure)
            callbacks.onConnected?.(false);
            scheduleReconnect();
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
            if (isClosed) return;

            reconnectAttempts = 0;
            const ws = currentWs;
            if (ws) {
                if (ws.readyState === WebSocket.CONNECTING) return; // an attempt is already under way
                // Open and framed recently needs nothing; open and silent is a zombie.
                if (ws.readyState === WebSocket.OPEN && !dropIfSilent(WAKE_FRAME_GAP_MS)) return;
            }
            connect();
        },
        msSinceFrame: () => Date.now() - lastActivityAt,
        subscribeProject: (projectId: string) => {
            if (subscribedProjects.has(projectId)) return;
            subscribedProjects.add(projectId);
            void sendSubscribeProject(projectId);
        },
        unsubscribeProject: (projectId: string) => {
            subscribedProjects.delete(projectId);
            sendMsg('unsubscribe_project', { project_id: projectId });
        },
        subscribeTask: (projectId: string, taskId: string) => {
            const key = `${projectId}:${taskId}`;
            if (subscribedTasks.has(key)) return;
            subscribedTasks.add(key);
            sendMsg('subscribe_task', { project_id: projectId, task_id: taskId });
        },
        unsubscribeTask: (projectId: string, taskId: string) => {
            subscribedTasks.delete(`${projectId}:${taskId}`);
            sendMsg('unsubscribe_task', { project_id: projectId, task_id: taskId });
        },
    };
}
