<script lang="ts">
    import { onMount } from 'svelte';
    import { ensureCsrfToken } from '$lib/api';
    import { showToast } from '$lib/store';
    import { toHomeRelative } from '$lib/utils/path';
    import { getHighlighter, detectLanguage } from '$lib/highlighter';

    let { filePath, lineNumber, onClose }: { filePath: string; lineNumber: number; onClose: (saved: boolean) => void } = $props();

    let content = $state('');
    let originalContent = $state('');
    let loading = $state(true);
    let saving = $state(false);
    let error = $state('');
    let fileTooLarge = $state(false);
    let displayPath = $state('');
    let highlightedHtml = $state('');

    let hasChanges = $derived(content !== originalContent);
    let lineCount = $derived(content.split('\n').length);

    let textareaEl: HTMLTextAreaElement | null = null;
    let preEl: HTMLDivElement | null = null;
    let lineNumbersEl: HTMLDivElement | null = null;
    let showConfirm = $state(false);



    async function updateHighlight() {
        const lang = detectLanguage(filePath);
        if (!lang) {
            highlightedHtml = '';
            return;
        }
        try {
            const hl = await getHighlighter();
            const html = hl.codeToHtml(content, { lang, theme: 'github-dark' });
            highlightedHtml = html;
        } catch {
            highlightedHtml = '';
        }
    }

    // Debounced highlight update
    let highlightTimer: ReturnType<typeof setTimeout> | null = null;
    function scheduleHighlight() {
        if (highlightTimer) clearTimeout(highlightTimer);
        highlightTimer = setTimeout(updateHighlight, 150);
    }

    onMount(() => {
        loadFile();
        return () => {
            if (highlightTimer) clearTimeout(highlightTimer);
        };
    });

    // Re-highlight when content changes
    $effect(() => {
        if (!loading && content) scheduleHighlight();
    });

    async function loadFile() {
        loading = true;
        error = '';
        fileTooLarge = false;
        displayPath = toHomeRelative(filePath);

        try {
            const res = await fetch(`/api/fs/read?path=${encodeURIComponent(filePath)}`);
            if (!res.ok) {
                if (res.status === 413) {
                    fileTooLarge = true;
                    return;
                }
                const err = await res.json().catch(() => ({ error: 'Failed to read file' }));
                throw new Error(err.error || 'Failed to read file');
            }
            const data = await res.json();
            content = data.content || '';
            originalContent = content;
        } catch (e) {
            error = String(e);
        } finally {
            loading = false;
        }
    }

    async function handleSave() {
        if (saving || !hasChanges || fileTooLarge) return;
        saving = true;
        try {
            const csrfToken = await ensureCsrfToken();
            const res = await fetch('/api/fs/write', {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json',
                    ...(csrfToken ? { 'X-CSRF-Token': csrfToken } : {}),
                },
                body: JSON.stringify({ path: filePath, content }),
                credentials: 'include',
            });
            if (!res.ok) {
                const err = await res.json().catch(() => ({ error: 'Failed to save file' }));
                throw new Error(err.error || 'Failed to save file');
            }
            originalContent = content;
            showToast('File saved', 'success');
        } catch (e) {
            showToast('Failed to save: ' + String(e), 'error');
        } finally {
            saving = false;
        }
    }

    function handleClose() {
        onClose(true);
    }

    function handleKeyDown(e: KeyboardEvent) {
        if ((e.ctrlKey || e.metaKey) && e.key === 's') {
            e.preventDefault();
            if (hasChanges && !fileTooLarge) handleSave();
            return;
        }
        if (e.key === 'Escape') {
            e.stopPropagation();
            if (showConfirm) {
                showConfirm = false;
                return;
            }
            requestClose();
            return;
        }
    }

    function requestClose() {
        if (hasChanges) {
            showConfirm = true;
        } else {
            handleClose();
        }
    }

    function confirmClose(discard: boolean) {
        showConfirm = false;
        if (discard) handleClose();
    }

    function handleScroll() {
        if (textareaEl && preEl) {
            preEl.scrollTop = textareaEl.scrollTop;
            preEl.scrollLeft = textareaEl.scrollLeft;
        }
        if (textareaEl && lineNumbersEl) {
            lineNumbersEl.scrollTop = textareaEl.scrollTop;
        }
    }

    // Scroll to target line after content loads
    $effect(() => {
        if (!loading && textareaEl && !fileTooLarge && lineNumber >= 1) {
            const lineIndex = lineNumber - 1;
            const lines = content.split('\n');
            let charOffset = 0;
            for (let i = 0; i < lineIndex && i < lines.length; i++) {
                charOffset += lines[i]!.length + 1;
            }
            textareaEl.setSelectionRange(charOffset, charOffset);
            textareaEl.focus();

            requestAnimationFrame(() => {
                if (!textareaEl) return;
                const style = window.getComputedStyle(textareaEl);
                const fontSize = parseFloat(style.fontSize) || 13;
                const lineHeightRaw = style.lineHeight;
                let lineHeight: number;
                if (lineHeightRaw.endsWith('px')) {
                    lineHeight = parseFloat(lineHeightRaw) || fontSize * 1.5;
                } else {
                    lineHeight = (parseFloat(lineHeightRaw) || 1.5) * fontSize;
                }
                const viewportHeight = textareaEl.clientHeight;
                const targetScroll = lineIndex * lineHeight - viewportHeight / 3;
                textareaEl.scrollTop = Math.max(0, targetScroll);
                if (preEl) preEl.scrollTop = textareaEl.scrollTop;
                if (lineNumbersEl) lineNumbersEl.scrollTop = textareaEl.scrollTop;
            });
        }
    });
