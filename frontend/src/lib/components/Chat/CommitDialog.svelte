<script lang="ts">
    import { onDestroy } from 'svelte';
    import { dialogStore } from '$lib/stores';
    import BaseModal from '../BaseModal.svelte';

    let open = $state(false);
    let streaming = $state(false);
    let message = $state('');
    let editedMessage = $state('');
    let confirmText = $state('Commit and move to Done');
    let cancelText = $state('Cancel');
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
        dialogStore.commitDialog = null;
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

    // Parse SSE stream from the commit preview endpoint
    async function streamCommitPreview(projectId: string, taskId: string) {
        abortController = new AbortController();
        streaming = true;
        message = '';
        editedMessage = '';
        promptProgress = 0;

        try {
            const res = await fetch(
                `/api/projects/${projectId}/tasks/${taskId}/commit-preview/stream`,
                {
                    method: 'GET',
                    credentials: 'include',
                    signal: abortController.signal,
                }
            );

            if (!res.ok) {
                throw new Error(`HTTP ${res.status}`);
            }

            const reader = res.body?.getReader();
            if (!reader) {
                throw new Error('No response body');
            }

            const decoder = new TextDecoder();
            let buffer = '';

            while (true) {
                const { done, value } = await reader.read();
                if (done) break;

                buffer += decoder.decode(value, { stream: true });

                // Process complete SSE frames
                const frames = buffer.split('\n\n');
                buffer = frames.pop() || ''; // keep incomplete frame in buffer

                for (const frame of frames) {
                    if (!frame.trim()) continue;

                    const lines = frame.split('\n');
                    let eventType = 'message';
                    let data = '';

                    for (const line of lines) {
                        if (line.startsWith('event: ')) {
                            eventType = line.slice(7).trim();
                        } else if (line.startsWith('data: ')) {
                            data = line.slice(6).trim();
                        }
                    }

                    if (!data) continue;

                    try {
                        const parsed = JSON.parse(data);

                        switch (eventType) {
                            case 'metadata': {
                                authorName = parsed.author_name || '';
                                authorEmail = parsed.author_email || '';
                                mergeTarget = parsed.merge_target || '';
                                break;
                            }
                            case 'token': {
                                message += parsed.content || '';
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
                                promptProgress = parsed.percent || 0;
                                break;
                            }
                            case 'done': {
                                message = parsed.content || message;
                                editedMessage = message;
                                streaming = false;
                                break;
                            }
                            case 'error': {
                                console.error('Commit preview error:', parsed.error);
                                streaming = false;
                                break;
                            }
                        }
                    } catch {
                        // Skip malformed JSON
                    }
                }
            }
        } catch (e: any) {
            if (e.name === 'AbortError') {
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
        const value = dialogStore.commitDialog;
        if (value) {
            // Only initialize on first run; re-runs would wipe streamed tokens.
            if (streamState.count === 0) {
                message = value.message || '';
                editedMessage = value.message || '';
                confirmText = value.confirmText ?? 'Commit and move to Done';
                cancelText = value.cancelText ?? 'Cancel';
                authorName = value.authorName || '';
                authorEmail = value.authorEmail || '';
                mergeTarget = value.mergeTarget || '';
                promptProgress = value.promptProgress || 0;
                streaming = value.streaming ?? false;
                resolveFn = value._resolve ?? null;
                open = true;

                if (value.projectId && value.taskId && !value.message) {
                    streamState.count++;
                    streamCommitPreview(value.projectId, value.taskId);
                }

                if (!streaming) {
                    setTimeout(() => {
                        textareaRef?.focus();
                    }, 100);
                }
            }

            // Abort only on dialog close, not on effect re-runs.
            return () => {
                if (dialogStore.commitDialog === null && abortController) {
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

    let placeholderText = $derived(streaming ? 'Generating commit message...' : 'Enter commit message...');

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

    <BaseModal title="Commit changes" width="560px" zIndex={1000} footer={footer} onClose={handleCancel}>
        <p id="commit-dialog-desc" class="sr-only">
            Review and edit the commit message, then confirm to commit changes and move the task to Done.
        </p>

        <textarea
            bind:this={textareaRef}
            bind:value={message}
            oninput={handleMessageChange}
            class="commit-textarea"
            rows="7"
            placeholder={placeholderText}
            aria-label="Commit message"
        ></textarea>

        {#if streaming}
            <div class="commit-progress" role="status" aria-busy={streaming}>
                <svg
                    class="progress-spinner"
                    width="14"
                    height="14"
                    viewBox="0 0 24 24"
                    fill="none"
                    stroke="currentColor"
                    stroke-width="2.5"
                    aria-hidden="true"
                >
                    <path d="M12 2a10 10 0 0 1 10 10"/>
                </svg>
                {#if promptProgress > 0 && promptProgress < 100}
                    <span class="progress-text">Processing prompt: {promptProgress}%</span>
                {:else}
                    <span class="progress-text">Generating...</span>
                {/if}
            </div>
        {/if}

        {#if authorName || authorEmail}
            <div class="commit-meta">
                Committing as: {authorName}{authorEmail ? ` <${authorEmail}>` : ''}
            </div>
        {/if}
        {#if mergeTarget}
            <div class="commit-meta">
                Merge into: <code>{mergeTarget}</code>
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
        font-size: 0.8125rem;
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
        font-size: 0.75rem;
    }

    .progress-text {
        flex: 1;
    }

    .progress-spinner {
        animation: spin 0.8s linear infinite;
        flex-shrink: 0;
        opacity: 0.6;
    }

    .commit-meta {
        margin-top: 10px;
        font-size: 0.75rem;
        color: var(--color-text-secondary);
    }

    .commit-meta code {
        background: var(--color-surface-2);
        padding: 1px 5px;
        border-radius: 3px;
        font-family: var(--font-mono);
        font-size: 0.75rem;
    }

    @keyframes fade-in {
        from { opacity: 0; }
        to { opacity: 1; }
    }

    @keyframes scale-in {
        from {
            opacity: 0;
            transform: scale(0.95);
        }
        to {
            opacity: 1;
            transform: scale(1);
        }
    }

    @keyframes spin {
        to { transform: rotate(360deg); }
    }
</style>
