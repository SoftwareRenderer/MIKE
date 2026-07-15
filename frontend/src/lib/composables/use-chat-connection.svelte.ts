import type { StreamEvent, StreamEventType } from '$lib/types';
import type { StreamState } from '$lib/services/stream-state.svelte';
import { createStreamHandler } from '$lib/services/stream-handler';
import { get } from 'svelte/store';
import { getAppStreamRef } from '$lib/stream-ref';
import { getStreamingContext } from '$lib/context';

/** Dependencies the component must provide to the connection composable. */
export interface ChatConnectionDeps {
    projectId: string;
    taskId: string;
    stream: StreamState;
    taskStatus: string;
    /** Called to load message history from DB. */
    loadHistory: (scrollAfter: boolean) => Promise<void>;
    /** Called to update the active thread ID in local state. */
    updateActiveThread: (threadId: string) => void;
    /** Called to update the URL with the active thread param. */
    updateThreadUrl: (threadId: string) => void;
    /** Called to scroll the chat to the bottom. */
    scroll: (force?: boolean) => void;
    /** Called to mark user input (scroll tracking). */
    markUserInput: () => void;
    /** Current array of threads (populated by history composable). */
    threads: Thread[];
    /** Current active thread ID (populated by history composable). */
    activeThreadId: string;
    /** Switch to a thread via API. */
    switchTaskThread: (projectId: string, taskId: string, threadId: string) => Promise<{ id: string }>;
    /** Get a single task from the API. */
    getTask: (projectId: string, taskId: string) => Promise<{ llama_server_url?: string; model_name?: string; reasoning_effort?: string; status?: string }>;
    /** Task event context store for receiving WS events. */
    taskEventContext: import('svelte/store').Readable<{ task_id: string; type: string; data: unknown } | null>;
}

/** Thread type — matches backend Thread model. */
interface Thread {
    id: string;
    name: string;
    is_active: boolean;
}

/**
 * Manages the WebSocket connection lifecycle and stream event handling for a task chat.
 *
 * Handles:
 * - connect / disconnect / reconnect state
 * - Stream event dispatch via createStreamHandler
 * - User message echo reconciliation
 * - Task event subscription (per-task WS)
 * - Visibility change reconciliation (stale streaming state)
 * - Keyboard input tracking
 *
 * Returns a setup() function that must be called from onMount.
 * The returned cleanup function must be returned from onMount.
 */
