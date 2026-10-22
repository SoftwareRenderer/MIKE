import type { ChatEntry, Thread } from '$lib/types';
import { t } from '$lib/i18n';
import { PENDING_SEND_ID_PREFIX } from './pending-sends';

/** Checkpoint state for a single entry.
 *
 * @knipignore A production file names it only in `EntryMeta`, which the chat list reads through. */
export interface CheckpointState {
    isPending: boolean;
    commitLabel?: string;
    commitHash?: string;
}

/** The three per-message facts a checkpoint band reads. They always travel together, and two of them are the
 * same `Map<string, string>` type, so naming them one by one invites a swap no type check would catch. */
type CheckpointSources = {
    pendingCheckpoints: Set<string>;
    checkpointLabels: Map<string, string>;
    checkpointCommitHashes: Map<string, string>;
};

/** Index of the last user message before `beforeIndex`, or -1 when there is none. */
export function findLastUserMessageBefore(entries: ChatEntry[], beforeIndex: number): number {
    for (let i = beforeIndex - 1; i >= 0; i--) {
        if (entries[i].role === 'user') return i;
    }
    return -1;
}

/** Cut the transcript back to the user message before the clicked entry; the echo of the re-send renames that row. */
export function trimEntriesForRegenerate(entries: ChatEntry[], entryIndex: number): ChatEntry[] {
    const lastUserIdx = findLastUserMessageBefore(entries, entryIndex);
    if (lastUserIdx === -1) return entries;
    return entries.slice(0, lastUserIdx + 1);
}

/** What submitting an edited message does. The button's label and the click both read this. */
export type EditSubmit = 'save' | 'send' | 'fork';

/** Where an edited message goes: a task in the backlog only saves, and in a started task only the thread's
 * last user message can be re-sent, so any other edit opens a new thread. `forcedSave` is the caller's own
 * in-place rewrite (summarized context, the mobile editor) and outranks the position. */
export function editSubmitAction(args: {
    isLastUserMessage: boolean;
    isFirstUserMessage?: boolean;
    taskStatus?: string;
    forcedSave?: boolean;
}): EditSubmit {
    if (args.forcedSave) return 'save';
    if (args.taskStatus === 'backlog') return 'save';
    if (!args.isFirstUserMessage && args.isLastUserMessage) return 'send';
    return 'fork';
}

/** The entry a `user_message` echo renames to its row id: the optimistic id while it is still on screen, else
 * the newest identical user message that is not queue-derived. An id that no longer resolves answers `null`
 * rather than renaming an older message that happens to share the words. */
export function takeEchoMatch(
    entries: ChatEntry[],
    echo: { entryId?: string | null; content: string },
): ChatEntry | null {
    if (echo.entryId) return entries.find(e => e.id === echo.entryId) ?? null;
    for (let i = entries.length - 1; i >= 0; i--) {
        const e = entries[i]!;
        if (e.role === 'user' && e.content === echo.content && !e.id.startsWith(PENDING_SEND_ID_PREFIX)) return e;
    }
    return null;
}

/** The notice a failed send leaves behind, named for its row so a re-derive does not draw a second copy. */
const FAILED_SEND_NOTICE_ID_PREFIX = 'send-failed:';

/** Drop a failed send's optimistic row by its id and append the notice. Matching on the text would remove
 * every message with those words, and an attachments-only send has no text to match. */
export function removeFailedSend(
    entries: ChatEntry[],
    args: { entryId: string; copy: { failed: string } },
): ChatEntry[] {
    const failed = args.copy.failed;
    return [...entries.filter(e => e.id !== args.entryId), {
        id: `${FAILED_SEND_NOTICE_ID_PREFIX}${args.entryId}`,
        role: 'error',
        content: failed,
        isStreaming: false,
        error: { code: 'send_failed', message: failed },
        createdAt: new Date().toISOString(),
        local: true,
    }];
}

/** What dismissing one entry means: `entries` is what to draw now, `deleteId` the row to ask the server for.
 * A row this browser wrote has no row to delete, and a queued row's send is still in the queue, so neither is
 * removed. */
