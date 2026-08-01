import type { ChatEntry, Thread } from '$lib/types';

/** Checkpoint state for a single entry. */
interface CheckpointState {
    isPending: boolean;
    commitLabel?: string;
    commitHash?: string;
}

/**
 * Find the index of the last user message before the given entry index.
 * Returns -1 if no user message exists before that position.
 */
export function findLastUserMessageBefore(entries: ChatEntry[], beforeIndex: number): number {
    for (let i = beforeIndex - 1; i >= 0; i--) {
        if (entries[i].role === 'user') return i;
    }
    return -1;
}

/**
 * Trim entries for regeneration: keep everything up to and including the last
 * user message before the clicked entry. The WebSocket echo will update this
 * entry's ID to match the server-assigned one. All entries after the last user
 * message (the assistant response being regenerated plus anything after it) are removed.
 */
export function trimEntriesForRegenerate(entries: ChatEntry[], entryIndex: number): ChatEntry[] {
    const lastUserIdx = findLastUserMessageBefore(entries, entryIndex);
    if (lastUserIdx === -1) return entries;
    return entries.slice(0, lastUserIdx + 1);
}

/** Per-entry metadata for the chat list. */
export interface EntryMeta {
    cpState: CheckpointState | null;
    cpThread: Thread | null;
    checkpointIndex: number; // 0-based index among checkpoints (oldest first)
    isLastUserMessage: boolean;
}

/**
 * Compute the set of user message IDs that have no assistant/tool_call entry after them.
 * Used to determine whether to show "Send" or "New Thread" in the edit form.
 */
export function computeLastUserMessageIds(entries: ChatEntry[]): Set<string> {
    const ids = new Set<string>();
    let lastUserId: string | null = null;
    for (const e of entries) {
        if (e.role === 'user') {
            lastUserId = e.id;
        } else if (lastUserId && (e.toolCalls || e.role === 'assistant')) {
            lastUserId = null;
        }
    }
    if (lastUserId) ids.add(lastUserId);
    return ids;
}

/**
 * Merge tool-call-only entries into the preceding assistant entry.
 * DB ordering: assistant(text+reasoning) → tool_call → assistant(response to tool) → tool_call...
 * This ensures tool calls appear below the assistant's reasoning/text in the UI.
 */
export function mergeToolCallsIntoAssistant(allEntries: ChatEntry[]): ChatEntry[] {
    const merged: ChatEntry[] = [];
    for (const entry of allEntries) {
        if (entry.toolCalls && entry.toolCalls.length > 0 && !entry.content) {
            const prev = merged[merged.length - 1];
            if (prev && prev.role === 'assistant') {
                if (!prev.toolCalls) prev.toolCalls = [];
                prev.toolCalls.push(...entry.toolCalls);
            } else {
                merged.push(entry);
            }
        } else {
            merged.push(entry);
        }
    }
    return merged;
}

/**
 * Find the thread whose label should be shown at a given checkpoint.
 * Walks from the active thread up to root. Returns the most specific ancestor
 * whose fork point is at or before the entry time.
 * When requireOptions is true, only returns a thread if 2+ threads are
 * relevant at this checkpoint.
 */
export function getCheckpointThreads(
    entry: ChatEntry,
    threads: Thread[],
    activeThread: Thread | undefined,
    requireOptions = false,
): Thread | null {
    if (!threads.length || !activeThread) return null;
    const entryTime = entry.createdAt;
    if (!entryTime) return null;

    // Compare as Date values, not strings: checkpoint_message_time is stored at
    // second precision (RFC3339) while entry.createdAt is nanosecond precision,
    // so a lexicographic compare misjudges equal instants ("...36Z" > "...36.6Z").
    const threadRelevantAtEntry = (ct: string | undefined): boolean => {
        if (!ct) return true; // root thread (Main)
        if (ct.startsWith('0001-') || ct.startsWith('1970-')) return true; // zero/epoch
        return new Date(ct).getTime() <= new Date(entryTime).getTime();
    };

    const threadMap = new Map(threads.map(t => [t.id, t]));
    let current: Thread = activeThread;
    let checkpointThread: Thread | null = null;

    while (true) {
        if (threadRelevantAtEntry(current.checkpoint_message_time)) {
            checkpointThread = current;
            break;
        }
        const parent = threadMap.get(current.parent_thread_id || '');
        if (!parent) return null;
        current = parent;
    }

    if (requireOptions) {
        let relevantCount = 0;
        for (const t of threads) {
            if (threadRelevantAtEntry(t.checkpoint_message_time)) {
                relevantCount++;
            }
        }
        if (relevantCount < 2) return null;
    }

    return checkpointThread;
}

