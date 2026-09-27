<script lang="ts">
    import { onDestroy } from 'svelte';
    import { dialogStore } from '$lib/stores';
    import BaseModal from '../BaseModal.svelte';
    import Spinner from '$lib/components/Spinner.svelte';
    import { t } from '$lib/i18n';
    import { streamCommitPreview } from '$lib/api/tasks';
    import { isAborted } from '$lib/api/helpers';

    let open = $state(false);
    let streaming = $state(false);
    let message = $state('');
    let editedMessage = $state('');
    // Caller-provided button labels stay raw: only the absence of one is copy, and resolving
    // it in a derived is what lets a locale switch re-label an already-open dialog.
    let confirmOverride = $state<string | null>(null);
    let cancelOverride = $state<string | null>(null);
    // The commit click asks for commit + merge + Done and a merge conflict only interrupts it
    // (docs/commit-and-done.md) — this label must keep saying "and move to Done", never "cancel".
    let confirmText = $derived(confirmOverride ?? t('chat.review.commitAndDone'));
    let cancelText = $derived(cancelOverride ?? t('chat.common.cancel'));
    let authorName = $state('');
    let authorEmail = $state('');
    let mergeTarget = $state('');
    let promptProgress = $state(0);
    let resolveFn: ((value: string | null) => void) | null = $state(null);
    let textareaRef: HTMLTextAreaElement | null = $state(null);
    let abortController: AbortController | null = null;
    // Ref to survive effect re-runs; guards against duplicate stream requests.
    const streamState = { count: 0 };

    function dismiss(value: string | null) {
        open = false;
        streaming = false;
        streamState.count = 0;
        if (abortController) {
            abortController.abort();
            abortController = null;
        }
        if (resolveFn) {
            resolveFn(value);
            resolveFn = null;
        }
        dialogStore.commitDialogSlot.clear();
    }

    function handleConfirm() {
        dismiss(editedMessage.trim() || null);
    }

    function handleCancel() {
        dismiss(null);
    }

    function handleKeydown(e: KeyboardEvent) {
        if (!open) return;
        if (e.key === 'Enter' && e.ctrlKey) {
            e.preventDefault();
            handleConfirm();
        }
    }

    function handleMessageChange() {
        editedMessage = message;
        // Cancel streaming commit generation when the user starts typing
        if (streaming && abortController) {
            abortController.abort();
            abortController = null;
            streaming = false;
        }
    }

    // Paint the preview stream. Framing, the request and the frame payloads are the API layer's;
    // this only decides what each event does to the dialog.
    async function startPreview(projectId: string, taskId: string) {
        abortController = new AbortController();
        streaming = true;
        message = '';
        editedMessage = '';
        promptProgress = 0;

        try {
            for await (const frame of streamCommitPreview(projectId, taskId, abortController.signal)) {
                switch (frame.event) {
                    case 'metadata': {
                        authorName = frame.authorName;
                        authorEmail = frame.authorEmail;
                        mergeTarget = frame.mergeTarget;
                        break;
                    }
                    case 'token': {
                        message += frame.content;
                        editedMessage = message;
                        // Auto-scroll textarea
                        setTimeout(() => {
                            if (textareaRef) {
                                textareaRef.scrollTop = textareaRef.scrollHeight;
                            }
                        }, 0);
                        break;
                    }
                    case 'prompt_progress': {
                        promptProgress = frame.percent;
                        break;
                    }
                    case 'done': {
                        message = frame.content || message;
                        editedMessage = message;
                        streaming = false;
                        break;
                    }
                    case 'error': {
                        console.error('Commit preview error:', frame.error);
                        streaming = false;
                        break;
                    }
                }
            }
        } catch (e) {
            if (isAborted(e)) {
                // User cancelled, don't show error
                return;
            }
            console.error('Commit preview stream failed:', e);
            streaming = false;
        } finally {
            abortController = null;
        }
    }

    $effect(() => {
        const value = dialogStore.commitDialogSlot.pending;
        if (value) {
            // Only initialize on first run; re-runs would wipe streamed tokens.
            if (streamState.count === 0) {
                message = value.message || '';
                editedMessage = value.message || '';
                confirmOverride = value.confirmText ?? null;
                cancelOverride = value.cancelText ?? null;
                authorName = value.authorName || '';
                authorEmail = value.authorEmail || '';
                mergeTarget = value.mergeTarget || '';
                promptProgress = value.promptProgress || 0;
                streaming = value.streaming ?? false;
                resolveFn = value._resolve ?? null;
                open = true;

                if (value.projectId && value.taskId && !value.message) {
                    streamState.count++;
                    startPreview(value.projectId, value.taskId);
                }

                if (!streaming) {
                    setTimeout(() => {
                        textareaRef?.focus();
                    }, 100);
                }
            }

            // Abort only on dialog close, not on effect re-runs.
            return () => {
                if (dialogStore.commitDialogSlot.pending === null && abortController) {
                    abortController.abort();
                    abortController = null;
                }
            };
        }
    });

    $effect(() => {
        window.addEventListener('keydown', handleKeydown);
        return () => window.removeEventListener('keydown', handleKeydown);
    });

    onDestroy(() => {
        if (abortController) {
            abortController.abort();
        }
    });

    let placeholderText = $derived(streaming ? t('chat.commit.generating') : t('chat.commit.placeholder'));

    $effect(() => {
        // When streaming finishes and message is populated, focus the textarea.
        if (open && !streaming && message) {
            setTimeout(() => {
                textareaRef?.focus();
            }, 100);
        }
    });
