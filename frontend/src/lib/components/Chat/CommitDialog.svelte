<script lang="ts">
    import { onMount, onDestroy } from 'svelte';
    import { commitDialog, type CommitDialogOptions } from '$lib/stores/dialogs';

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
    let overlayRef: HTMLDivElement | null = $state(null);
    // Track the element that had focus before the dialog opened, so we can restore it.
    let triggerElement: Element | null = $state(null);
    // Abort controller for streaming cancellation
    let abortController: AbortController | null = null;

    function dismiss(value: string | null) {
        open = false;
        streaming = false;
        if (abortController) {
            abortController.abort();
            abortController = null;
        }
        if (resolveFn) {
            resolveFn(value);
            resolveFn = null;
        }
        commitDialog.set(null);
        // Restore focus to the element that opened the dialog.
        // Use a short delay so the DOM has time to update after the dialog is removed.
        setTimeout(() => {
            triggerElement?.focus();
        }, 0);
    }

    function handleConfirm() {
        dismiss(editedMessage.trim() || null);
    }

    function handleCancel() {
        dismiss(null);
    }

    function handleOverlayClick(e: MouseEvent) {
        if (e.target === e.currentTarget) {
            handleCancel();
        }
    }

    function handleKeydown(e: KeyboardEvent) {
        if (!open) return;
        if (e.key === 'Escape') {
            e.preventDefault();
            handleCancel();
        } else if (e.key === 'Enter' && e.ctrlKey) {
            e.preventDefault();
            handleConfirm();
        } else if (e.key === 'Tab') {
            // Focus trap: keep Tab/Shift+Tab inside the dialog.
            trapFocus(e);
        }
    }

    function trapFocus(e: KeyboardEvent) {
        const el = overlayRef;
        if (!el) return;
        const focusable = el.querySelectorAll<HTMLElement>(
            'button:not([disabled]), textarea, input, [tabindex]:not([tabindex="-1"])'
        );
        const first = focusable[0];
        const last = focusable[focusable.length - 1];

        if (e.shiftKey) {
            if (document.activeElement === first) {
                e.preventDefault();
                last?.focus();
            }
        } else {
            if (document.activeElement === last) {
                e.preventDefault();
                first?.focus();
            }
        }
    }

    function handleMessageChange() {
        editedMessage = message;
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

    let unsub: (() => void) | null = null;

    onMount(() => {
        unsub = commitDialog.subscribe((value) => {
            if (value) {
                // Capture the currently focused element for later restoration.
                triggerElement = document.activeElement;
                message = value.message || '';
                editedMessage = value.message || '';
                confirmText = value.confirmText ?? 'Commit and move to Done';
                cancelText = value.cancelText ?? 'Cancel';
                authorName = value.authorName || '';
                authorEmail = value.authorEmail || '';
                mergeTarget = value.mergeTarget || '';
                promptProgress = value.promptProgress || 0;
                streaming = value.streaming ?? false;
                resolveFn = (value as CommitDialogOptions & { _resolve?: (value: string | null) => void })._resolve ?? null;
                open = true;

                // Start streaming if projectId and taskId are provided
                if (value.projectId && value.taskId && !value.message) {
                    streamCommitPreview(value.projectId, value.taskId);
                }

                // Focus textarea when dialog opens (after streaming starts).
                if (!streaming) {
                    setTimeout(() => {
                        textareaRef?.focus();
                    }, 100);
                }
            }
        });

        window.addEventListener('keydown', handleKeydown);
    });

    onDestroy(() => {
        unsub?.();
        window.removeEventListener('keydown', handleKeydown);
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
    <!-- svelte-ignore a11y_click_events_have_key_events — overlay click dismisses dialog, not an activation -->
    <div
        bind:this={overlayRef}
        class="commit-overlay"
        role="dialog"
        aria-modal="true"
        aria-labelledby="commit-dialog-title"
        aria-describedby="commit-dialog-desc"
        tabindex="-1"
        onmousedown={handleOverlayClick}
        onclick={handleOverlayClick}
    >
        <div class="commit-dialog">
            <h3 id="commit-dialog-title">Commit changes</h3>
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

            <div class="commit-actions">
                <button class="btn-cancel" onclick={handleCancel}>{cancelText}</button>
                <button
                    class="btn-confirm"
                    onclick={handleConfirm}
                    disabled={!editedMessage.trim() || streaming}
                    aria-disabled={!editedMessage.trim() || streaming}
                >
                    {confirmText}
                </button>
            </div>
        </div>
    </div>
{/if}

<style>
    /* Screen-reader only utility */
    .sr-only {
        position: absolute;
        width: 1px;
        height: 1px;
        padding: 0;
        margin: -1px;
        overflow: hidden;
        clip: rect(0, 0, 0, 0);
        white-space: nowrap;
        border: 0;
    }

    .commit-overlay {
        position: fixed;
        inset: 0;
        background: rgba(0, 0, 0, 0.6);
        display: flex;
        align-items: center;
        justify-content: center;
        z-index: 1000;
        animation: fade-in 0.15s ease-out;
    }

    .commit-dialog {
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-lg);
        padding: 24px 28px;
        max-width: 560px;
        width: 90%;
        animation: scale-in 0.15s ease-out;
    }

    .commit-dialog h3 {
        font-size: 0.875rem;
        font-weight: 600;
        margin: 0 0 12px;
        color: var(--color-text-primary);
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

    .commit-actions {
        display: flex;
        gap: 8px;
        justify-content: flex-end;
        margin-top: 16px;
    }

    .btn-cancel {
        padding: 6px 14px;
        border-radius: var(--radius-sm);
        border: 1px solid var(--color-border);
        background: var(--color-surface-2);
        color: var(--color-text-primary);
        font-size: 0.8125rem;
        cursor: pointer;
        transition: background 0.15s;
    }

    .btn-cancel:hover {
        background: var(--color-surface-3);
    }

    .btn-cancel:focus-visible {
        outline: 2px solid var(--color-accent);
        outline-offset: 2px;
    }

    .btn-confirm {
        padding: 6px 14px;
        border-radius: var(--radius-sm);
        border: none;
        background: var(--color-accent);
        color: white;
        font-size: 0.8125rem;
        font-weight: 500;
        cursor: pointer;
        transition: opacity 0.15s;
    }

    .btn-confirm:hover:not(:disabled) {
        opacity: 0.85;
    }

    .btn-confirm:focus-visible {
        outline: 2px solid var(--color-accent);
        outline-offset: 2px;
    }

    .btn-confirm:disabled {
        opacity: 0.5;
        cursor: not-allowed;
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
