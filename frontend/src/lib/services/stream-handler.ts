import type { StreamEvent, DiffData } from '$lib/types';
import type { ChatEntry } from '$lib/components/Chat/TaskChat.svelte';
import type { StreamState } from './stream-state.svelte';

/** Estimate token count from text. Average English token ≈ 4 chars. */
export function estimateTokens(text: string): number {
    return Math.ceil(text.length / 4);
}

/** Update context usage from accumulated token counts. */
function updateContextUsed(s: typeof import('./stream-state.svelte').StreamState['state']) {
    s.contextState = { ...s.contextState, used: s.lastPromptTokens + s.streamTokenCount };
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
    createTaskMessage: (projectId: string, taskId: string, role: string, content: string) => Promise<void>;
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
    const { genId, scroll, markStreaming, markNotStreaming, onRefreshDiff, onThreadSwitch, loadHistory, updateActiveThread, updateThreadUrl, createTaskMessage } = cb;
    const s = stream.state;

    // rAF batching for scroll — coalesces rapid calls during token bursts
    let scrollRaf: number | null = null;
    function scheduleScroll() {
        if (scrollRaf) return;
        scrollRaf = requestAnimationFrame(() => {
            scrollRaf = null;
            scroll();
        });
    }

    function ensureAssistantEntry(): ChatEntry {
        return stream.ensureAssistantEntry(genId);
    }

    return function handleEvent(event: StreamEvent, suppressEvents: boolean, projectId: string, taskId: string) {
        switch (event.type) {
            case 'token': {
                if (suppressEvents) break;
                stream.clearAwaiting();
                markStreaming();
                if (!s.currentAssistantId) s.streamTokenCount = 0;
                const data = event.data as { text: string };
                s.streamTokenCount += estimateTokens(data.text);
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
                s.streamTokenCount += estimateTokens(data.text);
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

                let entry = s.currentAssistantId ? s.entries.find(e => e.id === s.currentAssistantId) || null : null;
                if (!entry) entry = ensureAssistantEntry();

                if (entry.content.trim() === data.tool_name && !entry.reasoningContent) entry.content = '';
                if (!entry.toolCalls) entry.toolCalls = [];

                const existing = entry.toolCalls.find(tc => tc.toolId === data.tool_id);
                if (existing) {
                    if (!existing.isPending && existing.result !== undefined) break;
                    // Mutate in-place so activeToolCalls map reference stays valid
                    existing.toolName = data.tool_name;
                    existing.displayName = data.tool_display_name;
                    existing.args = data.args || {};
                    const newRawArgs = data.raw_args_delta ? (existing.raw_args || '') + data.raw_args_delta : data.raw_args;
                    existing.raw_args = newRawArgs;
                    existing.sandbox = data.sandbox;
                    existing.path = data.path;
                    if (data.lines_added !== undefined) existing.linesAdded = data.lines_added;
                    if (data.lines_removed !== undefined) existing.linesRemoved = data.lines_removed;
                } else if (s.processedToolIds.has(data.tool_id)) {
                    break;
                } else {
                    s.processedToolIds = new Set([...s.processedToolIds, data.tool_id]);
                    const ci = entry.content.length;
                    const ri = (entry.reasoningContent || '').length;
                    const newTc = {
                        toolId: data.tool_id, toolName: data.tool_name, displayName: data.tool_display_name,
                        args: data.args || {}, raw_args: data.raw_args, sandbox: data.sandbox, path: data.path,
                        linesAdded: data.lines_added, linesRemoved: data.lines_removed,
                        isPending: true, contentIndex: ci, reasoningIndex: ri,
                    };
                    entry.toolCalls = [...(entry.toolCalls || []), newTc];
                    entry.isStreaming = true;
                    const newActive = new Map(s.activeToolCalls);
                    newActive.set(data.tool_id, { entryId: entry.id, toolCall: newTc });
                    s.activeToolCalls = newActive;
                }
                scheduleScroll();
                break;
            }
            case 'tool_result': {
                const data = event.data as { tool_id: string; success: boolean; output: string; duration: number; args?: Record<string, unknown>; raw_args?: string; sandbox?: boolean; path?: string };
                const ref = s.activeToolCalls.get(data.tool_id);
                const newActive = new Map(s.activeToolCalls);
                newActive.delete(data.tool_id);
                s.activeToolCalls = newActive;
                if (ref) {
                    // Access tc through current s.entries proxy so mutations trigger reactivity.
                    const entry = s.entries.find(e => e.id === ref.entryId);
                    const tc = entry?.toolCalls?.find(t => t.toolId === data.tool_id);
                    if (tc) {
                        tc.result = data.output;
                        tc.success = data.success;
                        tc.duration = data.duration;
                        tc.isPending = false;
                        if (data.args) tc.args = data.args;
                        if (data.raw_args) tc.raw_args = data.raw_args;
                        if (data.sandbox !== undefined) tc.sandbox = data.sandbox;
                        if (data.path !== undefined) tc.path = data.path;
                    }
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
                if (data.tool_id) {
                    const ref = s.activeToolCalls.get(data.tool_id);
                    if (ref) {
                        // Direct property mutation via map reference — no array allocation
                        const tc = ref.toolCall;
                        if (data.bytes_written !== undefined) tc.bytesWritten = data.bytes_written;
                        if (data.lines_added !== undefined) tc.linesAdded = data.lines_added;
                        if (data.lines_removed !== undefined) tc.linesRemoved = data.lines_removed;
                        if (data.path !== undefined) tc.path = data.path;
                    }
                } else {
                    // Broadcast: update all active tool calls via direct mutation
                    for (const ref of s.activeToolCalls.values()) {
                        const tc = ref.toolCall;
                        if (data.bytes_written !== undefined) tc.bytesWritten = data.bytes_written;
                        if (data.lines_added !== undefined) tc.linesAdded = data.lines_added;
                        if (data.lines_removed !== undefined) tc.linesRemoved = data.lines_removed;
                        if (data.path !== undefined) tc.path = data.path;
                    }
                }
                break;
            }
            case 'tool_output': {
                const data = event.data as { tool_id: string; text: string };
                const ref = s.activeToolCalls.get(data.tool_id);
                if (ref) {
                    // Direct string concatenation via map reference — no array allocation
                    const tc = ref.toolCall;
                    tc.result = (tc.result || '') + data.text;
                }
                scheduleScroll();
                break;
            }
            case 'user_message': {
                const data = event.data as { id?: string; role: string; content: string; thread_id?: string; attachments?: Array<{ mimeType: string; data: string }> };
                // Echo handling is done in component (lastSentMessage check)
                if (data.id && s.entries.some(e => e.id === data.id)) break;
                stream.closeCurrentAssistant();
                s.entries = [...s.entries, { id: data.id || genId(), role: data.role, content: data.content, threadId: data.thread_id, isStreaming: false, createdAt: new Date().toISOString() }];
                break;
            }
            case 'complete': {
                markNotStreaming();
                s.awaitingResponse = false;
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
                scroll(true);
                break;
            }
            case 'error': {
                stream.clearAwaiting();
                markNotStreaming();
                stream.closeCurrentAssistant();
                const data = event.data as { message: string; code?: string };
                const code = data.code || 'error';
                if (code === 'ws_error' || code === 'stopped') break;
                const message = data.message || 'An error occurred';
                const entry: ChatEntry = { id: genId(), role: 'error', content: message, isStreaming: false, error: { code, message }, createdAt: new Date().toISOString() };
                s.entries = [...s.entries, entry];
                if (code !== 'ws_disconnected') {
                    createTaskMessage(projectId, taskId, 'error', `[${code}] ${message}`).catch(() => {});
                }
                break;
            }
            case 'status': {
                const data = event.data as { state: string; activity?: string };
                if (data.state === 'pulling_image') {
                    const imgName = data.activity?.replace('Pulling ', '') || 'sandbox image';
                    s.entries = [...s.entries, { id: genId(), role: 'system', content: `Pulling ${imgName}...`, isStreaming: false, createdAt: new Date().toISOString() }];
                } else if (data.state === 'building_image') {
                    s.entries = [...s.entries, { id: genId(), role: 'system', content: 'Building sandbox image from Dockerfile...', isStreaming: false, createdAt: new Date().toISOString() }];
                } else if (data.state === 'starting_container') {
                    s.entries = [...s.entries, { id: genId(), role: 'system', content: 'Starting sandbox container...', isStreaming: false, createdAt: new Date().toISOString() }];
                } else if (data.state === 'running') {
                    if (s.currentAssistantId) {
                        const entry = s.entries.find(e => e.id === s.currentAssistantId);
                        if (!entry || (!entry.content && !entry.reasoningContent)) stream.closeCurrentAssistant();
                    }
                    s.activeToolCalls = new Map();
                    s.entries = s.entries.filter(e => !(e.toolCalls && e.toolCalls.some(tc => tc.isPending) && !e.content && !e.reasoningContent));
                    markStreaming();
                    s.queuePosition = 0;
                    s.queueEstimatedWait = 0;
                } else if (data.state === 'cancelled') {
                    stream.closeCurrentAssistant();
                    s.activeToolCalls = new Map();
                    s.processedToolIds = new Set();
                    s.entries = s.entries.filter(e => !e.isStreaming);
                    markNotStreaming();
                    s.awaitingResponse = false;
                } else if (['stopped', 'failed', 'idle', 'completed', 'awaiting_review'].includes(data.state)) {
                    markNotStreaming();
                    s.awaitingResponse = false;
                }
                break;
            }
            case 'build_progress': {
                const data = event.data as { line: string };
                if (data.line) {
                    s.entries = [...s.entries, { id: genId(), role: 'system', content: data.line, isStreaming: false, createdAt: new Date().toISOString() }];
                }
                break;
            }
            case 'hint': {
                const data = event.data as { content: string; created_at: string };
                if (data.content) {
                    stream.closeCurrentAssistant();
                    s.entries = [...s.entries, { id: genId(), role: 'user', content: data.content, isStreaming: false, createdAt: data.created_at || new Date().toISOString() }];
                }
                break;
            }
            case 'stats': {
                const data = event.data as { model_name: string; prompt_tokens: number; predicted_tokens: number; predicted_ms: number; context_used?: number; context_limit?: number; context_percent?: number };
                if (data.prompt_tokens !== undefined && data.prompt_tokens > 0) s.lastPromptTokens = data.prompt_tokens;
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
            case 'prompt_progress': {
                const data = event.data as { total: number; cache: number; processed: number; time_ms: number };
                if (data.total > 0) s.promptProgress = Math.round((data.processed / data.total) * 100);
                break;
            }
            case 'summarization': {
                const data = event.data as { id?: string; summary: string; created_at?: string; prompt_tokens?: number; predicted_tokens?: number; prompt_ms?: number; predicted_ms?: number; model_name?: string };
                if (data.id && s.entries.some(e => e.id === data.id)) break;
                s.entries = [...s.entries, {
                    id: data.id || genId(), role: 'summarization', content: data.summary || 'Context was summarized to free space',
                    isStreaming: false, createdAt: data.created_at || new Date().toISOString(),
                    stats: data.model_name ? { model_name: data.model_name, prompt_tokens: data.prompt_tokens ?? 0, predicted_tokens: data.predicted_tokens ?? 0, prompt_ms: data.prompt_ms ?? 0, predicted_ms: data.predicted_ms ?? 0, duration: (data.prompt_ms ?? 0) + (data.predicted_ms ?? 0) } : undefined,
                }];
                break;
            }
            case 'merge_resolution': {
                const data = event.data as { id: string; conflicted_files?: string[]; summary?: string; content: string; created_at: string; label?: string; is_error?: boolean };
                const content = data.content || 'Merged';
                const label = data.label || (content.includes('conflict') ? 'Merge conflicts resolved' : 'Merged');
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
