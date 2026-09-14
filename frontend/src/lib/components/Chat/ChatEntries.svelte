<script lang="ts">
    import type { Thread, ChatEntry, LockWait } from '$lib/types';
    import { advisoryLabel, type EntryMeta } from '$lib/utils/chat-entries';
    import { computeStickyHeader, STICKY_LINE_FALLBACK, type StickyUserMessage } from '$lib/utils/chat-sticky-header';
    import { scrollToLine } from '$lib/utils/settle-scroll';
    import { isAtBottom, type ChatFollow } from '$lib/composables/use-chat-follow.svelte';
    import ChatMessage from './ChatMessage.svelte';
    import CheckpointDivider from './CheckpointDivider.svelte';
    import QueueIndicator from './QueueIndicator.svelte';
    import { appStore } from '$lib/stores';
    import { fullscreenToggleTitle } from '$lib/utils/keyboard';
    import { t } from '$lib/i18n';

    let {
        entries,
        entryMetas,
        lastEntryIdx,
        activeThreadId,
        threads,
        awaitingResponse,
        lockWait = null,
        queuePosition,
        queueEstimatedWait,
        promptProgress,
        retryCount,
        retryError,
        isAgentSelected,
        taskStatus,
        taskExecutionState,
        projectId,
        taskId,
        onDismissError,
        onThreadSwitch,
        isSwitching,
        onMsgDelete,
        onMsgFork,
        onMsgSend,
        onRegenerate,
        onNavigateTask,
        onViewChanges,
        onToggleFullscreen,
        isChatFullscreen,
        follow,
        historyLoading = false,
    }: {
        entries: ChatEntry[];
        entryMetas: EntryMeta[];
        lastEntryIdx: number;
        activeThreadId: string;
        threads: Thread[];
        awaitingResponse: boolean;
        lockWait?: LockWait | null;
        queuePosition: number;
        queueEstimatedWait: number;
        promptProgress: number;
        retryCount: number;
        retryError: string;
        isAgentSelected: () => boolean;
        taskStatus: string;
        taskExecutionState: string;
        projectId: string;
        taskId: string;
        onDismissError: (messageId: string) => Promise<void>;
        onThreadSwitch: (threadId: string) => Promise<void>;
        isSwitching?: boolean;
        onMsgDelete: (entry: ChatEntry) => void;
        onMsgFork: (msgId: string, origContent: string, newContent: string, atts?: Array<{ mimeType: string; data: string }>) => Promise<void>;
        onMsgSend: (msgId: string, origContent: string, newContent: string, atts?: Array<{ mimeType: string; data: string }>) => Promise<void>;
        onRegenerate: (entryId: string) => Promise<void>;
        onNavigateTask: (targetTaskId: string) => void;
        onViewChanges?: (commitHash: string) => void;
        onToggleFullscreen?: () => void;
        isChatFullscreen?: boolean;
        /** Shared follow-the-bottom tracker (created by TaskChat). */
        follow: ChatFollow;
        historyLoading?: boolean;
    } = $props();

    function isOptimisticId(id: string): boolean {
        return /^[a-z0-9]{8}$/.test(id);
    }

    // The real (non-virtualized) message container, and the wrapper whose height
    // is the conversation height. Reactive so the tracker is re-attached whenever
    // the mounted elements change.
    let chatScrollEl: HTMLElement | null = $state(null);
    let chatContentEl: HTMLElement | null = $state(null);

    const fullscreenTitle = $derived(fullscreenToggleTitle(appStore.config));

    // Hand both to the shared follow tracker — it watches them for layout changes.
    $effect(() => {
        follow.attach(chatScrollEl, chatContentEl);
    });

    // Edit diff expand/collapse overrides, keyed by toolId; undefined = the default (auto-expand for
    // trivial diffs). Here so the state survives entry re-renders and content-visibility skips.
    let editDiffOverrides = $state<Record<string, boolean>>({});

    const BUILD_LOG_BOTTOM_PX = 50; // same tolerance as the build log in SandboxConfigModal

    // Build/pull progress boxes: DOM refs + at-bottom tracking, keyed by entry id.
    let buildLogEls = $state<Record<string, HTMLDivElement | undefined>>({});
    let buildLogAtBottom = $state<Record<string, boolean>>({});

    function onBuildLogScroll(entryId: string) {
        const el = buildLogEls[entryId];
        if (!el) return;
        buildLogAtBottom = { ...buildLogAtBottom, [entryId]: isAtBottom(el, { threshold: BUILD_LOG_BOTTOM_PX }) };
    }

    // Auto-scroll build boxes to the bottom as new lines arrive, but only when
    // the user is already at the bottom of that box (don't fight manual scroll).
    $effect(() => {
        const _els = buildLogEls;
        const _atBottom = buildLogAtBottom;
        for (const entry of entries) {
            if (!entry.buildLog) continue;
            const el = _els[entry.id];
            if (!el || entry.buildLog.lines.length === 0) continue;
            if (_atBottom[entry.id] ?? true) el.scrollTop = el.scrollHeight;
        }
    });

    // Reasoning expand/collapse overrides — survives item unmount/remount during scroll.
    let reasoningExpandedOverrides = $state<Record<string, boolean>>({});
    // Cached full reasoning content — survives item unmount/remount during scroll.
    let resolvedReasoningOverrides = $state<Record<string, string>>({});

    function handleEditDiffToggle(toolId: string, currentExpanded: boolean) {
        editDiffOverrides = { ...editDiffOverrides, [toolId]: !currentExpanded };
    }

    function handleReasoningToggle(messageId: string, expanded: boolean) {
        reasoningExpandedOverrides = { ...reasoningExpandedOverrides, [messageId]: expanded };
    }

    function handleReasoningResolved(messageId: string, content: string) {
        resolvedReasoningOverrides = { ...resolvedReasoningOverrides, [messageId]: content };
    }

    // Compute the sticky chat header
    let computedStickyMessage = $state('');
    let computedShowHeader = $state(false);
    let stickyEntryIdx = $state(-1);
    // The overlay hangs over the top of the scroller, so it decides where a message
    // has to land to be readable rather than behind it.
    let stickyHeaderEl: HTMLElement | null = $state(null);

    /**
     * The sticky line: the overlay's bottom edge, measured. A tapped message lands on
     * it and the header selects against it, so tapping the same spot walks back one
     * message at a time.
     */
    function stickyLineOffset(): number {
        const scroller = chatScrollEl;
        const header = stickyHeaderEl;
        if (!scroller || !header) return STICKY_LINE_FALLBACK;
        const line = header.getBoundingClientRect().bottom - scroller.getBoundingClientRect().top;
        // 0 everywhere there is no layout (jsdom) and while the overlay has no box.
        return line > 0 ? line : STICKY_LINE_FALLBACK;
    }

    function computeStickyFromScroll() {
        const el = chatScrollEl;
        if (!el || entries.length === 0) {
            computedStickyMessage = '';
            stickyEntryIdx = -1;
            computedShowHeader = false;
            return;
        }
        const containerTop = el.getBoundingClientRect().top;
        const scrollTop = el.scrollTop;
        const userMessages: StickyUserMessage[] = [];
        for (const node of Array.from(el.querySelectorAll<HTMLElement>('.sticky-user-message'))) {
            const entryIdx = Number(node.dataset.entryIdx);
            if (!Number.isInteger(entryIdx)) continue;
            const rect = node.getBoundingClientRect();
            // Convert viewport-space rects to content space.
            userMessages.push({
                entryIdx,
                firstLine: node.dataset.userMsgFirstLine ?? '',
                top: rect.top - containerTop + scrollTop,
                bottom: rect.bottom - containerTop + scrollTop,
            });
        }
        const sticky = computeStickyHeader(userMessages, scrollTop, stickyLineOffset());
        computedStickyMessage = sticky.firstLine;
        stickyEntryIdx = sticky.entryIdx;
        computedShowHeader = sticky.show;
    }

    /** Land the message the header names on the sticky line, under the overlay. */
    function handleStickyClickInternal() {
        const scroller = chatScrollEl;
        if (stickyEntryIdx < 0 || !scroller) return;
        // The message, not its `.chat-entry`: a checkpoint divider above the message
        // lives in the entry, so the entry starts a divider-height too early.
        const target = scroller.querySelector<HTMLElement>(`.sticky-user-message[data-entry-idx="${stickyEntryIdx}"]`);
        if (!target) return;
        // Declare the takeover, or the follow tracker's pin loop writes scrollTop
        // against us.
        follow.noteUserIntent();
        scrollToLine(scroller, target, stickyLineOffset());
    }

    /** One scroll listener for both jobs: shared follow state + sticky header. */
    function handleCombinedScroll() {
        follow.handleScroll();
        computeStickyFromScroll();
    }

    // Recompute the sticky header when entries change, and keep the viewport
    // pinned while the user is following (pin() is a no-op once they scroll up).
    $effect(() => {
        entries.length;
        computeStickyFromScroll();
        follow.pin();
    });

    /** Force a pin: the button is explicit intent, even if state says otherwise. */
    function handleJumpLatest() {
        follow.pin(true);
    }
