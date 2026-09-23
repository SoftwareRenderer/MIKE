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
    /** ms since this stream last had anything to say: a frame, a close, or the attempt under way. */
    msSinceFrame: () => number;
    reconnectNow: () => void;
    subscribeProject: (projectId: string) => void;
    unsubscribeProject: (projectId: string) => void;
    subscribeTask: (projectId: string, taskId: string, wholeBuffer?: boolean) => void;
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

/** One of the four frames that move a subscription. */
type SubscriptionFrameType = 'subscribe_project' | 'unsubscribe_project' | 'subscribe_task' | 'unsubscribe_task';

/** A subscription frame the server has not been told about yet. */
interface OwedSubscription {
    type: SubscriptionFrameType;
    data: Record<string, any>;
    /** A `subscribe_project` whose `known_stats` are still resolving: nothing may be written yet. */
    waiting?: boolean;
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
    /**
     * Every task key this view holds, mapped to whether this browser has no copy of the conversation —
     * the `no_rows` claim that tells the server its replay buffer must not be trimmed. A held key owes its
     * current claim, which is what a reconnect replays.
     */
    const subscribedTasks = new Map<string, boolean>(); // "projectId:taskId"
    /**
     * What the server has not been told about these two sets: at most one frame per key, holding the
     * newest intent recorded for it, in the order those intents were formed. Keys are the sets' own — a
     * project id, or "projectId:taskId". Recording the intent is synchronous, so two calls in the same
     * tick coalesce; only a `subscribe_project`'s payload waits, and while it does its frame stays
     * unwritten.
     *
     * A projection rather than a message log, which is what bounds it: an entry can only name a key the
     * app has opened, so an outage costs at most `subscribedProjects.size + subscribedTasks.size` frames
     * however long it runs or how often the view changed during it. The two wire rules it exists to hold —
     * the newest intent for a key is the only one sent, and an undelivered subscribe is dropped rather
     * than cancelled — are in `docs/reconnection.md → One frame per key`.
     */
    const owed: Map<string, OwedSubscription> = new Map();

    /** Write one key's owed frame if the socket can carry it, and stop owing it when it did. */
    function deliverOwed(key: string): boolean {
        const frame = owed.get(key);
        if (!frame || frame.waiting) return false;
        if (!currentWs || currentWs.readyState !== WebSocket.OPEN) return false;
        currentWs.send(JSON.stringify({ type: frame.type, data: frame.data }));
        owed.delete(key);
        return true;
    }

    /** The server must be told it holds this key; if nothing is open, that becomes the key's debt. */
    function oweSubscribe(key: string, frame: OwedSubscription) {
        owed.set(key, frame);
        deliverOwed(key);
    }

    /**
     * The server must be told the client has left this key. A subscribe still owed for it is dropped
     * instead: nothing was ever asked for, so there is nothing for the server to release.
     */
    function oweUnsubscribe(key: string, frame: OwedSubscription) {
        if (owed.get(key)?.type.startsWith('subscribe_')) {
            owed.delete(key);
            return;
        }
        owed.set(key, frame);
        deliverOwed(key);
    }

    /**
     * Subscribe (or re-subscribe) to a project, declaring any cached done-task stats. The frame waits for
     * the async (IndexedDB-backed) provider so the skip-if-known handshake declares something on the
     * reconnect path too, where the frame is formed by {@link flushSubscriptions} and not by a click; with
     * no provider there is nothing to wait for and it goes out at once.
     */
    function requestProjectSubscribe(projectId: string) {
        const frame: OwedSubscription = {
            type: 'subscribe_project',
            data: { project_id: projectId },
            waiting: !!options.getKnownStats,
        };
        oweSubscribe(projectId, frame);
        if (frame.waiting) void fillKnownStats(projectId, frame);
    }