export function removeDismissedError(
    entries: ChatEntry[],
    id: string,
): { entries: ChatEntry[]; deleteId: string | null } {
    const entry = entries.find(e => e.id === id);
    if (!entry) return { entries, deleteId: null };
    if (entry.local) return { entries: entries.filter(e => e.id !== id), deleteId: null };
    if (entry.queued) return { entries, deleteId: null };
    return { entries, deleteId: id };
}

/** Per-entry metadata for the chat list. */
export interface EntryMeta {
    cpState: CheckpointState | null;
    cpThread: Thread | null;
    isLastUserMessage: boolean;
    /** The thread's first row: editing it forks, and deleting it takes the thread. Only true while nothing
     * older is unloaded — the oldest loaded row of a mid-thread window is not it. */
    isFirstUserMessage: boolean;
}

/** The per-entry metadata the chat list renders, in one pass. `hasEarlier` is what makes
 * `isFirstUserMessage` mean the thread's first row rather than the window's. */
export function buildEntryMetas(args: {
    entries: ChatEntry[];
    pendingCheckpoints: Set<string>;
    checkpointLabels: Map<string, string>;
    checkpointCommitHashes: Map<string, string>;
    threads: Thread[];
    hasEarlier: boolean;
    lastUserMessageIds: Set<string>;
    streaming: boolean;
}): EntryMeta[] {
    const { entries, threads } = args;
    const cpStateMap = buildCheckpointStateMap(entries, args);
    const activeThread = threads.find(t => t.is_active);
    const metas: EntryMeta[] = new Array(entries.length);
    for (let i = 0; i < entries.length; i++) {
        const entry = entries[i]!;
        if (entry.role !== 'user') {
            metas[i] = { cpState: null, cpThread: null, isLastUserMessage: false, isFirstUserMessage: false };
            continue;
        }
        const cpState = cpStateMap.get(i) ?? null;
        const isFirstUserMessage = i === 0 && !args.hasEarlier;
        metas[i] = {
            cpState,
            cpThread: threads.length > 0 ? getCheckpointThreads(entry, threads, activeThread, isFirstUserMessage) : null,
            isLastUserMessage: args.lastUserMessageIds.has(entry.id) && !args.streaming,
            isFirstUserMessage,
        };
    }
    return metas;
}

/** The tool calls on screen still waiting for an answer. They are the evidence that this screen is behind the
 * task, so a rebuild of the transcript carries them over.
 */
export function pendingToolIds(entries: ChatEntry[]): Set<string> {
    const ids = new Set<string>();
    for (const entry of entries) {
        for (const tc of entry.toolCalls ?? []) {
            if (tc.isPending) ids.add(tc.toolId);
        }
    }
    return ids;
}

/**
 * A stored conversation row, as far as the Review predicate reads it.
 *
 * @knipignore A case stores a card, then removes or rewrites one payload column of it.
 */
export interface WindowRow {
    role: string;
    tool_calls?: string;
    tool_result?: string;
    /** Sub-classification within a role: an advisory row, not a turn doing work. */
    kind?: string;
}

/** A tool-call row the server has answered: no `tool_result` is a call still in flight, which is the same
 * reading `buildEntriesFromMessages` takes. Payload that will not read is not evidence of anything. */
function isAnsweredToolCall(row: WindowRow, toolName: string): boolean {
    if (row.role !== 'tool_call' || !row.tool_result) return false;
    try {
        return JSON.parse(String(row.tool_calls))?.tool_name === toolName;
    } catch {
        return false;
    }
}

/**
 * True when a conversation window ends on a `request_review` result — the shape of a turn that finished and
 * asked for its reader.
 *
 * This is what lets an open draw the rows this browser already holds while its newest-window read is still
 * out: a window that ends on a Review result holds nothing that can still move, so nothing is lost by
 * showing it a round trip early. Any other newest row means the opposite — a user row is a send nothing has
 * answered, a `tool_call` without its result is work in flight — and then the browser's copy stays in the
 * cache. Advisory rows (`kind`) are walked past: a diagnostics or merge-conflict row after the card is still
 * a screen that ended in Review.
 */
export function endsOnReviewResult(rows: WindowRow[]): boolean {
    for (let i = rows.length - 1; i >= 0; i--) {
        const row = rows[i]!;
        if (row.role === 'title' || row.kind) continue;
        return isAnsweredToolCall(row, 'request_review');
    }
    return false;
}

