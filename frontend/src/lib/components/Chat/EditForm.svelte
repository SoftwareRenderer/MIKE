<script lang="ts">
    import FileMentionDropdown from '../FileMentionDropdown.svelte';
    import { useFileMention } from '$lib/composables/use-file-mention.svelte';
    import { dialogStore } from '$lib/stores';
    import { readImageAttachments, describeRejectedImages, MAX_IMAGES } from '$lib/utils/attachments';
    import { t } from '$lib/i18n';

    interface Props {
        content: string;
        attachments?: Array<{ mimeType: string; data: string }>;
        isFirstUserMessage?: boolean;
        isLastUserMessage?: boolean;
        taskStatus?: string;
        messageId?: string;
        projectId?: string;
        taskId?: string;
        isSubmitting?: boolean;
        onSubmit?: (content: string, attachments: Array<{ mimeType: string; data: string }>) => void;
        onCancel?: () => void;
    }

    let { content, attachments = [], isFirstUserMessage = false, isLastUserMessage = false, taskStatus = 'backlog', messageId, projectId = '', taskId = '', isSubmitting = false, onSubmit, onCancel }: Props = $props();

    let editContent = $state('');
    let textareaEl: HTMLTextAreaElement | null = $state(null);
    let editAttachments = $state<Array<{ mimeType: string; data: string }>>([]);

    $effect(() => { editContent = content; });
    $effect(() => { editAttachments = attachments ? [...attachments] : []; });
    let editFileInput: HTMLInputElement | null = $state(null);
    let canSubmit = $derived(editContent.trim().length > 0 || editAttachments.length > 0);

    // @ file mention — shared state machine (see use-file-mention.svelte.ts)
    const mention = useFileMention({
        getProjectId: () => projectId,
        getTaskId: () => taskId,
        getValue: () => editContent,
        setValue: (v) => { editContent = v; },
        getTextarea: () => textareaEl,
    });

    async function handleEditFileSelect(e: Event) {
        const input = e.target as HTMLInputElement;
        const files = input.files;
        if (!files || files.length === 0) return;
        const picked = await readImageAttachments(files, MAX_IMAGES - editAttachments.length);
        if (picked.accepted.length > 0) editAttachments = [...editAttachments, ...picked.accepted];
        const problem = describeRejectedImages(picked);
        if (problem) dialogStore.showToast(problem, 'error');
        if (editFileInput) editFileInput.value = '';
    }

    function removeEditAttachment(index: number) {
        editAttachments = editAttachments.filter((_, i) => i !== index);
    }

    function handleKeydown(e: KeyboardEvent) {
        if (mention.handleKeydown(e)) return;
        if (e.key === 'Enter' && !e.shiftKey) {
            e.preventDefault();
            if (!isSubmitting && canSubmit) onSubmit?.(editContent.trim(), editAttachments);
        } else if (e.key === 'Escape') {
            e.preventDefault();
            onCancel?.();
        }
    }

    /** Determine the submit button label based on context. */
    let submitLabel = $derived(() => {
        if (isFirstUserMessage && taskStatus === 'backlog') return t('chat.common.save');
        if (isFirstUserMessage) return t('chat.common.send');
        if (isLastUserMessage) return t('chat.common.send');
        return t('chat.common.newThread');
    });

    let submitIcon = $derived(() => {
        if (isFirstUserMessage && taskStatus === 'backlog') return 'check';
        if (isFirstUserMessage) return 'send';
        if (isLastUserMessage) return 'send';
        return 'fork';
    });

    $effect(() => {
        setTimeout(() => {
            textareaEl?.focus();
            textareaEl?.select();
        }, 50);
    });
</script>

<div class="edit-form">
    {#if editAttachments.length > 0}
        <div class="edit-attachments">
            {#each editAttachments as att, i}
                <div class="edit-attachment-chip">
                    <img src={att.data.startsWith('data:') ? att.data : `data:${att.mimeType};base64,${att.data}`} alt={t('chat.common.attachment')} />
                    <button class="remove-attachment" onclick={() => removeEditAttachment(i)} title={t('chat.common.remove')}>&times;</button>
                </div>
            {/each}
        </div>
    {/if}
    <textarea
        bind:this={textareaEl}
        bind:value={editContent}
        onkeydown={handleKeydown}
        oninput={() => mention.handleInput()}
        rows="3"
        aria-label={t('chat.edit.contentAria')}
    ></textarea>
    <div class="edit-actions">
        <input type="file" accept="image/*" multiple bind:this={editFileInput} onchange={handleEditFileSelect} style="display:none" />
        <button class="btn-edit-attach" onclick={() => editFileInput?.click()} title={t('chat.common.attachImage')}>
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                <path d="M21.44 11.05l-9.19 9.19a6 6 0 01-8.49-8.49l9.19-9.19a4 4 0 015.66 5.66l-9.2 9.19a2 2 0 01-2.83-2.83l8.49-8.48"/>
            </svg>
        </button>
        <button class="btn-edit-cancel" onclick={onCancel} title={t('chat.edit.cancelEditing')}>{t('chat.common.cancel')}</button>
        <button class="btn-edit-submit" onclick={() => { if (canSubmit) onSubmit?.(editContent.trim(), editAttachments); }} disabled={isSubmitting || !canSubmit} title={submitLabel()}>
            {#if isSubmitting}
                <span class="spinner-inline"></span>
            {:else if submitIcon() === 'check'}
                <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                    <polyline points="20 6 9 17 4 12"/>
                </svg>
                {t('chat.common.save')}
            {:else if submitIcon() === 'send'}
                <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                    <line x1="22" y1="2" x2="11" y2="13"/>
                    <polygon points="22 2 15 22 11 13 2 9 22 2"/>
                </svg>
                {t('chat.common.send')}
            {:else}
                <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                    <circle cx="18" cy="18" r="3"/>
                    <circle cx="6" cy="6" r="3"/>
                    <path d="M13 6h3a2 2 0 0 1 2 2v7"/>
                    <line x1="6" y1="9" x2="6" y2="21"/>
                </svg>
                {t('chat.common.newThread')}
            {/if}
        </button>
    </div>
</div>
<!-- @ file mention dropdown (edit mode) -->
<FileMentionDropdown
    results={mention.results}
    position={mention.position}
    activeIndex={mention.activeIndex}
    onSelect={(path) => mention.select(path)}
    onClose={() => mention.handleClose()}
/>

<style>
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
        background: var(--color-accent-dark);
        color: white;
        cursor: pointer;
    }
    .btn-edit-submit:hover {
        opacity: 0.85;
    }
    .btn-edit-submit:disabled {
        opacity: 0.6;
        cursor: not-allowed;
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