    /** Attach the cached stats to a project's held `subscribe_project` and write it if that freed it. */
    async function fillKnownStats(projectId: string, frame: OwedSubscription) {
        let knownStats: Record<string, string> | undefined;
        try {
            knownStats = await options.getKnownStats?.(projectId);
        } catch {
            knownStats = undefined; // provider failure must never block subscribing
        }
        // Only this frame's own call may fill it. The app may have left the project, and a subscribe
        // written behind that unsubscribe is one nothing will ever release; or it re-entered, and the
        // newer frame has its own call answering for it.
        if (owed.get(projectId) !== frame) return;
        if (knownStats && Object.keys(knownStats).length > 0) {
            frame.data.known_stats = knownStats;
        }
        frame.waiting = false;
        deliverOwed(projectId);
    }

    function taskSubscription(projectId: string, taskId: string, wholeBuffer: boolean): Record<string, any> {
        const data: Record<string, any> = { project_id: projectId, task_id: taskId };
        if (wholeBuffer) data.no_rows = true;
        return data;
    }

    /**
     * Say to a new connection everything it does not already know: what the sets changed while it was
     * down, then one subscribe per key still held. A held key can only owe a subscribe, so a key the
     * first pass covered — including one still waiting on its stats — is never asked for twice.
     */
    function flushSubscriptions() {
        const told = new Set<string>();
        for (const [key, frame] of [...owed]) {
            if (!frame.waiting && !deliverOwed(key)) return; // not open after all: the rest of the debt survives
            told.add(key); // covered, including a frame the provider will write to whichever socket is current
        }
        for (const projectId of subscribedProjects) {
            if (!told.has(projectId)) requestProjectSubscribe(projectId);
        }
        for (const [key, wholeBuffer] of subscribedTasks) {
            if (told.has(key)) continue;
            const [projectId, taskId] = key.split(':');
            oweSubscribe(key, { type: 'subscribe_task', data: taskSubscription(projectId, taskId, wholeBuffer) });
        }
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
     * Close a socket that has framed nothing for `maxSilentMs`. A peer that simply vanished fires no
     * `onclose`, so silence is the only thing that retires it — an attempt stuck in the handshake too.
     */
    function dropIfSilent(maxSilentMs: number): boolean {
        if (!currentWs || currentWs.readyState === WebSocket.CLOSED) return false;
        if (Date.now() - lastActivityAt <= maxSilentMs) return false;
        currentWs.close();
        return true;
    }

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
        // No sync parameter: what a reconnect catches up on is its own subscriptions and the
        // HTTP refetch they trigger, not a backlog the server kept for the socket it lost.
        const ws = new WebSocket(`${window.location.origin}${API_BASE}/stream`);
        currentWs = ws;
        // An attempt that never completes has no frame to start the silence clock, so it starts here.
        lastActivityAt = Date.now();
        startHeartbeat();

        ws.onopen = () => {
            if (gen !== connectGen) return; // stale
            reconnectAttempts = 0;
            callbacks.onConnected?.(true);
            flushSubscriptions();
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
            // connect() closes whatever it finds, so replacing a stalled attempt starts one socket, not two.
            if (!currentWs || dropIfSilent(WAKE_FRAME_GAP_MS)) connect();
        },
        msSinceFrame: () => Date.now() - lastActivityAt,
        subscribeProject: (projectId: string) => {
            if (subscribedProjects.has(projectId)) return;
            subscribedProjects.add(projectId);
            requestProjectSubscribe(projectId);
        },
        unsubscribeProject: (projectId: string) => {
            subscribedProjects.delete(projectId);
            oweUnsubscribe(projectId, { type: 'unsubscribe_project', data: { project_id: projectId } });
        },
        subscribeTask: (projectId: string, taskId: string, wholeBuffer = false) => {
            const key = `${projectId}:${taskId}`;
            // A key already held is re-declared, not re-taken, when the claim changes.
            if (subscribedTasks.has(key) && subscribedTasks.get(key) === wholeBuffer) return;
            subscribedTasks.set(key, wholeBuffer);
            oweSubscribe(key, { type: 'subscribe_task', data: taskSubscription(projectId, taskId, wholeBuffer) });
        },
        unsubscribeTask: (projectId: string, taskId: string) => {
            const key = `${projectId}:${taskId}`;
            subscribedTasks.delete(key);
            oweUnsubscribe(key, { type: 'unsubscribe_task', data: { project_id: projectId, task_id: taskId } });
        },
    };
}
