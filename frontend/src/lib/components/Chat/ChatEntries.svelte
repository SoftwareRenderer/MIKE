<script lang="ts">
    import type { Thread, ChatEntry } from '$lib/types';
    import type { EntryMeta } from '$lib/utils/chat-entries';
    import ChatMessage from './ChatMessage.svelte';
    import CheckpointDivider from './CheckpointDivider.svelte';
    import QueueIndicator from './QueueIndicator.svelte';
    import VirtualList from '$lib/components/VirtualList.svelte';
    import { formatToolArgs } from '$lib/utils/toolArgs';

    let {
        entries,
        entryMetas,
        lastEntryIdx,
        activeThreadId,
        threads,
        awaitingResponse,
        connected,
        queuePosition,
        queueEstimatedWait,
        promptProgress,
        isAgentSelected,
        taskStatus,
        projectId,
        taskId,
        onScroll,
        onWheel,
        onTouchStart,
        onDismissError,
        onRetryConnect,
        onThreadSwitch,
        isSwitching,
        onMsgDelete,
        onMsgFork,
        onMsgSend,
        onRegenerate,
        onNavigateTask,
        onViewChanges,
        scrollEl,
        onScrollElChange,
    }: {
        entries: ChatEntry[];
        entryMetas: EntryMeta[];
        lastEntryIdx: number;
        activeThreadId: string;
        threads: Thread[];
        awaitingResponse: boolean;
        connected: boolean;
        queuePosition: number;
        queueEstimatedWait: number;
        promptProgress: number;
        isAgentSelected: () => boolean;
        taskStatus: string;
        projectId: string;
        taskId: string;
        onScroll?: () => void;
        onWheel?: () => void;
        onTouchStart?: () => void;
        onDismissError: (messageId: string) => Promise<void>;
        onRetryConnect: () => void;
        onThreadSwitch: (threadId: string) => Promise<void>;
        isSwitching?: boolean;
        onMsgDelete: (entry: ChatEntry) => void;
        onMsgFork: (msgId: string, origContent: string, newContent: string, atts?: Array<{ mimeType: string; data: string }>) => Promise<void>;
        onMsgSend: (msgId: string, origContent: string, newContent: string, atts?: Array<{ mimeType: string; data: string }>) => Promise<void>;
        onMsgEdit: (msgId: string, newContent: string) => Promise<void>;
        onRegenerate: (entryId: string) => Promise<void>;
        onNavigateTask: (targetTaskId: string) => void;
        onViewChanges?: (commitHash: string) => void;
        scrollEl: HTMLElement | null;
        onScrollElChange?: (el: HTMLElement | null) => void;
    } = $props();

    // Estimated heights per entry type for virtual scrolling
    function getEstimatedHeight(entry: ChatEntry, _index: number): number {
        if (entry.role === 'user') return 60;
        if (entry.role === 'error') return 80;
        if (entry.role === 'summarization') return 100;
        if (entry.role === 'merge_resolution') return 80;
        // assistant entries: base height + extra for tool calls and reasoning
        let h = 80;
        if (entry.toolCalls && entry.toolCalls.length > 0) {
            h += entry.toolCalls.length * 60;
        }
        if (entry.hasReasoning) h += 40;
        if (entry.isStreaming) h += 20;
        return h;
    }

    // Virtual list scroll element — received from VirtualList via callback
    let chatScrollEl: HTMLElement | null = null;

    // VirtualList component instance for accessing scrollToItem
    let virtualListComp: {
        scrollToItem: (index: number, behavior?: ScrollBehavior) => void;
        vs: {
            getItemOffset: (index: number) => number;
            totalHeight: number;
        };
    } | null = $state(null);

    function handleScrollElChange(el: HTMLElement | null) {
        chatScrollEl = el;
        if (onScrollElChange) {
            onScrollElChange(el);
        }
    }

    // Compute sticky header from entry data based on scroll position.
    // This replaces the DOM-based query approach with a data-driven one
    // that works correctly with virtual scrolling.
    let computedStickyMessage = $state('');
    let computedShowHeader = $state(false);

    function computeStickyFromScroll() {
        if (!chatScrollEl || entries.length === 0) {
            computedStickyMessage = '';
            computedShowHeader = false;
            return;
        }
        const scrollTop = chatScrollEl.scrollTop;
        // Walk through entries computing cumulative offsets to find
        // the latest user message whose top has scrolled above the viewport.
        let offset = 0;
        let latestFirstLine = '';
        for (let i = 0; i < entries.length; i++) {
            const entry = entries[i];
            const h = getEstimatedHeight(entry, i);
            if (entry.role === 'user') {
                if (offset <= scrollTop) {
                    latestFirstLine = entry.content.split('\n')[0];
                }
            }
            offset += h;
        }
        computedStickyMessage = latestFirstLine;
        computedShowHeader = !!latestFirstLine;
    }

    /** Scroll to the user message shown in the sticky header. */
    function handleStickyClickInternal() {
        if (!computedStickyMessage || !virtualListComp) return;
        // Find the last user entry matching the sticky message content
        for (let i = entries.length - 1; i >= 0; i--) {
            const entry = entries[i];
            if (entry.role === 'user' && entry.content.split('\n')[0] === computedStickyMessage) {
                virtualListComp.scrollToItem(i, 'smooth');
                return;
            }
        }
    }

    /** Combined scroll handler: calls parent onScroll + computes sticky header. */
    function handleCombinedScroll() {
        onScroll?.();
        computeStickyFromScroll();
    }