/**
 * Build checkpoint entries from DB messages.
 * Filters out title messages, restores error entries, merges tool calls,
 * and parses attachments.
 */
export function buildEntriesFromMessages(
    messages: Array<{
        id: string;
        role: string;
        content: string;
        thread_id?: string;
        created_at?: string;
        git_commit_hash?: string;
        tool_calls?: string;
        tool_result?: string;
        result_truncated?: boolean;
        attachments?: string | Array<{ mimeType: string; data: string }>;
        reasoning_content?: string;
        has_reasoning?: boolean;
        model_name?: string;
        prompt_tokens?: number;
        predicted_tokens?: number;
        prompt_ms?: number;
        predicted_ms?: number;
        duration?: number;
    }>,
): { entries: ChatEntry[]; checkpointHashes: Map<string, string> } {
    const checkpointHashes = new Map<string, string>();
    for (const m of messages) {
        if (m.git_commit_hash) {
            checkpointHashes.set(m.id, m.git_commit_hash);
        }
    }

    const dbEntries = messages.filter(m => m.role !== 'title').map(m => {
        const base = { threadId: m.thread_id || undefined };

        // Error entries
        if (m.role === 'error') {
            const content = m.content || '';
            const match = content.match(/^\[([^\]]+)\]\s*(.*)/);
            const code = match ? match[1] : 'error';
            const message = match ? match[2] : content;
            if (code === 'stopped') return null;
            return {
                ...base, id: m.id, role: 'error' as const, content: message, isStreaming: false, createdAt: m.created_at,
                error: { code, message },
            };
        }

        // Tool call entries — merged into preceding assistant entry
        if (m.role === 'tool_call' && m.tool_calls) {
            const tc = JSON.parse(m.tool_calls);
            const result = m.tool_result ? JSON.parse(m.tool_result) : null;
            const isPending = !result;
            // Set startedAt for pending tool calls so the elapsed timer works on page return.
            const startedAt = isPending && m.created_at ? new Date(m.created_at).getTime() : undefined;
            return {
                ...base, id: m.id, role: 'assistant' as const, content: '', isStreaming: false, createdAt: m.created_at,
                toolCalls: [{
                    toolId: tc.tool_id || m.id,
                    toolName: tc.tool_name,
                    displayName: tc.tool_display_name,
                    args: tc.args ?? {},
                    raw_args: tc.raw_args,
                    sandbox: tc.sandbox,
                    result: result?.output,
                    resultTruncated: m.result_truncated,
                    originalLines: result?.original_lines,
                    originalBytes: result?.original_bytes,
                    messageId: m.id,
                    success: result?.success ?? true,
                    duration: result?.duration,
                    isPending,
                    startedAt,
                }],
            };
        }

        // Parse attachments
        let parsedAttachments: Array<{ mimeType: string; data: string }> | undefined;
        if (m.attachments) {
            try {
                const atts = typeof m.attachments === 'string' ? JSON.parse(m.attachments) : m.attachments;
                if (Array.isArray(atts)) parsedAttachments = atts;
            } catch { /* ignore */ }
        }

        const entry: Partial<ChatEntry> = {
            ...base, id: m.id, role: m.role, content: m.content,
            reasoningContent: m.reasoning_content, hasReasoning: m.has_reasoning,
            attachments: parsedAttachments, isStreaming: false, createdAt: m.created_at,
        };

        if (m.role === 'merge_resolution') {
            const isErr = m.content?.includes('Merge failed');
            (entry as ChatEntry).mergeLabel = isErr ? 'Merge failed' : (m.content?.includes('conflict') ? 'Merge conflicts resolved' : 'Merged');
            (entry as ChatEntry).isError = isErr || false;
        }

        return {
            ...entry,
            stats: m.model_name ? {
                model_name: m.model_name,
                prompt_tokens: m.prompt_tokens ?? 0,
                predicted_tokens: m.predicted_tokens ?? 0,
                prompt_ms: m.prompt_ms ?? 0,
                predicted_ms: m.predicted_ms ?? 0,
                duration: m.duration ?? 0,
            } : undefined,
        };
    });

    const validDbEntries = dbEntries.filter(e => e !== null) as ChatEntry[];
    const entries = mergeToolCallsIntoAssistant(validDbEntries);

    return { entries, checkpointHashes };
}

