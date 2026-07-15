import type { Thread } from '$lib/types';
import type { StreamState } from '$lib/services/stream-state.svelte';
import { getTaskMessages, getTaskThreads, getTask } from '$lib/api';
import { board as boardStore } from '$lib/stores/app';
import { buildEntriesFromMessages, buildCheckpointStateMap, computeLastUserMessageIds, getCheckpointThreads as getCheckpointThreadsUtil, type EntryMeta } from '$lib/utils/chat-entries';

/** Dependencies the component must provide. */
export interface ChatHistoryDeps {
    projectId: string;
    taskId: string;
    stream: StreamState;
    taskStatus: string;
    /** Thread switch callback (called by ThreadSelector, not by history itself). */
    onThreadSwitch?: () => void;
}

/**
 * Manages chat history and thread state.
 *
 * Handles:
 * - Loading message history from DB with merge/dedup
 * - Loading and tracking thread list
 * - Active thread state management
 * - Entry metadata computation (checkpoint state, fork threads, last-user flags)
 * - Thread URL sync helpers
 * - Stream state reset before thread switches
 *
 * Returns reactive state and functions for the component to use.
 */
export function useChatHistory(deps: ChatHistoryDeps) {
    let threads: Thread[] = $state([]);
    let activeThreadId = $state('');

    /** Read the `thread` URL param from the current URL. */
    function readThreadFromUrl(): string | null {
        return new URLSearchParams(location.search).get('thread');
    }

    /** Update the URL to include `&thread=<threadId>`, pushing to browser history. */
    function updateThreadUrl(threadId: string) {
        const url = new URL(location.href);
        url.searchParams.set('thread', threadId);
        history.pushState({ task: deps.taskId, thread: threadId }, '', url.toString());
    }

    /** Remove the `thread` param from the URL. */
    function clearThreadUrl() {
        const url = new URL(location.href);
        url.searchParams.delete('thread');
        history.pushState({ task: deps.taskId }, '', url.toString());
    }

    /** Reset stream state before loading a new thread's messages. */
    function resetStreamState() {
        deps.stream.closeCurrentAssistant();
        deps.stream.state.activeToolCalls = new Map();
        deps.stream.state.processedToolIds = new Set();
        deps.stream.state.entries = [];
        deps.stream.state.currentAssistantId = null;
        deps.stream.state.streamTokenCount = 0;
        deps.stream.state.awaitingResponse = false;
    }

    /** Merge an updated task into the board store. */
    function mergeTaskIntoBoard(updated: any) {
        boardStore.update(b => {
            if (!b) return b;
            const existing = b?.tasks.find(t => t.id === deps.taskId);
            const merged = (updated as any).queued ? { ...existing, ...updated } : updated;
            return { ...b, tasks: b.tasks.map(t => t.id === deps.taskId ? merged : t) };
        });
    }

    async function loadHistory(scrollAfter = false) {
        try {
            const messages = await getTaskMessages(deps.projectId, deps.taskId);
            // Build entries from DB messages using shared utility
            const { entries: mergedDbEntries, checkpointHashes } = buildEntriesFromMessages(messages);
            deps.stream.state.checkpointCommitHashes = checkpointHashes;

            // Merge with existing entries (e.g., from buffer replay).
            // DB entries have backend UUIDs; buffer entries have random genId() IDs.
            // Preserve buffer entries whose IDs are NOT in the DB (not yet persisted).
            const dbIds = new Set(messages.map(m => m.id));
            const existingNonDb = deps.stream.state.entries.filter(e => !dbIds.has(e.id));
            // Collect tool_ids from DB entries to deduplicate buffer entries.
            const dbToolIds = new Set<string>();
            for (const e of mergedDbEntries) {
                if ((e as any).toolCalls) {
                    for (const tc of (e as any).toolCalls) {
                        dbToolIds.add(tc.toolId);
                    }
                }
            }
            // Remove buffer entries whose tool calls are already in the DB.
            const dedupedNonDb = existingNonDb.filter((e: any) => {
                if (e.toolCalls) {
                    return !e.toolCalls.some((tc: any) => dbToolIds.has(tc.toolId));
                }
                return true;
            });
            deps.stream.state.entries = [...mergedDbEntries, ...dedupedNonDb];
            // Deduplicate by ID — keeps first occurrence (DB entries come first and are authoritative).
            const seenIds = new Set<string>();
            deps.stream.state.entries = deps.stream.state.entries.filter(e => {
                if (seenIds.has(e.id)) return false;
                seenIds.add(e.id);
                return true;
            });
            // Remove entries with pending tool calls from non-running tasks.
            if (deps.taskStatus !== 'in_progress') {
                deps.stream.state.entries = deps.stream.state.entries.filter(
                    (e: any) => !e.toolCalls?.some((tc: any) => tc.isPending)
                );
            }
            // Restore context usage from the last assistant message.
            const lastAssistant = [...deps.stream.state.entries].reverse().find(
                e => e.role === 'assistant' && e.stats
            );
            if (lastAssistant?.stats) {
                deps.stream.state.lastPromptTokens =
                    (lastAssistant.stats.prompt_tokens || 0) + (lastAssistant.stats.predicted_tokens || 0);
                deps.stream.state.contextState = { ...deps.stream.state.contextState, used: deps.stream.state.lastPromptTokens };
            }
            // Scroll to bottom on the next frame.
            if (scrollAfter) {
                await new Promise(resolve => requestAnimationFrame(resolve));
                // Scroll is handled by the component — we just ensure DOM is ready
            }
        } catch (e) {
            // Task may have been deleted or no history yet — ignore
            if (e instanceof Error && (e.message.includes('Not found') || e.message.includes('404'))) {
                // Task deleted — caller handles draft cleanup
            }
        }
    }

    async function loadThreads() {
        try {
            threads = await getTaskThreads(deps.projectId, deps.taskId);
            const active = threads.find(t => t.is_active);
            if (active) {
                activeThreadId = active.id;
            }
        } catch {
            threads = [];
        }
    }

    /** Update local thread state to reflect a new active thread (avoids refetching all threads). */
    function updateActiveThread(threadId: string) {
        activeThreadId = threadId;
        threads = threads.map(t => ({ ...t, is_active: t.id === threadId }));
    }

    // Precomputed per-entry metadata — O(n) single-pass via buildCheckpointStateMap.
    // Access via getter to ensure reactivity with stream entries.
    let _entryMetas: EntryMeta[] = $state([]);

    /**
     * Compute entry metadata from current stream state.
     * Call this after entries change (or use the $derived below).
     */
    function computeEntryMetas(
        entries: any[],
        isLoading: boolean,
        pendingCheckpoints: Set<string>,
        checkpointLabels: Map<string, string>,
        checkpointCommitHashes: Map<string, string>
    ): EntryMeta[] {
        const metas: EntryMeta[] = new Array(entries.length);
        const streaming = isLoading;
        const cpStateMap = buildCheckpointStateMap(entries, pendingCheckpoints, checkpointLabels, checkpointCommitHashes);
        const activeThread = threads.find(t => t.is_active);
        const lastUserIds = computeLastUserMessageIds(entries);
        for (let i = 0; i < entries.length; i++) {
            const entry = entries[i];
            metas[i] = entry.role === 'user'
                ? {
                    cpState: cpStateMap.get(i) ?? null,
                    cpThread: threads.length > 0
                        ? getCheckpointThreadsUtil(entry, threads, activeThread, i === 0)
                        : null,
                    isLastUserMessage: lastUserIds.has(entry.id) && !streaming,
                }
                : { cpState: null, cpThread: null, isLastUserMessage: false };
        }
        return metas;
    }

    return {
        threads,
        activeThreadId,
        readThreadFromUrl,
        updateThreadUrl,
        clearThreadUrl,
        resetStreamState,
        mergeTaskIntoBoard,
        loadHistory,
        loadThreads,
        updateActiveThread,
        computeEntryMetas,
    };
}
