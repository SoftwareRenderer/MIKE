<script lang="ts">
    import FileMentionDropdown from '../FileMentionDropdown.svelte';
    import { useFileMention } from '$lib/composables/use-file-mention.svelte';
    import { useImageAttachments } from '$lib/composables/use-image-attachments.svelte';
    import { uploadTaskFile } from '$lib/api';
    import { imageSrc } from '$lib/utils/attachments';
    import type { DraftAttachment } from '$lib/storage';
    import { editSubmitAction } from '$lib/utils/chat-entries';
    import { t } from '$lib/i18n';

    interface Props {
        content: string;
        attachments?: Array<{ mimeType: string; data: string }>;
        isFirstUserMessage?: boolean;
        isLastUserMessage?: boolean;
        taskStatus?: string;
        projectId?: string;
        taskId?: string;
        isSubmitting?: boolean;
        /** The dialog hands the form its whole box: no 80% cap, and the field grows into the space. */
        fill?: boolean;
        /** 'auto' labels the submit from the message's place in the thread (Send / New thread / Save).
         *  'save' rewrites text that already exists: Enter writes a newline, Ctrl/Cmd+Enter submits. */
        submitMode?: 'auto' | 'save';
        onSubmit?: (content: string, attachments: Array<{ mimeType: string; data: string }>) => void;
        onCancel?: () => void;
    }

    let { content, attachments = [], isFirstUserMessage = false, isLastUserMessage = false, taskStatus = 'backlog', projectId = '', taskId = '', isSubmitting = false, submitMode = 'auto', fill = false, onSubmit, onCancel }: Props = $props();

    let editContent = $state('');
    let textareaEl: HTMLTextAreaElement | null = $state(null);
    let editAttachments = $state<DraftAttachment[]>([]);

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

    const intake = useImageAttachments({
        getTextarea: () => textareaEl,
        getValue: () => editContent,
        setValue: (v) => { editContent = v; },
        held: () => editAttachments.length,
        addImages: (accepted) => { editAttachments = [...editAttachments, ...accepted]; },
        uploadFile: (file) => uploadTaskFile(projectId, taskId, file),
        hasTask: () => !!projectId && !!taskId,
    });

    function removeEditAttachment(index: number) {
        editAttachments = editAttachments.filter((_, i) => i !== index);
    }

    function handleKeydown(e: KeyboardEvent) {
        if (mention.handleKeydown(e)) return;
        if (e.key === 'Enter' && (submitMode === 'save' ? e.ctrlKey || e.metaKey : !e.shiftKey)) {
            e.preventDefault();
            if (!isSubmitting && canSubmit) onSubmit?.(editContent.trim(), editAttachments);
        } else if (e.key === 'Escape') {
            e.preventDefault();
            onCancel?.();
        }
    }

    /** The label, the icon and the routing are the one rule, read once. */
    const submitAction = $derived(
        editSubmitAction({ isFirstUserMessage, isLastUserMessage, taskStatus, forcedSave: submitMode === 'save' }),
    );
    const submitLabel = $derived(
        submitAction === 'save' ? t('chat.common.save')
            : submitAction === 'send' ? t('chat.common.send')
                : t('chat.common.newThread'),
    );
    const submitIcon = $derived(submitAction === 'save' ? 'check' : submitAction === 'send' ? 'send' : 'fork');

    $effect(() => {
        setTimeout(() => {
            textareaEl?.focus();
            textareaEl?.select();
        }, 50);
    });
</script>

<div
    class="edit-form image-drop-target"
    class:is-drop-target={intake.isOver}
    class:fill
    role="presentation"
    ondragenter={intake.handleDragEnter}
    ondragover={intake.handleDragOver}
    ondragleave={intake.handleDragLeave}
    ondrop={intake.handleDrop}
>
    {#if editAttachments.length > 0}
        <div class="edit-attachments">
            {#each editAttachments as att, i}
                <div class="edit-attachment-chip">
                    <img src={imageSrc(att)} alt={t('chat.common.attachment')} />
                    <button class="remove-attachment" onclick={() => removeEditAttachment(i)} title={t('chat.common.remove')}>&times;</button>
                </div>
            {/each}
        </div>
    {/if}
    <!-- The ring lights this box, so this box takes the drop; the paste belongs to the field
         the caret is in. -->
    <textarea
        bind:this={textareaEl}
        bind:value={editContent}
        onkeydown={handleKeydown}
        onpaste={intake.handlePaste}
        oninput={() => mention.handleInput()}
        rows="3"
        aria-label={t('chat.edit.contentAria')}
    ></textarea>
    <div class="edit-actions">
        <input type="file" multiple bind:this={editFileInput} onchange={intake.handlePicked} style="display:none" />
        <button class="btn-edit-attach" onclick={() => editFileInput?.click()} title={t('chat.common.attachFile')} aria-label={t('chat.common.attachFile')}>
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                <path d="M21.44 11.05l-9.19 9.19a6 6 0 01-8.49-8.49l9.19-9.19a4 4 0 015.66 5.66l-9.2 9.19a2 2 0 01-2.83-2.83l8.49-8.48"/>
            </svg>
        </button>
        <button class="btn-edit-cancel" onclick={onCancel} title={t('chat.edit.cancelEditing')}>{t('chat.common.cancel')}</button>
        <button class="btn-edit-submit" onclick={() => { if (canSubmit) onSubmit?.(editContent.trim(), editAttachments); }} disabled={isSubmitting || !canSubmit} title={submitLabel}>
            {#if isSubmitting}
                <span class="spinner-inline"></span>
            {:else if submitIcon === 'check'}
                <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                    <polyline points="20 6 9 17 4 12"/>
                </svg>
                {t('chat.common.save')}
            {:else if submitIcon === 'send'}
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
<FileMentionDropdown {...mention.dropdownProps} />

<style>
    @import '$lib/styles/shared.css';

    .edit-form {
        display: flex;
        flex-direction: column;
        gap: 6px;
        max-width: 80%;
        width: 100%;
    }
    .edit-form.fill {
        max-width: none;
        flex: 1;
        min-height: 0;
        padding: 16px;
    }
    .edit-form.fill textarea {
        flex: 1;
        min-height: 0;
        resize: none;
        font-size: var(--text-lg);
        line-height: 1.6;
    }
    .edit-form.fill .edit-actions {
        padding-top: 8px;
        border-top: 1px solid var(--color-border);
    }
    .edit-form textarea {
        width: 100%;
        min-height: 60px;
        padding: 8px 12px;
        border-radius: var(--radius-md);
        border: 1px solid var(--color-border);
        background: var(--color-surface-2);
        color: var(--color-text-primary);
        font-size: var(--text-base);
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
        font-size: var(--text-base);
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
        font-size: var(--text-sm);
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
        font-size: var(--text-sm);
        background: var(--color-accent-dark);
        color: white;
        cursor: pointer;
    }
    .btn-edit-submit:hover:not(:disabled) {
        opacity: 0.85;
    }
</style>
