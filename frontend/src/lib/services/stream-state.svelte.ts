import type { ChatEntry } from '$lib/types';

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
        promptProgress: 0,
        contextState: { used: 0, limit: 0 },
        streamTokenCount: 0,
        lastPromptTokens: 0,
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

    /** Clear awaiting response spinner. */
    clearAwaiting() {
        if (this.state.awaitingResponse) this.state.awaitingResponse = false;
        this.state.promptProgress = 0;
    }

    /** Reset all streaming state (thread switch, task change). */
    resetStreaming() {
        this.closeCurrentAssistant();
        this.state.activeToolCalls = new Map();
        this.state.processedToolIds = new Set();
        this.state.streamTokenCount = 0;
        this.state.awaitingResponse = false;
    }

    /** Full reset — clear entries and all state. */
    reset() {
        this.state.entries = [];
        this.resetStreaming();
        this.state.queuePosition = 0;
        this.state.queueEstimatedWait = 0;
        this.state.promptProgress = 0;
        this.state.contextState = { used: 0, limit: 0 };
        this.state.lastPromptTokens = 0;
        this.state.pendingCheckpoints = new Set();
        this.state.checkpointLabels = new Map();
        this.state.checkpointCommitHashes = new Map();
        this.state.checkpointHashesVersion = 0;
    }
}
