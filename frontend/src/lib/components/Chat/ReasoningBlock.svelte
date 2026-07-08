<script lang="ts">
    import { getTaskMessageFull } from '$lib/api';

    interface Props {
        reasoningContent?: string;
        hasReasoning?: boolean;
        messageId?: string;
        projectId?: string;
        taskId?: string;
        isStreaming?: boolean;
        toolCallsLength?: number;
        content?: string;
    }

    let { reasoningContent, hasReasoning = false, messageId, projectId, taskId, isStreaming = false, toolCallsLength = 0, content = '' }: Props = $props();

    // Resolved reasoning content — fetched on-demand when backend suppresses it
    let resolvedReasoningContent = $state<string | undefined>();
    let reasoningLoading = $state(false);

    /** Effective reasoning content: resolved (fetched) value, or the prop value. */
    let effectiveReasoning = $derived(resolvedReasoningContent ?? reasoningContent);

    /** Whether to show the reasoning toggle. */
    let showReasoningToggle = $derived(hasReasoning || !!reasoningContent);

    let reasoningUserToggled = $state(false);
    let reasoningUserValue = $state(false);
    let reasoningExpanded = $derived(
        reasoningUserToggled
            ? reasoningUserValue
            : (effectiveReasoning === '[HIDDEN]' ? false : !effectiveReasoning || (effectiveReasoning.length <= 200 && !content) || (toolCallsLength > 0 && !content) || (isStreaming && !toolCallsLength))
    );

    async function handleReasoningToggle() {
        if (reasoningExpanded) {
            reasoningUserToggled = true;
            reasoningUserValue = false;
            return;
        }
        if (effectiveReasoning === '[HIDDEN]' && messageId && projectId && taskId && !resolvedReasoningContent) {
            reasoningLoading = true;
            try {
                const fullMsg = await getTaskMessageFull(projectId, taskId, messageId);
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
</script>

{#if showReasoningToggle}
    <div class="reasoning-block" class:expanded={reasoningExpanded}>
        <button class="reasoning-toggle" onclick={handleReasoningToggle} aria-expanded={reasoningExpanded} title={reasoningExpanded ? 'Hide reasoning' : 'Show reasoning'} disabled={reasoningLoading}>
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

<style>
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
</style>
