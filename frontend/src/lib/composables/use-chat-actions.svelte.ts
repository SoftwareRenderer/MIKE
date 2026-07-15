import type { Provider, Thread, DiffData } from '$lib/types';
import { PI_SENTINEL_URL, isAgentProvider } from '$lib/types';
import type { StreamState } from '$lib/services/stream-state.svelte';
import { sendTaskMessage, createTaskMessage, deleteTaskMessagesAfter, regenerateTaskMessages, updateTaskMessage, stopTask, startTask, updateTask, moveTask, switchTaskThread, forkTaskThread, checkMessageDelete, commitAndDone, summarizeTask, compactTask, getOfflineStatus, getTask } from '$lib/api';
import { board as boardStore } from '$lib/stores/app';
import { showToast, showConfirm } from '$lib/stores/dialogs';
import { commitDialog as commitDialogStore } from '$lib/stores/dialogs';
import { findLastUserMessageBefore, trimEntriesForRegenerate } from '$lib/utils/chat-entries';

/** Dependencies the component/composables must provide. */
export interface ChatActionsDeps {
    projectId: string;
    taskId: string;
    stream: StreamState;
    taskStatus: string;
    taskPrompt: string;
    disabled: boolean;

    // Model state
    selectedModel: string;
    selectedUrl: string;
    selectedReasoningEffort: string;
    operatingMode: string;
    providers: Provider[];
    modelUpdatePending: boolean;

    // Thread state
    threads: Thread[];
    activeThreadId: string;

    // Streaming state
    isLoading: boolean;

    // Callbacks from other composables
    genId: () => string;
    scroll: (force?: boolean) => void;
    markStreaming: () => void;
    markNotStreaming: () => void;
    loadHistory: (scrollAfter: boolean) => Promise<void>;
    loadThreads: () => Promise<void>;
    updateActiveThread: (threadId: string) => void;
    updateThreadUrl: (threadId: string) => void;
    resetStreamState: () => void;
    mergeTaskIntoBoard: (updated: any) => void;

    // UI callbacks
    onRefreshDiff?: (data: DiffData) => void;
    onThreadSwitch?: () => void;
    onInvalidateDiff?: () => void;
    onMoveTask?: (status: string) => void;

    // Queue status
    commitQueueStatus: import('svelte/store').Writable<Record<string, { taskID: string; status: string }>>;
}

/**
 * Manages all user-initiated chat actions.
 *
 * Handles: send, regenerate, start, stop, commit, delete, edit/fork,
 * summarize, model selection, and operating mode toggle.
 *
 * All state variables (isStarting, isCommitting, isSummarizing, etc.)
 * are managed here to keep the component thin.
 */