</script>

<div class="file-editor" tabindex="-1">
    <!-- Header -->
    <div class="editor-header">
        <div class="header-left">
            <button class="btn-back" onclick={requestClose} aria-label="Back to diff" title="Back to diff">
                <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                    <path d="M10 3l-5 5 5 5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                </svg>
            </button>
            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                <path d="M3 2h6l4 4v8a1 1 0 0 1-1 1H3a1 1 0 0 1-1-1V3a1 1 0 0 1 1-1z" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round"/>
                <path d="M9 2v4h4" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round"/>
            </svg>
            <span class="file-path" title={filePath}>{displayPath}</span>
            {#if hasChanges}
                <span class="unsaved-dot" title="Unsaved changes">●</span>
            {/if}
        </div>
        <button
            class="btn-save"
            onclick={handleSave}
            disabled={!hasChanges || saving || fileTooLarge}
            title="Save (Ctrl+S)"
        >
            {#if saving}
                <svg class="save-spinner" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5">
                    <path d="M12 2a10 10 0 0 1 10 10"/>
                </svg>
                Saving...
            {:else}
                Save
            {/if}
        </button>
    </div>

    <!-- Body -->
    <div class="editor-body">
        {#if loading}
            <div class="editor-loading">
                <svg class="spinner" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5">
                    <path d="M12 2a10 10 0 0 1 10 10"/>
                </svg>
                <span>Loading file...</span>
            </div>
        {:else if error}
            <div class="editor-error">
                <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round">
                    <circle cx="12" cy="12" r="10"/>
                    <path d="M12 8v4M12 16h.01"/>
                </svg>
                <span>{error}</span>
            </div>
        {:else if fileTooLarge}
            <div class="editor-large">
                <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round">
                    <path d="M4 2h8a2 2 0 012 2v8a2 2 0 01-2 2H4a2 2 0 01-2-2V4a2 2 0 012-2z"/>
                    <path d="M6 6l4 4M10 6l-4 4"/>
                </svg>
                <span>File too large to edit (max 1MB)</span>
            </div>
        {:else}
            <div class="editor-content">
                <!-- Line numbers gutter -->
                <div class="line-numbers" bind:this={lineNumbersEl} aria-hidden="true">
                    {#each Array.from({ length: lineCount }, (_, i) => i + 1) as lineNum}
                        <div class="line-number">{lineNum}</div>
                    {/each}
                </div>
                <!-- Code area: highlight layer + textarea overlay -->
                <div class="code-area">
                    {#if highlightedHtml}
                        <div class="highlight-layer" bind:this={preEl}>{@html highlightedHtml}</div>
                    {/if}
                    <textarea
                        class="editor-textarea"
                        bind:this={textareaEl}
                        bind:value={content}
                        onkeydown={handleKeyDown}
                        onscroll={handleScroll}
                        spellcheck="false"
                        aria-label="File content editor"
                    ></textarea>
                </div>
            </div>
        {/if}
    </div>

    <!-- Footer -->
    {#if !loading && !error && !fileTooLarge}
        <div class="editor-footer">
            <span class="line-info">{lineCount} lines · {content.length} chars</span>
            {#if hasChanges}
                <span class="unsaved-info">Unsaved changes</span>
            {/if}
        </div>
    {/if}
</div>

<!-- Unsaved changes confirmation dialog -->
{#if showConfirm}
    <div class="confirm-overlay" role="alertdialog" aria-modal="true" aria-labelledby="confirm-title" aria-describedby="confirm-desc">
        <div class="confirm-dialog" onclick={(e) => e.stopPropagation()}>
            <h3 id="confirm-title">Unsaved Changes</h3>
            <p id="confirm-desc">You have unsaved changes. Discard them?</p>
            <div class="confirm-actions">
                <button class="btn-confirm-cancel" onclick={() => confirmClose(false)}>Keep Editing</button>
                <button class="btn-confirm-discard" onclick={() => confirmClose(true)}>Discard & Close</button>
            </div>
        </div>
    </div>
{/if}

<style>
    .file-editor {
        display: flex;
        flex-direction: column;
        height: 100%;
        overflow: hidden;
    }

    .editor-header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 6px 8px;
        flex-shrink: 0;
        background: var(--color-surface-2);
        border-bottom: 1px solid var(--color-border);
        gap: 8px;
    }
    .header-left {
        display: flex;
        align-items: center;
        gap: 6px;
        min-width: 0;
        flex: 1;
        color: var(--color-text-secondary);
    }
    .header-left svg {
        flex-shrink: 0;
        opacity: 0.6;
    }
    .file-path {
        font-size: 0.75rem;
        font-weight: 500;
        font-family: var(--font-mono);
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }
    .unsaved-dot {
        align-self: center;
        color: var(--color-status-yellow, #f0c000);
        font-size: 0.875rem;
        line-height: 0;
        flex-shrink: 0;
    }
    .btn-back {
        background: none;
        border: none;
        color: var(--color-text-secondary);
        cursor: pointer;
        padding: 6px;
        border-radius: var(--radius-sm);
        display: flex;
        align-items: center;
        justify-content: center;
        flex-shrink: 0;
    }
    .btn-back:hover {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }

    .btn-save {
        padding: 4px 12px;
        background: var(--color-accent);
        color: white;
        border: none;
        border-radius: var(--radius-sm);
        font-size: 0.75rem;
        font-weight: 500;
        cursor: pointer;
        display: flex;
        align-items: center;
        gap: 5px;
        font-family: inherit;
        flex-shrink: 0;
    }
    .btn-save:hover:not(:disabled) {
        background: var(--color-accent-hover);
    }
    .btn-save:disabled {
        opacity: 0.4;
        cursor: not-allowed;
    }
    .save-spinner {
        animation: spin 0.8s linear infinite;
        transform-origin: center;
    }

    .editor-body {
        flex: 1;
        overflow: hidden;
        display: flex;
        flex-direction: column;
        min-height: 0;
    }

    .editor-loading,
    .editor-error,
    .editor-large {
        flex: 1;
        display: flex;
        flex-direction: column;
        align-items: center;
        justify-content: center;
        gap: 12px;
        color: var(--color-text-tertiary);
        font-size: 0.8125rem;
    }
    .editor-error {
        color: var(--color-status-red);
    }
    .spinner {
        animation: spin 0.8s linear infinite;
        transform-origin: center;
    }
    @keyframes spin {
        to { transform: rotate(360deg); }
    }

    .editor-content {
        flex: 1;
        display: flex;
        overflow: hidden;
        min-height: 0;
    }

    .line-numbers {
        flex-shrink: 0;
        width: 2.5rem;
        padding: 8px 6px 8px 0;
        text-align: right;
        font-family: var(--font-mono);
        font-size: 0.75rem;
        line-height: 1.5;
        color: var(--color-text-tertiary);
        background: var(--color-surface-1);
        overflow: hidden;
        user-select: none;
        border-right: 1px solid var(--color-border);
    }
    .line-number {
        height: 1.5em;
    }

    /* Code area: highlight + textarea overlay */
    .code-area {
        flex: 1;
        position: relative;
        overflow: hidden;
        min-width: 0;
    }

    .highlight-layer {
        position: absolute;
        inset: 0;
        margin: 0;
        padding: 8px;
        font-family: var(--font-mono);
        font-size: 0.75rem;
        line-height: 1.5;
        white-space: pre;
        overflow: hidden;
        pointer-events: none;
        tab-size: 4;
    }

    .editor-textarea {
        position: absolute;
        inset: 0;
        width: 100%;
        height: 100%;
        padding: 8px;
        border: none;
        outline: none;
        resize: none;
        font-family: var(--font-mono);
        font-size: 0.75rem;
        line-height: 1.5;
        color: transparent;
        background: transparent;
        caret-color: var(--color-text-primary);
        tab-size: 4;
        white-space: pre;
        overflow-wrap: normal;
        overflow: auto;
        z-index: 1;
    }

    .editor-footer {
        display: flex;
        align-items: center;
        gap: 12px;
        padding: 4px 10px;
        border-top: 1px solid var(--color-border);
        flex-shrink: 0;
        font-size: 0.6875rem;
        color: var(--color-text-tertiary);
        background: var(--color-surface-2);
    }
    .line-info {
        font-family: var(--font-mono);
    }
    .unsaved-info {
        color: var(--color-status-yellow, #f0c000);
    }

    .confirm-overlay {
        position: absolute;
        inset: 0;
        background: rgba(0, 0, 0, 0.3);
        display: flex;
        align-items: center;
        justify-content: center;
        z-index: 10;
        border-radius: 6px;
    }
    .confirm-dialog {
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-lg);
        padding: 20px;
        width: 320px;
        max-width: 90%;
        box-shadow: 0 8px 32px rgba(0, 0, 0, 0.3);
    }
    .confirm-dialog h3 {
        margin: 0 0 6px;
        font-size: 0.9375rem;
    }
    .confirm-dialog p {
        margin: 0 0 16px;
        font-size: 0.8125rem;
        color: var(--color-text-secondary);
    }
    .confirm-actions {
        display: flex;
        gap: 8px;
        justify-content: flex-end;
    }
    .btn-confirm-cancel {
        padding: 5px 14px;
        background: var(--color-surface-3);
        color: var(--color-text-primary);
        border: none;
        border-radius: var(--radius-sm);
        font-size: 0.75rem;
        cursor: pointer;
        font-family: inherit;
    }
    .btn-confirm-cancel:hover {
        background: var(--color-surface-4);
    }
    .btn-confirm-discard {
        padding: 5px 14px;
        background: var(--color-status-red);
        color: white;
        border: none;
        border-radius: var(--radius-sm);
        font-size: 0.75rem;
        cursor: pointer;
        font-family: inherit;
    }
    .btn-confirm-discard:hover {
        opacity: 0.9;
    }
</style>
