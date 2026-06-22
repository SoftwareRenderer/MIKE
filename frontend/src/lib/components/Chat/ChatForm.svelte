<script lang="ts">
    import type { Provider } from '$lib/types';
    import ModelSelector from './ModelSelector.svelte';
    import ContextUsage from './ContextUsage.svelte';

    interface Props {
        value?: string;
        disabled?: boolean;
        isLoading?: boolean;
        placeholder?: string;
        providers?: Provider[];
        selectedModel?: string;
        selectedUrl?: string;
        selectedReasoningEffort?: string;
        contextUsed?: number;
        contextLimit?: number;
        onSubmit?: (message: string) => void;
        onStop?: () => void;
        onModelSelect?: (providerName: string, modelName: string, providerUrl: string, reasoningEffort?: string) => void;
    }

    let {
        value = $bindable(''),
        disabled = false,
        isLoading = false,
        placeholder = 'Type a message...',
        providers = [],
        selectedModel = '',
        selectedUrl = '',
        selectedReasoningEffort = '',
        contextUsed = 0,
        contextLimit = 0,
        onSubmit,
        onStop,
        onModelSelect,
    }: Props = $props();

    let textareaEl: HTMLTextAreaElement | undefined;
    let canSend = $derived(value.trim().length > 0 && !disabled && !(providers.length > 0 && !selectedModel));
    let showModelSelector = $derived(providers.length > 0);
    let contextDisplay = $derived({ used: contextUsed, limit: contextLimit });

    export function focus() {
        textareaEl?.focus();
    }

    export function resetHeight() {
        if (textareaEl) {
            textareaEl.style.height = 'auto';
        }
    }

    function autoResize() {
        if (textareaEl) {
            textareaEl.style.height = 'auto';
            textareaEl.style.height = Math.min(textareaEl.scrollHeight, 160) + 'px';
        }
    }

    function handleFocus() {
        // On mobile, scroll the textarea into view when the keyboard appears.
        // This is a fallback for browsers with limited visualViewport support (e.g., Firefox Android).
        if (textareaEl) {
            textareaEl.scrollIntoView({ block: 'end', behavior: 'instant' });
        }
    }

    function handleKeydown(e: KeyboardEvent) {
        if (e.key === 'Enter' && !e.shiftKey) {
            e.preventDefault();
            if (canSend) {
                handleSubmit();
            }
        }
    }

    function handleSubmit() {
        if (!canSend) return;
        const msg = value.trim();
        value = '';
        resetHeight();
        onSubmit?.(msg);
    }

    function handleStop() {
        onStop?.();
        setTimeout(() => focus(), 10);
    }

    $effect(() => {
        autoResize();
    });
</script>

<form class="chat-form" onsubmit={(e) => { e.preventDefault(); handleSubmit(); }}>
    <!-- Model selector bar -->
    {#if showModelSelector}
        <div class="model-bar">
            <ModelSelector
                {providers}
                currentModel={selectedModel}
                currentUrl={selectedUrl}
                currentReasoningEffort={selectedReasoningEffort}
                {disabled}
                onSelect={onModelSelect}
            />
            <ContextUsage used={contextDisplay.used} limit={contextDisplay.limit} />
        </div>
    {/if}
    <div class="chat-form-inner">
        <textarea
            bind:this={textareaEl}
            bind:value
            {disabled}
            {placeholder}
            rows="1"
            onkeydown={handleKeydown}
            oninput={autoResize}
            onfocus={handleFocus}
        ></textarea>
        {#if isLoading && !value.trim()}
            <button type="button" class="btn-stop" onclick={handleStop} aria-label="Stop generation" title="Stop generation">
                <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                    <rect x="4" y="4" width="8" height="8" rx="1.5" fill="currentColor"/>
                </svg>
            </button>
        {:else}
            <button type="submit" class="btn-send" disabled={!canSend} aria-label="Send message" title="Send message">
                <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                    <path d="M2 2l12 6-12 6V9l6-1-6-1V2z" fill="currentColor"/>
                </svg>
            </button>
        {/if}
    </div>
</form>

<style>
    .chat-form {
        padding: 8px 12px;
        border-top: 1px solid var(--color-border);
        background: var(--color-surface-0);
        flex-shrink: 0;
        display: flex;
        flex-direction: column;
        gap: 0;
    }
    .model-bar {
        padding: 0 0 6px;
        display: flex;
        align-items: center;
        gap: 8px;
    }
    .chat-form-inner {
        display: flex;
        align-items: flex-end;
        gap: 8px;
        background: var(--color-surface-2);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-lg);
        padding: 6px 8px;
        transition: border-color 0.15s ease, box-shadow 0.15s ease;
    }
    .chat-form-inner:focus-within {
        border-color: var(--color-accent);
        box-shadow: 0 0 0 2px var(--color-accent)22;
    }
    textarea {
        flex: 1;
        min-height: 24px;
        max-height: 160px;
        resize: none;
        border: none;
        background: transparent;
        color: var(--color-text-primary);
        font-size: 0.875rem;
        line-height: 1.5rem;
        padding: 4px 0;
        font-family: inherit;
        overflow-y: auto;
        outline: none;
    }
    textarea::placeholder { color: var(--color-text-tertiary); }
    textarea:disabled { opacity: 0.5; cursor: not-allowed; }
    .btn-send,
    .btn-stop {
        flex-shrink: 0;
        width: 32px;
        height: 32px;
        border-radius: 50%;
        border: none;
        cursor: pointer;
        display: flex;
        align-items: center;
        justify-content: center;
        transition: all 0.15s ease;
        padding: 0;
    }
    .btn-send { background: var(--color-accent); color: white; }
    .btn-send:hover:not(:disabled) { opacity: 0.85; transform: scale(1.05); }
    .btn-send:disabled { background: var(--color-surface-3); color: var(--color-text-tertiary); cursor: not-allowed; }
    .btn-stop {
        background: var(--color-status-red);
        color: white;
        animation: pulse-stop 1.5s ease-in-out infinite;
    }
    .btn-stop:hover { opacity: 0.85; transform: scale(1.05); }
    @keyframes pulse-stop {
        0%, 100% { opacity: 1; }
        50% { opacity: 0.7; }
    }

    @media (max-width: 768px) {
        .chat-form {
            padding: 8px;
        }
        .model-bar {
            overflow-x: auto;
            flex-wrap: nowrap;
            -webkit-overflow-scrolling: touch;
        }
        .btn-send,
        .btn-stop {
            width: 44px;
            height: 44px;
        }
        textarea {
            padding: 8px 0;
        }
    }
</style>
