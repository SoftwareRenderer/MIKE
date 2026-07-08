<script lang="ts">
    import { onMount } from 'svelte';
    import { createFocusTrap } from '$lib/a11y';
    import homeIcon from '$lib/assets/icons/home.svg';
    import folderIcon from '$lib/assets/icons/folder.svg';
    import fileIcon from '$lib/assets/icons/file.svg';

    let { initialPath, onClose, onSelect }: {
        initialPath: string;
        onClose: () => void;
        onSelect: (path: string) => void;
    } = $props();

    let currentPath = $state('');
    let entries = $state<{ name: string; isDir: boolean; path: string }[]>([]);
    let selectedFile = $state<string | null>(null);
    let loading = $state(false);
    let error = $state('');

    function isDockerfile(name: string): boolean {
        const lower = name.toLowerCase();
        return lower === 'dockerfile' || lower.endsWith('.dockerfile');
    }

    async function loadDir(path: string) {
        loading = true;
        error = '';
        currentPath = path;
        selectedFile = null;
        try {
                        const res = await fetch(`/api/fs?path=${encodeURIComponent(path)}&files=1`);
            if (!res.ok) {
                const err = await res.json();
                throw new Error(err.error || 'Failed to read directory');
            }
            const data = await res.json();
            // Filter: show directories and Dockerfile-matching files
            entries = (data.entries || []).filter(
                (e: { isDir: boolean; name: string }) => e.isDir || isDockerfile(e.name)
            );
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
        } else {
            selectedFile = entry.path;
        }
    }

    function handleSelect() {
        if (selectedFile) {
            onSelect(selectedFile);
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
    <div class="browser" onclick={(e) => e.stopPropagation()} onkeydown={(e) => { if (e.key === 'Escape') handleClose(); }} bind:this={browserEl} role="dialog" aria-modal="true" aria-labelledby="dockerfile-picker-title" tabindex="-1">
        <div class="browser-header">
            <h2 id="dockerfile-picker-title">Select Dockerfile</h2>
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
                        aria-label="File path"
                    />
                </div>
            </div>

            {#if error}
                <div class="error">{error}</div>
            {/if}

            {#if loading}
                <div class="loading">Loading...</div>
            {:else}
                <!-- File list -->
                {#if entries.length === 0}
                    <div class="empty">No Dockerfiles found in this directory</div>
                {:else}
                    <div class="file-list" role="list" aria-label="Directory contents">
                        {#each entries as entry}
                            <button
                                type="button"
                                class={`file-entry${entry.isDir ? ' dir' : ''}${selectedFile === entry.path ? ' selected' : ''}`}
                                onclick={() => handleClick(entry)}
                                aria-label={`${entry.isDir ? 'Folder' : 'File'}: ${entry.name}`}
                                title={`${entry.isDir ? 'Open folder' : 'Select file'}: ${entry.name}`}
                            >
                                <span class="icon" aria-hidden="true">
                                    {#if entry.isDir}
                                        <img src={folderIcon} width="16" height="16" alt="" />
                                    {:else}
                                        <img src={fileIcon} width="16" height="16" alt="" />
                                    {/if}
                                </span>
                                <span class="name">{entry.name}</span>
                            </button>
                        {/each}
                    </div>
                {/if}
            {/if}
        </div>

        <div class="browser-footer">
            <span class="current-path">{formatPath(currentPath)}</span>
            <div class="footer-actions">
                <button class="btn-cancel" onclick={handleClose} title="Cancel">Cancel</button>
                <button
                    class="btn-select"
                    onclick={handleSelect}
                    disabled={!selectedFile}
                    title="Select this Dockerfile"
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
    }
    .browser-body {
        flex: 1;
        overflow-y: auto;
        padding: 0 16px 16px;
        display: flex;
        flex-direction: column;
        gap: 8px;
    }
    .path-bar {
        display: flex;
        flex-direction: column;
        gap: 8px;
        position: sticky;
        top: 0;
        z-index: 5;
        background: var(--color-surface-1);
        padding: 12px 0 8px;
    }
    .path-bar-row {
        display: flex;
        gap: 8px;
        align-items: stretch;
        background: var(--color-surface-2);
        border-radius: var(--radius-md);
        padding: 4px;
    }
    .home-btn {
        display: flex;
        align-items: center;
        justify-content: center;
        padding: 6px;
        background: transparent;
        border-radius: var(--radius-sm);
        border: none;
        cursor: pointer;
        color: var(--color-text-secondary);
    }
    .home-btn:hover {
        background: var(--color-surface-3);
    }
    .path-input {
        flex: 1;
        padding: 6px 10px;
        background: transparent;
        border: none;
        color: var(--color-text-primary);
        font-size: 0.875rem;
        font-family: var(--font-mono);
        outline: none;
    }
    .error {
        color: var(--color-status-red);
        font-size: 0.875rem;
        padding: 8px;
        background: color-mix(in srgb, var(--color-status-red) 10%, transparent);
        border-radius: var(--radius-sm);
    }
    .loading {
        text-align: center;
        color: var(--color-text-secondary);
        padding: 24px;
    }
    .empty {
        text-align: center;
        color: var(--color-text-secondary);
        padding: 24px;
        font-size: 0.875rem;
    }
    .file-list {
        display: flex;
        flex-direction: column;
        gap: 2px;
    }
    .file-entry {
        display: flex;
        align-items: center;
        gap: 8px;
        padding: 8px 12px;
        border-radius: var(--radius-sm);
        cursor: pointer;
        font-size: 0.875rem;
        text-align: left;
        width: 100%;
        font: inherit;
        color: inherit;
        background: none;
        border: none;
    }
    .file-entry:hover {
        background: var(--color-surface-2);
    }
    .file-entry.dir {
        font-weight: 500;
    }
    .file-entry.selected {
        background: var(--color-accent);
        color: white;
    }
    .icon {
        display: flex;
        align-items: center;
        flex-shrink: 0;
    }
    .name {
        flex: 1;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }
    .browser-footer {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 12px 16px;
        border-top: 1px solid var(--color-border);
        gap: 12px;
    }
    .current-path {
        font-size: 0.75rem;
        color: var(--color-text-tertiary);
        font-family: var(--font-mono);
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
        flex: 1;
    }
    .footer-actions {
        display: flex;
        gap: 8px;
        align-items: center;
    }
    .btn-cancel {
        padding: 6px 16px;
        border-radius: var(--radius-md);
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
        padding: 6px 16px;
        border-radius: var(--radius-md);
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
        opacity: 0.4;
        cursor: not-allowed;
    }
</style>
