import type { ChatEntry, LockWait, ToolCallInfo } from '$lib/types';
import { findToolCall } from '$lib/utils/tool-call-lookup';

/** All stream-processing state in one reactive object, read directly as `stream.state` in templates. */
export class StreamState {
    /** Reactive state — deeply tracked by Svelte 5. */
    state = $state({
        entries: [] as ChatEntry[],
        currentAssistantId: null as string | null,
        activeToolCalls: new Map<string, { entryId: string; toolCall: ToolCallInfo }>(),
        processedToolIds: new Set<string>(),
        /**
         * A tool call this screen saw but holds no answered card for. A `tool_call` is broadcast once, so such
         * a card is gone until the transcript is read from the database again.
         */
        unresolvedToolIds: new Set<string>(),
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
     * Re-point `activeToolCalls` at the tool calls currently in `entries`. Call this after anything that
     * replaces the entry list: the new entries carry new ids, so refs captured while streaming would keep
     * writing into a copy nobody renders.
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

    /**
     * Drop the entries holding a tool call the server never answered. Correct only once the task is out of
     * flight (`taskIsInFlight`): the turn that abandoned the call was cancelled before `finalizeTurn`, so a
     * rebuild from the database shows neither its text nor its card.
     */
    dropUnresolvedToolCalls() {
        if (!this.state.entries.some(e => e.toolCalls?.some(tc => tc.isPending))) return;
        this.state.entries = this.state.entries.filter(e => !e.toolCalls?.some(tc => tc.isPending));
        this.reconcileActiveToolCalls();
    }

    /**
     * Drop the turn the provider abandoned: the entry still streaming, and the tool calls it
     * announced but never ran. Filter on isStreaming before closing anything — closing first clears
     * the flag that identifies the entry.
     */
    dropAbandonedTurn() {
        this.state.entries = this.state.entries.filter(e => !e.isStreaming);
        this.state.currentAssistantId = null;
        this.state.activeToolCalls = new Map();
        this.state.processedToolIds = new Set();
    }

    /**
     * A user message interrupted the turn in flight: the entries no row answers go, the rest stay so
     * the frames answering an interrupted call still have a card to land on. A `messageId` means a row
     * exists — `finalizeTurn` (backend/exec/loop.go) writes the assistant row and one `tool_call` row
     * per call before running any of them, and names each row in a `tool_progress` frame.
     *
     * Filter before closing: closing clears the `isStreaming` flag this filter reads.
     */
    abandonInterruptedTurn() {
        const discarded: string[] = [];
        this.state.entries = this.state.entries.filter(e => {
            if (!e.isStreaming) return true;
            if (!e.toolCalls?.some(tc => !!tc.messageId)) {
                discarded.push(...(e.toolCalls ?? []).map(tc => tc.toolId));
                return false;
            }
            e.isStreaming = false; // its row exists, so the text is finished
            return true;
        });
        this.state.currentAssistantId = null;
        this.reconcileActiveToolCalls();
        if (discarded.length > 0) {
            // An id still held after its card goes swallows the announcement that would redraw it.
            const gone = new Set(discarded);
            this.state.processedToolIds = new Set([...this.state.processedToolIds].filter(id => !gone.has(id)));
        }
    }

    /** `resetStreaming` plus the rows it does not own. Old rows hold the tool calls `mayBeBehind` reads, so
     * leaving them keeps the tab-return gate open for a turn that no longer exists here. */
    resetForThreadSwitch() {
        this.resetStreaming();
        this.state.entries = [];
        this.state.currentAssistantId = null;
    }

    /** Forgets the streaming ref unless an entry on screen is still streaming under it, so the next `token`
     * cannot write into an entry a rebuild dropped. */
    settleStreamingRef() {
        if (!this.state.currentAssistantId) return;
        if (this.state.entries.some(e => e.id === this.state.currentAssistantId && e.isStreaming)) return;
        this.state.currentAssistantId = null;
    }

    /** Reset streaming state (thread switch). */
    resetStreaming() {
        this.closeCurrentAssistant();
        this.state.activeToolCalls = new Map();
        this.state.processedToolIds = new Set();
        this.state.unresolvedToolIds = new Set();
        this.state.streamTokenCount = 0;
        this.state.awaitingResponse = false;
        // sessionCost stays: it is the task's accumulated cost, restored from the stored task on load.
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