/** The user messages with nothing after them — the ones whose edit offers "Send" rather than "New Thread". */
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

/** Fold tool-call-only entries into the assistant entry before them, so a card sits under the text that called
 * it rather than in its own row.
 *
 * @knipignore The fold is asserted on entries; `buildEntriesFromMessages` folds and rebuilds in one pass.
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

/** The thread whose label a checkpoint shows: the nearest ancestor of the active thread forked at or before
 * the entry. With `requireOptions`, answer only when 2+ threads are relevant there.
 *
 * @knipignore Which threads a checkpoint can offer is asserted per entry and thread set.
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

/** Error event payload as it arrives on the WebSocket. */
export interface ErrorEventData {
    message_id?: string;
    code?: string;
    message?: string;
}

/** Normalize an error event payload for display. */
export function errorFromEvent(data: ErrorEventData): { code: string; message: string } {
    // `code` is a protocol value and stays as-is; only the missing-message fallback is copy.
    // Safe to translate: isRepeatError() compares two values this same function produced.
    return { code: data.code || 'error', message: data.message || t('chat.error.fallback') };
}

/**
 * True when the error of this event is already on screen: either the stored row
 * it belongs to is there (a replayed frame, or the copy loadHistory put there),
 * or the identical error is what the last card already shows.
 */
export function isRepeatError(entries: ChatEntry[], data: ErrorEventData): boolean {
    if (data.message_id && entries.some(e => e.id === data.message_id)) return true;
    const { code, message } = errorFromEvent(data);
    const last = entries[entries.length - 1];
    return last?.role === 'error' && last.error?.code === code && last.content === message;
}

/** Turn DB rows into chat entries, and collect each row's checkpoint hash. */
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
        severity?: string;
        kind?: string;
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

        if (m.role === 'error') {
            const content = m.content || '';
            const match = content.match(/^\[([^\]]+)\]\s*(.*)/);
            const code = match ? match[1] : 'error';
            const message = match ? match[2] : content;
            return {
                ...base, id: m.id, role: 'error' as const, content: message, isStreaming: false, createdAt: m.created_at,
                error: { code, message },
            };
        }

        if (m.role === 'tool_call') {
            // A fork that copied the role but lost the payload has nothing to render.
            if (!m.tool_calls) return null;
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
                    interrupted: result?.interrupted === true,
                    duration: result?.duration,
                    isPending,
                    startedAt,
                }],
            };
        }

        let parsedAttachments: Array<{ mimeType: string; data: string }> | undefined;
        if (m.attachments) {
            try {
                const atts = typeof m.attachments === 'string' ? JSON.parse(m.attachments) : m.attachments;
                if (Array.isArray(atts)) parsedAttachments = atts;
            } catch { /* ignore */ }
        }

        const entry: Partial<ChatEntry> = {
            ...base, id: m.id, role: m.role, content: m.content,
            severity: m.severity,
            kind: m.kind,
            reasoningContent: m.reasoning_content, hasReasoning: m.has_reasoning,
            attachments: parsedAttachments, isStreaming: false, createdAt: m.created_at,
        };

        if (m.role === 'merge_resolution') {
            // The probes below read the server's own row text — they must stay English.
            const isErr = m.content?.includes('Merge failed');
            (entry as ChatEntry).mergeLabel = isErr ? t('chat.merge.failed') : (m.content?.includes('conflict') ? t('chat.merge.conflictsResolved') : t('chat.merge.merged'));
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

    const validDbEntries = collapseRepeatErrors(dbEntries.filter(e => e !== null) as ChatEntry[]);
    const entries = mergeToolCallsIntoAssistant(validDbEntries);

    return { entries, checkpointHashes };
}

/**
 * Combine the DB rows of a task with the entries the browser already shows. The DB wins for every row it has,
 * and a live entry stays only when the DB provably lacks it: a tool_call event is sent once, and the rows carry
 * the identity, so an entry whose tool id already has a row is a duplicate.
 *
 * `taskIsRunning` decides whether a pending tool call is work or a leftover, and it must come from
 * `taskIsInFlight(execution_state)`, never from the task's status, which stays `in_progress` after a stop.
 */