</script>

{#if open}
    {#snippet footer()}
        <button type="button" class="btn-secondary" onclick={handleCancel}>{cancelText}</button>
        <button
            type="button"
            class="btn-primary"
            onclick={handleConfirm}
            disabled={!editedMessage.trim() || streaming}
        >
            {confirmText}
        </button>
    {/snippet}

    <BaseModal title={t('chat.commit.title')} width="560px" footer={footer} onClose={handleCancel}>
        <p id="commit-dialog-desc" class="sr-only">
            {t('chat.commit.desc')}
        </p>

        <textarea
            bind:this={textareaRef}
            bind:value={message}
            oninput={handleMessageChange}
            class="commit-textarea"
            rows="7"
            placeholder={placeholderText}
            aria-label={t('chat.commit.messageAria')}
        ></textarea>

        {#if streaming}
            <div class="commit-progress" role="status" aria-busy={streaming}>
                <Spinner size={14} decorative />
                {#if promptProgress > 0 && promptProgress < 100}
                    <span class="progress-text">{t('chat.commit.promptProgress', { percent: promptProgress })}</span>
                {:else}
                    <span class="progress-text">{t('chat.commit.generatingShort')}</span>
                {/if}
            </div>
        {/if}

        {#if authorName || authorEmail}
            <div class="commit-meta">
                {t('chat.commit.committingAs')} {authorName}{authorEmail ? ` <${authorEmail}>` : ''}
            </div>
        {/if}
        {#if mergeTarget}
            <div class="commit-meta">
                {t('chat.commit.mergeInto')} <code>{mergeTarget}</code>
            </div>
        {/if}
    </BaseModal>
{/if}

<style>
    /* Screen-reader only utility */

    :global(.overlay) {
        animation: fade-in 0.15s ease-out;
    }
    :global(.modal) {
        animation: scale-in 0.15s ease-out;
    }

    .commit-textarea {
        width: 100%;
        padding: 10px 12px;
        border-radius: var(--radius-sm);
        border: 1px solid var(--color-border);
        background: var(--color-surface-0);
        color: var(--color-text-primary);
        font-size: var(--text-sm);
        font-family: inherit;
        line-height: 1.5;
        resize: vertical;
        outline: none;
        transition: border-color 0.15s;
        box-sizing: border-box;
    }

    .commit-textarea:focus {
        border-color: var(--color-accent);
        box-shadow: 0 0 0 2px color-mix(in srgb, var(--color-accent) 30%, transparent);
    }

    .commit-textarea::placeholder {
        color: var(--color-text-tertiary);
    }

    .commit-progress {
        display: flex;
        align-items: center;
        gap: 8px;
        margin-top: 10px;
        color: var(--color-text-secondary);
        font-size: var(--text-xs);
    }

    .progress-text {
        flex: 1;
    }

    .commit-meta {
        margin-top: 10px;
        font-size: var(--text-xs);
        color: var(--color-text-secondary);
    }

    .commit-meta code {
        background: var(--color-surface-2);
        padding: 1px 5px;
        border-radius: var(--radius-sm);
        font-family: var(--font-mono);
        font-size: var(--text-xs);
    }
</style>
