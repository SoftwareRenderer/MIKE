<script lang="ts">
    import BaseModal from './BaseModal.svelte';
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
</script>

{#snippet footer()}
    <span class="current-path">{formatPath(selectedPath)}</span>
    <div class="footer-actions">
        <button type="button" class="btn-secondary" onclick={onClose} title="Cancel">Cancel</button>
        <button type="button" class="btn-primary" onclick={handleSelect} disabled={!selectedPath} title="Select this directory">
            Select
        </button>
    </div>
{/snippet}

<BaseModal title="Select Directory" width="560px" zIndex={1100} footer={footer} onClose={onClose}>
    <!-- Path bar -->
    <div class="path-bar">
        <div class="path-bar-row">
            <button type="button" class="home-btn" onclick={() => loadDir('/home')} aria-label="Go to home directory" title="Go to home directory">
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
</BaseModal>

<style>
    @import '$lib/styles/shared.css';

    :global(.modal) {
        max-height: 80vh;
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

    /* Footer layout */
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
</style>
