<script lang="ts">
    import { onMount } from 'svelte';
    import { createFocusTrap } from '$lib/a11y';
    import homeIcon from '$lib/assets/icons/home.svg';
    import folderIcon from '$lib/assets/icons/folder.svg';

    let { initialPath, onClose, onSelect }: {
        initialPath: string;
        onClose: () => void;
        onSelect: (path: string) => void;
    } = $props();

    let currentPath = $state('');
    let entries = $state<{ name: string; isDir: boolean; path: string }[]>([]);
    let selectedPath = $state<string | null>(null);
    let loading = $state(false);
    let error = $state('');
    let parentPath = $derived(
        currentPath && currentPath !== '/'
            ? (() => {
                const parts = currentPath.split('/').filter(Boolean);
                parts.pop();
                return parts.length === 0 ? '/' : '/' + parts.join('/');
            })()
            : null
    );

    async function loadDir(path: string) {
        loading = true;
        error = '';
        currentPath = path;
        selectedPath = path; // selecting the directory itself
        try {
            const res = await fetch(`/api/fs?path=${encodeURIComponent(path)}`);
            if (!res.ok) {
                const err = await res.json();
                throw new Error(err.error || 'Failed to read directory');
            }
            const data = await res.json();
            // Only show directories
            entries = (data.entries || []).filter((e: { isDir: boolean }) => e.isDir);
        } catch (e) {
            error = String(e);
            entries = [];
        } finally {
            loading = false;
        }
    }

    function handleClick(entry: { isDir: boolean; path: string }) {
        if (entry.isDir) {
            loadDir(entry.path);
        }
    }

    function handleSelect() {
        if (selectedPath) {
            onSelect(selectedPath);
        }
    }

    function handleInputChange() {
        const p = currentPath.trim();
        if (p) {
            loadDir(p);
        }
    }

    function formatPath(p: string): string {
        return p || '/';
    }

    // Load initial path on mount
    $effect(() => {
        loadDir(initialPath);
    });

    let browserEl: HTMLDivElement | null = null;
    let trap: ReturnType<typeof createFocusTrap> | null = null;
    let triggerEl: HTMLElement | null = null;

    onMount(() => {
        triggerEl = document.activeElement as HTMLElement;
        if (browserEl) {
            trap = createFocusTrap(browserEl, handleClose);
            setTimeout(() => trap?.focusFirst(), 50);
        }
    });

    function handleClose() {
        trap?.destroy();
        trap = null;
        onClose();
        setTimeout(() => triggerEl?.focus(), 50);
    }
</script>