export function mergeLoadedHistory(
    dbEntries: ChatEntry[],
    dbMessageIds: Iterable<string>,
    liveEntries: ChatEntry[],
    taskIsRunning: boolean,
): ChatEntry[] {
    // Hide a card a stopped task will never answer, not the message holding it: dropping the entry
    // would take the turn's text and finished cards too.
    const settled = taskIsRunning ? dbEntries : dbEntries.map(withoutStalePendingCards).filter((e): e is ChatEntry => e !== null);

    const dbIds = new Set(dbMessageIds);
    // Over dbEntries, not settled: a hidden card is still a row that describes the call, so a live
    // copy of it is a duplicate.
    const dbToolIds = new Set<string>();
    for (const e of dbEntries) {
        for (const tc of e.toolCalls ?? []) dbToolIds.add(tc.toolId);
    }
    const lastLive = liveEntries[liveEntries.length - 1];

    const merged: ChatEntry[] = [...settled];
    for (const e of liveEntries) {
        // The complete event renames a streaming entry to its row id.
        if (dbIds.has(e.id)) continue;

        const calls = e.toolCalls ?? [];
        if (calls.length > 0) {
            if (!taskIsRunning && calls.some(tc => tc.isPending)) continue;
            const missing = calls.filter(tc => !dbToolIds.has(tc.toolId));
            if (missing.length === calls.length) {
                merged.push(e);
                continue;
            }
            if (missing.length === 0) continue;
            // Join the DB's copy of the turn instead of opening a second one: that entry keeps the
            // row id, which the checkpoint divider and its "view changes" link are keyed on.
            const shared = new Set(calls.map(tc => tc.toolId));
            const host = merged.findIndex(x => x.toolCalls?.some(tc => shared.has(tc.toolId)));
            if (host === -1) {
                merged.push({ ...e, toolCalls: missing, content: '', reasoningContent: undefined, hasReasoning: false, stats: undefined });
            } else {
                merged[host] = { ...merged[host]!, toolCalls: [...merged[host]!.toolCalls!, ...missing] };
            }
            continue;
        }

        if (isRowCopy(e, dbEntries, e === lastLive)) continue;

        merged.push(e);
    }

    const seenIds = new Set<string>();
    return merged.filter(e => {
        if (seenIds.has(e.id)) return false;
        seenIds.add(e.id);
        return true;
    });
}

/** True when no stored row holds this entry, yet or ever: the turn streaming now, an unanswered call, a
 * queued send, a build still running, a notice this browser wrote. */
function noRowHolds(entry: ChatEntry): boolean {
    return !!entry.local || !!entry.isStreaming || !!entry.queued
        || !!entry.buildLog && entry.buildLog.done !== true
        || (entry.toolCalls?.some(tc => tc.isPending) ?? false);
}

/**
 * The tail of the transcript that survives a read which replaced the conversation: the run at its end that no
 * stored row holds, oldest first. The cut sits at the end of that content rather than its start, because a
 * merge can only append and an entry above the last row-backed one is older than a row the read proved exists.
 * `docs/reconnection.md → The tab that comes back`.
 */
export function entriesWithoutRows(entries: ChatEntry[]): ChatEntry[] {
    return entries.slice(entries.findLastIndex(e => !noRowHolds(e)) + 1);
}

/**
 * True when a live entry repeats the answer one of the read's rows holds, so that row's entry stands for it.
 * Only the newest streaming entry is exempt: no row holds the turn in flight yet.
 * `docs/reconnection.md → The reload meets the replay buffer`.
 */
function isRowCopy(entry: ChatEntry, rows: ChatEntry[], isNewestOnScreen: boolean): boolean {
    if (entry.role !== 'assistant' || !entry.content) return false;
    if (entry.isStreaming && isNewestOnScreen) return false;
    return rows.some(r => r.role === 'assistant' && !!r.content
        && (r.content === entry.content || r.content.startsWith(entry.content)));
}