</script>

<div class="chat-messages-wrapper" style={!follow.ready ? 'opacity: 0;' : ''}>
    {#if entries.length > 0 && computedShowHeader}
        <div class="chat-sticky-header" bind:this={stickyHeaderEl} role="status" aria-live="polite">
            <button class="chat-sticky-bubble" onclick={handleStickyClickInternal} title={t('chat.entries.scrollToMessage')} aria-label={t('chat.entries.scrollToMessage')}>{computedStickyMessage}</button>
            {#if onToggleFullscreen}
                <button type="button" class="chat-sticky-fullscreen" onclick={onToggleFullscreen} title={fullscreenTitle} aria-label={t('chat.entries.toggleFullscreen')}>
                    {#if isChatFullscreen}
                        <svg width="14" height="14" viewBox="0 0 16 16" fill="none"><path d="M10 2V6H14M6 14V10H2" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
                    {:else}
                        <svg width="14" height="14" viewBox="0 0 16 16" fill="none"><path d="M10 2h4v4M6 14H2v-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
                    {/if}
                </button>
            {/if}
        </div>
    {/if}

    {#if entries.length === 0}
        <div class="chat-messages" bind:this={chatScrollEl} role="region" aria-label={t('chat.entries.region')} tabindex="-1">
            {#if historyLoading}
                <div class="chat-loading">
                    <span class="spinner"></span>
                    <p>{t('chat.entries.loading')}</p>
                </div>
            {:else}
                <div class="chat-empty">
                    <svg width="32" height="32" viewBox="0 0 16 16" fill="none">
                        <path d="M2 3h12v8H5l-3 3V3z" stroke="currentColor" stroke-width="1" stroke-linejoin="round" opacity="0.3"/>
                    </svg>
                    <p>{t('chat.entries.empty')}</p>
                    <span class="hint">{t('chat.entries.emptyHint')}</span>
                </div>
            {/if}
        </div>
    {:else}
        <div class="chat-messages" bind:this={chatScrollEl} role="region" aria-label={t('chat.entries.region')} tabindex="-1" onscroll={handleCombinedScroll}>
          <div class="chat-content" bind:this={chatContentEl}>
            {#each entries as entry, entryIdx (entry.id)}
                {@const meta = entryMetas[entryIdx] as EntryMeta}
                <div class="chat-entry">
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
                                checkpointIndex={meta.checkpointIndex}
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
                                <button class="btn-dismiss" onclick={() => onDismissError(entry.id)} title={t('chat.entries.dismissError')} aria-label={t('chat.entries.dismissError')}>
                                    <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                                        <line x1="18" y1="6" x2="6" y2="18"/>
                                        <line x1="6" y1="6" x2="18" y2="18"/>
                                    </svg>
                                </button>
                            </div>
                            <div class="error-message">{entry.content}</div>
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
                                <span class="summarization-label">{t('chat.entries.summarized')}</span>
                            </div>
                            <div class="divider-line"></div>
                        </div>
                        <ChatMessage role="assistant" content={entry.content} reasoningContent={entry.reasoningContent} hasReasoning={entry.hasReasoning} isStreaming={entry.isStreaming || false} isOptimistic={isOptimisticId(entry.id)} timestamp={entry.createdAt} messageId={entry.id} attachments={entry.attachments} stats={entry.stats} toolCalls={entry.toolCalls || []} onDelete={() => onMsgDelete(entry)} isLastUserMessage={false} isFirstUserMessage={false} taskStatus={taskStatus} onNavigateTask={onNavigateTask} queued={entry.queued || false} {editDiffOverrides} onToggleEditDiff={handleEditDiffToggle} projectId={projectId} taskId={taskId} reasoningExpandedOverride={reasoningExpandedOverrides[entry.id]} onReasoningToggle={(exp) => handleReasoningToggle(entry.id, exp)} resolvedReasoningOverride={resolvedReasoningOverrides[entry.id]} onReasoningResolved={(content) => handleReasoningResolved(entry.id, content)} />
                    </div>
                {:else if entry.role === 'advisory'}
                    <div class="advisory-entry">
                        <div class="advisory-card" class:advisory-error={entry.severity === 'error'}>
                            <div class="advisory-header">
                                <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                                    <polyline points="22 12 18 12 15 21 9 3 6 12 2 12"/>
                                </svg>
                                <span class="advisory-label">{advisoryLabel(entry.kind)}</span>
                            </div>
                            <div class="advisory-text">{entry.content}</div>
                        </div>
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
                                <span class="merge-resolution-label">{entry.mergeLabel || t('chat.merge.conflictsResolved')}</span>
                            </div>
                            <div class="merge-resolution-content">{entry.content}</div>
                        </div>
                    </div>
                {:else if entry.role === 'user'}
                    <div class="sticky-user-message" data-entry-idx={entryIdx} data-user-msg-first-line={entry.content.split('\n')[0]}>
                        <ChatMessage role={entry.role} content={entry.content} reasoningContent={entry.reasoningContent} hasReasoning={entry.hasReasoning} isStreaming={entry.isStreaming || false} isOptimistic={isOptimisticId(entry.id)} timestamp={entry.createdAt} messageId={entry.id} attachments={entry.attachments} stats={entry.stats} toolCalls={entry.toolCalls || []} onDelete={() => onMsgDelete(entry)} onFork={onMsgFork} onSend={onMsgSend} isLastUserMessage={meta.isLastUserMessage} isFirstUserMessage={entryIdx === 0} taskStatus={taskStatus} onRegenerate={entryIdx === lastEntryIdx ? () => onRegenerate(entry.id) : undefined} onNavigateTask={onNavigateTask} queued={entry.queued || false} {editDiffOverrides} onToggleEditDiff={handleEditDiffToggle} projectId={projectId} taskId={taskId} reasoningExpandedOverride={reasoningExpandedOverrides[entry.id]} onReasoningToggle={(exp) => handleReasoningToggle(entry.id, exp)} resolvedReasoningOverride={resolvedReasoningOverrides[entry.id]} onReasoningResolved={(content) => handleReasoningResolved(entry.id, content)} />
                    </div>
                {:else if entry.buildLog}
                    <div class="build-box">
                        <div class="build-box-header">
                            {#if entry.buildLog.done}
                                <svg class="build-box-icon build-box-ok" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                                    <path d="M22 11.08V12a10 10 0 1 1-5.93-9.14"/>
                                    <polyline points="22 4 12 14.01 9 11.01"/>
                                </svg>
                            {:else}
                                <svg class="spin build-box-icon" width="14" height="14" viewBox="0 0 14 14" fill="none" aria-hidden="true">
                                    <circle cx="7" cy="7" r="5" stroke="currentColor" stroke-width="1.5" stroke-dasharray="20 10" stroke-linecap="round"/>
                                </svg>
                            {/if}
                            <span class="build-box-title">{entry.buildLog.title}</span>
                            {#if entry.buildLog.done}
                                <span class="build-box-status">{t('chat.entries.buildDone')}</span>
                            {/if}
                        </div>
                        <div
                            class="build-box-log"
                            role="log"
                            aria-label={t('chat.entries.buildOutput')}
                            bind:this={buildLogEls[entry.id]}
                            onscroll={() => onBuildLogScroll(entry.id)}
                        >
                            {#each entry.buildLog.lines as line}
                                <div class="build-box-line">{line}</div>
                            {/each}
                            {#if entry.buildLog.lines.length === 0 && !entry.buildLog.done}
                                <div class="build-box-pending">{t('chat.entries.building')}</div>
                            {/if}
                        </div>
                    </div>
                {:else}
                    <div>
                        <ChatMessage role={entry.role} severity={entry.severity} content={entry.content} reasoningContent={entry.reasoningContent} hasReasoning={entry.hasReasoning} isStreaming={entry.isStreaming || false} isOptimistic={isOptimisticId(entry.id)} timestamp={entry.createdAt} messageId={entry.id} attachments={entry.attachments} stats={entry.stats} toolCalls={entry.toolCalls || []} onDelete={entry.role === 'system' ? () => onDismissError(entry.id) : () => onMsgDelete(entry)} onFork={onMsgFork} onSend={onMsgSend} isLastUserMessage={meta.isLastUserMessage} isFirstUserMessage={entryIdx === 0} taskStatus={taskStatus} onRegenerate={entryIdx === lastEntryIdx ? () => onRegenerate(entry.id) : undefined} onNavigateTask={onNavigateTask} queued={entry.queued || false} {editDiffOverrides} onToggleEditDiff={handleEditDiffToggle} projectId={projectId} taskId={taskId} reasoningExpandedOverride={reasoningExpandedOverrides[entry.id]} onReasoningToggle={(exp) => handleReasoningToggle(entry.id, exp)} resolvedReasoningOverride={resolvedReasoningOverrides[entry.id]} onReasoningResolved={(content) => handleReasoningResolved(entry.id, content)} />
                    </div>
                {/if}
                </div>
            {/each}
          </div>
        </div>

        {#if !follow.atBottom && entries.length > 0}
            <button
                class="chat-jump-latest"
                onclick={handleJumpLatest}
                title={t('chat.entries.scrollToLatest')}
                aria-label={t('chat.entries.scrollToLatest')}
            >
                <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                    <path d="M4 6l4 4 4-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                </svg>
                <span class="chat-jump-latest-label">{t('chat.entries.latest')}</span>
            </button>
        {/if}

        <!-- The wait line. Without a lock wait it shows on the provider's request/task slots, which is what
             awaitingResponse means; a lock wait shows on its own, since nothing of ours is in flight. -->
        {#if lockWait || (awaitingResponse && (taskExecutionState === 'running' || (taskStatus === 'in_progress' && taskExecutionState === 'idle')))}
            <QueueIndicator
                {lockWait}
                {queuePosition}
                {queueEstimatedWait}
                {promptProgress}
                {retryCount}
                {retryError}
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
    /* Wrapper around the entry list: the follow tracker observes its height to
       catch growth that fires no scroll event. */
    .chat-content {
        display: block;
    }
    /* Inset focus ring — the global ring's 2px offset is clipped. */
    .chat-messages:focus-visible {
        outline-offset: -2px;
    }
    /* Render every entry in the DOM (no JS virtualizer) but let the browser skip
       paint/layout for entries far off-screen. `auto` remembers each entry's last
       rendered size so scroll height stays stable; 64px is the fallback estimate.
       This keeps long conversations smooth without a height cache. */
    .chat-entry {
        content-visibility: auto;
        contain-intrinsic-size: auto 64px;
    }
    .chat-sticky-header {
        position: absolute;
        top: 0;
        left: 0;
        z-index: 5;
        display: flex;
        justify-content: flex-end;
        align-items: center;
        padding: 6px 12px;
        background: var(--color-surface-1);
        border-bottom: 1px solid var(--color-border);
        width: calc(100% - 14px);
        border-bottom-right-radius: 1.125rem;
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
    .chat-sticky-fullscreen {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 28px;
        height: 28px;
        padding: 0;
        margin-right: 6px;
        border: none;
        background: transparent;
        color: var(--color-text-tertiary);
        cursor: pointer;
        border-radius: 4px;
        flex-shrink: 0;
    }
    .chat-sticky-fullscreen:hover {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
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
    .chat-loading {
        display: flex;
        flex-direction: column;
        align-items: center;
        justify-content: center;
        gap: 12px;
        padding: 48px 16px;
        color: var(--color-text-tertiary);
        text-align: center;
        height: 100%;
    }
    .chat-loading p { font-size: 0.875rem; margin: 0; font-weight: 500; }
    .chat-loading .spinner {
        width: 20px;
        height: 20px;
        border-width: 2px;
        border-color: var(--color-accent);
        border-top-color: transparent;
    }
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
    .error-message {
        font-size: 0.8125rem;
        color: var(--color-text-secondary);
        line-height: 1.5;
        white-space: pre-wrap;
        word-break: break-word;
    }
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
        color: white;
        opacity: 0.8;
        white-space: nowrap;
    }
    .advisory-entry {
        padding: 4px 0;
    }
    .advisory-card {
        background: color-mix(in srgb, var(--color-accent) 5%, transparent);
        border-radius: var(--radius-md);
        border-left: 3px solid var(--color-accent);
        padding: 8px 10px;
    }
    .advisory-error {
        background: color-mix(in srgb, var(--color-status-red) 6%, transparent);
        border-left-color: var(--color-status-red);
    }
    .advisory-header {
        display: flex;
        align-items: center;
        gap: 6px;
        margin-bottom: 4px;
        color: var(--color-text-muted);
    }
    .advisory-error .advisory-header { color: var(--color-status-red); }
    .advisory-label {
        font-size: 0.7rem;
        text-transform: uppercase;
        letter-spacing: 0.04em;
    }
    .advisory-text {
        font-size: 0.78rem;
        line-height: 1.45;
        white-space: pre-wrap;
        word-break: break-word;
        color: var(--color-text);
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

    /* Jump to latest button */
    .chat-jump-latest {
        position: absolute;
        bottom: 12px;
        right: 12px;
        z-index: 4;
        display: flex;
        align-items: center;
        gap: 4px;
        padding: 6px 10px;
        min-width: 32px;
        min-height: 32px;
        border-radius: var(--radius-md);
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        color: var(--color-text-secondary);
        cursor: pointer;
        font-size: 0.75rem;
        font-weight: 500;
        box-shadow: 0 2px 8px rgba(0, 0, 0, 0.12);
        transition: opacity 0.2s ease, transform 0.2s ease, background 0.15s ease;
        animation: chat-jump-latest-in 0.2s ease-out;
    }
    .chat-jump-latest:hover {
        background: color-mix(in srgb, var(--color-accent) 100%);
        color: var(--color-text-primary);
    }
    .chat-jump-latest-label {
        line-height: 1;
    }
    @keyframes chat-jump-latest-in {
        from { opacity: 0; transform: translateY(8px); }
        to { opacity: 1; transform: translateY(0); }
    }

    /* Sandbox build/pull progress box — mirrors the Sandbox config modal's build log */
    .build-box {
        padding: 8px 0;
        max-width: 100%;
    }
    .spin {
        animation: spin 1s linear infinite;
    }
    .build-box-header {
        display: flex;
        align-items: center;
        gap: 6px;
        padding: 6px 10px;
        background: var(--color-bg-elevated);
        border: 1px solid var(--color-border);
        border-bottom: none;
        border-radius: var(--radius-sm) var(--radius-sm) 0 0;
        color: var(--color-text-secondary);
        font-size: 0.75rem;
    }
    .build-box-icon {
        flex-shrink: 0;
        color: var(--color-status-yellow);
    }
    .build-box-icon.build-box-ok {
        color: var(--color-status-green, #3fb950);
    }
    .build-box-title {
        font-weight: 500;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }
    .build-box-status {
        margin-left: auto;
        flex-shrink: 0;
        font-size: 0.6875rem;
        color: var(--color-status-green, #3fb950);
        text-transform: uppercase;
        letter-spacing: 0.3px;
    }
    .build-box-log {
        max-height: 180px;
        overflow-y: auto;
        padding: 8px 10px;
        background: var(--color-bg-elevated);
        border: 1px solid var(--color-border);
        border-radius: 0 0 var(--radius-sm) var(--radius-sm);
        font-family: var(--font-mono);
        font-size: 0.7rem;
        line-height: 1.5;
        color: var(--color-text-secondary);
    }
    .build-box-line {
        white-space: pre-wrap;
        word-break: break-all;
    }
    .build-box-pending {
        color: var(--color-text-tertiary);
        font-style: italic;
    }

</style>
