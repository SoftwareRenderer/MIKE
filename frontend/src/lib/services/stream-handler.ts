import type { StreamEvent, DiffData } from '$lib/types';
import type { ChatEntry } from '$lib/types';
import type { ToolCallInfo } from './stream-state.svelte';
import { StreamState } from './stream-state.svelte';
import { resolveLiveToolCall, type LiveToolCall } from '$lib/utils/tool-call-lookup';
import { errorFromEvent, isRepeatError, type ErrorEventData } from '$lib/utils/chat-entries';
import { t } from '$lib/i18n';

/** Update context usage from accumulated token counts. */
function updateContextUsed(s: typeof StreamState.prototype.state) {
    s.contextState = { ...s.contextState, used: s.lastPromptTokens + s.streamTokenCount };
}

/**
 * Re-point the `activeToolCalls` ref at a resolved live tool call. Skipped when the
 * owning entry is unchanged, so the common case doesn't churn the map components
 * derive from.
 */
function pointToolCallRef(s: typeof StreamState.prototype.state, toolId: string, live: LiveToolCall) {
    const ref = s.activeToolCalls.get(toolId);
    if (ref && ref.entryId === live.entry.id) return;
    const next = new Map(s.activeToolCalls);
    next.set(toolId, { entryId: live.entry.id, toolCall: live.tc });
    s.activeToolCalls = next;
}

/** External callbacks the component must provide. */
export interface StreamHandlerCallbacks {
    genId: () => string;
    scroll: (force?: boolean) => void;
    markStreaming: () => void;
    markNotStreaming: () => void;
    onRefreshDiff: (data: DiffData) => void;
    onThreadSwitch: () => void;
    loadHistory: (scrollAfter: boolean) => void;
    updateActiveThread: (threadId: string) => void;
    updateThreadUrl: (threadId: string) => void;
    /** Reports a failure the client has not shown yet — never called for a replayed frame. */
    onNewError?: () => void;
}

/**
 * Factory function that creates a stream event handler.
 * Plain TypeScript — no Svelte runes, unit-testable.
 *
 * Usage:
 *   const stream = new StreamState();
 *   const handleEvent = createStreamHandler(stream, { genId, scroll, ... });
 *   // In WebSocket onmessage: handleEvent(event);
 */
