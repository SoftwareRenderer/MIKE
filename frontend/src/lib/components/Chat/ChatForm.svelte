<script lang="ts">
    import type { Provider } from '$lib/types';
    import { AGENT_URL_PREFIX } from '$lib/types';
    import ModelSelector from './ModelSelector.svelte';
    import ContextUsage from './ContextUsage.svelte';
    import ModeToggle from './ModeToggle.svelte';
    import FileMentionDropdown from '../FileMentionDropdown.svelte';
    import { useFileMention } from '$lib/composables/use-file-mention.svelte';
    import { uploadTaskFile } from '$lib/api';
    import type { DraftAttachment } from '$lib/storage';
    import { useImageAttachments } from '$lib/composables/use-image-attachments.svelte';
    import { imageSrc } from '$lib/utils/attachments';
    import { t } from '$lib/i18n';

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
        sessionCost?: number;
        operatingMode?: string;
        attachments?: DraftAttachment[];
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
        onInputFocus?: () => void;
        onInputBlur?: () => void;
    }

    let {
        value = $bindable(''),
        disabled = false,
        isLoading = false,
        placeholder = '',
        providers = [],
        selectedModel = '',
        selectedUrl = '',
        selectedReasoningEffort = '',
        contextUsed = 0,
        contextLimit = 0,
        sessionCost = 0,
        operatingMode = 'act',
        attachments = $bindable<DraftAttachment[]>([]),
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
        onInputFocus,
        onInputBlur,
    }: Props = $props();
    let canSend = $derived((value.trim().length > 0 || attachments.length > 0) && !disabled && !(providers.length > 0 && !selectedModel));
    let isAgent = $derived(selectedUrl.startsWith(AGENT_URL_PREFIX));
    let showModelSelector = $derived(providers.length > 0 || isAgent);
    let contextDisplay = $derived({ used: contextUsed, limit: contextLimit });

    // The placeholder default is copy, so it cannot live in the prop default (a default is
    // evaluated with the props, not with the locale): an empty prop means "use the catalog".
    const effectivePlaceholder = $derived(placeholder || t('chat.form.placeholder'));

    let textareaEl: HTMLTextAreaElement | undefined;
    let fileInputEl: HTMLInputElement | undefined;
    let formEl: HTMLFormElement | undefined;

    // @ file mention — shared state machine (see use-file-mention.svelte.ts)
    const mention = useFileMention({
        getProjectId: () => projectId,
        getTaskId: () => taskId,
        getValue: () => value,
        setValue: (v) => { value = v; },
        getTextarea: () => textareaEl,
        // Position dropdown over the input area (chat-form-inner = textarea's parent)
        getPositionSource: () => textareaEl?.parentElement,
    });

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

    function handleInputFocus() {
        // On mobile, scroll the textarea into view when the keyboard appears.
        // This is a fallback for browsers with limited visualViewport support (e.g., Firefox Android).
        if (textareaEl) {
            textareaEl.scrollIntoView({ block: 'end', behavior: 'instant' });
        }
        onInputFocus?.();
    }

    function handleInputBlur(e: FocusEvent) {
        const next = e.relatedTarget as Node | null;
        if (next && formEl?.contains(next)) return;
        if (mention.isOpen) return;
        onInputBlur?.();
    }

    /**
     * Escape semantics: the chat input never cancels generation with Escape (an
     * in-flight turn is stopped only via the Stop button) — Escape must mean the
     * same thing whether or not the model is streaming, because the textarea stays
     * editable while a turn runs (drafting the next message).
     * With the @ mention dropdown open, Escape closes it and keeps focus in the
     * input. Otherwise Escape blurs the input and swallows the event, so the
     * Task View's window-level "back to board" Escape fires on the NEXT press,
     * not this one (blur-first).
     */
    function handleKeydown(e: KeyboardEvent) {
        if (mention.handleKeydown(e)) return;
        if (e.key === 'Escape') {
            e.preventDefault();
            e.stopPropagation();
            textareaEl?.blur();
            return;
        }
        if (e.key === 'Enter' && !e.shiftKey) {
            e.preventDefault();
            if (canSend) {
                handleSubmit();
            }
        }
    }

    const intake = useImageAttachments({
        getTextarea: () => textareaEl,
        getValue: () => value,
        setValue: (v) => { value = v; },
        held: () => attachments.length,
        addImages: (accepted) => { attachments = [...attachments, ...accepted]; },
        uploadFile: (file) => uploadTaskFile(projectId, taskId, file),
        hasTask: () => !!projectId && !!taskId,
        onChanged: autoResize,
    });
    function removeAttachment(index: number) {
        attachments = attachments.filter((_, i) => i !== index);
    }

    function handleSubmit() {
        if (!canSend) return;
        const msg = value.trim();
        const imageAttachments = attachments.map(a => ({ mimeType: a.mimeType, data: a.data }));

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

<form class="chat-form" bind:this={formEl} onsubmit={(e) => { e.preventDefault(); handleSubmit(); }}>
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
            <ContextUsage used={contextDisplay.used} limit={contextDisplay.limit} sessionCost={sessionCost} {onSummarize} onSummarizeCancel={isAgent ? undefined : onSummarizeCancel} {isSummarizing} disabled={disabled} />
            {#if onOperatingModeToggle && !isAgent}
                <ModeToggle variant="bar" mode={operatingMode} onToggle={onOperatingModeToggle} />
            {/if}
        </div>
    {/if}
    <!-- Image attachments preview -->
    {#if attachments.length > 0}
        <div class="attachment-preview">
            {#each attachments as att, i}
                <div class="attachment-thumb">
                    <img src={imageSrc(att)} alt="" />
                    <button type="button" class="attachment-remove" onclick={() => removeAttachment(i)} aria-label={t('chat.form.removeImage')} title={t('chat.common.remove')}>
                        <svg width="12" height="12" viewBox="0 0 16 16" fill="none"><path d="M4 4l8 8M12 4l-8 8" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg>
                    </button>
                </div>
            {/each}
        </div>
    {/if}
    <div
        class="chat-form-inner image-drop-target"
        class:is-drop-target={intake.isOver}
        role="presentation"
        ondragenter={intake.handleDragEnter}
        ondragover={intake.handleDragOver}
        ondragleave={intake.handleDragLeave}
        ondrop={intake.handleDrop}
    >
        <label for="chat-message-input" class="sr-only">{t('chat.form.input')}</label>
        <textarea
            id="chat-message-input"
            bind:this={textareaEl}
            bind:value
            {disabled}
            placeholder={effectivePlaceholder}
            aria-label={t('chat.form.input')}
            rows="1"
            onkeydown={handleKeydown}
            onpaste={intake.handlePaste}
            oninput={(e) => { autoResize(); mention.handleInput(); }}
            onfocus={handleInputFocus}
            onblur={handleInputBlur}
        ></textarea>
        <button type="button" class="btn-attach" onclick={() => fileInputEl?.click()} aria-label={t('chat.common.attachFile')} title={t('chat.common.attachFile')} disabled={disabled || isLoading}>
            <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                <path d="M21.44 11.05l-9.19 9.19a6 6 0 01-8.49-8.49l9.19-9.19a4 4 0 015.66 5.66l-9.2 9.19a2 2 0 01-2.83-2.83l8.49-8.48"/>
            </svg>
        </button>
        <!-- No `accept` here, and not by oversight — see ATTACHABLE_IMAGE_ACCEPT in $lib/utils/attachments. -->
        <input type="file" bind:this={fileInputEl} class="file-input-hidden" multiple onchange={intake.handlePicked} />
        {#if isLoading && (!value.trim() && attachments.length === 0)}
            <button type="button" class="btn-stop" onclick={handleStop} aria-label={t('chat.form.stop')} title={t('chat.form.stop')}>
                <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                    <rect x="4" y="4" width="8" height="8" rx="1.5" fill="currentColor"/>
                </svg>
            </button>
        {:else}
            <button type="submit" class="btn-send" disabled={!canSend} aria-label={t('chat.form.send')} title={t('chat.form.send')}>
                <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                    <path d="M2 2l12 6-12 6V9l6-1-6-1V2z" fill="currentColor"/>
                </svg>
            </button>
        {/if}
    </div>
</form>

<!-- @ file mention dropdown (rendered as portal overlay) -->
<FileMentionDropdown
    results={mention.results}
    position={mention.position}
    activeIndex={mention.activeIndex}
    onSelect={(path) => mention.select(path)}
    onClose={() => mention.handleClose()}
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
        flex-wrap: wrap;
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
