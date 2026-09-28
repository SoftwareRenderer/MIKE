<script lang="ts">
    import { tick } from 'svelte';
    import type { Thread, ChatEntry, LockWait, ChatTurn } from '$lib/types';
    import { advisoryLabel, type EntryMeta } from '$lib/utils/chat-entries';
    import { computeStickyHeader, turnLabel, type StickyTurn } from '$lib/utils/chat-sticky-header';
    import { scrollToLine } from '$lib/utils/settle-scroll';
    import { isAtBottom, type ChatFollow } from '$lib/composables/use-chat-follow.svelte';
    import { atTopOfList, type WindowReadNotice } from '$lib/utils/chat-window';
    import ChatMessage from './ChatMessage.svelte';
    import CheckpointDivider from './CheckpointDivider.svelte';
    import QueueIndicator from './QueueIndicator.svelte';
    import Spinner from '$lib/components/Spinner.svelte';
    import EmptyState from '$lib/components/EmptyState.svelte';
    import { appStore } from '$lib/stores';
    import { fullscreenToggleTitle } from '$lib/utils/keyboard';
    import { t } from '$lib/i18n';

    let {
        entries,
        entryMetas,
        lastEntryIdx,
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
        turnInFlight = false,
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
        onSummaryEdit,
        onViewChanges,
        onToggleFullscreen,
        isChatFullscreen,
        follow,
        historyLoading = false,
        historyNotice = null,
        onRetryHistory,
        hasEarlier = false,
        loadingEarlier = false,
        onLoadEarlier,
        turns = [],
        turnsHasMore = false,
        onJumpToTurn,
    }: {
        entries: ChatEntry[];
        entryMetas: EntryMeta[];
        lastEntryIdx: number;
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
        /** A turn of this task is running, so no row may be edited into it. */
        turnInFlight?: boolean;
        projectId: string;
        taskId: string;
        onDismissError: (messageId: string) => Promise<void>;
        onThreadSwitch: (threadId: string) => Promise<void>;
        isSwitching?: boolean;
        onMsgDelete: (entry: ChatEntry) => void;
        onMsgFork: (msgId: string, origContent: string, newContent: string, atts?: Array<{ mimeType: string; data: string }>) => Promise<void>;
        onMsgSend: (msgId: string, origContent: string, newContent: string, atts?: Array<{ mimeType: string; data: string }>) => Promise<void>;
        /** Rewrites the summarized context. Offered on one entry only — see `editableSummaryId`. */
        onSummaryEdit?: (msgId: string, newContent: string) => Promise<void>;
        onRegenerate: (entryId: string) => Promise<void>;
        onNavigateTask: (targetTaskId: string) => void;
        onViewChanges?: (commitHash: string) => void;
        onToggleFullscreen?: () => void;
        isChatFullscreen?: boolean;
        /** Shared follow-the-bottom tracker (created by TaskChat). */
        follow: ChatFollow;
        historyLoading?: boolean;
        /** Older rows exist beyond the window on screen; scrolling to the top edge asks for them. */
        hasEarlier?: boolean;
        loadingEarlier?: boolean;
        /** Why what is on screen may not be the newest copy of the conversation. `'pending'` is not a fault:
         * the rows are this browser's own and the read that replaces them is still out. */
        historyNotice?: WindowReadNotice;
        /** Ask the server again — what the notice offers beside the fact it reports. */
        onRetryHistory?: () => void;
        /** Fetch the next-older window, called when the reader reaches the top edge. Whether rows
         * arrived is the jump's business; the top edge ignores the answer. */
        onLoadEarlier?: () => Promise<unknown>;
        /** The prompts the header can name, oldest first: what the prompt read answers. */
        turns?: ChatTurn[];
        /** Older prompts exist beyond that list. */
        turnsHasMore?: boolean;
        /** Fetch until the prompt the header names is on screen. True once it is. */
        onJumpToTurn?: (entryId: string) => Promise<boolean>;
    } = $props();

    function isOptimisticId(id: string): boolean {
        return /^[a-z0-9]{8}$/.test(id);
    }

    // Only one summary is worth editing: a rolling summarization appends a row and the model reads the
    // newest one, so anything older is history. An entry with an optimistic id is not a row at all —
    // agent-mode compaction hands the summary straight to the client — so it has nothing to rewrite.
    const editableSummaryId = $derived.by(() => {
        if (!onSummaryEdit) return null;
        for (let i = entries.length - 1; i >= 0; i--) {
            const entry = entries[i];
            if (entry.role !== 'summarization') continue;
            return isOptimisticId(entry.id) ? null : entry.id;
        }
        return null;
    });

    // The callback's one promise is what the prop asks for; the optional prop is why the fallback exists.
    function editSummary(msgId: string, newContent: string): Promise<void> {
        return onSummaryEdit?.(msgId, newContent) ?? Promise.resolve();
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
    let stickyNeedsRows = $state(false);
    let stickyEntryId = $state('');
    // The named prompt's row is being fetched: the header says so rather than look idle.
    let stickyJumping = $state(false);
    // The overlay hangs over the top of the scroller, so it decides where a message
    // has to land to be readable rather than behind it.
    let stickyHeaderEl: HTMLElement | null = $state(null);
    let tapped: { entryId: string; intent: number } | null = null;

    /**
     * The prompts the header can name: the rendered ones, plus the older ones from the prompt read that no
     * rendered row holds.
     */
    function stickyTurns(el: HTMLElement, containerTop: number, scrollTop: number): StickyTurn[] {
        const rendered: StickyTurn[] = [];
        for (const node of Array.from(el.querySelectorAll<HTMLElement>('.sticky-user-message'))) {
            const entryIdx = Number(node.dataset.entryIdx);
            if (!Number.isInteger(entryIdx)) continue;
            const rect = node.getBoundingClientRect();
            // Convert viewport-space rects to content space.
            rendered.push({
                entryId: node.dataset.entryId ?? '',
                entryIdx,
                firstLine: node.dataset.userMsgFirstLine ?? '',
                top: rect.top - containerTop + scrollTop,
                bottom: rect.bottom - containerTop + scrollTop,
            });
        }
        const onScreen = new Set(rendered.map(turn => turn.entryId));
        const earlier = turns
            .filter(turn => !onScreen.has(turn.id))
            .map(turn => ({ entryId: turn.id, entryIdx: -1, firstLine: turnLabel(turn.text), top: 0, bottom: 0 }));
        // Unrendered prompts are always the older end: the window holds the newest rows of the thread.
        return [...earlier, ...rendered];
    }

    // the overlay's bottom edge
    function stickyLineOffset(): number | null {
        const scroller = chatScrollEl;
        const header = stickyHeaderEl;
        if (!scroller || !header) return null;
        const containerBox = scroller.getBoundingClientRect();
        if (containerBox.height <= 0) return null;
        const line = header.getBoundingClientRect().bottom - containerBox.top;
        return line > 0 ? line : null;
    }

    function computeStickyFromScroll() {
        const el = chatScrollEl;
        // No measurable line means no choice — a hidden column and an empty chat both arrive here.
        const line = stickyLineOffset();
        if (!el || line === null) {
            computedStickyMessage = '';
            stickyNeedsRows = false;
            stickyEntryId = '';
            computedShowHeader = false;
            return;
        }
        const containerTop = el.getBoundingClientRect().top;
        const scrollTop = el.scrollTop;

        if (tapped && (follow.intent !== tapped.intent || follow.atBottom)) tapped = null;
        // Without a prompt read the row window is all the header knows about what lies above, and
        // `hasEarlier` is as much as it can claim.
        const olderTurnsBeyond = turnsHasMore || (turns.length === 0 && hasEarlier);
        const sticky = computeStickyHeader(
            stickyTurns(el, containerTop, scrollTop), scrollTop, line, tapped?.entryId ?? '', olderTurnsBeyond,
        );
        computedStickyMessage = sticky.firstLine;
        stickyNeedsRows = sticky.needsRows;
        stickyEntryId = sticky.entryId;
        computedShowHeader = sticky.show;
    }

    /** Land the message the header names on the sticky line, under the overlay. */
    async function handleStickyClickInternal() {
        const scroller = chatScrollEl;
        if (!scroller || stickyEntryId === '' || stickyJumping) return;
        // Declare the takeover before anything moves, or the follow tracker's pin loop and the settle
        // loop of a page that lands write scrollTop against us.
        follow.stopFollowing();
        tapped = { entryId: stickyEntryId, intent: follow.intent };
        if (stickyNeedsRows) {
            // The prompt is older than every row held: fetch until its row is on screen.
            stickyJumping = true;
            try {
                const landed = await (onJumpToTurn?.(stickyEntryId) ?? Promise.resolve(false));
                if (!landed) return;
                await tick();
            } finally {
                stickyJumping = false;
            }
        }
        // The message, not its `.chat-entry`: a checkpoint divider above the message
        // lives in the entry, so the entry starts a divider-height too early.
        const target = scroller.querySelector<HTMLElement>(`.sticky-user-message[data-entry-id="${CSS.escape(stickyEntryId)}"]`);
        if (!target) return;
        const line = stickyLineOffset();
        if (line === null) return;
        scrollToLine(scroller, target, line);
        // A tap that moves nothing fires no scroll event, so update the header here too.
        computeStickyFromScroll();
    }

    /** One scroll listener for both jobs: shared follow state + sticky header. */
    function handleCombinedScroll() {
        follow.handleScroll();
        computeStickyFromScroll();
        if (chatScrollEl && hasEarlier && !loadingEarlier && atTopOfList(chatScrollEl.scrollTop, chatScrollEl.clientHeight)) {
            void onLoadEarlier?.();
        }
    }

    // Recompute the sticky header when entries change, and keep the viewport
    // pinned while the user is following (pin() is a no-op once they scroll up).
    $effect(() => {
        entries.length;
        // A prompt read lands labels the rows on screen cannot carry, and nothing else wakes this.
        void turns;
        // A turn arriving or an older page landing changes which message is the previous one.
        tapped = null;
        computeStickyFromScroll();
        follow.pin();
    });

    /**
     * Layout moves what is above the line without a scroll event: the keyboard or auto-maximize resizing the
     * column, a tab switch, entries reaching their real height. The follow tracker watches these nodes too, but
     * for who owns `scrollTop`.
     */
    $effect(() => {
        const nodes = [chatScrollEl, chatContentEl, stickyHeaderEl];
        if (typeof ResizeObserver === 'undefined') return;
        let frame = 0;
        const observer = new ResizeObserver(() => {
            // A landing page of entries resizes on every row that realizes.
            if (frame) return;
            frame = requestAnimationFrame(() => { frame = 0; computeStickyFromScroll(); });
        });
        for (const node of nodes) if (node) observer.observe(node);
        return () => {
            if (frame) cancelAnimationFrame(frame);
            observer.disconnect();
        };
    });

    /** Force a pin: the button is explicit intent, even if state says otherwise. */
    function handleJumpLatest() {
        follow.pin(true);
    }
</script>

<div class="chat-messages-wrapper" style={!follow.ready ? 'opacity: 0;' : ''}>
    {#if entries.length > 0}
        <!-- Mounted whenever there are entries and hidden by class: the sticky line is this element's
             measured bottom edge. -->
        <div class="chat-sticky-header" class:hidden-header={!computedShowHeader} bind:this={stickyHeaderEl}
             role="status" aria-live="polite" aria-busy={stickyJumping || undefined}>
            <button class="chat-sticky-bubble" onclick={handleStickyClickInternal} title={t('chat.entries.scrollToMessage')} aria-label={t('chat.entries.scrollToMessage')}>
                {#if stickyJumping}<Spinner decorative />{/if}
                <span class="chat-sticky-text">{computedStickyMessage}</span>
            </button>
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

    {#if historyNotice === 'cached' || historyNotice === 'failed' || historyNotice === 'owing'}
        <p class="history-notice" role="status">
            {historyNotice === 'cached'
                ? t('chat.entries.historyCached')
                : historyNotice === 'owing'
                    ? t('chat.entries.historyOwing')
                    : t('chat.entries.historyFailed')}
            {#if onRetryHistory}
                <button class="history-notice-retry" onclick={() => onRetryHistory()}>
                    {t('chat.entries.historyRetry')}
                </button>
            {/if}
        </p>
    {/if}

    {#if entries.length === 0}
        <div class="chat-messages" bind:this={chatScrollEl} role="region" aria-label={t('chat.entries.region')} tabindex="-1">
            {#if historyLoading}
                <div class="chat-loading">
                    <Spinner size={20} decorative />
                    <p>{t('chat.entries.loading')}</p>
                </div>
            {:else}
                <EmptyState class="parent-height" title={t('chat.entries.empty')} hint={t('chat.entries.emptyHint')} />
            {/if}
        </div>
    {:else}
        <div class="chat-messages" bind:this={chatScrollEl} role="region" aria-label={t('chat.entries.region')} tabindex="-1"
             aria-busy={loadingEarlier} onscroll={handleCombinedScroll}>
          <div class="chat-content" bind:this={chatContentEl}>
            {#each entries as entry, entryIdx (entry.id)}
                {@const meta = entryMetas[entryIdx] as EntryMeta}
                <div class="chat-entry" data-entry-id={entry.id}>
                {#if entry.role === 'user'}
                    {#if meta.cpThread || meta.cpState}
                        <div>
                            <CheckpointDivider
                                timestamp={entry.createdAt}
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
                        <ChatMessage role="assistant" content={entry.content} reasoningContent={entry.reasoningContent} hasReasoning={entry.hasReasoning} isStreaming={entry.isStreaming || false} isOptimistic={isOptimisticId(entry.id)} timestamp={entry.createdAt} messageId={entry.id} attachments={entry.attachments} stats={entry.stats} toolCalls={entry.toolCalls || []} onDelete={() => onMsgDelete(entry)} isLastUserMessage={false} isFirstUserMessage={false} taskStatus={taskStatus} onNavigateTask={onNavigateTask} queued={entry.queued || false} {editDiffOverrides} onToggleEditDiff={handleEditDiffToggle} projectId={projectId} taskId={taskId} reasoningExpandedOverride={reasoningExpandedOverrides[entry.id]} onReasoningToggle={(exp) => handleReasoningToggle(entry.id, exp)} resolvedReasoningOverride={resolvedReasoningOverrides[entry.id]} onReasoningResolved={(content) => handleReasoningResolved(entry.id, content)} onEditSummary={entry.id === editableSummaryId ? (newContent) => editSummary(entry.id, newContent) : undefined} />
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
                    <div class="sticky-user-message" data-entry-idx={entryIdx} data-entry-id={entry.id} data-user-msg-first-line={turnLabel(entry.content)}>
                        <ChatMessage role={entry.role} content={entry.content} reasoningContent={entry.reasoningContent} hasReasoning={entry.hasReasoning} isStreaming={entry.isStreaming || false} isOptimistic={isOptimisticId(entry.id)} timestamp={entry.createdAt} messageId={entry.id} attachments={entry.attachments} stats={entry.stats} toolCalls={entry.toolCalls || []} onDelete={() => onMsgDelete(entry)} onFork={onMsgFork} onSend={onMsgSend} isLastUserMessage={meta.isLastUserMessage} isFirstUserMessage={meta.isFirstUserMessage} turnInFlight={turnInFlight} taskStatus={taskStatus} onRegenerate={entryIdx === lastEntryIdx ? () => onRegenerate(entry.id) : undefined} onNavigateTask={onNavigateTask} queued={entry.queued || false} {editDiffOverrides} onToggleEditDiff={handleEditDiffToggle} projectId={projectId} taskId={taskId} reasoningExpandedOverride={reasoningExpandedOverrides[entry.id]} onReasoningToggle={(exp) => handleReasoningToggle(entry.id, exp)} resolvedReasoningOverride={resolvedReasoningOverrides[entry.id]} onReasoningResolved={(content) => handleReasoningResolved(entry.id, content)} />
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
                                <span class="build-box-icon"><Spinner decorative /></span>
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
                        <ChatMessage role={entry.role} severity={entry.severity} kind={entry.kind} content={entry.content} reasoningContent={entry.reasoningContent} hasReasoning={entry.hasReasoning} isStreaming={entry.isStreaming || false} isOptimistic={isOptimisticId(entry.id)} timestamp={entry.createdAt} messageId={entry.id} attachments={entry.attachments} stats={entry.stats} toolCalls={entry.toolCalls || []} onDelete={entry.role === 'system' ? () => onDismissError(entry.id) : () => onMsgDelete(entry)} onFork={onMsgFork} onSend={onMsgSend} isLastUserMessage={meta.isLastUserMessage} isFirstUserMessage={meta.isFirstUserMessage} turnInFlight={turnInFlight} taskStatus={taskStatus} onRegenerate={entryIdx === lastEntryIdx ? () => onRegenerate(entry.id) : undefined} onNavigateTask={onNavigateTask} queued={entry.queued || false} {editDiffOverrides} onToggleEditDiff={handleEditDiffToggle} projectId={projectId} taskId={taskId} reasoningExpandedOverride={reasoningExpandedOverrides[entry.id]} onReasoningToggle={(exp) => handleReasoningToggle(entry.id, exp)} resolvedReasoningOverride={resolvedReasoningOverrides[entry.id]} onReasoningResolved={(content) => handleReasoningResolved(entry.id, content)} />
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

        {#if historyNotice === 'pending'}
            <!-- The rows on screen are this browser's own copy and the read that replaces them is still out.
                 Out of the scroll region's flow, and only its opacity animates: a row must not move because
                 this appeared or went. It floats at the opposite corner from the jump button, so a reader who
                 scrolls away while the read is out gets both. -->
            <div class="chat-pending" role="status" aria-label={t('chat.entries.loading')}>
                <Spinner decorative />
            </div>
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
    /* Every entry stays in the DOM; the browser skips painting ones far off-screen. `auto` keeps each
       entry's last size so scroll height stays stable, and 64px is the estimate before that. */
    .chat-entry {
        content-visibility: auto;
        contain-intrinsic-size: auto 64px;
    }
    .chat-sticky-header {
        position: absolute;
        top: 0;
        left: 0;
        z-index: var(--z-sticky);
        display: flex;
        justify-content: flex-end;
        align-items: center;
        padding: 6px 12px;
        background: var(--color-surface-1);
        border-bottom: 1px solid var(--color-border);
        width: calc(100% - 14px);
        border-bottom-right-radius: var(--radius-bubble);
        /* Only its own controls take a click, so what lies under it stays reachable. */
        pointer-events: none;
    }
    /* Hidden but laid out: the sticky line is this element's measured height. */
    .chat-sticky-header.hidden-header {
        visibility: hidden;
    }
    .chat-sticky-bubble {
        display: inline-flex;
        align-items: center;
        gap: 6px;
        font-size: var(--text-sm);
        pointer-events: auto;
        color: var(--color-text-primary);
        background: color-mix(in srgb, var(--color-accent) 18%, transparent);
        border-radius: var(--radius-bubble);
        padding: 6px 12px;
        max-width: 80%;
        border: none;
        cursor: pointer;
        font: inherit;
        text-align: left;
        transition: background 0.15s ease;
    }
    /* A flex item ellipsizes only with a 0 floor of its own. */
    .chat-sticky-text {
        min-width: 0;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }
    .chat-sticky-bubble:hover {
        opacity: 1;
        background: color-mix(in srgb, var(--color-accent) 28%, transparent);
    }
    .chat-sticky-fullscreen {
        display: flex;
        pointer-events: auto;
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
        border-radius: var(--radius-sm);
        flex-shrink: 0;
    }
    .chat-sticky-fullscreen:hover {
        opacity: 1;
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }
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
    .chat-loading p { font-size: var(--text-base); margin: 0; font-weight: 500; }
    .history-notice {
        margin: 0;
        padding: 6px 12px;
        flex-shrink: 0;
        font-size: var(--text-xs);
        color: var(--color-warning);
        background: color-mix(in srgb, var(--color-warning) 12%, transparent);
    }
    .history-notice-retry {
        margin-left: 8px;
        padding: 0 4px;
        border: none;
        border-radius: var(--radius-sm);
        background: none;
        color: inherit;
        font: inherit;
        font-weight: 600;
        text-decoration: underline;
        cursor: pointer;
    }
    .history-notice-retry:hover {
        opacity: 1;
        background: color-mix(in srgb, var(--color-warning) 18%, transparent);
    }
    @keyframes pulse { 0%, 100% { opacity: 1; } 50% { opacity: 0.3; } }
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
        border-radius: var(--radius-sm);
        transition: all 0.15s ease;
    }
    .btn-dismiss:hover {
        opacity: 1;
        background: color-mix(in srgb, var(--color-status-red) 15%, transparent);
        color: var(--color-status-red);
    }
    .error-code {
        font-size: var(--text-xs);
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.3px;
    }
    .error-message {
        font-size: var(--text-sm);
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
        font-size: var(--text-xs);
        color: var(--color-text-primary);
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
        font-size: var(--text-xs);
        text-transform: uppercase;
        letter-spacing: 0.04em;
    }
    .advisory-text {
        font-size: var(--text-xs);
        line-height: 1.45;
        white-space: pre-wrap;
        word-break: break-word;
        color: var(--color-text-primary);
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
        font-size: var(--text-xs);
        font-weight: 600;
        color: var(--color-status-green);
    }
    .merge-resolution-content {
        padding: 4px 12px 10px;
        font-size: var(--text-sm);
        color: var(--color-text-secondary);
        line-height: 1.5;
        white-space: pre-wrap;
    }

    /* Jump to latest button. It and `.chat-pending` share the content rank and own opposite bottom
       corners, so which one wins a tie is never visible. */
    .chat-jump-latest {
        position: absolute;
        bottom: 12px;
        right: 12px;
        z-index: var(--z-content);
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
        font-size: var(--text-xs);
        font-weight: 500;
        box-shadow: 0 2px 8px rgba(0, 0, 0, 0.12);
        transition: opacity 0.2s ease, transform 0.2s ease, background 0.15s ease;
        animation: chat-jump-latest-in 0.2s ease-out;
    }
    .chat-jump-latest:hover {
        opacity: 1;
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

    /* The wait for a window read, floating over a transcript this browser drew from its own cache.
       Absolute rather than in the flex column: the rows above it keep their offsets while it is on screen.
       Bottom-left, where the jump button never reaches — it owns bottom-right, and it appears only once the
       reader has scrolled away from the bottom this paint pins to. */
    .chat-pending {
        position: absolute;
        bottom: 12px;
        left: 12px;
        z-index: var(--z-content);
        display: flex;
        padding: 7px 9px;
        border-radius: var(--radius-md);
        border: 1px solid var(--color-border);
        background: var(--color-surface-1);
        box-shadow: 0 2px 8px rgba(0, 0, 0, 0.12);
        /* Nothing to click here, and it floats over the transcript: a click must reach the entry under it. */
        pointer-events: none;
        animation: chat-pending-in 0.2s ease-out;
    }
    @keyframes chat-pending-in { from { opacity: 0; } to { opacity: 1; } }

    /* Sandbox build/pull progress box — mirrors the Sandbox config modal's build log */
    .build-box {
        padding: 8px 0;
        max-width: 100%;
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
        font-size: var(--text-xs);
    }
    .build-box-icon {
        flex-shrink: 0;
        color: var(--color-status-yellow);
    }
    .build-box-icon.build-box-ok {
        color: var(--color-status-green);
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
        font-size: var(--text-2xs);
        color: var(--color-status-green);
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
        font-size: var(--text-xs);
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