/** The entry without the pending tool calls a stopped task will never resolve; null if nothing renders. */
function withoutStalePendingCards(entry: ChatEntry): ChatEntry | null {
    const pending = entry.toolCalls?.filter(tc => tc.isPending) ?? [];
    if (pending.length === 0) return entry;
    const settledCalls = entry.toolCalls!.filter(tc => !tc.isPending);
    const rendersAnything = settledCalls.length > 0 || !!entry.content || entry.hasReasoning || !!entry.reasoningContent
        || !!entry.attachments?.length || !!entry.buildLog;
    if (!rendersAnything) return null;
    return { ...entry, toolCalls: settledCalls.length > 0 ? settledCalls : undefined };
}

// Drop an error entry repeating the one right before it: old databases hold one copy of a failure per
// page view.
function collapseRepeatErrors(entries: ChatEntry[]): ChatEntry[] {
    return entries.filter((e, i) => {
        if (e.role !== 'error') return true;
        const prev = entries[i - 1];
        return !(prev && prev.role === 'error' && prev.error?.code === e.error?.code && prev.content === e.content);
    });
}

/** The state one turn left behind. A band is drawn for a turn only once its state was saved, or while it is
 * being saved: a bookmark over nothing to show is a lie about what the panel can answer. */
function checkpointStateFor(entry: ChatEntry, sources: CheckpointSources): CheckpointState | null {
    const commitHash = sources.checkpointCommitHashes.get(entry.id) || '';
    const isPending = sources.pendingCheckpoints.has(entry.id);
    if (!commitHash && !isPending) return null;
    return {
        isPending,
        commitLabel: sources.checkpointLabels.get(entry.id),
        commitHash: commitHash || undefined,
    };
}

/** The newest checkpoint of the turn that ends at `from`. The round that ends a turn usually saved nothing,
 * so the scan passes over those rounds instead of answering that the turn has none. */
function newestCheckpointOfTurn(entries: ChatEntry[], from: number, sources: CheckpointSources): CheckpointState | null {
    for (let i = from; i >= 0; i--) {
        const entry = entries[i]!;
        // The question that started the turn is where it ends.
        if (entry.role === 'user') return null;
        if (entry.role !== 'assistant') continue;
        const state = checkpointStateFor(entry, sources);
        if (state) return state;
    }
    return null;
}

/** The band above each user message: the state the work was in when that message arrived, never a later turn's.
 * A turn with no later message gets no band — its state is what the Changes panel already answers, so the only
 * bands a reader meets mark a state they have scrolled back past.
 *
 * @knipignore Which checkpoint a message shows the state before is asserted per entry order.
 */
export function buildCheckpointStateMap(
    entries: ChatEntry[],
    sources: CheckpointSources,
): Map<number, CheckpointState | null> {
    const result = new Map<number, CheckpointState | null>();
    for (let i = 0; i < entries.length; i++) {
        // Scan from the entry above: what this message's own turn leaves behind belongs to the next band.
        if (entries[i]!.role === 'user') result.set(i, newestCheckpointOfTurn(entries, i - 1, sources));
    }
    return result;
}

/**
 * Title for an advisory note — something the model had to see that the user never sent. Keyed on the
 * `kind` the executor stores, not on severity, which only picks how the card is drawn; a kind that is
 * unknown or missing gets the generic note.
 */
export function advisoryLabel(kind?: string): string {
    switch (kind) {
        case 'diagnostics': return t('chat.advisory.diagnostics');
        case 'merge_conflict': return t('chat.advisory.mergeConflict');
        case 'auto_answer': return t('chat.advisory.autoAnswer');
        case 'context_cut': return t('chat.advisory.contextCut');
        default: return t('chat.advisory.note');
    }
}

/**
 * Body of a system note whose wording the catalog owns: the server writes a `kind` plus English text, and
 * the kind picks the translated sentence. The kinds are the ones backend/exec/system_note.go lists and must
 * agree case for case; anything else shows the text stored on the row.
 */
export function systemNoteText(kind: string | undefined, fallback: string): string {
    switch (kind) {
        case 'memory_pressure': return t('chat.system.memoryPaused');
        case 'memory_resumed': return t('chat.system.memoryResumed');
        case 'loop_breaker': return t('chat.system.loopBreaker');
        default: return fallback;
    }
}
