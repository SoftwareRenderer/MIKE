<script lang="ts">
    import type { Provider } from '$lib/types';
    import { PI_SENTINEL_URL } from '$lib/types';
    import ModelSelector from './ModelSelector.svelte';
    import ContextUsage from './ContextUsage.svelte';
    import FileMentionDropdown from '../FileMentionDropdown.svelte';
    import { detectMentionAtCursor, insertMention, getInputRect } from '$lib/utils/file-mention';

    interface ImageAttachment {
        file: File;
        preview: string;
        mimeType: string;
        base64: string;
    }

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
        operatingMode?: string;
        attachments?: ImageAttachment[];
        projectId?: string;
        taskId?: string;
        onSubmit?: (message: string, attachments?: Array<{ mimeType: string; data: string }>) => void;
        onClearDraft?: () => void;
        onStop?: () => void;
        onModelSelect?: (providerName: string, modelName: string, providerUrl: string, reasoningEffort?: string) => void;
        onOperatingModeToggle?: () => void;
        onSummarize?: () => Promise<void>;
        onSummarizeCancel?: () => void;
        isSummarizing?: boolean;
        onInput?: (value: string) => void;
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
        operatingMode = 'act',
        attachments = $bindable<ImageAttachment[]>([]),
        projectId = '',
        taskId = '',
        onSubmit,
        onStop,
        onModelSelect,
        onOperatingModeToggle,
        onSummarize,
        onSummarizeCancel,
        isSummarizing = false,
        onInput,
        onClearDraft,
    }: Props = $props();
    let canSend = $derived((value.trim().length > 0 || attachments.length > 0) && !disabled && !(providers.length > 0 && !selectedModel));
    let showModelSelector = $derived(providers.length > 0);
    let isAgent = $derived(selectedUrl === PI_SENTINEL_URL);
    let contextDisplay = $derived({ used: contextUsed, limit: contextLimit });
    const MAX_IMAGE_SIZE = 4 * 1024 * 1024; // 4MB
    const MAX_IMAGES = 4;

    let textareaEl: HTMLTextAreaElement | undefined;
    let fileInputEl: HTMLInputElement | undefined;

    // @ file mention state
    let mentionTrigger = $state<{ atIndex: number; query: string } | null>(null);
    let mentionResults = $state<Array<{ path: string }>>([]);
    let mentionPosition = $state<{ top: number; bottom: number; left: number; width: number } | null>(null);
    let mentionActiveIndex = $state(0);
    let mentionSearchTimer: ReturnType<typeof setTimeout> | null = null;
    const MENTION_DEBOUNCE_MS = 150;

    function handleMentionInput() {
        if (!textareaEl) return;
        const cursorPos = textareaEl.selectionStart ?? 0;
        const trigger = detectMentionAtCursor(value, cursorPos);

        if (trigger) {
            mentionTrigger = trigger;
            mentionActiveIndex = 0;
            // Position dropdown over the input area (chat-form-inner = textarea's parent)
            const container = textareaEl.parentElement as HTMLElement;
            const rect = getInputRect(container);
            mentionPosition = rect;

            // Debounced search
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
        if (taskId) params.set('task', taskId);
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
        const { value: newValue, cursorPos } = insertMention(value, mentionTrigger.atIndex, mentionTrigger.query, path);
        value = newValue;
        closeMention();
        // Restore cursor after reactive update
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
        if (e.key === 'Escape' && isLoading) {
            e.preventDefault();
            e.stopPropagation();
            handleStop();
            return;
        }
        if (e.key === 'Enter' && !e.shiftKey) {
            e.preventDefault();
            if (canSend) {
                handleSubmit();
            }
        }
    }

    function handleFileSelect(e: Event) {
        const target = e.target as HTMLInputElement;
        const files = target.files;
        if (!files || files.length === 0) return;

        for (let i = 0; i < files.length; i++) {
            if (attachments.length >= MAX_IMAGES) break;
            const file = files[i];
            if (!file.type.startsWith('image/')) continue;
            if (file.size > MAX_IMAGE_SIZE) {
                console.warn(`Image too large: ${file.name} (${(file.size / 1024 / 1024).toFixed(1)}MB > ${MAX_IMAGE_SIZE / 1024 / 1024}MB)`);
                continue;
            }
            readImageFile(file);
        }
        // Reset input so the same file can be selected again
        target.value = '';
    }

    function readImageFile(file: File) {
        const reader = new FileReader();
        reader.onload = (e) => {
            const result = e.target?.result as string;
            const base64 = result.split(',')[1] || result;
            attachments = [...attachments, {
                file,
                preview: result,
                mimeType: file.type,
                base64,
            }];
            autoResize();
        };
        reader.onerror = (e) => console.error('FileReader error:', e);
        reader.readAsDataURL(file);
    }

    function removeAttachment(index: number) {
        attachments = attachments.filter((_, i) => i !== index);
    }

    function handleSubmit() {
        if (!canSend) return;
        const msg = value.trim();
        const imageAttachments = attachments.map(a => ({
            mimeType: a.mimeType,
            data: a.base64,
        }));

        value = '';
        attachments = [];
        resetHeight();
        // Clear draft synchronously before onSubmit (component may unmount after)
        onClearDraft?.();
        onSubmit?.(msg, imageAttachments.length > 0 ? imageAttachments : undefined);
    }

    function handleStop() {
        onStop?.();
        setTimeout(() => focus(), 10);
    }

    $effect(() => {
        autoResize();
    });

    // Notify parent when value changes — persists draft text to IndexedDB
    $effect(() => {
        const _val = value;
        onInput?.(_val);
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
                onSelect={onModelSelect!}
            />
            {#if !isAgent}
                <ContextUsage used={contextDisplay.used} limit={contextDisplay.limit} onSummarize={onSummarize} onSummarizeCancel={onSummarizeCancel} {isSummarizing} />
            {/if}
            {#if onOperatingModeToggle && !isAgent}
                <button
                    type="button"
                    class="btn-plan-toggle"
                    class:active={operatingMode === 'plan'}
                    class:ideate={operatingMode === 'ideate'}
                    onclick={onOperatingModeToggle}
                    title={operatingMode === 'act' ? 'Act mode: AI is required to take formal actions. Click to switch.' : operatingMode === 'plan' ? 'Plan mode: AI decomposes work into subtasks. Click to switch.' : 'Ideate mode: Free-form brainstorming. Storage is mounted read-only. AI is not required to take formal actions. Click to switch.'}
                    aria-label="Toggle operating mode"
                    style="margin-left: auto"
                >
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                        {#if operatingMode === 'plan'}
                            <path d="M2 4h12M2 8h12M2 12h12" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
                        {:else if operatingMode === 'ideate'}
                            <path d="M5 6a3 3 0 1 1 6 0c0 2-1.5 3-1.5 3v1h-3v-1s-1.5-1-1.5-3Z" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round"/>
                            <path d="M6.5 10h3v1h-3Z" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round"/>
                            <path d="M6.5 12h3" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
                        {:else}
                            <path d="M3 8l4 4L13 4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                        {/if}
                    </svg>
                    <span>{operatingMode === 'plan' ? 'Plan' : operatingMode === 'ideate' ? 'Ideate' : 'Act'}</span>
                </button>
            {/if}
        </div>
    {/if}
    <!-- Image attachments preview -->
    {#if attachments.length > 0}
        <div class="attachment-preview">
            {#each attachments as att, i}
                <div class="attachment-thumb">
                    <img src={att.preview} alt="" />
                    <button type="button" class="attachment-remove" onclick={() => removeAttachment(i)} aria-label="Remove image" title="Remove">
                        <svg width="12" height="12" viewBox="0 0 16 16" fill="none"><path d="M4 4l8 8M12 4l-8 8" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg>
                    </button>
                </div>
            {/each}
        </div>
    {/if}
    <div class="chat-form-inner">
        <label for="chat-message-input" class="sr-only">Message input</label>
        <textarea
            id="chat-message-input"
            bind:this={textareaEl}
            bind:value
            {disabled}
            {placeholder}
            aria-label="Message input"
            rows="1"
            onkeydown={handleKeydown}
            oninput={(e) => { autoResize(); handleMentionInput(); }}
            onfocus={handleFocus}
        ></textarea>
        <button type="button" class="btn-attach" onclick={() => fileInputEl?.click()} aria-label="Attach image" title="Attach image" disabled={disabled || isLoading}>
            <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                <path d="M21.44 11.05l-9.19 9.19a6 6 0 01-8.49-8.49l9.19-9.19a4 4 0 015.66 5.66l-9.2 9.19a2 2 0 01-2.83-2.83l8.49-8.48"/>
            </svg>
        </button>
        <input type="file" bind:this={fileInputEl} class="file-input-hidden" accept="image/png,image/jpeg,image/gif,image/webp" multiple onchange={handleFileSelect} />
        {#if isLoading && (!value.trim() && attachments.length === 0)}
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

<!-- @ file mention dropdown (rendered as portal overlay) -->
<FileMentionDropdown
    results={mentionResults}
    position={mentionPosition}
    activeIndex={mentionActiveIndex}
    onSelect={handleMentionSelect}
    onClose={handleMentionClose}
/>

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
    .btn-plan-toggle {
        display: flex;
        align-items: center;
        gap: 4px;
        padding: 3px 8px;
        border-radius: var(--radius-md);
        border: 1px solid var(--color-border);
        background: var(--color-surface-2);
        color: var(--color-text-secondary);
        font-size: 0.75rem;
        font-weight: 500;
        cursor: pointer;
        transition: all 0.15s ease;
        flex-shrink: 0;
    }
    .btn-plan-toggle:hover {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }
    .btn-plan-toggle.active {
        background: color-mix(in srgb, var(--color-accent) 22%, transparent);
        border-color: var(--color-accent);
        color: #fff;
    }
    .btn-plan-toggle.ideate {
        background: color-mix(in srgb, var(--color-status-yellow) 15%, transparent);
        border-color: var(--color-status-yellow);
        color: var(--color-status-yellow);
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
    .attachment-preview {
        display: flex;
        gap: 8px;
        padding: 0 0 6px;
        flex-wrap: wrap;
    }
    .attachment-thumb {
        position: relative;
        width: 48px;
        height: 48px;
        border-radius: 8px;
        overflow: hidden;
        border: 1px solid var(--color-border);
        flex-shrink: 0;
    }
    .attachment-thumb img {
        width: 100%;
        height: 100%;
        object-fit: cover;
    }
    .attachment-remove {
        position: absolute;
        top: 2px;
        right: 2px;
        width: 16px;
        height: 16px;
        border-radius: 50%;
        background: var(--color-status-red-dark);
        color: white;
        border: none;
        cursor: pointer;
        display: flex;
        align-items: center;
        justify-content: center;
        padding: 0;
        opacity: 0.85;
    }
    .attachment-remove:hover {
        opacity: 1;
    }
    .btn-attach {
        flex-shrink: 0;
        width: 32px;
        height: 32px;
        border-radius: 50%;
        border: none;
        cursor: pointer;
        display: flex;
        align-items: center;
        justify-content: center;
        background: transparent;
        color: var(--color-text-tertiary);
        transition: all 0.15s ease;
        padding: 0;
    }
    .btn-attach:hover:not(:disabled) {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }
    .btn-attach:disabled {
        opacity: 0.3;
        cursor: not-allowed;
    }
    .file-input-hidden {
        display: none;
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
    .btn-send { background: var(--color-accent-dark); color: white; }
    .btn-send:hover:not(:disabled) { opacity: 0.85; transform: scale(1.05); }
    .btn-send:disabled { background: var(--color-surface-3); color: var(--color-text-tertiary); cursor: not-allowed; }
    .btn-stop {
        background: var(--color-status-red-dark);
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
        .chat-form-inner {
            align-items: center;
        }
        .btn-send,
        .btn-stop,
        .btn-attach {
            width: 44px;
            height: 44px;
        }
        textarea {
            padding: 8px 0;
        }
    }
</style>
