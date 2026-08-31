<script lang="ts">
    import BaseModal from './BaseModal.svelte';
    import Icon from '$lib/components/Icon.svelte';
    import homeIcon from '$lib/assets/icons/home.svg?raw';
    import folderIcon from '$lib/assets/icons/folder.svg?raw';
    import fileIcon from '$lib/assets/icons/file.svg?raw';

    interface Entry { name: string; isDir: boolean; path: string }

    let { title, initialPath, emptyLabel, selectTitle, fileFilter, onClose, onSelect }: {
        title: string;
        initialPath: string;
        /** Shown when a directory lists nothing selectable. */
        emptyLabel: string;
        /** Tooltip on the footer Select button. */
        selectTitle: string;
        /**
         * When provided, the picker runs in file mode: files matching this
         * predicate are listed alongside directories and are the selectable
         * entries. Without it the picker is directory-only and every loaded
         * directory itself is the selection.
         */
        fileFilter?: (name: string) => boolean;
        onClose: () => void;
        onSelect: (path: string) => void;
    } = $props();

    const fileMode = $derived(fileFilter !== undefined);

    let currentPath = $state('');
    let entries = $state<Entry[]>([]);
    /** Directory mode: the loaded directory; file mode: the picked file. */
    let selected = $state<string | null>(null);
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
        selected = fileMode ? null : path; // selecting the directory itself
        try {
            const res = await fetch(`/api/fs?path=${encodeURIComponent(path)}${fileMode ? '&files=1' : ''}`);
            if (!res.ok) {
                const err = await res.json();
                throw new Error(err.error || 'Failed to read directory');
            }
            const data = await res.json();
            entries = (data.entries || []).filter((e: Entry) =>
                e.isDir || (fileFilter && fileFilter(e.name))
            );
        } catch (e) {
            error = String(e);
            entries = [];
        } finally {
            loading = false;
        }
    }

    function handleClick(entry: Entry) {
        if (entry.isDir) {
            loadDir(entry.path);
        } else {
            selected = entry.path;
        }
    }

    function handleSelect() {
        if (selected) {
            onSelect(selected);
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
    <span class="current-path">{formatPath(selected ?? currentPath)}</span>
    <div class="footer-actions">
        <button type="button" class="btn-secondary" onclick={onClose} title="Cancel">Cancel</button>
        <button type="button" class="btn-primary" onclick={handleSelect} disabled={!selected} title={selectTitle}>
            Select
        </button>
    </div>
{/snippet}

<BaseModal {title} width="560px" zIndex={1100} footer={footer} onClose={onClose}>
    <!-- Path bar -->
    <div class="path-bar">
        <div class="path-bar-row">
            <button type="button" class="home-btn" onclick={() => loadDir('/home')} aria-label="Go to home directory" title="Go to home directory">
                <Icon src={homeIcon} color="#8B949E" width={18} height={18} />
            </button>
            <input
                type="text"
                class="path-input"
                bind:value={currentPath}
                onkeydown={(e) => { if (e.key === 'Enter') handleInputChange(); }}
                placeholder="Enter path or browse..."
                aria-label={fileMode ? 'File path' : 'Directory path'}
            />
        </div>
    </div>

    {#if loading}
        <div class="loading">Loading...</div>
    {:else}
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
                        <Icon src={folderIcon} color="#6E7681" width={16} height={16} />
                    </span>
                    <span class="name">..</span>
                </button>
            {/if}
        {:else if entries.length === 0}
            <div class="empty">{emptyLabel}</div>
        {:else}
            <div class="file-list" role="list" aria-label="Directory contents">
                {#each entries as entry}
                    <button
                        type="button"
                        class="file-entry"
                        class:dir={entry.isDir}
                        class:selected={selected === entry.path && fileMode}
                        onclick={() => handleClick(entry)}
                        aria-label={`${entry.isDir ? 'Folder' : 'File'}: ${entry.name}`}
                        title={`${entry.isDir ? 'Open folder' : 'Select file'}: ${entry.name}`}
                    >
                        <span class="icon" aria-hidden="true">
                            {#if entry.isDir}
                                <Icon src={folderIcon} color="#6E7681" width={16} height={16} />
                            {:else}
                                <Icon src={fileIcon} color="#6E7681" width={16} height={16} />
                            {/if}
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
    :global(.modal-body) {
        padding: 0 20px 16px;
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
        background: var(--color-accent-dark);
        color: white;
    }
    .file-entry.parent-entry {
        color: var(--color-text-secondary);
        font-style: italic;
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

    /* Footer layout */
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
</style>
