<script lang="ts">
    import { marked } from 'marked';
    import DOMPurify from 'dompurify';
    import ToolCall from './ToolCall.svelte';
    import MessageActions from './MessageActions.svelte';
    import EditForm from './EditForm.svelte';
    import ReasoningBlock from './ReasoningBlock.svelte';
    import MessageStats from './MessageStats.svelte';
    import { formatTimestamp } from '$lib/utils/format-time';
    import { useIsMobile } from '$lib/composables/use-is-mobile.svelte';

    interface ToolCallInfo {
        toolId: string;
        toolName: string;
        displayName?: string;
        args: Record<string, unknown>;
        raw_args?: string;
        sandbox?: boolean;
        path?: string;
        result?: string;
        resultTruncated?: boolean;
        originalLines?: number;
        originalBytes?: number;
        messageId?: string;
        success?: boolean;
        duration?: number;
        isPending?: boolean;
        bytesWritten?: number;
        linesAdded?: number;
        linesRemoved?: number;
        contentIndex?: number;
        reasoningIndex?: number;
    }

    interface Props {
        role: string;
        severity?: string;
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
        formatToolArgs?: (toolName: string, args: Record<string, unknown>) => string;
        onCopy?: () => void;
        onDelete?: () => void;
        onFork?: (messageId: string, originalContent: string, newContent: string, attachments?: Array<{ mimeType: string; data: string }>) => Promise<void>;
        onSend?: (messageId: string, originalContent: string, newContent: string, attachments?: Array<{ mimeType: string; data: string }>) => Promise<void>;
        onEdit?: (messageId: string, newContent: string) => Promise<void>;
        onRegenerate?: () => void;
        isLastUserMessage?: boolean;
        isFirstUserMessage?: boolean;
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

    let { role, severity = '', content, reasoningContent, hasReasoning = false, isStreaming = false, isOptimistic = false, timestamp, messageId, attachments, stats, toolCalls = [], onCopy, onDelete, onFork, onSend, onEdit, onRegenerate, isLastUserMessage = false, isFirstUserMessage = false, taskStatus = 'backlog', onNavigateTask, queued = false, editDiffOverrides = {}, onToggleEditDiff = () => {}, projectId = '', taskId: msgTaskId = '', reasoningExpandedOverride, onReasoningToggle, resolvedReasoningOverride, onReasoningResolved }: Props = $props();

    // Edit state
    let isEditing = $state(false);
    let isSubmitting = $state(false);
    let mobileEditTextarea: HTMLTextAreaElement | null = $state(null);
    let mobileEditDialog: HTMLDialogElement | null = $state(null);
    let copied = $state(false);
    let expandedArgs = $state<Set<string>>(new Set());

    // Mobile detection
    const isMobileCtx = useIsMobile();

    // Timestamp info
    let timestampInfo = $derived(timestamp ? formatTimestamp(timestamp) : { display: '', tooltip: '' });

    // Memoized markdown / HTML rendering — avoids re-parsing on unrelated state changes
    const renderCache = new Map<string, string>();
    const CACHE_MAX = 100;

    function memoizedRender(role: string, content: string): string {
        const key = role + '\x00' + content;
        const cached = renderCache.get(key);
        if (cached !== undefined) return cached;
        let result: string;
        if (role === 'user') {
            result = content.replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/\n/g, '<br>');
        } else {
            result = DOMPurify.sanitize(marked.parse(content, { async: false }) as string);
        }
        if (renderCache.size >= CACHE_MAX) {
            const firstKey = renderCache.keys().next().value;
            if (firstKey) renderCache.delete(firstKey);
        }
        renderCache.set(key, result);
        return result;
    }

    // Markdown rendering
    let renderedHtml = $derived(
        (role === 'assistant' || role === 'system')
            ? memoizedRender(role, content || '')
            : ''
    );

    let userHtml = $derived(
        role === 'user'
            ? memoizedRender('user', content)
            : ''
    );

    /** Build interleaved segments for reasoning-region. Returns layout or null. */
    type Segment = { type: 'reasoning', text: string } | { type: 'tool', tc: ToolCallInfo };

    function _computeLayout(tcs: ToolCallInfo[] | undefined, reasoning: string | undefined, txt: string | undefined): { reasoningSegments: Segment[]; trailingContent: string; trailingTools: ToolCallInfo[] } | null {
        if (!tcs || tcs.length === 0) return null;
        const hasPositionData = tcs.some(tc => tc.contentIndex !== undefined && tc.reasoningIndex !== undefined);
        if (!hasPositionData) return null;

        const reasoningText = reasoning || '';
        const contentText = txt || '';
        const positioned = tcs.filter(tc => tc.contentIndex !== undefined && tc.reasoningIndex !== undefined);
        const unpositioned = tcs.filter(tc => tc.contentIndex === undefined || tc.reasoningIndex === undefined);
        const withinReasoning = positioned.filter(tc => reasoningText.length > 0 && (tc.reasoningIndex || 0) <= reasoningText.length).sort((a, b) => (a.reasoningIndex || 0) - (b.reasoningIndex || 0));
        const afterReasoning = positioned.filter(tc => reasoningText.length === 0 || (tc.reasoningIndex || 0) > reasoningText.length).sort((a, b) => (a.contentIndex || 0) - (b.contentIndex || 0));

        const reasoningSegments: Segment[] = [];
        let lastIdx = 0;
        for (const tc of withinReasoning) {
            const rIdx = tc.reasoningIndex || 0;
            if (rIdx > lastIdx) {
                reasoningSegments.push({ type: 'reasoning', text: reasoningText.slice(lastIdx, rIdx) });
            }
            reasoningSegments.push({ type: 'tool', tc });
            lastIdx = rIdx;
        }
        if (lastIdx < reasoningText.length) {
            reasoningSegments.push({ type: 'reasoning', text: reasoningText.slice(lastIdx) });
        }
        if (withinReasoning.length === 0) return null;

        return { reasoningSegments, trailingContent: contentText, trailingTools: afterReasoning.concat(unpositioned) };
    }

    // Resolved reasoning for interleaved layout (fetched on-demand)
    let resolvedReasoningContent = $state<string | undefined>();
    let effectiveReasoning = $derived(resolvedReasoningContent ?? reasoningContent);
    let interleavedLayout = $derived(_computeLayout(toolCalls, effectiveReasoning, content));

    function toggleArgs(toolId: string) {
        const next = new Set(expandedArgs);
        next.has(toolId) ? next.delete(toolId) : next.add(toolId);
        expandedArgs = next;
    }

    async function handleCopy() {
        try {
            await navigator.clipboard.writeText(content);
        } catch {
            try {
                const textarea = document.createElement('textarea');
                textarea.value = content;
                textarea.style.position = 'fixed';
                textarea.style.opacity = '0';
                document.body.appendChild(textarea);
                textarea.select();
                document.execCommand('copy');
                document.body.removeChild(textarea);
            } catch (err) {
                console.error('Failed to copy to clipboard:', err);
            }
        }
        copied = true;
        setTimeout(() => copied = false, 2000);
    }

    function startEdit() {
        isEditing = true;
        if (isMobileCtx.isMobile) {
            setTimeout(() => {
                mobileEditDialog?.showModal();
                mobileEditTextarea?.focus();
            }, 50);
        }
    }

    function cancelEdit() {
        mobileEditDialog?.close();
        isEditing = false;
    }

    async function submitEdit(newContent: string, newAttachments?: Array<{ mimeType: string; data: string }>) {
        if (newContent.trim() && messageId) {
            isSubmitting = true;
            try {
                if (onEdit) {
                    await onEdit(messageId, newContent.trim());
                } else if (isFirstUserMessage) {
                    await onFork?.(messageId, content, newContent.trim(), newAttachments?.length ? newAttachments : undefined);
                } else if (isLastUserMessage && onSend) {
                    await onSend(messageId, content, newContent.trim(), newAttachments?.length ? newAttachments : undefined);
                } else {
                    await onFork?.(messageId, content, newContent.trim(), newAttachments?.length ? newAttachments : undefined);
                }
            } finally {
                isEditing = false;
                isSubmitting = false;
            }
        }
    }

    // Mobile edit state
    let mobileEditContent = $state('');
    let mobileEditAttachments = $state<Array<{ mimeType: string; data: string }>>([]);

    function handleMobileKeydown(e: KeyboardEvent) {
        if (e.key === 'Enter' && (e.ctrlKey || e.metaKey) && !isSubmitting) {
            e.preventDefault();
            submitEdit(mobileEditContent, mobileEditAttachments.length > 0 ? mobileEditAttachments : undefined);
        } else if (e.key === 'Escape') {
            e.preventDefault();
            cancelEdit();
        }
    }

    $effect(() => {
        if (isEditing) {
            mobileEditContent = content;
            mobileEditAttachments = attachments ? [...attachments] : [];
        }
    });

    let mobileFileInput: HTMLInputElement | null = $state(null);

    function handleMobileFileSelect(e: Event) {
        const files = (e.target as HTMLInputElement).files;
        if (!files || files.length === 0) return;
        const remaining = 4 - mobileEditAttachments.length;
        for (let i = 0; i < Math.min(remaining, files.length); i++) {
            const file = files[i];
            if (!file.type.startsWith('image/')) continue;
            if (file.size > 4 * 1024 * 1024) continue;
            const reader = new FileReader();
            reader.onload = () => {
                mobileEditAttachments = [...mobileEditAttachments, { mimeType: file.type, data: reader.result as string }];
            };
            reader.readAsDataURL(file);
        }
        if (mobileFileInput) mobileFileInput.value = '';
    }

    function removeMobileAttachment(index: number) {
        mobileEditAttachments = mobileEditAttachments.filter((_, i) => i !== index);
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
                <h3>Edit message</h3>
                <button class="close-btn" onclick={cancelEdit} aria-label="Close">✕</button>
            </div>
            <textarea
                bind:this={mobileEditTextarea}
                bind:value={mobileEditContent}
                onkeydown={handleMobileKeydown}
                rows="8"
                aria-label="Edit message content"
            ></textarea>
            {#if mobileEditAttachments.length > 0}
                <div class="edit-attachments-preview">
                    {#each mobileEditAttachments as att, i}
                        <div class="edit-attachment-chip">
                            <img src={att.data.startsWith('data:') ? att.data : `data:${att.mimeType};base64,${att.data}`} alt="Attachment" />
                            <button type="button" class="remove-attachment" onclick={() => removeMobileAttachment(i)} title="Remove">&times;</button>
                        </div>
                    {/each}
                </div>
            {/if}
            <div class="edit-modal-footer">
                <input type="file" accept="image/*" multiple bind:this={mobileFileInput} onchange={handleMobileFileSelect} style="display:none" />
                <button type="button" class="edit-attach-btn" onclick={() => mobileFileInput?.click()} title="Attach image" aria-label="Attach image">
                    <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                        <path d="M21.44 11.05l-9.19 9.19a6 6 0 01-8.49-8.49l9.19-9.19a4 4 0 015.66 5.66l-9.2 9.19a2 2 0 01-2.83-2.83l8.49-8.48"/>
                    </svg>
                </button>
                <button type="button" class="edit-cancel-btn" onclick={cancelEdit}>Cancel</button>
                <button type="button" class="edit-save-btn" onclick={() => submitEdit(mobileEditContent, mobileEditAttachments.length > 0 ? mobileEditAttachments : undefined)} disabled={isSubmitting}>
                    {#if isSubmitting}
                        <span class="spinner-inline"></span>
                    {:else}
                        Save
                    {/if}
                </button>
            </div>
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

{#snippet userMessage()}
    <div class="message-wrapper user-wrapper">
        {#if isEditing && !isMobileCtx.isMobile}
            <EditForm
                {content}
                {attachments}
                {isFirstUserMessage}
                {isLastUserMessage}
                {taskStatus}
                {messageId}
                {projectId}
                taskId={msgTaskId}
                {isSubmitting}
                onSubmit={submitEdit}
                onCancel={cancelEdit}
            />
        {:else}
            {#if attachments && attachments.length > 0}
                <div class="message-images">
                    {#each attachments as att}
                        <img src={att.data.startsWith('data:') ? att.data : `data:${att.mimeType};base64,${att.data}`} alt="" class="message-image" />
                    {/each}
                </div>
            {/if}
            <div class="message-row">
                {#if queued}
                    <span class="queued-indicator" title="Queued — will process after summarization completes">
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
                onFork={startEdit}
                {copied}
            />
        {/if}
    </div>
{/snippet}

{#snippet assistantMessage()}
    <div class="message-wrapper assistant-wrapper">
        {#if isEditing && !isMobileCtx.isMobile}
            <EditForm
                {content}
                {attachments}
                {isFirstUserMessage}
                {isLastUserMessage}
                {taskStatus}
                {messageId}
                {projectId}
                taskId={msgTaskId}
                {isSubmitting}
                onSubmit={submitEdit}
                onCancel={cancelEdit}
            />
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
                    onEdit={onEdit ? startEdit : undefined}
                    onRegenerate={onRegenerate}
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
                {@html memoizedRender('assistant', interleavedLayout!.trailingContent)}
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
                {@html renderedHtml}
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
        <div class="system-card" class:system-info={severity === 'info'}>
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
                <span class="system-code">System</span>
                {#if onDelete}
                    <button class="btn-dismiss" onclick={onDelete} title="Dismiss message" aria-label="Dismiss message">
                        <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                            <line x1="18" y1="6" x2="6" y2="18"/>
                            <line x1="6" y1="6" x2="18" y2="18"/>
                        </svg>
                    </button>
                {/if}
            </div>
            <div class="system-message">{content}</div>
        </div>
    </div>
{/snippet}

<style>
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
        border-radius: 8px;
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
        font-size: 0.7rem;
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
        font-size: 0.875rem;
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
        font-size: 0.75rem;
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
        border-radius: 4px;
        transition: all 0.15s ease;
    }
    .system-header .btn-dismiss:hover {
        background: color-mix(in srgb, var(--color-status-red) 15%, transparent);
        color: var(--color-status-red);
    }
    .system-message {
        font-size: 0.8125rem;
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
        font-size: 0.8125rem;
        line-height: 1.5rem;
    }
    .assistant .message-text :global(code) {
        background: var(--color-surface-0);
        padding: 2px 6px;
        border-radius: 4px;
        font-size: 0.8125rem;
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
    .assistant .message-text :global(h5) { font-size: 1rem; }
    .assistant .message-text :global(h6) { font-size: 1rem; color: var(--color-text-secondary); }
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
        font-size: 0.875rem;
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
        border: 1px solid var(--color-border);
        color: var(--color-text-primary);
    }
    .assistant .message-text :global(td) {
        padding: 6px 12px;
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

    /* Mobile edit dialog */
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
        z-index: 10001;
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
        font-size: 1rem;
        font-weight: 600;
    }
    .close-btn {
        background: none;
        border: none;
        color: var(--color-text-secondary);
        font-size: 1.125rem;
        cursor: pointer;
        padding: 4px;
    }
    .edit-modal textarea {
        flex: 1;
        padding: 16px;
        border: none;
        background: transparent;
        color: var(--color-text-primary);
        font-family: inherit;
        font-size: 0.875rem;
        line-height: 1.5;
        resize: none;
        outline: none;
    }
    .edit-modal-footer {
        display: flex;
        gap: 8px;
        justify-content: flex-end;
        padding: 16px;
        border-top: 1px solid var(--color-border);
    }
    .edit-cancel-btn {
        padding: 7px 14px;
        border-radius: var(--radius-md);
        background: var(--color-surface-3);
        color: var(--color-text-primary);
        font-size: 0.8125rem;
        font-weight: 500;
        cursor: pointer;
        border: none;
        font-family: inherit;
        min-height: 44px;
    }
    .edit-cancel-btn:hover {
        background: var(--color-surface-4);
    }
    .edit-save-btn {
        padding: 7px 18px;
        border-radius: var(--radius-md);
        background: var(--color-accent-dark);
        color: var(--color-btn-text);
        font-size: 0.8125rem;
        font-weight: 500;
        cursor: pointer;
        border: none;
        font-family: inherit;
        min-height: 44px;
    }
    .edit-save-btn:hover {
        opacity: 0.85;
    }
    .edit-save-btn:disabled {
        opacity: 0.6;
        cursor: not-allowed;
    }
    .edit-attach-btn {
        background: none;
        border: none;
        cursor: pointer;
        padding: 4px;
        color: var(--color-text-secondary);
        opacity: 0.6;
        display: flex;
        align-items: center;
        justify-content: center;
    }
    .edit-attach-btn:hover {
        opacity: 1;
        color: var(--color-text-primary);
    }
    .edit-attachments-preview {
        display: flex;
        flex-wrap: wrap;
        gap: 6px;
        padding: 8px 16px;
    }
    .edit-attachment-chip {
        position: relative;
        width: 48px;
        height: 48px;
        border-radius: var(--radius-sm);
        overflow: hidden;
        border: 1px solid var(--color-border);
    }
    .edit-attachment-chip img {
        width: 100%;
        height: 100%;
        object-fit: cover;
    }
    .edit-attachment-chip .remove-attachment {
        position: absolute;
        top: 0;
        right: 0;
        width: 18px;
        height: 18px;
        border-radius: 50%;
        background: rgba(0,0,0,0.7);
        color: white;
        border: none;
        font-size: 14px;
        line-height: 1;
        cursor: pointer;
        display: flex;
        align-items: center;
        justify-content: center;
        padding: 0;
    }
    .spinner-inline {
        display: inline-block;
        width: 14px;
        height: 14px;
        border: 2px solid currentColor;
        border-top-color: transparent;
        border-radius: 50%;
        animation: spin 0.6s linear infinite;
    }
    @keyframes spin { to { transform: rotate(360deg); } }
</style>
