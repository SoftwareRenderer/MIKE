<script lang="ts">
    import ToolCall from './ToolCall.svelte';
    import MessageActions from './MessageActions.svelte';
    import MarkdownBody from './MarkdownBody.svelte';
    import EditForm from './EditForm.svelte';
    import ReasoningBlock from './ReasoningBlock.svelte';
    import MessageStats from './MessageStats.svelte';
    import { tick } from 'svelte';
    import { formatTimestamp } from '$lib/utils/format-time';
    import { escapeHtml } from '$lib/utils/format';
    import { copyText } from '$lib/utils/clipboard';
    import { computeInterleavedLayout } from '$lib/utils/chat-layout';
    import { useIsMobile } from '$lib/composables/use-is-mobile.svelte';
    import { imageSrc } from '$lib/utils/attachments';
    import { systemNoteText, editSubmitAction } from '$lib/utils/chat-entries';
    import type { ToolCallInfo } from '$lib/types';
    import { t } from '$lib/i18n';

    interface Props {
        role: string;
        severity?: string;
        kind?: string;
        content: string;
        reasoningContent?: string;
        hasReasoning?: boolean;
        isStreaming?: boolean;
        isOptimistic?: boolean;
        timestamp?: string;
        messageId?: string;
        attachments?: Array<{ mimeType: string; data: string }>;
        stats?: { model_name: string; prompt_tokens: number; predicted_tokens: number; prompt_ms: number; predicted_ms: number; duration: number };
        toolCalls?: ToolCallInfo[];
        onDelete?: () => void;
        onFork?: (messageId: string, originalContent: string, newContent: string, attachments?: Array<{ mimeType: string; data: string }>) => Promise<void>;
        onSend?: (messageId: string, originalContent: string, newContent: string, attachments?: Array<{ mimeType: string; data: string }>) => Promise<void>;
        onRegenerate?: () => void;
        /** Present on the summarized-context entry: the edit rewrites that text where it stands. */
        onEditSummary?: (content: string) => Promise<void>;
        isLastUserMessage?: boolean;
        isFirstUserMessage?: boolean;
        /** A turn of this task is running, so no row may be edited into it. */
        turnInFlight?: boolean;
        taskStatus?: string;
        onNavigateTask?: (taskId: string) => void;
        queued?: boolean;
        editDiffOverrides?: Record<string, boolean>;
        onToggleEditDiff?: (toolId: string, currentExpanded: boolean) => void;
        projectId?: string;
        taskId?: string;
        reasoningExpandedOverride?: boolean;
        onReasoningToggle?: (expanded: boolean) => void;
        resolvedReasoningOverride?: string;
        onReasoningResolved?: (content: string) => void;
    }

    let { role, severity = '', kind = '', content, reasoningContent, hasReasoning = false, isStreaming = false, isOptimistic = false, timestamp, messageId, attachments, stats, toolCalls = [], onDelete, onFork, onSend, onRegenerate, onEditSummary, isLastUserMessage = false, isFirstUserMessage = false, turnInFlight = false, taskStatus = 'backlog', onNavigateTask, queued = false, editDiffOverrides = {}, onToggleEditDiff = () => {}, projectId = '', taskId: msgTaskId = '', reasoningExpandedOverride, onReasoningToggle, resolvedReasoningOverride, onReasoningResolved }: Props = $props();

    // Edit state
    let isEditing = $state(false);
    let isSubmitting = $state(false);
    let mobileEditDialog: HTMLDialogElement | null = $state(null);
    let copied = $state(false);
    let expandedArgs = $state<Set<string>>(new Set());

    // Mobile detection
    const isMobileCtx = useIsMobile();

    let timestampInfo = $derived(timestamp ? formatTimestamp(timestamp) : { display: '', tooltip: '' });

    // The person's own text is escaped, never parsed: it says what was typed, markdown included.
    let userHtml = $derived(
        role === 'user'
            ? escapeHtml(content).replace(/\n/g, '<br>')
            : ''
    );

    // Resolved reasoning for interleaved layout (fetched on-demand)
    let resolvedReasoningContent = $state<string | undefined>();
    let effectiveReasoning = $derived(resolvedReasoningContent ?? reasoningContent);
    let interleavedLayout = $derived(computeInterleavedLayout(toolCalls, effectiveReasoning, content));

    function toggleArgs(toolId: string) {
        const next = new Set(expandedArgs);
        next.has(toolId) ? next.delete(toolId) : next.add(toolId);
        expandedArgs = next;
    }

    async function handleCopy() {
        copied = await copyText(content);
        if (!copied) return;
        setTimeout(() => copied = false, 2000);
    }

    async function startEdit() {
        isEditing = true;
        if (!isMobileCtx.isMobile) return;
        // The dialog and the form inside it exist after this tick; the form focuses its own field,
        // which is only useful once the dialog is on screen.
        await tick();
        mobileEditDialog?.showModal();
    }

    function cancelEdit() {
        mobileEditDialog?.close();
        isEditing = false;
    }

    /** Submit an edit. An attachment alone is a message, which the form's own submit already allows. */
    async function submitEdit(newContent: string, newAttachments?: Array<{ mimeType: string; data: string }>) {
        const text = newContent.trim();
        const atts = newAttachments?.length ? newAttachments : undefined;
        if (!messageId || (!text && !atts)) return;
        isSubmitting = true;
        try {
            // The summarized context is the one text a turn did not write, so it is rewritten in place.
            if (onEditSummary) {
                await onEditSummary(text);
            } else if (editSubmitAction({ isFirstUserMessage, isLastUserMessage, taskStatus }) === 'fork') {
                await onFork?.(messageId, content, text, atts);
            } else {
                await onSend?.(messageId, content, text, atts);
            }
        } finally {
            isEditing = false;
            isSubmitting = false;
        }
    }