export function createStreamHandler(stream: StreamState, cb: StreamHandlerCallbacks) {
    const { genId, scroll, markStreaming, markNotStreaming, onRefreshDiff, onThreadSwitch, loadHistory, updateActiveThread, updateThreadUrl, onNewError } = cb;
    const s = stream.state;

    // rAF batching for scroll — coalesces rapid calls during token bursts
    let scrollRaf: number | null = null;
    function scheduleScroll() {
        // rAF is paused in a hidden tab, so a latch set there fires one stale scroll on return; the
        // follow tracker re-pins then.
        if (scrollRaf || document.hidden) return;
        scrollRaf = requestAnimationFrame(() => {
            scrollRaf = null;
            scroll();
        });
    }

    function ensureAssistantEntry(): ChatEntry {
        return stream.ensureAssistantEntry(genId);
    }

    // Max lines kept in a build/pull progress box — bounds memory for long builds.
    const MAX_BUILD_LOG_LINES = 200;

    /**
     * Returns the active (in-progress) build/pull progress box entry, creating one
     * if needed. A completed box (done === true) is never reused — a later build
     * opens a fresh box.
     */
    function ensureBuildLogEntry(title: string): ChatEntry {
        const active = [...s.entries].reverse().find(e => e.buildLog && !e.buildLog.done);
        if (active) return active;
        const entry: ChatEntry = {
            id: genId(), role: 'system', content: title, isStreaming: false,
            createdAt: new Date().toISOString(),
            buildLog: { title, lines: [] },
        };
        s.entries = [...s.entries, entry];
        // Re-read through the reactive proxy so in-place mutations (lines push,
        // done flag) notify subscribers — the raw `entry` object is not proxied.
        // Scroll the chat to reveal the newly-appeared box (line-by-line growth
        // is handled inside the box itself, so we don't fight the user's scroll).
        scheduleScroll();
        return s.entries[s.entries.length - 1]!;
    }

    return function handleEvent(event: StreamEvent, suppressEvents: boolean) {
        switch (event.type) {
            case 'token': {
                if (suppressEvents) break;
                stream.clearAwaiting();
                markStreaming();
                if (!s.currentAssistantId) s.streamTokenCount = 0;
                const data = event.data as { text: string };
                s.streamTokenCount += Math.ceil(data.text.length / 4);
                updateContextUsed(s);
                const entry = ensureAssistantEntry();
                if (!s.currentAssistantId) s.currentAssistantId = entry.id;
                entry.content += data.text;
                scheduleScroll();
                break;
            }
            case 'reasoning': {
                if (suppressEvents) break;
                stream.clearAwaiting();
                markStreaming();
                if (!s.currentAssistantId) s.streamTokenCount = 0;
                const data = event.data as { text: string };
                s.streamTokenCount += Math.ceil(data.text.length / 4);
                updateContextUsed(s);
                const entry = ensureAssistantEntry();
                entry.reasoningContent = (entry.reasoningContent || '') + data.text;
                entry.hasReasoning = true;
                scheduleScroll();
                break;
            }
            case 'tool_call': {
                if (suppressEvents) break;
                stream.clearAwaiting();
                const data = event.data as { tool_id: string; tool_name: string; tool_display_name?: string; args: Record<string, unknown>; raw_args?: string; raw_args_delta?: string; sandbox?: boolean; path?: string; lines_added?: number; lines_removed?: number };
                if (!data.tool_id || data.tool_id === '') break;

                // A replay after a history reload, or an auto-answer re-broadcast, can
                // target a tool call that no longer lives in the current assistant entry
                // — update it in place instead of duplicating it.
                const existing = resolveLiveToolCall(s.entries, s.activeToolCalls, data.tool_id);
                if (existing) {
                    if (!existing.tc.isPending && existing.tc.result !== undefined) break;
                    // Mutate the live (proxied) tool call so subscribers are notified.
                    existing.tc.toolName = data.tool_name;
                    existing.tc.displayName = data.tool_display_name;
                    existing.tc.args = data.args || {};
                    const newRawArgs = data.raw_args_delta ? (existing.tc.raw_args || '') + data.raw_args_delta : data.raw_args;
                    existing.tc.raw_args = newRawArgs;
                    existing.tc.sandbox = data.sandbox;
                    // A frame that names no file says nothing about which file this is — the one
                    // already known came from the arguments as they streamed.
                    if (data.path) existing.tc.path = data.path;
                    if (data.lines_added !== undefined) existing.tc.linesAdded = data.lines_added;
                    if (data.lines_removed !== undefined) existing.tc.linesRemoved = data.lines_removed;
                    pointToolCallRef(s, data.tool_id, existing);
                    scheduleScroll();
                    break;
                }
                if (s.processedToolIds.has(data.tool_id)) break;

                let entry = s.currentAssistantId ? s.entries.find(e => e.id === s.currentAssistantId) || null : null;
                if (!entry) entry = ensureAssistantEntry();

                if (entry.content.trim() === data.tool_name && !entry.reasoningContent) entry.content = '';
                if (!entry.toolCalls) entry.toolCalls = [];

                s.processedToolIds = new Set([...s.processedToolIds, data.tool_id]);
                s.unresolvedToolIds = new Set([...s.unresolvedToolIds, data.tool_id]);
                const ci = entry.content.length;
                const ri = (entry.reasoningContent || '').length;
                const newTc = {
                    toolId: data.tool_id, toolName: data.tool_name, displayName: data.tool_display_name,
                    args: data.args || {}, raw_args: data.raw_args, sandbox: data.sandbox, path: data.path,
                    linesAdded: data.lines_added, linesRemoved: data.lines_removed,
                    isPending: true, startedAt: Date.now(), contentIndex: ci, reasoningIndex: ri,
                };
                entry.toolCalls = [...(entry.toolCalls || []), newTc];
                entry.isStreaming = true;
                const newActive = new Map(s.activeToolCalls);
                newActive.set(data.tool_id, { entryId: entry.id, toolCall: newTc });
                s.activeToolCalls = newActive;
                scheduleScroll();
                break;
            }
            case 'tool_result': {
                const data = event.data as { tool_id: string; success: boolean; output: string; duration: number; args?: Record<string, unknown>; raw_args?: string; sandbox?: boolean; path?: string; result_truncated?: boolean; original_lines?: number; original_bytes?: number; streamed?: boolean; message_id?: string };
                // Resolve the live tool call: the ref's entryId may have been invalidated
                // by a history reload, and then the result would be dropped with the card
                // left "Running" forever.
                const found = resolveLiveToolCall(s.entries, s.activeToolCalls, data.tool_id);
                const ref = s.activeToolCalls.get(data.tool_id);
                // The ref may point at a detached copy that already received streamed
                // output — keep it rather than the short preview.
                if (found && ref && ref.toolCall.result && !found.tc.result) {
                    found.tc.result = ref.toolCall.result;
                }
                const newActive = new Map(s.activeToolCalls);
                newActive.delete(data.tool_id);
                s.activeToolCalls = newActive;
                const tc: ToolCallInfo | undefined = found?.tc;
                if (tc) {
                    // Keep full streamed content over the preview when present.
                    if (!(data.streamed && tc.result)) {
                        tc.result = data.output;
                    }
                    tc.success = data.success;
                    tc.duration = data.duration;
                    tc.isPending = false;
                    if (data.args) tc.args = data.args;
                    if (data.raw_args) tc.raw_args = data.raw_args;
                    if (data.sandbox !== undefined) tc.sandbox = data.sandbox;
                    if (data.path !== undefined) tc.path = data.path;
                    if (data.result_truncated !== undefined) tc.resultTruncated = data.result_truncated;
                    if (data.original_lines !== undefined) tc.originalLines = data.original_lines;
                    if (data.original_bytes !== undefined) tc.originalBytes = data.original_bytes;
                    if (data.message_id) tc.messageId = data.message_id;
                }
                // An answer with no card to land on means the announcement was lost with it, and
                // only a rebuild from the database can put the card back.
                const owed = !found;
                if (s.unresolvedToolIds.has(data.tool_id) !== owed) {
                    const nextOwed = new Set(s.unresolvedToolIds);
                    if (owed) nextOwed.add(data.tool_id);
                    else nextOwed.delete(data.tool_id);
                    s.unresolvedToolIds = nextOwed;
                }
                if (s.activeToolCalls.size === 0) {
                    stream.closeCurrentAssistant();
                    s.awaitingResponse = true;
                }
                scheduleScroll();
                break;
            }
            case 'tool_progress': {
                const data = event.data as { tool_id?: string; bytes_written?: number; lines_added?: number; lines_removed?: number; path?: string };
                // Progress lands on the live tool call — a ref captured before a history
                // reload points at a detached copy and would swallow it.
                const applyProgress = (toolId: string, fallback: ToolCallInfo | undefined) => {
                    const tc = resolveLiveToolCall(s.entries, s.activeToolCalls, toolId)?.tc ?? fallback;
                    if (!tc) return;
                    if (data.bytes_written !== undefined) tc.bytesWritten = data.bytes_written;
                    if (data.lines_added !== undefined) tc.linesAdded = data.lines_added;
                    if (data.lines_removed !== undefined) tc.linesRemoved = data.lines_removed;
                    if (data.path) tc.path = data.path;
                };
                if (data.tool_id) {
                    applyProgress(data.tool_id, s.activeToolCalls.get(data.tool_id)?.toolCall);
                } else {
                    // Broadcast: update all active tool calls
                    for (const [toolId, ref] of s.activeToolCalls) applyProgress(toolId, ref.toolCall);
                }
                break;
            }
            case 'tool_output': {
                const data = event.data as { tool_id: string; text: string };
                // Stream into the live tool call: after a history reload the ref from
                // creation time is a detached copy, and the visible card froze.
                const found = resolveLiveToolCall(s.entries, s.activeToolCalls, data.tool_id);
                if (found) {
                    found.tc.result = (found.tc.result || '') + data.text;
                    pointToolCallRef(s, data.tool_id, found);
                }
                scheduleScroll();
                break;
            }
            case 'user_message': {
                const data = event.data as { id?: string; role: string; content: string; thread_id?: string; severity?: string; attachments?: Array<{ mimeType: string; data: string }> };
                // Echo handling is done in component (lastSentMessage check)
                if (data.id && s.entries.some(e => e.id === data.id)) break;
                stream.closeCurrentAssistant();
                s.entries = [...s.entries, { id: data.id || genId(), role: data.role, severity: data.severity, content: data.content, threadId: data.thread_id, isStreaming: false, createdAt: new Date().toISOString() }];
                break;
            }
            case 'complete': {
                markNotStreaming();
                s.awaitingResponse = false;
                stream.clearRetry();
                const data = event.data as { state: string; model_name: string; prompt_tokens: number; predicted_tokens: number; prompt_ms: number; predicted_ms: number; duration: number; assistant_message_id?: string };
                if (data.assistant_message_id && s.currentAssistantId) {
                    const entry = s.entries.find(e => e.id === s.currentAssistantId);
                    if (entry && entry.id !== data.assistant_message_id) {
                        if (s.entries.some(e => e.id === data.assistant_message_id)) {
                            s.entries = s.entries.filter(e => e.id !== s.currentAssistantId);
                        } else {
                            entry.id = data.assistant_message_id;
                        }
                    }
                }
                stream.closeCurrentAssistant();
                for (const e of s.entries) {
                    if (e.role === 'assistant' && e.isStreaming) e.isStreaming = false;
                }
                if (data.prompt_tokens !== undefined) {
                    s.lastPromptTokens = data.prompt_tokens + (data.predicted_tokens || 0);
                    s.contextState = { ...s.contextState, used: s.lastPromptTokens };
                }
                const lastAssistant = [...s.entries].reverse().find(e => e.role === 'assistant' && !e.toolCalls);
                if (lastAssistant && data.predicted_tokens !== undefined) {
                    lastAssistant.stats = { model_name: data.model_name, prompt_tokens: data.prompt_tokens, predicted_tokens: data.predicted_tokens, prompt_ms: data.prompt_ms, predicted_ms: data.predicted_ms, duration: data.duration };
                }
                // Non-forced scroll: pin to the bottom only if the user is already
                // there — a user who scrolled up to read keeps their position.
                scroll();
                break;
            }
            case 'error': {
                const data = event.data as ErrorEventData;
                // The server stores the error before sending it and names that row in the
                // event, so a frame that was already drawn must not be drawn a second time.
                if (isRepeatError(s.entries, data)) break;
                onNewError?.();
                stream.clearAwaiting();
                markNotStreaming();
                stream.closeCurrentAssistant();
                const { code, message } = errorFromEvent(data);
                // Sandbox setup failures (e.g. Dockerfile build) leave the progress box
                // open — mark it done so the spinner stops and the error card stands out.
                if (code === 'setup_failed') {
                    const buildEntry = s.entries.find(e => e.buildLog && !e.buildLog.done);
                    if (buildEntry && buildEntry.buildLog) buildEntry.buildLog.done = true;
                }
                const entry: ChatEntry = { id: data.message_id || genId(), role: 'error', content: message, isStreaming: false, error: { code, message }, createdAt: new Date().toISOString() };
                s.entries = [...s.entries, entry];
                break;
            }
            case 'status': {
                const data = event.data as { state: string; activity?: string };
                if (data.state === 'pulling_image') {
                    // `activity` is the server's own status line: the 'Pulling ' prefix it is
                    // stripped with is a protocol string, not copy, and stays English.
                    const imgName = data.activity?.replace('Pulling ', '') || t('chat.stream.sandboxImage');
                    ensureBuildLogEntry(t('chat.stream.pullingImage', { name: imgName }));
                } else if (data.state === 'building_image') {
                    ensureBuildLogEntry(t('chat.stream.buildingImage'));
                } else if (data.state === 'starting_container') {
                    s.entries = [...s.entries, { id: genId(), role: 'system', content: t('chat.stream.startingContainer'), isStreaming: false, createdAt: new Date().toISOString() }];
                } else if (data.state === 'running') {
                    const wasInFlight = s.currentAssistantId !== null || s.activeToolCalls.size > 0;
                    // The sandbox container started — any build/pull box is complete.
                    const buildEntry = s.entries.find(e => e.buildLog && !e.buildLog.done);
                    if (buildEntry && buildEntry.buildLog) buildEntry.buildLog.done = true;
                    if (s.currentAssistantId) {
                        const entry = s.entries.find(e => e.id === s.currentAssistantId);
                        if (!entry || (!entry.content && !entry.reasoningContent)) stream.closeCurrentAssistant();
                    }
                    s.activeToolCalls = new Map();
                    // An entry that is only an unanswered call belongs to the run this frame ends, so it
                    // goes — and its ids leave `processedToolIds` with it, or that set swallows the call's
                    // next announcement. `docs/reconnection.md`.
                    const discarded: string[] = [];
                    const kept = s.entries.filter(e => {
                        const pending = e.toolCalls?.filter((tc: ToolCallInfo) => tc.isPending) ?? [];
                        if (pending.length === 0 || e.content || e.reasoningContent || pending.some(tc => !!tc.messageId)) return true;
                        discarded.push(...pending.map(tc => tc.toolId));
                        return false;
                    });
                    s.entries = kept;
                    if (discarded.length > 0) {
                        const gone = new Set(discarded);
                        s.processedToolIds = new Set([...s.processedToolIds].filter(id => !gone.has(id)));
                    }
                    s.queuePosition = 0;
                    s.queueEstimatedWait = 0;
                    markStreaming();
                    // Executor (re)started
                    if (!wasInFlight) {
                        s.awaitingResponse = true;
                    }
                } else if (data.state === 'cancelled') {
                    stream.closeCurrentAssistant();
                    s.activeToolCalls = new Map();
                    s.processedToolIds = new Set();
                    s.entries = s.entries.filter(e => !e.isStreaming);
                    markNotStreaming();
                    s.awaitingResponse = false;
                    stream.clearRetry();
                } else if (['stopped', 'failed', 'idle', 'completed', 'paused'].includes(data.state)) {
                    markNotStreaming();
                    s.awaitingResponse = false;
                    stream.clearRetry();
                }
                break;
            }
            case 'build_progress': {
                const data = event.data as { line: string };
                if (data.line) {
                    const entry = ensureBuildLogEntry(t('chat.stream.buildingImage'));
                    const log = entry.buildLog!;
                    log.lines.push(data.line);
                    if (log.lines.length > MAX_BUILD_LOG_LINES) {
                        log.lines.splice(0, log.lines.length - MAX_BUILD_LOG_LINES);
                    }
                }
                break;
            }
            case 'hint': {
                const data = event.data as { id?: string; role?: string; content: string; kind?: string; severity?: string; error?: string; created_at: string };
                if (data.content) {
                    // Dedupe replayed hint events (reconnect buffer replay).
                    if (data.id && s.entries.some(e => e.id === data.id)) break;
                    if (data.kind === 'retry') {
                        // The dead call is retried with the same messages, so what it streamed has no
                        // row behind it and the retry streams the answer over it.
                        stream.dropAbandonedTurn();
                        s.retryCount = 1;
                        s.retryError = data.error || '';
                        s.promptProgress = 0;
                        s.awaitingResponse = true;
                    } else {
                        stream.closeCurrentAssistant();
                    }
                    // Use the backend message id so loadHistory() dedupes against the DB entry.
                    // Role from the backend; legacy frames without a role default to "user".
                    // Without `severity` and `kind` a live note is drawn differently from the reloaded row.
                    s.entries = [...s.entries, { id: data.id || genId(), role: data.role || 'user', content: data.content, kind: data.kind, severity: data.severity, isStreaming: false, createdAt: data.created_at || new Date().toISOString() }];
                }
                break;
            }
            case 'stats': {
                const data = event.data as { model_name: string; prompt_tokens: number; predicted_tokens: number; predicted_ms: number; context_used?: number; context_limit?: number; context_percent?: number; cost?: number };
                if (data.prompt_tokens !== undefined && data.prompt_tokens > 0) s.lastPromptTokens = data.prompt_tokens;
                if (data.cost !== undefined && !isNaN(data.cost)) s.sessionCost = data.cost;
                if (data.predicted_tokens !== s.streamTokenCount) {
                    s.streamTokenCount = data.predicted_tokens;
                }
                // Pi mode: use direct context usage from get_session_stats
                if (data.context_used !== undefined) {
                    s.contextState = { ...s.contextState, used: data.context_used };
                    if (data.context_limit !== undefined) {
                        s.contextState = { ...s.contextState, limit: data.context_limit };
                    }
                } else {
                    updateContextUsed(s);
                }
                if (s.currentAssistantId) {
                    const entry = s.entries.find(e => e.id === s.currentAssistantId);
                    if (entry) {
                        entry.stats = { model_name: data.model_name, prompt_tokens: data.prompt_tokens ?? entry.stats?.prompt_tokens ?? 0, predicted_tokens: data.predicted_tokens, prompt_ms: entry.stats?.prompt_ms ?? 0, predicted_ms: data.predicted_ms, duration: entry.stats?.duration ?? 0 };
                    }
                }
                break;
            }
            case 'diff_changed': {
                onRefreshDiff(event.data as DiffData);
                break;
            }
            case 'thread_switch': {
                const data = event.data as { thread_id: string; task_id: string };
                updateActiveThread(data.thread_id);
                updateThreadUrl(data.thread_id);
                onThreadSwitch();
                stream.resetStreaming();
                s.entries = [];
                s.streamTokenCount = 0;
                loadHistory(true);
                break;
            }
            case 'queue': {
                const data = event.data as { position: number; estimated_wait_seconds: number };
                s.queuePosition = data.position || 0;
                s.queueEstimatedWait = data.estimated_wait_seconds || 0;
                if (s.queuePosition > 0) s.awaitingResponse = true;
                break;
            }
            case 'lock_wait': {
                // Repeated while the wait lasts, acquired=true at the end: a lost frame costs one repeat
                // instead of a spinner that never stops.
                const data = event.data as { domain?: string; waited_seconds?: number; acquired?: boolean; phase?: string };
                s.lockWait = data.acquired
                    ? null
                    : { domain: data.domain || 'other', seconds: data.waited_seconds || 0, phase: data.phase };
                break;
            }
            case 'prompt_progress': {
                const data = event.data as { total: number; cache: number; processed: number; time_ms: number };
                if (data.total > 0) s.promptProgress = Math.round((data.processed / data.total) * 100);
                break;
            }
            case 'advisory': {
                // An environment note (LSP diagnostics, merge-conflict instructions): the
                // model had to see it, the user never said it. Rendered as a note, never as
                // a user bubble. `kind` is what the card is titled with; `severity` only
                // decides whether it is drawn in the warning color.
                const data = event.data as { id?: string; kind?: string; severity?: string; text?: string; created_at?: string };
                if (data.id && s.entries.some(e => e.id === data.id)) break;
                stream.closeCurrentAssistant();
                const entry: ChatEntry = {
                    id: data.id || genId(),
                    role: 'advisory',
                    kind: data.kind || '',
                    content: data.text || '',
                    severity: data.severity || '',
                    isStreaming: false,
                    createdAt: data.created_at || new Date().toISOString(),
                };
                // Before a message the server has not accepted yet (queued): its row is
                // written after the note's, so it stays last. Same rule as summarization.
                const firstQueuedIdx = s.entries.findIndex(e => e.queued);
                if (firstQueuedIdx >= 0) {
                    s.entries = [...s.entries.slice(0, firstQueuedIdx), entry, ...s.entries.slice(firstQueuedIdx)];
                } else {
                    s.entries = [...s.entries, entry];
                }
                break;
            }
            case 'summarization': {
                const data = event.data as { id?: string; summary: string; created_at?: string; prompt_tokens?: number; predicted_tokens?: number; prompt_ms?: number; predicted_ms?: number; model_name?: string; context_used?: number };
                // The same id twice is an edit of the summary, not a second one: the row keeps its
                // place in the conversation and only its text moves.
                if (data.id && s.entries.some(e => e.id === data.id)) {
                    s.entries = s.entries.map(e => (e.id === data.id ? { ...e, content: data.summary || e.content } : e));
                    break;
                }
                const summaryEntry = {
                    id: data.id || genId(), role: 'summarization' as const, content: data.summary || t('chat.stream.summarized'),
                    isStreaming: false, createdAt: data.created_at || new Date().toISOString(),
                    stats: data.model_name ? { model_name: data.model_name, prompt_tokens: data.prompt_tokens ?? 0, predicted_tokens: data.predicted_tokens ?? 0, prompt_ms: data.prompt_ms ?? 0, predicted_ms: data.predicted_ms ?? 0, duration: (data.prompt_ms ?? 0) + (data.predicted_ms ?? 0) } : undefined,
                };
                // Insert before any queued entries (messages sent during summarization).
                const firstQueuedIdx = s.entries.findIndex(e => e.queued);
                if (firstQueuedIdx >= 0) {
                    s.entries = [...s.entries.slice(0, firstQueuedIdx), summaryEntry, ...s.entries.slice(firstQueuedIdx)];
                } else {
                    s.entries = [...s.entries, summaryEntry];
                }
                // Every later `used` is rebuilt from `lastPromptTokens`, not from `contextState`,
                // so a compressed window has to move that number too.
                if (typeof data.context_used === 'number' && data.context_used > 0) {
                    s.lastPromptTokens = data.context_used;
                    s.contextState = { ...s.contextState, used: data.context_used };
                }
                break;
            }
            case 'merge_resolution': {
                const data = event.data as { id: string; conflicted_files?: string[]; summary?: string; content: string; created_at: string; label?: string; is_error?: boolean };
                const content = data.content || t('chat.merge.merged');
                // `includes('conflict')` reads the server's row text, so it stays English.
                const label = data.label || (content.includes('conflict') ? t('chat.merge.conflictsResolved') : t('chat.merge.merged'));
                s.entries = [...s.entries, { id: data.id || genId(), role: 'merge_resolution', content, mergeLabel: label, isError: data.is_error || false, isStreaming: false, createdAt: data.created_at || new Date().toISOString() }];
                break;
            }
            case 'git_checkpoint_pending': {
                const data = event.data as { message_id: string };
                const pending = new Set(s.pendingCheckpoints);
                pending.add(data.message_id);
                s.pendingCheckpoints = pending;
                break;
            }
            case 'git_checkpoint_complete': {
                const data = event.data as { message_id: string; commit_hash: string; label: string };
                const pending = new Set(s.pendingCheckpoints);
                pending.delete(data.message_id);
                s.pendingCheckpoints = pending;
                if (data.label) {
                    const labels = new Map(s.checkpointLabels);
                    labels.set(data.message_id, data.label);
                    s.checkpointLabels = labels;
                }
                if (data.commit_hash) {
                    const hashes = new Map(s.checkpointCommitHashes);
                    hashes.set(data.message_id, data.commit_hash);
                    let entryId = s.currentAssistantId;
                    if (!entryId) {
                        for (let i = s.entries.length - 1; i >= 0; i--) {
                            const entry = s.entries[i];
                            if (entry && entry.role === 'assistant' && entry.toolCalls && entry.toolCalls.length > 0) { entryId = entry.id; break; }
                        }
                    }
                    if (entryId) hashes.set(entryId, data.commit_hash);
                    s.checkpointCommitHashes = hashes;
                    s.checkpointHashesVersion++;
                }
                break;
            }
        }
    };
}
