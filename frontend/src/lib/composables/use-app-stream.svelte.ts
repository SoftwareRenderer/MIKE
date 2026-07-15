import type { Project, Task, Config, StreamEvent } from '$lib/types';
import { createAppStream, setOfflineStatus } from '$lib/api';
import { setAppStreamRef } from '$lib/stream-ref';
import { projects as projectsStore, board as boardStore, selectedTask, selectedProject, projectTerminalPorts } from '$lib/stores/app';
import { createStreamingContext, createTaskDiffContext, createCommitQueueContext, createTaskEventContext } from '$lib/context';
import { getBoardCache } from '$lib/storage';

/**
 * Manages the app-level WebSocket stream lifecycle.
 *
 * Handles:
 * - AppStream creation with all event callbacks
 * - Connection state tracking (wsConnected, countdown, stale time)
 * - Visibility change handling (stale check, reconnect)
 * - Task subscription management
 * - Countdown/stale-time ticker management
 * - Fallback stream for offline cache mode
 */
export function useAppStream() {
    let appStream: ReturnType<typeof createAppStream> | null = $state(null);
    let wsConnected = $state(false);
    let lastSyncedAt = $state(Infinity); // epoch ms of last successful data sync
    let nextReconnectAt = $state(0);
    let countdownSeconds = $state(0);
    let countdownTimer: ReturnType<typeof setInterval> | null = null;
    let staleTimeText = $state('');
    let staleTimer: ReturnType<typeof setInterval> | null = null;
    let servingFromCache = $state(false);

    // Context stores (initialized by App.svelte, referenced here)
    let taskDiffStatsContext: ReturnType<typeof createTaskDiffContext>['taskDiffStats'] | null = null;
    let taskDiffContext: ReturnType<typeof createTaskDiffContext>['taskDiff'] | null = null;
    let commitQueueStatusContext: ReturnType<typeof createCommitQueueContext> | null = null;
    let taskEventContext: ReturnType<typeof createTaskEventContext> | null = null;

    /** Set the context stores (called by App.svelte after creation). */
    function setContexts(
        taskDiffStats: ReturnType<typeof createTaskDiffContext>['taskDiffStats'],
        taskDiff: ReturnType<typeof createTaskDiffContext>['taskDiff'],
        commitQueue: ReturnType<typeof createCommitQueueContext>,
        taskEvent: ReturnType<typeof createTaskEventContext>
    ) {
        taskDiffStatsContext = taskDiffStats;
        taskDiffContext = taskDiff;
        commitQueueStatusContext = commitQueue;
        taskEventContext = taskEvent;
    }

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

    function timeAgo(seconds: number): string {
        if (seconds < 60) return `just now`;
        const mins = Math.floor(seconds / 60);
        if (mins < 60) return `${mins}m ago`;
        const hrs = Math.floor(mins / 60);
        return `${hrs}h ago`;
    }

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

    /** Build the AppStream callback handler object. */
    function buildStreamCallbacks(onRefresh: () => void, currentProject: Project | null, selectedTaskVal: Task | null, tasks: Task[]) {
        return {
            onProjectCounts: (counts: any) => {
                // Handled by App.svelte via projectCounts state
            },
            onRefresh: () => onRefresh(),
            onDiffStats: (task_id: string, hash: string, short_stat: string, files_n: number, untracked_n: number, untracked_lines: number, file_hashes: any, removed_files: any, staged_paths: any) => {
                if (!taskDiffStatsContext) return;
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
            onDiff: (task_id: string, hash: string, diff: any, untracked: any, files: any) => {
                if (!taskDiffContext) return;
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
            onBranch: () => {}, // handled by App.svelte
            onFsEvent: () => {}, // handled by App.svelte
            onTaskUpdated: (updatedTask: Task) => {
                // Handled by App.svelte via tasks state
            },
            onTaskDeleted: (task_id: string) => {
                // Handled by App.svelte via tasks state
            },
            onCommitStatus: (task_id: string, status: string) => {
                if (!commitQueueStatusContext || !currentProject) return;
                commitQueueStatusContext.update(map => ({ ...map, [currentProject.id]: { taskID: task_id, status } }));
            },
            onTaskEvent: (task_id: string, event: StreamEvent) => {
                if (!taskEventContext || !selectedTaskVal || selectedTaskVal.id !== task_id) return;
                taskEventContext.set({ task_id, ...event });
            },
            onConnected: (connected: boolean) => {
                wsConnected = connected;
                setOfflineStatus(!connected);
                if (!connected) {
                    startStaleTick();
                } else {
                    stopCountdown();
                    stopStaleTick();
                    nextReconnectAt = 0;
                    countdownSeconds = 0;
                    if (servingFromCache) {
                        // Auto-refresh handled by App.svelte
                    }
                }
            },
            onReconnectScheduled: (nextAttemptAt: number) => {
                nextReconnectAt = nextAttemptAt;
                startCountdown();
            },
            onConnectAttempt: () => {
                stopCountdown();
                countdownSeconds = 0;
            },
            onPortConflict: () => {}, // handled by App.svelte
            onProjectPorts: (ports: any) => {
                projectTerminalPorts.set(ports);
            },
        };
    }

    /** Handle visibility change (tab wake, phone wake from sleep). */
    function handleVisibilityChange() {
        if (!document.hidden && appStream) {
            appStream.checkStale();
            if (!wsConnected) {
                appStream.reconnectNow();
            }
        }
    }

    /** Cleanup function for onDestroy. */
    function cleanup() {
        stopCountdown();
        stopStaleTick();
        window.removeEventListener('visibilitychange', handleVisibilityChange);
        window.removeEventListener('focus', handleVisibilityChange);
        if (appStream) {
            appStream.close();
            appStream = null;
        }
    }

    return {
        appStream,
        wsConnected,
        lastSyncedAt,
        nextReconnectAt,
        countdownSeconds,
        staleTimeText,
        servingFromCache,
        tryReconnectNow,
        startCountdown,
        stopCountdown,
        startStaleTick,
        stopStaleTick,
        lastSyncedAgo,
        buildStreamCallbacks,
        handleVisibilityChange,
        setContexts,
        cleanup,
    };
}