/**
 * Build a map from user-entry index to checkpoint state in O(n) time.
 *
 * The checkpoint above a user message represents the repo state BEFORE that
 * message's work was applied. So prefer the last SAVED checkpoint (assistant with
 * a commit hash) that PRECEDES the user (backward scan, bounded by the previous
 * user). Prefer a hashed assistant over a tool-call-only one, since the "view
 * changes" button needs a commit hash. Only when nothing precedes it — e.g. the
 * very first message or a fork point — fall back to the NEXT qualifying assistant
 * (forward scan, stopping at the first assistant after the user).
 */
export function buildCheckpointStateMap(
    entries: ChatEntry[],
    pendingCheckpoints: Set<string>,
    checkpointLabels: Map<string, string>,
    checkpointCommitHashes: Map<string, string>,
): Map<number, CheckpointState | null> {
    const result = new Map<number, CheckpointState | null>();
    const n = entries.length;

    // Phase 1: Precompute forward scan targets.
    // For each user, find the index of the next assistant (any) in forward direction.
    // This is the entry the forward scan would stop at.
    const nextAssistant = new Int32Array(n).fill(-1);
    for (let i = n - 2; i >= 0; i--) {
        if (entries[i + 1].role === 'assistant') {
            nextAssistant[i] = i + 1;
        } else {
            nextAssistant[i] = nextAssistant[i + 1];
        }
    }

    // Phase 2: Single forward pass — resolve forward scan and track backward targets.
    let lastQualifyingAssistant: number | null = null;
    let lastHashAssistant: number | null = null;

    for (let i = 0; i < n; i++) {
        const entry = entries[i];

        if (entry.role === 'assistant') {
            const hasToolCalls = entry.toolCalls && entry.toolCalls.length > 0;
            const hasHash = checkpointCommitHashes.has(entry.id);
            if (hasToolCalls || hasHash) {
                lastQualifyingAssistant = i;
            }
            if (hasHash) {
                lastHashAssistant = i;
            }
        }

        if (entry.role === 'user') {
            // Backward scan: the state before this message is the last SAVED checkpoint
            // (assistant with a commit hash). Prefer it over a tool-call-only assistant,
            // which has no commit and would drop the "view changes" button.
            const bwdIdx = lastHashAssistant ?? lastQualifyingAssistant;
            const bwd = bwdIdx !== null ? entries[bwdIdx] : null;
            // Forward scan: next assistant after this user (stops at the first one).
            const fwdIdx = nextAssistant[i];
            const fwd = fwdIdx >= 0 ? entries[fwdIdx] : null;
            const fwdQualifying = fwd !== null && ((fwd.toolCalls && fwd.toolCalls.length > 0) || checkpointCommitHashes.has(fwd.id));

            const target = bwd ?? (fwdQualifying ? fwd : null);
            if (target !== null) {
                result.set(i, {
                    isPending: pendingCheckpoints.has(target.id),
                    commitLabel: checkpointLabels.get(target.id),
                    commitHash: checkpointCommitHashes.get(target.id) || undefined,
                });
            } else {
                result.set(i, null);
            }

            // Reset backward scan targets — bounded by this user entry.
            lastQualifyingAssistant = null;
            lastHashAssistant = null;
        }
    }

    return result;
}
