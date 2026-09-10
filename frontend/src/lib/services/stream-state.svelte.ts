import type { ChatEntry, LockWait } from '$lib/types';
import { findToolCall } from '$lib/utils/tool-call-lookup';

/** Tool call info — matches ChatEntry['toolCalls'][number]. */
export type ToolCallInfo = NonNullable<ChatEntry['toolCalls']>[number];

/**
 * Reactive class holding all stream-processing state.
 * Uses Svelte 5 $state() for deep reactivity — no version bumper needed.
 *
 * Usage:
 *   const stream = new StreamState();
 *   // Read directly from stream.state in templates:
 *   {#each stream.state.entries as entry}
 */
export class StreamState {
    /** Reactive state — deeply tracked by Svelte 5. */
    state = $state({
        entries: [] as ChatEntry[],
        currentAssistantId: null as string | null,
        activeToolCalls: new Map<string, { entryId: string; toolCall: ToolCallInfo }>(),
        processedToolIds: new Set<string>(),
        awaitingResponse: false,
        queuePosition: 0,
        queueEstimatedWait: 0,
        lockWait: null as LockWait | null,
        promptProgress: 0,
        retryCount: 0,
        retryError: '',
        contextState: { used: 0, limit: 0 },
        streamTokenCount: 0,
        lastPromptTokens: 0,
        sessionCost: 0,
        pendingCheckpoints: new Set<string>(),
        checkpointLabels: new Map<string, string>(),
        checkpointCommitHashes: new Map<string, string>(),
        checkpointHashesVersion: 0,
    });

    /** Create a new assistant entry or return existing. */
    ensureAssistantEntry(genId: () => string): ChatEntry {
        if (this.state.currentAssistantId) {
            const entry = this.state.entries.find(e => e.id === this.state.currentAssistantId);
            if (entry) return entry;
        }
        const id = genId();
        this.state.currentAssistantId = id;
        const entry: ChatEntry = { id, role: 'assistant', content: '', isStreaming: true, createdAt: new Date().toISOString() };
        this.state.entries = [...this.state.entries, entry];
        return entry;
    }

    /** Close the current streaming assistant entry. */
    closeCurrentAssistant() {
        if (this.state.currentAssistantId) {
            const entry = this.state.entries.find(e => e.id === this.state.currentAssistantId);
            if (entry) entry.isStreaming = false;
            this.state.currentAssistantId = null;
        }
    }

    /** Clear awaiting spinner and retry state. */
    clearAwaiting() {
        if (this.state.awaitingResponse) this.state.awaitingResponse = false;
        this.state.promptProgress = 0;
        this.state.queuePosition = 0;
        this.state.queueEstimatedWait = 0;
        this.state.lockWait = null;
        this.clearRetry();
    }

    /**
     * Re-point `activeToolCalls` at the tool calls currently in `entries`, dropping
     * refs whose tool call is gone. Call after anything that replaces the entry list
     * (loadHistory): the new entries carry new ids, so refs captured while streaming
     * are detached and would keep writing into a copy nobody renders.
     */
    reconcileActiveToolCalls() {
        if (this.state.activeToolCalls.size === 0) return;
        const next = new Map<string, { entryId: string; toolCall: ToolCallInfo }>();
        for (const toolId of this.state.activeToolCalls.keys()) {
            const live = findToolCall(this.state.entries, toolId);
            if (live) next.set(toolId, { entryId: live.entry.id, toolCall: live.tc });
        }
        this.state.activeToolCalls = next;
    }

    /** Reset streaming state (thread switch). */
    resetStreaming() {
        this.closeCurrentAssistant();
        this.state.activeToolCalls = new Map();
        this.state.processedToolIds = new Set();
        this.state.streamTokenCount = 0;
        this.state.awaitingResponse = false;
        // Note: sessionCost is NOT reset here — it reflects the task's accumulated
        // session cost, which persists across thread switches and is restored
        // from the persisted task.session_cost on load.
    }

    /** Clear retry state. */
    clearRetry() {
        this.state.retryCount = 0;
        this.state.retryError = '';
    }

    /** Full reset. */
    reset() {
        this.state.entries = [];
        this.resetStreaming();
        this.state.queuePosition = 0;
        this.state.queueEstimatedWait = 0;
        this.state.lockWait = null;
        this.state.promptProgress = 0;
        this.clearRetry();
        this.state.contextState = { used: 0, limit: 0 };
        this.state.lastPromptTokens = 0;
        this.state.sessionCost = 0;
        this.state.pendingCheckpoints = new Set();
        this.state.checkpointLabels = new Map();
        this.state.checkpointCommitHashes = new Map();
        this.state.checkpointHashesVersion = 0;
    }
}
