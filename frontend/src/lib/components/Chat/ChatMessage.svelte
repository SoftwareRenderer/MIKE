<script lang="ts">
    import { marked } from 'marked';
    import ToolCall from './ToolCall.svelte';
    import FileMentionDropdown from '../FileMentionDropdown.svelte';
    import { detectMentionAtCursor, insertMention, getInputRect } from '$lib/utils/file-mention';
    import { getTaskMessageFull } from '$lib/api';

    interface ToolCallInfo {
        toolId: string;
        toolName: string;
        displayName?: string;
        args: Record<string, unknown>;
        raw_args?: string;
        sandbox?: boolean;
        path?: string;
        result?: string;
        resultTruncated?: boolean; // true if backend truncated the result
        originalLines?: number; // original line count before truncation
        originalBytes?: number; // original byte count before truncation
        messageId?: string; // tool_call message ID for fetching full result
        success?: boolean;
        duration?: number;
        isPending?: boolean;
        bytesWritten?: number;
        // Position in the content stream when this tool call was added.
        // Used to interleave tool calls with text during rendering.
        // contentIndex = entry.content.length at time of tool call.
        // reasoningIndex = entry.reasoningContent.length at time of tool call.
        // When undefined (DB-loaded entries), tool calls render at the end.
        contentIndex?: number;
        reasoningIndex?: number;
    }

    interface Props {
        role: string;
        content: string;
        reasoningContent?: string;
        hasReasoning?: boolean; // true if message has reasoning (even if content is suppressed)
        isStreaming?: boolean;
        timestamp?: string;
        messageId?: string;
        attachments?: Array<{ mimeType: string; data: string }>;
        stats?: { model_name: string; prompt_tokens: number; predicted_tokens: number; prompt_ms: number; predicted_ms: number; duration: number };
        toolCalls?: ToolCallInfo[];
        formatToolArgs?: (toolName: string, args: Record<string, unknown>) => string;
        onCopy?: () => void;
        onDelete?: () => void;
        onFork?: (messageId: string, originalContent: string, newContent: string, attachments?: Array<{ mimeType: string; data: string }>) => void;
        onSend?: (messageId: string, originalContent: string, newContent: string, attachments?: Array<{ mimeType: string; data: string }>) => void;
        onEdit?: (messageId: string, newContent: string) => void;
        onRegenerate?: () => void;
        isLastUserMessage?: boolean;
        isFirstUserMessage?: boolean;
        taskStatus?: string;
        onNavigateTask?: (taskId: string) => void;
        queued?: boolean; // true if message is pending (sent while offline)
        projectId?: string;
        taskId?: string;
    }

    let { role, content, reasoningContent, hasReasoning = false, isStreaming = false, timestamp, messageId, attachments, stats, toolCalls = [], onCopy, onDelete, onFork, onSend, onEdit, onRegenerate, isLastUserMessage = false, isFirstUserMessage = false, taskStatus = 'backlog', onNavigateTask, queued = false, projectId = '', taskId: msgTaskId = '' }: Props = $props();

    // Resolved reasoning content — fetched on-demand when backend suppresses it
    let resolvedReasoningContent = $state<string | undefined>();
    let reasoningLoading = $state(false);

    /** Effective reasoning content: resolved (fetched) value, or the prop value. */
    let effectiveReasoning = $derived(resolvedReasoningContent ?? reasoningContent);

    /** Whether to show the reasoning toggle (has reasoning that isn't yet resolved). */
    let showReasoningToggle = $derived(hasReasoning || !!reasoningContent);

    let reasoningUserToggled = $state(false);
    let reasoningUserValue = $state(false);
    let reasoningExpanded = $derived(
        reasoningUserToggled
            ? reasoningUserValue
            : (effectiveReasoning === '[HIDDEN]' ? false : !effectiveReasoning || (effectiveReasoning.length <= 200 && !content) || (toolCalls && toolCalls.length > 0 && !content) || (isStreaming && !(toolCalls && toolCalls.length > 0)))
    );

    let expandedArgs = $state<Set<string>>(new Set());

    /** Build interleaved segments for reasoning-region (inside collapsible reasoning block).
        Returns { reasoningSegments, trailingContent, trailingTools } or null if no interleaving needed. */
    type Segment = { type: 'reasoning', text: string } | { type: 'tool', tc: ToolCallInfo };

    // Helper to compute interleaved layout from current reactive values.
    // Called inside $derived to establish reactive dependencies.
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

        return {
            reasoningSegments,
            trailingContent: contentText,
            trailingTools: afterReasoning.concat(unpositioned)
        };
    }

    let interleavedLayout = $derived(_computeLayout(toolCalls, effectiveReasoning, content));

    function toggleArgs(toolId: string) {
        const next = new Set(expandedArgs);
        if (next.has(toolId)) {
            next.delete(toolId);
        } else {
            next.add(toolId);
        }
        expandedArgs = next;
    }

    async function handleReasoningToggle() {
        // If hiding, just toggle
        if (reasoningExpanded) {
            reasoningUserToggled = true;
            reasoningUserValue = false;
            return;
        }
        // If showing and content is [HIDDEN], fetch full message first
        if (effectiveReasoning === '[HIDDEN]' && messageId && projectId && msgTaskId && !resolvedReasoningContent) {
            reasoningLoading = true;
            try {
                const fullMsg = await getTaskMessageFull(projectId, msgTaskId, messageId);
                resolvedReasoningContent = fullMsg.reasoning_content;
            } catch (e) {
                console.error('Failed to fetch full reasoning:', e);
            } finally {
                reasoningLoading = false;
            }
        }
        reasoningUserToggled = true;
        reasoningUserValue = true;
    }

    function formatTimestamp(isoStr: string): { display: string; tooltip: string } {
        const date = new Date(isoStr);
        if (isNaN(date.getTime())) return { display: '', tooltip: '' };

        const now = new Date();
        const todayStart = new Date(now.getFullYear(), now.getMonth(), now.getDate());
        const msgStart = new Date(date.getFullYear(), date.getMonth(), date.getDate());
        const diffMs = todayStart.getTime() - msgStart.getTime();
        const diffDays = Math.round(diffMs / (1000 * 60 * 60 * 24));

        const timeStr = date.toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' });
        const fullDate = date.toLocaleDateString([], { month: 'long', day: 'numeric', year: 'numeric' });

        if (diffDays === 0) {
            // Today — time only
            return { display: timeStr, tooltip: '' };
        } else if (diffDays === 1) {
            // Yesterday
            return { display: `Yesterday at ${timeStr}`, tooltip: fullDate };
        } else if (diffDays >= 2 && diffDays <= 7) {
            // Last [weekday]
            const weekday = date.toLocaleDateString([], { weekday: 'long' });
            return { display: `Last ${weekday} at ${timeStr}`, tooltip: fullDate };
        } else {
            // Older — full short date + time
            const shortDate = date.toLocaleDateString([], { month: 'short', day: 'numeric', year: 'numeric' });
            return { display: `${shortDate} at ${timeStr}`, tooltip: fullDate };
        }
    }

    let timestampInfo = $derived(
        timestamp ? formatTimestamp(timestamp) : { display: '', tooltip: '' }
    );

    function formatTime(isoStr: string): string {
        return formatTimestamp(isoStr).display;
    }

    let isEditing = $state(false);
    let editContent = $state('');
    let textareaEl: HTMLTextAreaElement | null = $state(null);
    let editAttachments = $state<Array<{ mimeType: string; data: string }>>([]);
    let editFileInput: HTMLInputElement | null = $state(null);
    let mobileEditTextarea: HTMLTextAreaElement | null = $state(null);
    let mobileEditDialog: HTMLDialogElement | null = $state(null);

    // Mobile detection
    let isMobile = $state(false);
    {
        const check = () => isMobile = window.matchMedia('(max-width: 768px)').matches;
        check();
        window.matchMedia('(max-width: 768px)').addEventListener('change', check);
    }

    // @ file mention state (for edit mode)
    let mentionTrigger = $state<{ atIndex: number; query: string } | null>(null);
    let mentionResults = $state<Array<{ path: string }>>([]);
    let mentionPosition = $state<{ top: number; bottom: number; left: number; width: number } | null>(null);
    let mentionActiveIndex = $state(0);
    let mentionSearchTimer: ReturnType<typeof setTimeout> | null = null;
    const MENTION_DEBOUNCE_MS = 150;

    function handleMentionInput() {
        if (!textareaEl || !isEditing) return;
        const cursorPos = textareaEl.selectionStart ?? 0;
        const trigger = detectMentionAtCursor(editContent, cursorPos);

        if (trigger) {
            mentionTrigger = trigger;
            mentionActiveIndex = 0;
            const rect = getInputRect(textareaEl);
            mentionPosition = rect;

            if (mentionSearchTimer) clearTimeout(mentionSearchTimer);
            mentionSearchTimer = setTimeout(() => {
                searchFiles(trigger.query);
            }, MENTION_DEBOUNCE_MS);
        } else {
            closeMention();
        }
    }

    async function searchFiles(query: string) {
        if (!projectId) return;
        const params = new URLSearchParams({ q: query, project: projectId });
        if (msgTaskId) params.set('task', msgTaskId);
        try {
            const res = await fetch(`/api/fs/search?${params.toString()}`);
            if (!res.ok) return;
            const data = await res.json();
            mentionResults = data.results || [];
        } catch {
            mentionResults = [];
        }
    }

    function closeMention() {
        mentionTrigger = null;
        mentionResults = [];
        mentionPosition = null;
        mentionActiveIndex = 0;
        if (mentionSearchTimer) {
            clearTimeout(mentionSearchTimer);
            mentionSearchTimer = null;
        }
    }

    function handleMentionSelect(path: string) {
        if (!mentionTrigger || !textareaEl) return;
        const { value: newValue, cursorPos } = insertMention(editContent, mentionTrigger.atIndex, mentionTrigger.query, path);
        editContent = newValue;
        closeMention();
        requestAnimationFrame(() => {
            if (textareaEl) {
                textareaEl.focus();
                textareaEl.setSelectionRange(cursorPos, cursorPos);
            }
        });
    }

    function handleMentionClose() {
        closeMention();
        textareaEl?.focus();
    }

    $effect(() => {
        editContent = content;
    });
    let copied = $state(false);

    function handleEditFileSelect(e: Event) {
        const files = (e.target as HTMLInputElement).files;
        if (!files || files.length === 0) return;
        const remaining = 4 - editAttachments.length;
        for (let i = 0; i < Math.min(remaining, files.length); i++) {
            const file = files[i];
            if (file.size > 4 * 1024 * 1024) continue;
            const reader = new FileReader();
            reader.onload = () => {
                editAttachments = [...editAttachments, { mimeType: file.type, data: reader.result as string }];
            };
            reader.readAsDataURL(file);
        }
        // Reset input so the same file can be selected again
        if (editFileInput) editFileInput.value = '';
    }

    function removeEditAttachment(index: number) {
        editAttachments = editAttachments.filter((_, i) => i !== index);
    }

    // Generated token count for stats display
    let genTokens = $derived(stats?.predicted_tokens ?? 0);

    // Markdown rendering — marked.parse() is fast enough on typical message sizes.
    let renderedHtml = $derived(
        (role === 'assistant' || role === 'system')
            ? (marked.parse(content || '', { async: false }) as string)
            : ''
    );

    let userHtml = $derived(
        role === 'user'
            ? content.replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/\n/g, '<br>')
            : ''
    );

    async function handleCopy() {
        try {
            await navigator.clipboard.writeText(content);
            copied = true;
            setTimeout(() => copied = false, 2000);
        } catch (err) {
            // Fallback: create a temporary textarea and copy
            try {
                const textarea = document.createElement('textarea');
                textarea.value = content;
                textarea.style.position = 'fixed';
                textarea.style.opacity = '0';
                document.body.appendChild(textarea);
                textarea.select();
                document.execCommand('copy');
                document.body.removeChild(textarea);
                copied = true;
                setTimeout(() => copied = false, 2000);
            } catch (_) {
                // Both methods failed
                console.error('Failed to copy to clipboard:', err);
            }
        }
    }

    function handleDelete() {
        onDelete?.();
    }

    function startEdit() {
        editContent = content;
        editAttachments = attachments ? [...attachments] : [];
        isEditing = true;
        if (isMobile) {
            setTimeout(() => {
                mobileEditDialog?.showModal();
                mobileEditTextarea?.focus();
            }, 50);
        } else {
            setTimeout(() => {
                if (textareaEl) {
                    textareaEl.focus();
                    textareaEl.select();
                }
            }, 50);
        }
    }

    function cancelEdit() {
        mobileEditDialog?.close();
        isEditing = false;
        editContent = content;
        editAttachments = [];
    }

    function submitEdit() {
        if (editContent.trim() && messageId) {
            if (onEdit) {
                onEdit(messageId, editContent.trim());
            } else if (isFirstUserMessage) {
                onFork?.(messageId, content, editContent.trim(), editAttachments.length > 0 ? editAttachments : undefined);
            } else if (isLastUserMessage && onSend) {
                onSend(messageId, content, editContent.trim(), editAttachments.length > 0 ? editAttachments : undefined);
            } else {
                onFork?.(messageId, content, editContent.trim(), editAttachments.length > 0 ? editAttachments : undefined);
            }
            isEditing = false;
        }
    }

    function handleKeydown(e: KeyboardEvent) {
        // Handle @ mention dropdown navigation
        if (mentionTrigger && mentionResults.length > 0) {
            if (e.key === 'ArrowDown') {
                e.preventDefault();
                mentionActiveIndex = (mentionActiveIndex + 1) % mentionResults.length;
                return;
            }
            if (e.key === 'ArrowUp') {
                e.preventDefault();
                mentionActiveIndex = (mentionActiveIndex - 1 + mentionResults.length) % mentionResults.length;
                return;
            }
            if (e.key === 'Enter' && !e.shiftKey) {
                e.preventDefault();
                if (mentionActiveIndex >= 0 && mentionActiveIndex < mentionResults.length) {
                    handleMentionSelect(mentionResults[mentionActiveIndex].path);
                }
                return;
            }
            if (e.key === 'Escape') {
                e.preventDefault();
                e.stopPropagation();
                closeMention();
                return;
            }
        }
        if (e.key === 'Enter' && !e.shiftKey) {
            e.preventDefault();
            submitEdit();
        } else if (e.key === 'Escape') {
            e.preventDefault();
            cancelEdit();
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
    {#if role === 'user'}
        <div class="message-wrapper user-wrapper">
            {#if isEditing}
                <div class="edit-form">
                    {#if editAttachments.length > 0}
                        <div class="edit-attachments">
                            {#each editAttachments as att, i}
                                <div class="edit-attachment-chip">
                                    <img src={att.data} alt="Attachment" />
                                    <button class="remove-attachment" onclick={() => removeEditAttachment(i)} title="Remove">&times;</button>
                                </div>
                            {/each}
                        </div>
                    {/if}
                    <textarea
                        bind:this={textareaEl}
                        bind:value={editContent}
                        onkeydown={handleKeydown}
                        oninput={handleMentionInput}
                        rows="3"
                        aria-label="Edit message content"
                    ></textarea>
                    <div class="edit-actions">
                        <input type="file" accept="image/*" multiple bind:this={editFileInput} onchange={handleEditFileSelect} style="display:none" />
                        <button class="btn-edit-attach" onclick={() => editFileInput?.click()} title="Attach image">
                            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                                <path d="M21.44 11.05l-9.19 9.19a6 6 0 01-8.49-8.49l9.19-9.19a4 4 0 015.66 5.66l-9.2 9.19a2 2 0 01-2.83-2.83l8.49-8.48"/>
                            </svg>
                        </button>
                        <button class="btn-edit-cancel" onclick={cancelEdit} title="Cancel editing">Cancel</button>
                        <button class="btn-edit-submit" onclick={submitEdit} title="{isFirstUserMessage && taskStatus === 'backlog' ? 'Save changes' : 'Send'}">
                            {#if isFirstUserMessage && taskStatus === 'backlog'}
                                <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                                    <polyline points="20 6 9 17 4 12"/>
                                </svg>
                                Save
                            {:else if isFirstUserMessage}
                                <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                                    <line x1="22" y1="2" x2="11" y2="13"/>
                                    <polygon points="22 2 15 22 11 13 2 9 22 2"/>
                                </svg>
                                Send
                            {:else if isLastUserMessage}
                                <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                                    <line x1="22" y1="2" x2="11" y2="13"/>
                                    <polygon points="22 2 15 22 11 13 2 9 22 2"/>
                                </svg>
                                Send
                            {:else}
                                <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                                    <circle cx="18" cy="18" r="3"/>
                                    <circle cx="6" cy="6" r="3"/>
                                    <path d="M13 6h3a2 2 0 0 1 2 2v7"/>
                                    <line x1="6" y1="9" x2="6" y2="21"/>
                                </svg>
                                New Thread
                            {/if}
                        </button>
                    </div>
                </div>
                <!-- @ file mention dropdown (edit mode) -->
                <FileMentionDropdown
                    results={mentionResults}
                    position={mentionPosition}
                    activeIndex={mentionActiveIndex}
                    onSelect={handleMentionSelect}
                    onClose={handleMentionClose}
                />
            {:else}
                {#if attachments && attachments.length > 0}
                    <div class="message-images">
                        {#each attachments as att}
                            <img src={att.data} alt="Attached image" class="message-image" />
                        {/each}
                    </div>
                {/if}
                <div class="message-bubble">
                    <div class="message-text">{@html userHtml}</div>
                    {#if queued}
                        <span class="queued-indicator" title="Queued (will send when reconnected)">
                            <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                                <circle cx="12" cy="12" r="10"/>
                                <polyline points="12 6 12 12 16 14"/>
                            </svg>
                        </span>
                    {/if}
                </div>
                <div class="message-actions user-actions">
                    {#if timestampInfo.display}
                        <span class="message-timestamp" title={timestampInfo.tooltip || undefined}>{timestampInfo.display}</span>
                    {/if}
                    <button class="action-btn" class:copied={copied} onclick={handleCopy} aria-label="Copy message" title="Copy message">
                        <svg class="icon" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                            <rect x="9" y="9" width="13" height="13" rx="2" ry="2"/>
                            <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>
                        </svg>
                        <span class="check">✓</span>
                    </button>
                    {#if onFork}
                        <button class="action-btn" onclick={startEdit} aria-label="Start new thread from this message" title="Edit / new thread">
                            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                                <path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/>
                                <path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"/>
                            </svg>
                        </button>
                    {/if}
                    {#if onDelete}
                        <button class="action-btn" onclick={handleDelete} aria-label="Delete message and all following messages" title="Delete message and all following messages">
                            <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                                <path d="M2 4h12M5 4V3a1 1 0 011-1h4a1 1 0 011 1v1M6 7v5M10 7v5" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                                <path d="M3 4l1 9a1 1 0 001 1h6a1 1 0 001-1l1-9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                            </svg>
                        </button>
                    {/if}
                </div>
            {/if}
        </div>
    {:else if role === 'assistant'}
        <div class="message-wrapper assistant-wrapper">
            {#if isEditing && !isMobile}
                <div class="edit-form">
                    <textarea
                        bind:this={textareaEl}
                        bind:value={editContent}
                        onkeydown={(e) => { if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) { e.preventDefault(); submitEdit(); } else if (e.key === 'Escape') { cancelEdit(); } }}
                        rows="5"
                        aria-label="Edit message content"
                    ></textarea>
                    <div class="edit-actions">
                        <button class="btn-edit-cancel" onclick={cancelEdit} title="Cancel editing">Cancel</button>
                        <button class="btn-edit-submit" onclick={submitEdit} title="Save changes">
                            <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                                <polyline points="20 6 9 17 4 12"/>
                            </svg>
                            Save
                        </button>
                    </div>
                </div>
            {:else}
                {#if interleavedLayout}
                    <!-- Interleaved rendering: reasoning (with tools) → content → trailing tools -->
                    {#if showReasoningToggle}
                        <div class="reasoning-block" class:expanded={reasoningExpanded}>
                            <button class="reasoning-toggle" onclick={() => handleReasoningToggle()} aria-expanded={reasoningExpanded} title={reasoningExpanded ? 'Hide reasoning' : 'Show reasoning'} disabled={reasoningLoading}>
                                <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                                    <path d="M12 3l1.5 4.5L18 9l-4.5 1.5L12 15l-1.5-4.5L6 9l4.5-1.5L12 3z"/>
                                </svg>
                                {reasoningExpanded ? 'Hide' : 'Show'} reasoning
                            </button>
                            {#if reasoningExpanded}
                                {#if reasoningLoading}
                                    <div class="reasoning-content reasoning-loading">Loading reasoning...</div>
                                {:else}
                                    {#each interleavedLayout.reasoningSegments as seg}
                                        {#if seg.type === 'reasoning'}
                                            <div class="reasoning-content">{seg.text}</div>
                                        {/if}
                                    {/each}
                                {/if}
                            {/if}
                            <!-- Always show tool calls, even when reasoning is hidden -->
                            {#each interleavedLayout.reasoningSegments as seg}
                                {#if seg.type === 'tool'}
                                    <ToolCall toolCall={seg.tc} {expandedArgs} onToggleArgs={toggleArgs} {onNavigateTask} messageId={seg.tc.messageId} projectId={projectId} taskId={msgTaskId} />
                                {/if}
                            {/each}
                        </div>
                    {/if}
                    {#if interleavedLayout.trailingContent}
                        <div class="message-bubble assistant-bubble">
                            <div class="message-text" class:streaming={isStreaming}>
                                {@html marked.parse(interleavedLayout.trailingContent, { async: false }) as string}
                                {#if isStreaming}
                                    <span class="cursor-blink"></span>
                                {/if}
                            </div>
                        </div>
                    {/if}
                    {#if interleavedLayout.trailingTools && interleavedLayout.trailingTools.length > 0}
                        {#each interleavedLayout.trailingTools as tc (tc.toolId)}
                            <ToolCall toolCall={tc} {expandedArgs} onToggleArgs={toggleArgs} {onNavigateTask} messageId={tc.messageId} projectId={projectId} taskId={msgTaskId} />
                        {/each}
                    {/if}
                {:else}
                    <!-- Standard rendering (no interleaving): reasoning → content → tool calls -->
                    {#if showReasoningToggle}
                    <div class="reasoning-block" class:expanded={reasoningExpanded}>
                        <button class="reasoning-toggle" onclick={() => handleReasoningToggle()} aria-expanded={reasoningExpanded} title={reasoningExpanded ? 'Hide reasoning' : 'Show reasoning'} disabled={reasoningLoading}>
                            <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                                <path d="M12 3l1.5 4.5L18 9l-4.5 1.5L12 15l-1.5-4.5L6 9l4.5-1.5L12 3z"/>
                            </svg>
                            {reasoningExpanded ? 'Hide' : 'Show'} reasoning
                        </button>
                        {#if reasoningExpanded}
                            {#if reasoningLoading}
                                <div class="reasoning-content reasoning-loading">Loading reasoning...</div>
                            {:else if effectiveReasoning && effectiveReasoning !== '[HIDDEN]'}
                                <div class="reasoning-content">{effectiveReasoning}</div>
                            {/if}
                        {/if}
                    </div>
                {/if}
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
                        <ToolCall toolCall={tc} {expandedArgs} onToggleArgs={toggleArgs} {onNavigateTask} messageId={tc.messageId} projectId={projectId} taskId={msgTaskId} />
                    {/each}
                {/if}
                {/if}
            {#if stats}
                <div class="message-stats">
                    <span class="stats-model-name">{stats.model_name || 'model'}</span>
                    {#if genTokens > 0}
                        <span class="stat-separator" aria-hidden="true">·</span>
                        <span class="stat-item">{genTokens} tokens</span>
                    {/if}
                    {#if stats.duration > 0}
                        <span class="stat-separator" aria-hidden="true">·</span>
                        <span class="stat-item">{(stats.duration / 1000).toFixed(1)}s</span>
                    {/if}
                </div>
            {/if}
            {#if content || showReasoningToggle}
                <div class="message-actions assistant-actions">
                    {#if timestampInfo.display}
                        <span class="message-timestamp" title={timestampInfo.tooltip || undefined}>{timestampInfo.display}</span>
                    {/if}
                    {#if content}
                        <button class="action-btn" class:copied={copied} onclick={handleCopy} aria-label="Copy message" title="Copy message">
                            <svg class="icon" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                                <rect x="9" y="9" width="13" height="13" rx="2" ry="2"/>
                                <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>
                            </svg>
                            <span class="check">✓</span>
                        </button>
                    {/if}
                    {#if onEdit && !isStreaming}
                        <button class="action-btn" onclick={startEdit} aria-label="Edit message" title="Edit message">
                            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                                <path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/>
                                <path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"/>
                            </svg>
                        </button>
                    {/if}
                    {#if onRegenerate && !isStreaming}
                        <button class="action-btn" onclick={onRegenerate} aria-label="Regenerate response" title="Regenerate response">
                            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                                <path d="M21 2v6h-6"/>
                                <path d="M3 12a9 9 0 0 1 15-6.7L21 8"/>
                                <path d="M3 22v-6h6"/>
                                <path d="M21 12a9 9 0 0 1-15 6.7L3 16"/>
                            </svg>
                        </button>
                    {/if}
                    {#if onDelete}
                        <button class="action-btn" onclick={handleDelete} aria-label="Delete message and all following messages" title="Delete message and all following messages">
                            <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                                <path d="M2 4h12M5 4V3a1 1 0 011-1h4a1 1 0 011 1v1M6 7v5M10 7v5" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                                <path d="M3 4l1 9a1 1 0 001 1h6a1 1 0 001-1l1-9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                            </svg>
                        </button>
                    {/if}
                </div>
            {/if}
            {/if}
        </div>
    {:else}
        <div class="message-wrapper system-wrapper">
            <div class="message-text system-text">{content}</div>
        </div>
    {/if}
</div>

<!-- Mobile edit -->
{#if isMobile && isEditing}
    <dialog class="edit-dialog" bind:this={mobileEditDialog} onclick={(e) => { if (e.target === mobileEditDialog) cancelEdit(); }}>
        <div class="edit-modal">
            <div class="edit-modal-header">
                <h3>Edit message</h3>
                <button class="close-btn" onclick={cancelEdit} aria-label="Close">✕</button>
            </div>
            <textarea
                bind:this={mobileEditTextarea}
                bind:value={editContent}
                onkeydown={(e) => { if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) { e.preventDefault(); submitEdit(); } else if (e.key === 'Escape') { cancelEdit(); } }}
                rows="8"
                aria-label="Edit message content"
            ></textarea>
            <div class="edit-modal-footer">
                <button type="button" class="edit-cancel-btn" onclick={cancelEdit}>Cancel</button>
                <button type="button" class="edit-save-btn" onclick={submitEdit}>Save</button>
            </div>
        </div>
    </dialog>
{/if}

<style>
    .chat-message {
        padding: 8px 0;
        /* Skip layout/paint for off-screen messages */
        content-visibility: auto;
        contain-intrinsic-size: 1px 300px;
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
    }
    .queued-indicator {
        flex-shrink: 0;
        color: var(--color-text-secondary);
        display: flex;
        align-items: center;
    }
    .assistant .message-bubble {
        max-width: 100%;
        background: var(--color-surface-3);
        border-radius: var(--radius-md);
        padding: 10px 14px;
        border: 1px solid var(--color-border);
    }
    .message-text {
        font-size: 1rem;
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
        /* Ensure text respects bubble width on mobile — inline elements (a, code, strong)
         * inside flex children don't shrink by default, so content can overflow the bubble. */
        min-width: 0;
        overflow: hidden;
    }
    .assistant .message-text.streaming {
        border-left: 2px solid var(--color-accent);
        padding-left: 12px;
    }
    .system-text {
        color: var(--color-text-tertiary);
        font-style: italic;
        font-size: 0.8125rem;
        line-height: 1.5rem;
    }
    .reasoning-block {
        margin-bottom: 6px;
    }
    .reasoning-toggle {
        display: inline-flex;
        align-items: center;
        gap: 4px;
        background: none;
        border: none;
        color: var(--color-text-tertiary);
        font-size: 0.75rem;
        cursor: pointer;
        padding: 2px 6px;
        border-radius: var(--radius-sm);
        transition: all 0.15s ease;
    }
    .reasoning-toggle:hover {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }
    .reasoning-toggle:disabled {
        opacity: 0.5;
        cursor: wait;
    }
    .reasoning-content {
        margin-top: 4px;
        padding: 8px 12px;
        background: var(--color-surface-0);
        border-radius: var(--radius-sm);
        font-size: 0.8125rem;
        line-height: 1.5rem;
        color: var(--color-text-secondary);
        white-space: pre-wrap;
        font-style: italic;
    }
    .reasoning-loading {
        color: var(--color-text-tertiary);
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
        color: var(--color-accent-hover);
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
        background: var(--color-surface-3);
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
    .message-stats {
        display: flex;
        align-items: center;
        gap: 6px;
        padding: 2px 4px 4px;
        font-size: 0.75rem;
        color: var(--color-text-tertiary);
    }
    .stats-model-name {
        font-size: 0.75rem;
        color: var(--color-text-secondary);
    }
    .stat-separator {
        font-size: 0.75rem;
        color: var(--color-text-tertiary);
    }
    .stat-item {
        font-size: 0.75rem;
        color: var(--color-text-secondary);
    }
    .message-actions {
        display: flex;
        align-items: center;
        gap: 2px;
        padding: 2px 4px;
    }
    .user-actions {
        justify-content: flex-end;
    }
    .assistant-actions {
        justify-content: flex-start;
    }
    .message-timestamp {
        display: flex;
        align-items: center;
        font-size: 0.75rem;
        color: var(--color-text-secondary);
        white-space: nowrap;
        margin: 0 4px;
        padding: 4px 0;
        height: 22px;
    }
    .user-actions .message-timestamp {
        margin-left: 0;
    }
    .assistant-actions .message-timestamp {
        margin: 0;
    }
    .action-btn {
        background: none;
        border: none;
        color: var(--color-text-tertiary);
        cursor: pointer;
        padding: 4px;
        border-radius: var(--radius-sm);
        display: flex;
        align-items: center;
        justify-content: center;
        transition: all 0.15s ease;
        position: relative;
        width: 22px;
        height: 22px;
        flex-shrink: 0;
    }
    .action-btn:hover {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }
    .action-btn .icon {
        opacity: 1;
        transition: opacity 0.2s ease;
    }
    .action-btn.copied .icon {
        opacity: 0;
        pointer-events: none;
        position: absolute;
    }
    .action-btn .check {
        position: absolute;
        opacity: 0;
        font-size: 14px;
        font-weight: 700;
        line-height: 1;
        transition: opacity 0.2s ease;
        pointer-events: none;
    }
    .action-btn.copied .check {
        opacity: 1;
    }
    .edit-form {
        display: flex;
        flex-direction: column;
        gap: 6px;
        max-width: 80%;
        width: 100%;
    }
    .edit-form textarea {
        width: 100%;
        min-height: 60px;
        padding: 8px 12px;
        border-radius: var(--radius-md);
        border: 1px solid var(--color-border);
        background: var(--color-surface-2);
        color: var(--color-text-primary);
        font-size: 0.875rem;
        line-height: 1.5rem;
        font-family: inherit;
        resize: vertical;
    }
    .edit-form textarea:focus-visible {
        border-color: var(--color-accent);
        box-shadow: 0 0 0 2px var(--color-accent)22;
        outline: none;
    }
    .edit-actions {
        display: flex;
        gap: 6px;
        justify-content: flex-end;
        align-items: center;
    }
    .edit-attachments {
        display: flex;
        flex-wrap: wrap;
        gap: 6px;
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
    .btn-edit-attach {
        background: none;
        border: none;
        cursor: pointer;
        padding: 4px;
        color: var(--color-text-secondary);
        opacity: 0.6;
        display: flex;
        align-items: center;
    }
    .btn-edit-attach:hover {
        opacity: 1;
        color: var(--color-text-primary);
    }
    .btn-edit-cancel {
        display: inline-flex;
        align-items: center;
        gap: 4px;
        padding: 4px 12px;
        border-radius: var(--radius-sm);
        font-size: 0.8125rem;
        background: var(--color-surface-3);
        color: var(--color-text-primary);
        cursor: pointer;
    }
    .btn-edit-cancel:hover {
        background: var(--color-surface-4);
    }
    .btn-edit-submit {
        display: inline-flex;
        align-items: center;
        gap: 4px;
        padding: 4px 12px;
        border-radius: var(--radius-sm);
        font-size: 0.8125rem;
        background: var(--color-accent);
        color: white;
        cursor: pointer;
    }
    .btn-edit-submit:hover {
        opacity: 0.85;
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

    /* Mobile edit */
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
        background: var(--color-accent);
        color: #fff;
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


</style>