</script>

<div
    class="chat-message"
    class:user={role === 'user'}
    class:assistant={role === 'assistant'}
    class:system={role === 'system'}
    data-message-id={messageId}
>
    {@render getMessageContent()}
</div>

<!-- Mobile edit dialog -->
{#if isMobileCtx.isMobile && isEditing}
    <dialog class="edit-dialog" bind:this={mobileEditDialog} onclick={(e) => { if (e.target === mobileEditDialog) cancelEdit(); }}>
        <div class="edit-modal">
            <div class="edit-modal-header">
                <h3>{t('chat.message.editTitle')}</h3>
                <button class="close-btn" onclick={cancelEdit} aria-label={t('chat.common.close')}>✕</button>
            </div>
            <!-- The one edit form: Ctrl/⌘+Enter saves, Escape cancels, attachments ride along. -->
            {@render editForm('save', true)}
        </div>
    </dialog>
{/if}

{#snippet getMessageContent()}
    {#if role === 'user'}
        {@render userMessage()}
    {:else if role === 'assistant'}
        {@render assistantMessage()}
    {:else}
        {@render systemMessage()}
    {/if}
{/snippet}

{#snippet editForm(submitMode?: 'auto' | 'save', fill = false)}
    <EditForm
        {content}
        {attachments}
        {isFirstUserMessage}
        {isLastUserMessage}
        {taskStatus}
        {projectId}
        taskId={msgTaskId}
        {isSubmitting}
        {submitMode}
        {fill}
        onSubmit={submitEdit}
        onCancel={cancelEdit}
    />
{/snippet}

{#snippet userMessage()}
    <div class="message-wrapper user-wrapper">
        {#if isEditing && !isMobileCtx.isMobile}
            {@render editForm()}
        {:else}
            {#if attachments && attachments.length > 0}
                <div class="message-images">
                    {#each attachments as att}
                        <img src={imageSrc(att)} alt="" class="message-image" />
                    {/each}
                </div>
            {/if}
            <div class="message-row">
                {#if queued}
                    <span class="queued-indicator" title={t('chat.message.queued')}>
                        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                            <circle cx="12" cy="12" r="10"/>
                            <polyline points="12 6 12 12 16 14"/>
                        </svg>
                    </span>
                {/if}
                <div class="message-bubble">
                    <div class="message-text">{@html userHtml}</div>
                </div>
            </div>
            <MessageActions
                variant="user"
                timestampDisplay={timestampInfo.display}
                timestampTooltip={timestampInfo.tooltip}
                onCopy={handleCopy}
                onDelete={!isFirstUserMessage ? onDelete : undefined}
                onFork={turnInFlight ? undefined : startEdit}
                {copied}
            />
        {/if}
    </div>
{/snippet}

{#snippet assistantMessage()}
    <div class="message-wrapper assistant-wrapper">
        {#if isEditing && !isMobileCtx.isMobile}
            {@render editForm(onEditSummary ? 'save' : 'auto')}
        {:else}
            {#if interleavedLayout}
                {@render interleavedContent()}
            {:else}
                {@render standardContent()}
            {/if}
            {#if stats}
                <MessageStats {stats} />
            {/if}
            {#if content || hasReasoning || reasoningContent}
                <MessageActions
                    variant="assistant"
                    timestampDisplay={timestampInfo.display}
                    timestampTooltip={timestampInfo.tooltip}
                    hasContent={!!content}
                    {isStreaming}
                    isOptimistic={isOptimistic}
                    onCopy={handleCopy}
                    onRegenerate={onRegenerate}
                    onEdit={onEditSummary ? startEdit : undefined}
                    onDelete={onDelete}
                    {copied}
                />
            {/if}
        {/if}
    </div>
{/snippet}

{#snippet interleavedContent()}
    {#if hasReasoning || reasoningContent}
        <div class="reasoning-block-interleaved">
            <ReasoningBlock
                reasoningContent={reasoningContent}
                {hasReasoning}
                {messageId}
                {projectId}
                taskId={msgTaskId}
                {isStreaming}
                toolCallsLength={toolCalls.length}
                {content}
                reasoningExpandedOverride={reasoningExpandedOverride}
                onReasoningToggle={onReasoningToggle}
                resolvedReasoningOverride={resolvedReasoningOverride}
                onReasoningResolved={onReasoningResolved}
            />
            <!-- Tool calls within reasoning region always visible -->
            {#each interleavedLayout!.reasoningSegments as seg}
                {#if seg.type === 'tool'}
                    <ToolCall toolCall={seg.tc} {expandedArgs} onToggleArgs={toggleArgs} {editDiffOverrides} onToggleEditDiff={onToggleEditDiff} {onNavigateTask} {projectId} taskId={msgTaskId} />
                {/if}
            {/each}
        </div>
    {/if}
    {#if interleavedLayout!.trailingContent}
        <div class="message-bubble assistant-bubble">
            <div class="message-text" class:streaming={isStreaming}>
                <MarkdownBody content={interleavedLayout!.trailingContent} />
                {#if isStreaming}
                    <span class="cursor-blink"></span>
                {/if}
            </div>
        </div>
    {/if}
    {#if interleavedLayout!.trailingTools && interleavedLayout!.trailingTools.length > 0}
        {#each interleavedLayout!.trailingTools as tc (tc.toolId)}
            <ToolCall toolCall={tc} {expandedArgs} onToggleArgs={toggleArgs} {editDiffOverrides} onToggleEditDiff={onToggleEditDiff} {onNavigateTask} {projectId} taskId={msgTaskId} />
        {/each}
    {/if}
{/snippet}

{#snippet standardContent()}
    <ReasoningBlock
        reasoningContent={reasoningContent}
        {hasReasoning}
        {messageId}
        {projectId}
        taskId={msgTaskId}
        {isStreaming}
        toolCallsLength={toolCalls.length}
        {content}
        reasoningExpandedOverride={reasoningExpandedOverride}
        onReasoningToggle={onReasoningToggle}
        resolvedReasoningOverride={resolvedReasoningOverride}
        onReasoningResolved={onReasoningResolved}
    />
    {#if content}
        <div class="message-bubble assistant-bubble">
            <div class="message-text" class:streaming={isStreaming}>
                <MarkdownBody {content} />
                {#if isStreaming}
                    <span class="cursor-blink"></span>
                {/if}
            </div>
        </div>
    {/if}
    {#if toolCalls && toolCalls.length > 0}
        {#each toolCalls as tc (tc.toolId)}
            <ToolCall toolCall={tc} {expandedArgs} onToggleArgs={toggleArgs} {editDiffOverrides} onToggleEditDiff={onToggleEditDiff} {onNavigateTask} {projectId} taskId={msgTaskId} />
        {/each}
    {/if}
{/snippet}

{#snippet systemMessage()}
    <div class="message-wrapper system-wrapper">
        <div class="system-card" class:system-info={severity === 'info'} class:system-warning={severity === 'warning'}>
            <div class="system-header">
                {#if severity === 'info'}
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                        <circle cx="12" cy="12" r="10"/>
                        <line x1="12" y1="16" x2="12" y2="12"/>
                        <line x1="12" y1="8" x2="12.01" y2="8"/>
                    </svg>
                {:else}
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                        <path d="M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"/>
                        <line x1="12" y1="9" x2="12" y2="13"/>
                        <line x1="12" y1="17" x2="12.01" y2="17"/>
                    </svg>
                {/if}
                <span class="system-code">{t('chat.message.system')}</span>
                {#if onDelete}
                    <button class="btn-dismiss" onclick={onDelete} title={t('chat.message.dismiss')} aria-label={t('chat.message.dismiss')}>
                        <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                            <line x1="18" y1="6" x2="6" y2="18"/>
                            <line x1="6" y1="6" x2="18" y2="18"/>
                        </svg>
                    </button>
                {/if}
            </div>
            <div class="system-message">{systemNoteText(kind, content)}</div>
        </div>
    </div>
{/snippet}

<style>
    @import '$lib/styles/shared.css';

    .chat-message {
        padding: 12px 0;
    }
    .message-wrapper {
        display: flex;
        flex-direction: column;
        gap: 4px;
    }
    .user-wrapper {
        align-items: flex-end;
    }
    .message-images {
        display: flex;
        gap: 6px;
        flex-wrap: wrap;
        justify-content: flex-end;
        max-width: 80%;
    }
    .message-image {
        max-width: 160px;
        max-height: 160px;
        border-radius: var(--radius-lg);
        object-fit: cover;
        border: 1px solid var(--color-border);
    }
    .assistant-wrapper {
        align-items: flex-start;
    }
    .system-wrapper {
        align-items: flex-start;
    }
    .message-bubble {
        max-width: 80%;
        background: color-mix(in srgb, var(--color-accent) 18%, transparent);
        border-radius: 1.125rem;
        padding: 8px 14px;
        display: flex;
        align-items: flex-end;
        gap: 6px;
        flex-shrink: 0;
    }
    .message-row {
        display: flex;
        align-items: center;
        gap: 8px;
        justify-content: flex-end;
        width: 100%;
    }
    .queued-indicator {
        flex-shrink: 0;
        color: var(--color-warning, #f59e0b);
        display: inline-flex;
        align-items: center;
        gap: 4px;
        font-size: var(--text-xs);
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.04em;
    }
    .assistant .message-bubble {
        max-width: 100%;
        background: var(--color-surface-3);
        border-radius: 1.125rem;
        padding: 10px 14px;
        border: 1px solid var(--color-border);
    }
    .message-text {
        font-size: var(--text-base);
        line-height: 1.875rem;
        word-break: break-word;
        white-space: pre-wrap;
    }
    .user .message-text {
        color: var(--color-text-primary);
    }
    .assistant .message-text {
        color: var(--color-text-primary);
        white-space: normal;
        min-width: 0;
        overflow: hidden;
    }
    .assistant .message-text.streaming {
        border-left: 2px solid var(--color-accent);
        padding-left: 12px;
    }
    .system-card {
        background: color-mix(in srgb, var(--color-status-red) 6%, transparent);
        border: 1px solid color-mix(in srgb, var(--color-status-red) 30%, transparent);
        border-left: 3px solid var(--color-status-red);
        border-radius: var(--radius-md);
        padding: 10px 14px;
        max-width: 80%;
    }
    .system-card.system-info {
        background: color-mix(in srgb, var(--color-surface-3) 70%, transparent);
        border: 1px solid var(--color-border);
        border-left: 3px solid var(--color-text-tertiary);
    }
    .system-card.system-warning {
        background: color-mix(in srgb, var(--color-status-yellow) 8%, transparent);
        border: 1px solid color-mix(in srgb, var(--color-status-yellow) 32%, transparent);
        border-left: 3px solid var(--color-status-yellow);
    }
    .system-card.system-warning .system-header {
        color: var(--color-status-yellow);
    }
    .system-header {
        display: flex;
        align-items: center;
        gap: 6px;
        margin-bottom: 4px;
        color: var(--color-status-red);
    }
    .system-card.system-info .system-header {
        color: var(--color-text-tertiary);
    }
    .system-code {
        font-size: var(--text-xs);
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.3px;
    }
    .system-header .btn-dismiss {
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
    .system-header .btn-dismiss:hover {
        background: color-mix(in srgb, var(--color-status-red) 15%, transparent);
        color: var(--color-status-red);
    }
    .system-message {
        font-size: var(--text-sm);
        color: var(--color-text-secondary);
        line-height: 1.5;
        white-space: pre-wrap;
        word-break: break-word;
    }
    .reasoning-block-interleaved {
        margin-bottom: 6px;
    }
    .assistant .message-text :global(pre) {
        background: var(--color-surface-0);
        padding: 10px 14px;
        border-radius: var(--radius-md);
        overflow-x: auto;
        margin: 8px 0;
        font-size: var(--text-sm);
        line-height: 1.5rem;
    }
    .assistant .message-text :global(code) {
        background: var(--color-surface-0);
        padding: 2px 6px;
        border-radius: var(--radius-sm);
        font-size: var(--text-sm);
        line-height: 1.25rem;
        font-family: var(--font-mono);
    }
    .assistant .message-text :global(pre code) {
        background: none;
        padding: 0;
    }
    .assistant .message-text :global(p) {
        margin: 0.75rem 0;
        line-height: 1.7;
    }
    .assistant .message-text :global(p:first-child) {
        margin-top: 0;
    }
    .assistant .message-text :global(p:last-child) {
        margin-bottom: 0;
    }
    .assistant .message-text :global(h1),
    .assistant .message-text :global(h2),
    .assistant .message-text :global(h3),
    .assistant .message-text :global(h4),
    .assistant .message-text :global(h5),
    .assistant .message-text :global(h6) {
        margin: 1rem 0 0.5rem;
        font-weight: 600;
        line-height: 1.4;
        color: var(--color-text-primary);
    }
    .assistant .message-text :global(h1) { font-size: 1.43rem; }
    .assistant .message-text :global(h2) { font-size: 1.29rem; }
    .assistant .message-text :global(h3) { font-size: 1.14rem; }
    .assistant .message-text :global(h4) { font-size: 1.07rem; }
    .assistant .message-text :global(h5) { font-size: var(--text-lg); }
    .assistant .message-text :global(h6) { font-size: var(--text-lg); color: var(--color-text-secondary); }
    .assistant .message-text :global(h1:first-child),
    .assistant .message-text :global(h2:first-child),
    .assistant .message-text :global(h3:first-child) {
        margin-top: 0;
    }
    .assistant .message-text :global(hr) {
        border: none;
        border-top: 1px solid var(--color-border);
        margin: 1.25rem 0;
    }
    .assistant .message-text :global(a) {
        color: var(--color-accent);
        text-decoration: underline;
        text-underline-offset: 2px;
    }
    .assistant .message-text :global(a:hover) {
        color: var(--color-accent-light);
    }
    .assistant .message-text :global(ul),
    .assistant .message-text :global(ol) {
        margin: 0.75rem 0;
        padding-left: 1.5rem;
        white-space: normal;
        line-height: 1.7;
    }
    .assistant .message-text :global(ul:first-child),
    .assistant .message-text :global(ol:first-child) {
        margin-top: 0;
    }
    .assistant .message-text :global(ul:last-child),
    .assistant .message-text :global(ol:last-child) {
        margin-bottom: 0;
    }
    .assistant .message-text :global(li) {
        margin: 0.125rem 0;
        line-height: 1.7;
        white-space: normal;
    }
    .assistant .message-text :global(li > p) {
        margin: 0.25rem 0;
        line-height: 1.7;
    }
    .assistant .message-text :global(blockquote) {
        border-left: 3px solid var(--color-border);
        padding-left: 12px;
        margin: 8px 0;
        color: var(--color-text-secondary);
    }
    .assistant .message-text :global(table) {
        border-collapse: collapse;
        width: 100%;
        margin: 0.75rem 0;
        font-size: var(--text-base);
        line-height: 1.5rem;
        display: block;
        overflow-x: auto;
    }
    .assistant .message-text :global(table:first-child) {
        margin-top: 0;
    }
    .assistant .message-text :global(table:last-child) {
        margin-bottom: 0;
    }
    .assistant .message-text :global(thead) {
        background: var(--color-surface-2);
    }
    .assistant .message-text :global(th) {
        font-weight: 600;
        text-align: left;
        padding: 6px 12px;
        min-width: 80px;
        border: 1px solid var(--color-border);
        color: var(--color-text-primary);
    }
    .assistant .message-text :global(td) {
        padding: 6px 12px;
        min-width: 80px;
        border: 1px solid var(--color-border);
        color: var(--color-text-primary);
        vertical-align: top;
    }
    .assistant .message-text :global(tbody tr) {
        background: var(--color-surface-1);
    }
    .cursor-blink {
        display: inline-block;
        width: 2px;
        height: 14px;
        background: var(--color-accent);
        margin-left: 2px;
        vertical-align: text-bottom;
        animation: blink 0.8s step-end infinite;
    }
    @keyframes blink {
        0%, 100% { opacity: 1; }
        50% { opacity: 0; }
    }

    /* Mobile edit dialog. A modal `<dialog>` paints in the top layer, so this rank only orders it
       against other top-layer boxes — it is not why the dialog covers a toast. */
    .edit-dialog {
        position: fixed;
        inset: 0;
        width: 100vw;
        height: 100dvh;
        max-width: none;
        max-height: none;
        padding: 0;
        border: none;
        background: transparent;
        z-index: var(--z-modal);
        display: flex;
        align-items: center;
        justify-content: center;
    }
    .edit-dialog::backdrop {
        background: rgba(0, 0, 0, 0.6);
    }
    .edit-modal {
        background: var(--color-surface-1);
        width: 100vw;
        max-width: none;
        height: 100dvh;
        display: flex;
        flex-direction: column;
        color: var(--color-text-primary);
    }
    .edit-modal-header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 16px;
        border-bottom: 1px solid var(--color-border);
    }
    .edit-modal-header h3 {
        margin: 0;
        font-size: var(--text-lg);
        font-weight: 600;
    }
    .close-btn {
        background: none;
        border: none;
        color: var(--color-text-secondary);
        font-size: var(--text-lg);
        cursor: pointer;
        padding: 4px;
    }
</style>