export function useChatActions(deps: ChatActionsDeps) {
    let isStarting = $state(false);
    let isCommitting = $state(false);
    let isSummarizing = $state(false);
    let summarizeController: AbortController | null = null;
    let modelUpdatePending = $state(false);

    function isAgentSelected(): boolean {
        if (!deps.selectedUrl) return false;
        if (deps.selectedUrl === PI_SENTINEL_URL) return true;
        const p = deps.providers.find(p => p.url === deps.selectedUrl);
        return p ? isAgentProvider(p) : false;
    }

    /** Send a message via the API with tracking state. Returns false on failure. */
    async function sendWithTracking(msg: string, attachments?: Array<{ mimeType: string; data: string }>, regenerate = false, entryId?: string): Promise<boolean> {
        deps.stream.state.awaitingResponse = true;
        try {
            await sendTaskMessage(deps.projectId, deps.taskId, msg, deps.selectedModel, deps.selectedUrl, attachments, deps.selectedReasoningEffort || undefined, regenerate);
            return true;
        } catch (e) {
            const errorMsg = e instanceof Error ? e.message : 'Failed to send message';
            deps.stream.state.awaitingResponse = false;
            return false;
        }
    }

    async function handleSend(msg: string, attachments?: Array<{ mimeType: string; data: string }>) {
        if ((!msg && (!attachments || attachments.length === 0)) || deps.disabled) return;
        // If LLM is streaming, cancel the current response by clearing partial assistant entry.
        if (deps.isLoading) {
            deps.stream.state.entries = deps.stream.state.entries.filter(e => !e.isStreaming);
            if (deps.stream.state.currentAssistantId) {
                deps.stream.state.currentAssistantId = null;
            }
            deps.stream.state.activeToolCalls = new Map();
            deps.stream.state.processedToolIds = new Set();
            deps.markNotStreaming();
        }
        // If task is in Done, move it back to In Progress before sending
        if (deps.taskStatus === 'done') {
            try {
                const updated = await moveTask(deps.projectId, deps.taskId, 'in_progress');
                deps.mergeTaskIntoBoard(updated);
            } catch (e) {
                showToast(String(e));
                return;
            }
        }
        // Remove all error entries (cleared by next user action)
        deps.stream.state.entries = deps.stream.state.entries.filter(e => e.role !== 'error');
        // Add message optimistically with current thread context
        const entryId = deps.genId();
        const isOfflineNow = getOfflineStatus();
        deps.stream.state.entries = [...deps.stream.state.entries, {
            id: entryId, role: 'user', content: msg,
            attachments: attachments || undefined,
            threadId: deps.activeThreadId,
            isStreaming: false,
            createdAt: new Date().toISOString(),
            queued: isOfflineNow,
        }];
        // Send with model info for lazy-start
        if (!await sendWithTracking(msg, attachments, false, entryId)) {
            deps.stream.state.entries = deps.stream.state.entries.filter(
                e => !(e.role === 'user' && e.content === msg)
            );
            deps.stream.state.entries = [...deps.stream.state.entries, {
                id: deps.genId(), role: 'error', content: 'Failed to send message',
                isStreaming: false,
                error: { code: 'send_failed', message: 'Failed to send message' },
                createdAt: new Date().toISOString(),
            }];
            return;
        }
        deps.scroll();
    }

    async function handleRegenerate(entryId: string) {
        if (deps.isLoading || deps.disabled) return;
        const idx = deps.stream.state.entries.findIndex(e => e.id === entryId);
        if (idx === -1) return;
        // Find the last user message before this assistant message
        const lastUserIdx = findLastUserMessageBefore(deps.stream.state.entries, idx);
        if (lastUserIdx === -1) return;
        const lastUser = deps.stream.state.entries[lastUserIdx];
        // Regenerate from backend starting after the last user message
        try {
            await regenerateTaskMessages(deps.projectId, deps.taskId, lastUser.id, true);
        } catch (e) {
            console.error('Failed to regenerate messages:', e);
        }
        // Trim local entries: keep everything up to and including the last user message
        deps.stream.state.entries = trimEntriesForRegenerate(deps.stream.state.entries, idx);
        if (!await sendWithTracking(lastUser.content, undefined, true)) {
            showToast('Failed to regenerate');
            return;
        }
        deps.scroll();
    }

    async function handleStartTask() {
        if (isStarting || deps.disabled) return;
        // Ensure model is set on the task before starting
        if (deps.selectedModel || deps.selectedUrl) {
            try {
                await updateTask(deps.projectId, deps.taskId, {
                    model_name: deps.selectedModel,
                    llama_server_url: deps.selectedUrl,
                });
            } catch (e) {
                console.error('Failed to update task model before start:', e);
            }
        }
        // If the last message is already from the LLM, just update status without re-triggering
        const entries = deps.stream.state.entries;
        const lastEntry = entries[entries.length - 1];
        if (lastEntry && lastEntry.role === 'assistant') {
            try {
                const updated = await startTask(deps.projectId, deps.taskId);
                deps.mergeTaskIntoBoard(updated);
            } catch (e) {
                showToast(String(e), 'error');
            }
            return;
        }
        isStarting = true;
        deps.stream.state.awaitingResponse = true;
        deps.scroll();
        try {
            const updated = await startTask(deps.projectId, deps.taskId);
            deps.mergeTaskIntoBoard(updated);
        } catch (e) {
            showToast(String(e), 'error');
            deps.stream.state.awaitingResponse = false;
            return;
        }
        isStarting = false;
    }

    async function handleStop() {
        deps.markNotStreaming();
        deps.stream.state.awaitingResponse = false;
        // Save the streaming assistant ID before clearing
        const streamingId = deps.stream.state.currentAssistantId;
        deps.stream.closeCurrentAssistant();
        // Remove the streaming assistant entry and entries with pending tool calls.
        deps.stream.state.entries = deps.stream.state.entries.filter(
            (e: any) => e.id !== streamingId && !(e.toolCalls?.some((tc: any) => tc.isPending))
        );
        // Clear active tool calls (immediate UI update)
        deps.stream.state.activeToolCalls = new Map();
        deps.stream.state.processedToolIds = new Set();
        try {
            await stopTask(deps.projectId, deps.taskId);
        } catch (e) {
            console.error('Failed to stop task:', e);
        }
        // Reload history from DB to discard any unpersisted (streaming) entries
        await deps.loadHistory(false);
    }

    async function handleCommitAndDone() {
        if (isCommitting || deps.disabled) return;
        isCommitting = true;
        try {
            // Open dialog — it will stream the commit message from the backend
            const confirmedMessage = await new Promise<string | null>((resolve) => {
                (commitDialogStore as any).set({
                    streaming: true,
                    message: '',
                    confirmText: 'Commit and move to Done',
                    cancelText: 'Cancel',
                    projectId: deps.projectId,
                    taskId: deps.taskId,
                    _resolve: resolve,
                });
            });
            if (!confirmedMessage) return;
            const updated = await commitAndDone(deps.projectId, deps.taskId, confirmedMessage);
            deps.mergeTaskIntoBoard(updated);
            // Reload history to show any system messages from merge conflict resolution
            await deps.loadHistory(false);
        } catch (e) {
            showToast(String(e));
        } finally {
            isCommitting = false;
            // Clear queue status for this project
            deps.commitQueueStatus.update(map => {
                const copy = { ...map };
                delete copy[deps.projectId];
                return copy;
            });
        }
    }

    async function handleMoveToDone() {
        if (isCommitting || deps.disabled) return;
        isCommitting = true;
        try {
            const updated = await moveTask(deps.projectId, deps.taskId, 'done');
            deps.mergeTaskIntoBoard(updated);
            await deps.loadHistory(false);
        } catch (e) {
            showToast(String(e));
        } finally {
            isCommitting = false;
        }
    }

    /** Called when the user clicks the delete button on a message. */
    async function handleDeleteRequest(entryId: string, count: number) {
        let willDeleteBranch = false;
        let branchName = '';
        try {
            const info = await checkMessageDelete(deps.projectId, deps.taskId, entryId);
            willDeleteBranch = info.will_delete_branch;
            branchName = info.branch_name || '';
        } catch {
            // If check fails, proceed without branch warning
        }

        let message = `This will delete ${count} message${count > 1 ? 's' : ''}. This action cannot be undone.`;
        if (willDeleteBranch && branchName) {
            message += `\n\nThis will also delete the git branch "${branchName}".`;
        }

        const confirmed = await showConfirm({
            title: 'Delete Message',
            message,
            confirmText: 'Delete',
            cancelText: 'Cancel',
        });
        if (!confirmed) return;

        const idx = deps.stream.state.entries.findIndex(e => e.id === entryId);
        if (idx === -1) return;
        try {
            await deleteTaskMessagesAfter(deps.projectId, deps.taskId, entryId);
            // Discard deleted entries so loadHistory doesn't merge them back as non-DB entries.
            deps.stream.state.entries = deps.stream.state.entries.filter((_, i) => i < idx);
            // Reload from DB
            await deps.loadHistory(true);
            // Refresh threads
            await deps.loadThreads();
            const active = deps.threads.find(t => t.is_active);
            if (active && active.id !== deps.activeThreadId) {
                deps.updateActiveThread(active.id);
                deps.updateThreadUrl(active.id);
            }
            // Invalidate diff cache
            deps.onInvalidateDiff?.();
        } catch (e) {
            showToast(String(e));
        }
    }

    async function handleEditFork(entryId: string, _originalContent: string, newContent: string, attachments?: Array<{ mimeType: string; data: string }>) {
        const entry = deps.stream.state.entries.find(e => e.id === entryId);
        if (!entry) return;
        // Soft edit for backlog tasks
        if (deps.taskStatus === 'backlog') {
            const idx = deps.stream.state.entries.findIndex(e => e.id === entryId);
            if (idx !== -1) {
                deps.stream.state.entries[idx].content = newContent;
                deps.stream.state.entries[idx].createdAt = new Date().toISOString();
            }
            updateTaskMessage(deps.projectId, deps.taskId, entryId, newContent).catch(e => showToast(String(e)));
            updateTask(deps.projectId, deps.taskId, { prompt: newContent }).catch(e => showToast(String(e)));
            return;
        }
        if (deps.isLoading || deps.disabled) return;
        try {
            const fromThreadId = entry.threadId || deps.activeThreadId;
            const forked = await forkTaskThread(deps.projectId, deps.taskId, fromThreadId, entryId, newContent);
            await deps.loadThreads();
            deps.updateActiveThread(forked.id);
            deps.resetStreamState();
            await deps.loadHistory(true);
            deps.updateThreadUrl(forked.id);
            deps.onThreadSwitch?.();
            await sendWithTracking(newContent, attachments);
        } catch (e) {
            showToast(String(e));
        }
    }

    async function handleEditSend(entryId: string, _originalContent: string, newContent: string, attachments?: Array<{ mimeType: string; data: string }>) {
        const idx = deps.stream.state.entries.findIndex(e => e.id === entryId);
        if (idx === -1) return;
        if (!newContent.trim() && (!attachments || attachments.length === 0)) return;

        // Backlog: persist edit only (soft edit, no LLM trigger)
        if (deps.taskStatus === 'backlog') {
            deps.stream.state.entries = deps.stream.state.entries.map((e, i) =>
                i === idx ? { ...e, content: newContent, createdAt: new Date().toISOString() } : e
            );
            updateTaskMessage(deps.projectId, deps.taskId, entryId, newContent).catch(e => showToast(String(e)));
            updateTask(deps.projectId, deps.taskId, { prompt: newContent }).catch(e => showToast(String(e)));
            return;
        }

        if (deps.isLoading || deps.disabled) return;
        deps.stream.state.entries = deps.stream.state.entries.slice(0, idx);
        deps.stream.closeCurrentAssistant();
        try {
            await deleteTaskMessagesAfter(deps.projectId, deps.taskId, entryId);
            await deps.loadThreads();
            const active = deps.threads.find(t => t.is_active);
            if (active) {
                deps.updateActiveThread(active.id);
                deps.updateThreadUrl(active.id);
            }
            deps.onInvalidateDiff?.();
        } catch (e) {
            showToast(String(e));
            return;
        }
        const newMsg = await createTaskMessage(deps.projectId, deps.taskId, 'user', newContent);
        deps.stream.state.entries = [...deps.stream.state.entries, {
            id: newMsg.id, role: 'user', content: newContent, attachments,
            threadId: deps.activeThreadId, isStreaming: false,
            createdAt: newMsg.created_at || new Date().toISOString(),
        }];
        await sendWithTracking(newContent, attachments);
        deps.scroll();
    }

    async function handleModelSelect(providerName: string, modelName: string, providerUrl: string, reasoningEffort?: string) {
        // Note: caller updates selectedModel/selectedUrl/selectedReasoningEffort
        // before calling this — we just persist to the task.
        modelUpdatePending = true;
        try {
            await updateTask(deps.projectId, deps.taskId, {
                model_name: modelName,
                llama_server_url: providerUrl,
                reasoning_effort: reasoningEffort || undefined,
            });
        } catch (e) {
            console.error('Failed to update task model:', e);
        } finally {
            modelUpdatePending = false;
        }
    }

    function handleOperatingModeToggle(onUpdate: (mode: string) => void) {
        const modes = ['act', 'plan', 'ideate'];
        const prevMode = deps.operatingMode;
        const nextIdx = (modes.indexOf(deps.operatingMode) + 1) % modes.length;
        const newMode = modes[nextIdx];
        onUpdate(newMode);
        updateTask(deps.projectId, deps.taskId, { operating_mode: newMode }).catch((e) => {
            console.error('Failed to update operating mode:', e);
            onUpdate(prevMode);
        });
    }

    async function handleSummarize() {
        const confirmed = await showConfirm({
            title: 'Compress Context',
            message: 'This will create a summary of the conversation to reduce token usage. All messages are preserved — delete the summary entry to restore the full context.',
            confirmText: 'Compress',
            cancelText: 'Cancel',
            variant: 'default',
        });
        if (!confirmed) return;
        isSummarizing = true;
        try {
            if (isAgentSelected()) {
                const result = await compactTask(deps.projectId, deps.taskId);
                deps.stream.state.entries = [...deps.stream.state.entries, {
                    id: deps.genId(),
                    role: 'summarization',
                    content: result.summary || 'Context was summarized to free space',
                    isStreaming: false,
                    createdAt: new Date().toISOString(),
                    stats: { model_name: deps.selectedModel || 'Pi', prompt_tokens: 0, predicted_tokens: 0, prompt_ms: 0, predicted_ms: 0, duration: 0 },
                }];
            } else {
                summarizeController = new AbortController();
                const result = await summarizeTask(deps.projectId, deps.taskId, summarizeController.signal);
                console.log('[summarize] context summarized:', result.old_count, '->', result.new_count, 'messages');
            }
        } catch (e: any) {
            if (e?.name === 'AbortError') return;
            console.error('Failed to compress context:', e);
            showToast(e?.message || 'Failed to compress context', 'error');
        } finally {
            isSummarizing = false;
            summarizeController = null;
        }
    }

    async function handleSummarizeCancel() {
        const confirmed = await showConfirm({
            title: 'Cancel Summarization',
            message: 'Summarization is in progress. Cancel it?',
            confirmText: 'Cancel',
            cancelText: 'Keep Running',
            variant: 'default',
        });
        if (!confirmed) return;
        summarizeController?.abort();
    }

    // Thread switch/fork handlers (bridge between actions and history)
    async function handleThreadSwitch(threadId: string) {
        if (threadId === deps.activeThreadId) return;
        try {
            const switched = await switchTaskThread(deps.projectId, deps.taskId, threadId);
            await deps.loadThreads();
            deps.updateActiveThread(switched.id);
            deps.resetStreamState();
            await deps.loadHistory(true);
            deps.onThreadSwitch?.();
            deps.updateThreadUrl(threadId);
        } catch (e) {
            showToast(String(e));
        }
    }

    async function handleThreadFork(messageId: string, fromThreadId: string) {
        if (deps.isLoading || deps.disabled) return;
        try {
            const forked = await forkTaskThread(deps.projectId, deps.taskId, fromThreadId, messageId);
            await deps.loadThreads();
            deps.updateActiveThread(forked.id);
            deps.resetStreamState();
            await deps.loadHistory(true);
            deps.onThreadSwitch?.();
            deps.updateThreadUrl(forked.id);
        } catch (e) {
            showToast(String(e));
        }
    }

    return {
        isStarting,
        isCommitting,
        isSummarizing,
        modelUpdatePending,
        isAgentSelected,
        sendWithTracking,
        handleSend,
        handleRegenerate,
        handleStartTask,
        handleStop,
        handleCommitAndDone,
        handleMoveToDone,
        handleDeleteRequest,
        handleEditFork,
        handleEditSend,
        handleModelSelect,
        handleOperatingModeToggle,
        handleSummarize,
        handleSummarizeCancel,
        handleThreadSwitch,
        handleThreadFork,
    };
}