<div class="overlay" onclick={handleClose} role="presentation">
    <div class="browser" onclick={(e) => e.stopPropagation()} onkeydown={(e) => { if (e.key === 'Escape') handleClose(); }} bind:this={browserEl} role="dialog" aria-modal="true" aria-labelledby="directory-picker-title" tabindex="-1">
        <div class="browser-header">
            <h2 id="directory-picker-title">Select Directory</h2>
            <button class="close-btn" onclick={handleClose} aria-label="Close" title="Close">✕</button>
        </div>

        <div class="browser-body">
            <!-- Path bar -->
            <div class="path-bar">
                <div class="path-bar-row">
                    <button class="home-btn" onclick={() => loadDir('/home')} aria-label="Go to home directory" title="Go to home directory">
                        <img src={homeIcon} width="18" height="18" alt="" aria-hidden="true" />
                    </button>
                    <input
                        type="text"
                        class="path-input"
                        bind:value={currentPath}
                        onkeydown={(e) => { if (e.key === 'Enter') handleInputChange(); }}
                        placeholder="Enter path or browse..."
                        aria-label="Directory path"
                    />
                </div>
            </div>

            {#if loading}
                <div class="loading">Loading...</div>
            {:else}
                <!-- Directory list -->
                {#if error}
                    <div class="error">{error}</div>
                    {#if parentPath}
                        <button
                            type="button"
                            class="file-entry dir parent-entry"
                            onclick={() => loadDir(parentPath)}
                            aria-label="Go to parent directory"
                            title="Go to parent directory"
                        >
                            <span class="icon" aria-hidden="true">
                                <img src={folderIcon} width="16" height="16" alt="" />
                            </span>
                            <span class="name">..</span>
                        </button>
                    {/if}
                {:else if entries.length === 0}
                    <div class="empty">No subdirectories in this path</div>
                {:else}
                    <div class="file-list" role="list" aria-label="Directory contents">
                        {#each entries as entry}
                            <button
                                type="button"
                                class="file-entry dir"
                                onclick={() => handleClick(entry)}
                                aria-label={`Folder: ${entry.name}`}
                                title={`Open folder: ${entry.name}`}
                            >
                                <span class="icon" aria-hidden="true">
                                    <img src={folderIcon} width="16" height="16" alt="" />
                                </span>
                                <span class="name">{entry.name}</span>
                            </button>
                        {/each}
                    </div>
                {/if}
            {/if}
        </div>

        <div class="browser-footer">
            <span class="current-path">{formatPath(selectedPath)}</span>
            <div class="footer-actions">
                <button class="btn-cancel" onclick={handleClose} title="Cancel">Cancel</button>
                <button
                    class="btn-select"
                    onclick={handleSelect}
                    disabled={!selectedPath}
                    title="Select this directory"
                >
                    Select
                </button>
            </div>
        </div>
    </div>
</div>

<style>
    @import '$lib/styles/shared.css';

    :global(.overlay) {
        z-index: 1100;
    }
    :global(.close-btn) {
        width: 28px;
        height: 28px;
        font-size: 1rem;
    }
    .browser {
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-lg);
        width: 560px;
        max-width: 90vw;
        max-height: 80vh;
        display: flex;
        flex-direction: column;
        overflow: hidden;
    }
    .browser-header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 16px 20px;
        border-bottom: 1px solid var(--color-border);
    }
    .browser-header h2 {
        font-size: 1rem;
        font-weight: 600;
        color: var(--color-text-primary);
    }
    .browser-body {
        flex: 1;
        overflow-y: auto;
        padding: 16px 20px;
        min-height: 0;
    }
    .path-bar {
        margin-bottom: 12px;
    }
    .path-bar-row {
        display: flex;
        align-items: center;
        gap: 8px;
    }
    .home-btn {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 32px;
        height: 32px;
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        background: var(--color-surface-2);
        cursor: pointer;
        transition: background 0.15s;
        flex-shrink: 0;
    }
    .home-btn:hover {
        background: var(--color-surface-3);
    }
    .path-input {
        flex: 1;
        padding: 6px 10px;
        border-radius: var(--radius-sm);
        border: 1px solid var(--color-border);
        background: var(--color-surface-1);
        color: var(--color-text-primary);
        font-size: 0.8125rem;
        font-family: var(--font-mono, monospace);
        transition: border-color 0.15s;
    }
    .path-input:focus {
        outline: none;
        border-color: var(--color-accent);
    }
    .error {
        padding: 8px 12px;
        background: rgba(248, 81, 73, 0.1);
        border: 1px solid rgba(248, 81, 73, 0.3);
        border-radius: var(--radius-sm);
        color: var(--color-status-red);
        font-size: 0.8125rem;
    }
    .loading {
        padding: 20px;
        text-align: center;
        color: var(--color-text-secondary);
        font-size: 0.8125rem;
    }
    .empty {
        padding: 20px;
        text-align: center;
        color: var(--color-text-tertiary);
        font-size: 0.8125rem;
        font-style: italic;
    }
    .file-list {
        display: flex;
        flex-direction: column;
        gap: 2px;
    }
    .file-entry {
        display: flex;
        align-items: center;
        gap: 10px;
        width: 100%;
        padding: 8px 12px;
        border: none;
        border-radius: var(--radius-sm);
        background: transparent;
        color: var(--color-text-primary);
        font-size: 0.8125rem;
        cursor: pointer;
        transition: background 0.15s;
        text-align: left;
    }
    .file-entry:hover {
        background: var(--color-surface-2);
    }
    .file-entry .icon {
        flex-shrink: 0;
        width: 20px;
        height: 20px;
        display: flex;
        align-items: center;
        justify-content: center;
    }
    .file-entry .name {
        flex: 1;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }
    .file-entry.parent-entry {
        color: var(--color-text-secondary);
        font-style: italic;
    }
    .browser-footer {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 12px 20px;
        border-top: 1px solid var(--color-border);
        gap: 12px;
    }
    .current-path {
        font-size: 0.75rem;
        font-family: var(--font-mono, monospace);
        color: var(--color-text-secondary);
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
        min-width: 0;
        flex: 1;
    }
    .footer-actions {
        display: flex;
        gap: 8px;
        flex-shrink: 0;
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
    .btn-select {
        padding: 6px 14px;
        border-radius: var(--radius-sm);
        border: none;
        background: var(--color-accent);
        color: white;
        font-size: 0.8125rem;
        font-weight: 500;
        cursor: pointer;
        transition: background 0.15s;
    }
    .btn-select:hover:not(:disabled) {
        background: var(--color-accent-hover);
    }
    .btn-select:disabled {
        opacity: 0.5;
        cursor: not-allowed;
    }

    @media (max-width: 768px) {
        .browser {
            width: 100vw;
            max-width: none;
            max-height: 100dvh;
            height: 100dvh;
            border-radius: 0;
            border: none;
        }
    }
</style>