export function useChatConnection(deps: ChatConnectionDeps) {
    let connected = $state(false);
    let connecting = $state(true);
    let firstConnectDone = $state(false);
    let destroyed = $state(false);
    // Suppress buffer replay for non-running tasks to avoid flicker from stale entries.
    let suppressEvents = $state(true);
    $effect(() => { suppressEvents = deps.taskStatus !== 'in_progress'; });

    let lastSentMessage: string | null = $state(null);
    let lastSentEntryId: string | null = $state(null);
    let isRegenerating = $state(false);

    const streamingIds = getStreamingContext();

    function genId(): string {
        return Math.random().toString(36).slice(2, 10);
    }

    function markStreaming() {
        streamingIds.update(s => { const n = new Set(s); n.add(deps.taskId); return n; });
    }

    function markNotStreaming() {
        streamingIds.update(s => { const n = new Set(s); n.delete(deps.taskId); return n; });
        deps.stream.state.queuePosition = 0;
        deps.stream.state.queueEstimatedWait = 0;
        deps.stream.state.streamTokenCount = 0;
        deps.stream.state.contextState = { ...deps.stream.state.contextState, used: deps.stream.state.lastPromptTokens };
        deps.stream.state.promptProgress = 0;
    }

    // Stream event handler — delegated to service
    const _handleStreamEvent = createStreamHandler(deps.stream, {
        genId,
        scroll: deps.scroll,
        markStreaming,
        markNotStreaming,
        onRefreshDiff: () => {}, // handled by App-level WS
        onThreadSwitch: () => {}, // handled by stream handler internally
        loadHistory: deps.loadHistory,
        updateActiveThread: deps.updateActiveThread,
        updateThreadUrl: deps.updateThreadUrl,
        createTaskMessage: async () => {}, // no-op — messages created via API directly
    });

    /** Wrapper around stream handler with component-specific echo logic. */
    function handleEvent(event: StreamEvent) {
        // Handle user_message echo before delegating to stream handler
        if (event.type === 'user_message') {
            const data = event.data as { id?: string; role: string; content: string; thread_id?: string; attachments?: Array<{ mimeType: string; data: string }> };
            if (lastSentMessage === data.content) {
                lastSentMessage = null;
                lastSentEntryId = null;
                isRegenerating = false;
                if (data.id) {
                    // Match by optimistic entry ID when available (reliable),
                    // fall back to content match for backward compat (e.g. regenerate).
                    if (lastSentEntryId) {
                        const entry = deps.stream.state.entries.find(e => e.id === lastSentEntryId);
                        if (entry) {
                            entry.id = data.id;
                            if (data.thread_id) entry.threadId = data.thread_id;
                            if (data.attachments) entry.attachments = data.attachments;
                        }
                    } else {
                        for (let i = deps.stream.state.entries.length - 1; i >= 0; i--) {
                            if (deps.stream.state.entries[i].role === 'user' && deps.stream.state.entries[i].content === data.content) {
                                deps.stream.state.entries[i].id = data.id;
                                if (data.thread_id) deps.stream.state.entries[i].threadId = data.thread_id;
                                if (data.attachments) deps.stream.state.entries[i].attachments = data.attachments;
                                break;
                            }
                        }
                    }
                }
                return;
            }
        }
        // Handle error event side effects (isRegenerating, connected)
        if (event.type === 'error') {
            isRegenerating = false;
            connected = false;
        }
        _handleStreamEvent(event, suppressEvents, deps.projectId, deps.taskId);
    }

    function connect() {
        if (connected) return;
        connecting = true;
        connected = true;
        connecting = false;
        if (!firstConnectDone) {
            firstConnectDone = true;
            deps.stream.state.entries = deps.stream.state.entries.filter(
                (e: any) => !(e.role === 'error' && e.error?.code === 'ws_disconnected')
            );
        }
    }

    /** Dismiss a single error message without affecting other messages. */
    async function handleDismissError(messageId: string, deleteTaskMessage: (pid: string, tid: string, mid: string) => Promise<void>) {
        try {
            await deleteTaskMessage(deps.projectId, deps.taskId, messageId);
            deps.stream.state.entries = deps.stream.state.entries.filter(e => e.id !== messageId);
        } catch (e) {
            console.error('Failed to dismiss error:', e);
        }
    }

    /** Force immediate reconnection. */
    function handleRetryConnect() {
        connected = false;
        // Remove existing connection lost errors
        deps.stream.state.entries = deps.stream.state.entries.filter(
            (e: any) => !(e.role === 'error' && e.error?.code === 'ws_disconnected')
        );
        connect();
    }

    /**
     * Initialize the connection: subscribe to task events, load history, connect.
     * Must be called from onMount. Returns a cleanup function for onMount's return.
     */
    function setup(): () => void {
        const handleUrlChange = () => {
            // URL thread sync is handled by the component (reads URL, calls handleThreadSwitch)
        };
        window.addEventListener('urlchanged', handleUrlChange);

        const handleKeydown = (e: KeyboardEvent) => {
            if (['ArrowUp', 'ArrowDown', 'PageUp', 'PageDown', 'Home', 'End'].includes(e.key)) {
                deps.markUserInput();
            }
        };
        window.addEventListener('keydown', handleKeydown);

        // Async initialization
        (async () => {
            // 1. Set up task event handler FIRST (before subscribing to task).
            const unsubTaskEvent = deps.taskEventContext.subscribe((event) => {
                if (event && event.task_id === deps.taskId) {
                    handleEvent({ type: event.type as StreamEventType, data: event.data });
                }
            });

            // 2. Clear stale streaming state for non-running tasks.
            if (deps.taskStatus === 'review' || deps.taskStatus === 'done' || deps.taskStatus === 'backlog') {
                markNotStreaming();
            }

            // 3. Subscribe to this task directly (triggers buffer replay from server).
            const appStream = getAppStreamRef();
            if (appStream) {
                appStream.subscribeTask(deps.projectId, deps.taskId);
            }

            // 4. Load history from DB (caller ensures this is done before setup is called)
            // History loading is handled by the component before setup().

            // 5. Allow live events — buffer replay is done, DB state is loaded.
            suppressEvents = false;

            // 6. Connect
            connect();

            // 7. Scroll to bottom after history is loaded and DOM has rendered.
            deps.scroll();

            // 8. If task was started externally and is running with no assistant response yet,
            // show the "awaiting LLM" spinner until first token/tool_call.
            if (deps.taskStatus === 'in_progress') {
                const entries = deps.stream.state.entries;
                const lastEntry = entries[entries.length - 1];
                if (lastEntry?.role === 'user') {
                    deps.stream.state.awaitingResponse = true;
                }
            }
        })();

        // Reconcile stale streaming state on tab return.
        const handleTabVisibility = async () => {
            if (!document.hidden) {
                const streamingIdsSet = get(getStreamingContext());
                const isStreaming = streamingIdsSet.has(deps.taskId);
                if (isStreaming) {
                    try {
                        const task = await deps.getTask(deps.projectId, deps.taskId);
                        if (task && task.status !== 'in_progress') {
                            // Drop stale streaming entries
                            deps.stream.state.entries = deps.stream.state.entries.filter(
                                (e: any) => !e.isStreaming && !(e.toolCalls?.some((tc: any) => tc.isPending))
                            );
                            deps.stream.resetStreaming();
                            markNotStreaming();
                            await deps.loadHistory(false);
                        }
                    } catch {
                        // task deleted — ignore
                    }
                }
            }
        };
        window.addEventListener('visibilitychange', handleTabVisibility);

        return () => {
            window.removeEventListener('urlchanged', handleUrlChange);
            window.removeEventListener('keydown', handleKeydown);
            window.removeEventListener('visibilitychange', handleTabVisibility);
        };
    }

    return {
        connected,
        connecting,
        destroyed,
        isRegenerating,
        lastSentMessage,
        lastSentEntryId,
        handleEvent,
        connect,
        handleDismissError,
        handleRetryConnect,
        markStreaming,
        markNotStreaming,
        genId,
        setup,
    };
}