</script>

<div class="chat-messages-wrapper">
    {#if entries.length > 0 && computedShowHeader}
        <div class="chat-sticky-header" role="status" aria-live="polite">
            <button class="chat-sticky-bubble" onclick={handleStickyClickInternal} title="Scroll to message" aria-label="Scroll to message">{computedStickyMessage}</button>
        </div>
    {/if}

    {#if entries.length === 0}
        <div class="chat-messages" bind:this={scrollEl} role="region" aria-label="Chat messages" tabindex="-1">
            <div class="chat-empty">
                <svg width="32" height="32" viewBox="0 0 16 16" fill="none">
                    <path d="M2 3h12v8H5l-3 3V3z" stroke="currentColor" stroke-width="1" stroke-linejoin="round" opacity="0.3"/>
                </svg>
                <p>No messages yet</p>
                <span class="hint">Send a message to start the conversation</span>
            </div>
        </div>
    {:else}
        <VirtualList
            items={entries}
            itemHeight={getEstimatedHeight}
            getKey={(e: ChatEntry) => e.id}
            overscan={5}
            onScrollElChange={handleScrollElChange}
            onscroll={handleCombinedScroll}
            onwheel={onWheel}
            ontouchstart={onTouchStart}
            bind:this={virtualListComp}
        >
            {#snippet children({ item: entry, index: entryIdx })}
                {@const meta = entryMetas[entryIdx] as EntryMeta}
                {#if entry.role === 'user'}
                    {#if meta.cpThread || meta.cpState}
                        <div>
                            <CheckpointDivider
                                timestamp={entry.createdAt}
                                threadId={entry.threadId || activeThreadId}
                                threads={threads}
                                activeThreadId={meta.cpThread?.id || ''}
                                onSwitch={onThreadSwitch}
                                isSwitching={isSwitching}
                                isPending={meta.cpState?.isPending || false}
                                commitLabel={meta.cpState?.commitLabel || undefined}
                                commitHash={meta.cpState?.commitHash || meta.cpThread?.checkpoint_commit_hash || undefined}
                                onViewChanges={onViewChanges}
                            />
                        </div>
                    {/if}
                {/if}
                {#if entry.role === 'error'}
                    <div class="error-entry">
                        <div class="error-card">
                            <div class="error-header">
                                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                                    <path d="M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"/>
                                    <line x1="12" y1="9" x2="12" y2="13"/>
                                    <line x1="12" y1="17" x2="12.01" y2="17"/>
                                </svg>
                                <span class="error-code">{entry.error?.code || 'error'}</span>
                                {#if entry.error?.code === 'ws_disconnected'}
                                    <span class="error-reconnecting"><span class="spinner"></span> Reconnecting...</span>
                                {/if}
                                <button class="btn-dismiss" onclick={() => onDismissError(entry.id)} title="Dismiss error" aria-label="Dismiss error">
                                    <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                                        <line x1="18" y1="6" x2="6" y2="18"/>
                                        <line x1="6" y1="6" x2="18" y2="18"/>
                                    </svg>
                                </button>
                            </div>
                            <div class="error-message">{entry.content}</div>
                            {#if entry.error?.code === 'ws_disconnected'}
                                <button class="btn-retry" onclick={onRetryConnect} title="Retry connection">
                                    <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                                        <polyline points="23 4 23 10 17 10"/>
                                        <path d="M20.49 15a9 9 0 1 1-2.12-9.36L23 10"/>
                                    </svg>
                                    Retry now
                                </button>
                            {/if}
                        </div>
                    </div>
                {:else if entry.role === 'summarization'}
                    <div class="summarization-entry">
                        <div class="summarization-divider">
                            <div class="divider-line"></div>
                            <div class="divider-content">
                                <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                                    <path d="M19 21l-7-5-7 5V5a2 2 0 0 1 2-2h10a2 2 0 0 1 2 2z"/>
                                </svg>
                                <span class="summarization-label">Context summarized</span>
                            </div>
                            <div class="divider-line"></div>
                        </div>
                        <ChatMessage role="assistant" content={entry.content} reasoningContent={entry.reasoningContent} hasReasoning={entry.hasReasoning} isStreaming={entry.isStreaming || false} timestamp={entry.createdAt} messageId={entry.id} attachments={entry.attachments} stats={entry.stats} toolCalls={entry.toolCalls || []} formatToolArgs={formatToolArgs} onDelete={() => onMsgDelete(entry)} onEdit={(msgId, newContent) => onMsgEdit(msgId, newContent)} isLastUserMessage={false} isFirstUserMessage={false} taskStatus={taskStatus} onRegenerate={() => onRegenerate(entry.id)} onNavigateTask={onNavigateTask} queued={entry.queued || false} projectId={projectId} taskId={taskId} />
                    </div>
                {:else if entry.role === 'merge_resolution'}
                    <div class="merge-resolution-entry">
                        <div class="merge-resolution-card" class:merge-resolution-error={entry.isError}>
                            <div class="merge-resolution-header">
                                {#if entry.isError}
                                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                                        <circle cx="12" cy="12" r="10"/>
                                        <line x1="15" y1="9" x2="9" y2="15"/>
                                        <line x1="9" y1="9" x2="15" y2="15"/>
                                    </svg>
                                {:else}
                                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                                        <circle cx="18" cy="18" r="3"/>
                                        <circle cx="6" cy="6" r="3"/>
                                        <path d="M13 6h3a2 2 0 0 1 2 2v7"/>
                                        <line x1="6" y1="9" x2="6" y2="21"/>
                                    </svg>
                                {/if}
                                <span class="merge-resolution-label">{entry.mergeLabel || 'Merge conflicts resolved'}</span>
                            </div>
                            <div class="merge-resolution-content">{entry.content}</div>
                        </div>
                    </div>
                {:else if entry.role === 'user'}
                    <div class="sticky-user-message" data-user-msg-first-line={entry.content.split('\n')[0]}>
                        <ChatMessage role={entry.role} content={entry.content} reasoningContent={entry.reasoningContent} hasReasoning={entry.hasReasoning} isStreaming={entry.isStreaming || false} timestamp={entry.createdAt} messageId={entry.id} attachments={entry.attachments} stats={entry.stats} toolCalls={entry.toolCalls || []} formatToolArgs={formatToolArgs} onDelete={() => onMsgDelete(entry)} onFork={onMsgFork} onSend={onMsgSend} isLastUserMessage={meta.isLastUserMessage} isFirstUserMessage={entryIdx === 0} taskStatus={taskStatus} onRegenerate={entryIdx === lastEntryIdx ? () => onRegenerate(entry.id) : undefined} onNavigateTask={onNavigateTask} queued={entry.queued || false} projectId={projectId} taskId={taskId} />
                    </div>
                {:else}
                    <div>
                        <ChatMessage role={entry.role} content={entry.content} reasoningContent={entry.reasoningContent} hasReasoning={entry.hasReasoning} isStreaming={entry.isStreaming || false} timestamp={entry.createdAt} messageId={entry.id} attachments={entry.attachments} stats={entry.stats} toolCalls={entry.toolCalls || []} formatToolArgs={formatToolArgs} onDelete={() => onMsgDelete(entry)} onFork={onMsgFork} onSend={onMsgSend} isLastUserMessage={meta.isLastUserMessage} isFirstUserMessage={entryIdx === 0} taskStatus={taskStatus} onRegenerate={entryIdx === lastEntryIdx ? () => onRegenerate(entry.id) : undefined} onNavigateTask={onNavigateTask} queued={entry.queued || false} projectId={projectId} taskId={taskId} />
                    </div>
                {/if}
            {/snippet}
        </VirtualList>

        {#if awaitingResponse && connected}
            <QueueIndicator
                {queuePosition}
                {queueEstimatedWait}
                {promptProgress}
                {isAgentSelected}
            />
        {/if}
    {/if}
</div>

<style>
    .chat-messages-wrapper {
        flex: 1;
        position: relative;
        display: flex;
        flex-direction: column;
        min-height: 0;
    }
    .chat-messages {
        flex: 1;
        overflow-y: auto;
        padding: 8px 12px;
        min-height: 0;
    }
    .chat-sticky-header {
        position: absolute;
        top: 0;
        left: 0;
        right: 0;
        z-index: 5;
        display: flex;
        justify-content: flex-end;
        align-items: center;
        padding: 6px 12px;
        background: var(--color-surface-1);
        border-bottom: 1px solid var(--color-border);
    }
    .chat-sticky-bubble {
        font-size: 0.8125rem;
        color: var(--color-text-primary);
        background: color-mix(in srgb, var(--color-accent) 18%, transparent);
        border-radius: 1.125rem;
        padding: 6px 12px;
        max-width: 80%;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
        border: none;
        cursor: pointer;
        font: inherit;
        text-align: left;
        transition: background 0.15s ease;
    }
    .chat-sticky-bubble:hover {
        background: color-mix(in srgb, var(--color-accent) 28%, transparent);
    }
    .chat-empty {
        display: flex;
        flex-direction: column;
        align-items: center;
        justify-content: center;
        gap: 8px;
        padding: 48px 16px;
        color: var(--color-text-tertiary);
        text-align: center;
        height: 100%;
    }
    .chat-empty p { font-size: 0.875rem; margin: 0; font-weight: 500; }
    .chat-empty .hint { font-size: 0.75rem; }
    @keyframes pulse { 0%, 100% { opacity: 1; } 50% { opacity: 0.3; } }
    .spinner {
        display: inline-block;
        width: 10px;
        height: 10px;
        border: 1.5px solid var(--color-status-yellow);
        border-top-color: transparent;
        border-radius: 50%;
        animation: spin 0.6s linear infinite;
    }
    @keyframes spin { to { transform: rotate(360deg); } }
    /* Error entry styles */
    .error-entry {
        padding: 6px 0;
    }
    .error-card {
        background: color-mix(in srgb, var(--color-status-red) 6%, transparent);
        border: 1px solid color-mix(in srgb, var(--color-status-red) 30%, transparent);
        border-left: 3px solid var(--color-status-red);
        border-radius: var(--radius-md);
        padding: 10px 14px;
    }
    .error-header {
        display: flex;
        align-items: center;
        gap: 6px;
        margin-bottom: 4px;
        color: var(--color-status-red);
    }
    .error-header svg { flex-shrink: 0; }
    .btn-dismiss {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        width: 18px;
        height: 18px;
        padding: 0;
        margin-left: auto;
        background: none;
        border: none;
        color: var(--color-text-tertiary);
        cursor: pointer;
        border-radius: 4px;
        transition: all 0.15s ease;
    }
    .btn-dismiss:hover {
        background: color-mix(in srgb, var(--color-status-red) 15%, transparent);
        color: var(--color-status-red);
    }
    .error-code {
        font-size: 0.75rem;
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.3px;
    }
    .error-reconnecting {
        font-size: 0.75rem;
        color: var(--color-status-yellow);
        margin-left: 4px;
        display: inline-flex;
        align-items: center;
        gap: 4px;
    }
    .error-message {
        font-size: 0.8125rem;
        color: var(--color-text-secondary);
        line-height: 1.5;
        white-space: pre-wrap;
        word-break: break-word;
    }
    .btn-retry {
        display: inline-flex;
        align-items: center;
        gap: 4px;
        margin-top: 8px;
        padding: 4px 10px;
        border-radius: var(--radius-sm);
        font-size: 0.75rem;
        font-weight: 500;
        background: var(--color-status-red-dark);
        color: white;
        border: none;
        cursor: pointer;
        transition: all 0.15s ease;
    }
    .btn-retry:hover { opacity: 0.85; }

    .summarization-entry {
        padding: 8px 0;
    }
    .summarization-divider {
        display: flex;
        align-items: center;
        gap: 10px;
        padding: 4px 0;
    }
    .summarization-divider .divider-line {
        flex: 1;
        height: 1px;
        background: var(--color-accent);
        opacity: 0.7;
    }
    .summarization-divider .divider-content {
        display: flex;
        align-items: center;
        gap: 6px;
        flex-shrink: 0;
    }
    .summarization-label {
        font-size: 0.75rem;
        color: var(--color-accent);
        opacity: 0.8;
        white-space: nowrap;
    }
    .merge-resolution-entry {
        padding: 8px 0;
    }
    .merge-resolution-card {
        background: color-mix(in srgb, var(--color-status-green) 6%, transparent);
        border-radius: var(--radius-md);
        border-left: 3px solid var(--color-status-green);
        overflow: hidden;
    }
    .merge-resolution-error {
        background: color-mix(in srgb, var(--color-status-red) 6%, transparent);
        border-left-color: var(--color-status-red);
    }
    .merge-resolution-error .merge-resolution-header,
    .merge-resolution-error .merge-resolution-label {
        color: var(--color-status-red);
    }
    .merge-resolution-header {
        display: flex;
        align-items: center;
        gap: 6px;
        padding: 8px 12px 4px;
        color: var(--color-status-green);
    }
    .merge-resolution-label {
        font-size: 0.75rem;
        font-weight: 600;
        color: var(--color-status-green);
    }
    .merge-resolution-content {
        padding: 4px 12px 10px;
        font-size: 0.8125rem;
        color: var(--color-text-secondary);
        line-height: 1.5;
        white-space: pre-wrap;
    }
</style>
