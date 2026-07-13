<script lang="ts">
    import { onMount } from 'svelte';
    import BaseModal from './BaseModal.svelte';
    import { ensureCsrfToken } from '$lib/api';
    import Icon from '$lib/components/Icon.svelte';
    import homeIcon from '$lib/assets/icons/home.svg?raw';
    import folderIcon from '$lib/assets/icons/folder.svg?raw';
    import fileIcon from '$lib/assets/icons/file.svg?raw';

    let { onClose, onSelect }: { onClose: () => void; onSelect: (path: string, sandboxEnabled: boolean) => void } = $props();

    let sandboxEnabled = $state(true);

    let currentPath = $state('');
    let entries = $state<{ name: string; isDir: boolean; path: string; isGitRepo: boolean }[]>([]);
    let isGitRepo = $state(false);
    let loading = $state(false);
    let error = $state('');
    let initGitLoading = $state(false);
    let showNewFolder = $state(false);
    let newFolderName = $state('');
    let mkdirLoading = $state(false);

    async function loadDir(path: string) {
        loading = true;
        error = '';
        currentPath = path;
        try {
            const res = await fetch(`/api/fs?path=${encodeURIComponent(path)}`);
            if (!res.ok) {
                const err = await res.json();
                throw new Error(err.error || 'Failed to read directory');
            }
            const data = await res.json();
            entries = data.entries || [];
            isGitRepo = data.isGitRepo || false;
        } catch (e) {
            error = String(e);
            entries = [];
        } finally {
            loading = false;
        }
    }

    function handleClick(entry: { isDir: boolean; path: string; isGitRepo: boolean }) {
        if (entry.isGitRepo) {
            onSelect(entry.path, sandboxEnabled);
        } else if (entry.isDir) {
            loadDir(entry.path);
        }
    }

    function handleSelect() {
        if (isGitRepo) {
            onSelect(currentPath, sandboxEnabled);
        }
    }

    function handleInputChange() {
        const p = currentPath.trim();
        if (p) {
            loadDir(p);
        }
    }

    async function handleInitGit() {
        initGitLoading = true;
        error = '';
        try {
            const csrfToken = await ensureCsrfToken();
            const res = await fetch('/api/fs/init-git', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json', ...(csrfToken ? { 'X-CSRF-Token': csrfToken } : {}) },
                body: JSON.stringify({ path: currentPath }),
                credentials: 'include',
            });
            if (!res.ok) {
                const err = await res.json();
                throw new Error(err.error || 'Failed to initialize git repository');
            }
            // Refresh directory to confirm git init succeeded, then auto-select
            await loadDir(currentPath);
            onSelect(currentPath, sandboxEnabled);
        } catch (e) {
            error = String(e);
        } finally {
            initGitLoading = false;
        }
    }

    function toggleNewFolder() {
        if (!showNewFolder) {
            newFolderName = '';
        }
        showNewFolder = !showNewFolder;
    }

    async function handleCreateFolder() {
        const name = newFolderName.trim();
        if (!name) return;
        mkdirLoading = true;
        error = '';
        try {
            const newPath = currentPath.endsWith('/') ? currentPath + name : currentPath + '/' + name;
            const csrfToken = await ensureCsrfToken();
            const res = await fetch('/api/fs/mkdir', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json', ...(csrfToken ? { 'X-CSRF-Token': csrfToken } : {}) },
                body: JSON.stringify({ path: newPath }),
                credentials: 'include',
            });
            if (!res.ok) {
                const err = await res.json();
                throw new Error(err.error || 'Failed to create folder');
            }
            // Navigate into the new folder
            showNewFolder = false;
            newFolderName = '';
            await loadDir(newPath);
        } catch (e) {
            error = String(e);
        } finally {
            mkdirLoading = false;
        }
    }

    // Load user's home directory on mount
    onMount(() => {
        fetch('/api/home')
            .then(r => r.json())
            .then(data => loadDir(data.home || '/home'))
            .catch(() => loadDir('/home'));
    });

    function formatPath(p: string): string {
        return p || '/';
    }

    function handleClose() {
        onClose();
    }
</script>

<BaseModal title="Select Project Directory" width="560px" zIndex={300} onClose={handleClose}>
        {#snippet toolbar()}
            <div class="path-bar">
            <div class="path-bar-row">
                <button class="home-btn" onclick={() => { fetch('/api/home').then(r => r.json()).then(d => loadDir(d.home)).catch(() => loadDir('/home')); }} aria-label="Go to home directory" title="Go to home directory">
                    <Icon src={homeIcon} color="#8B949E" width={18} height={18} />
                </button>
                <input
                    type="text"
                    class="path-input"
                    bind:value={currentPath}
                    onkeydown={(e) => { if (e.key === 'Enter') handleInputChange(); }}
                    placeholder="Enter path or browse..."
                    aria-label="File path"
                />
                <button class="icon-btn-sm" onclick={toggleNewFolder} aria-label="New folder" title="New folder" disabled={loading}>
                    <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                        <path d="M3 7v10a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2h-6l-2-2H5a2 2 0 00-2 2z"/>
                        <line x1="12" y1="10" x2="12" y2="16"/>
                        <line x1="9" y1="13" x2="15" y2="13"/>
                    </svg>
                </button>
            </div>
            {#if showNewFolder}
                <div class="new-folder-row">
                    <!-- svelte-ignore a11y_autofocus -->
                    <input
                        type="text"
                        class="folder-name-input"
                        bind:value={newFolderName}
                        placeholder="Folder name"
                        onkeydown={(e) => { if (e.key === 'Enter') handleCreateFolder(); if (e.key === 'Escape') showNewFolder = false; }}
                        autofocus
                        aria-label="New folder name"
                    />
                    <button class="btn-mini" onclick={handleCreateFolder} disabled={mkdirLoading || !newFolderName.trim()} title="Create">{mkdirLoading ? '...' : 'Create'}</button>
                    <button class="btn-mini btn-mini-cancel" onclick={() => { showNewFolder = false; newFolderName = ''; }} title="Cancel">✕</button>
                </div>
            {/if}
            </div>
        {/snippet}

        <div class="body-wrapper">
            {#if error}
                <div class="error">{error}</div>
            {/if}

            {#if loading}
                <div class="loading">Loading...</div>
            {:else}
                <!-- Git indicator -->
                {#if isGitRepo}
                    <div class="git-indicator">✓ This is a git repository</div>
                {/if}

                <!-- File list -->
                <div class="file-list" role="list" aria-label="Directory contents">
                    {#each entries as entry}
                        <button
                            type="button"
                            class={`file-entry${entry.isDir ? ' dir' : ''}${entry.isGitRepo ? ' git-repo' : ''}`}
                            onclick={() => handleClick(entry)}
                            aria-label={`${entry.isDir ? 'Folder' : 'File'}: ${entry.name}${entry.isGitRepo ? ', git repository' : ''}`}
                            title={`${entry.isDir ? 'Open folder' : 'File'}: ${entry.name}${entry.isGitRepo ? ' (git repository)' : ''}`}
                        >
                            <span class="icon" aria-hidden="true">
                                {#if entry.isDir}
                                    <Icon src={folderIcon} color="#6E7681" width={16} height={16} />
                                {:else}
                                    <Icon src={fileIcon} color="#6E7681" width={16} height={16} />
                                {/if}
                            </span>
                            <span class="name">{entry.name}</span>
                            {#if entry.isGitRepo}
                                <span class="git-badge">git</span>
                            {/if}
                        </button>
                    {/each}
                </div>
            {/if}
        </div>

        {#snippet footer()}
            <div class="footer-wrapper">
            <span class="current-path">{formatPath(currentPath)}</span>
            <div class="footer-actions">
                <label class="sandbox-toggle">
                    <input type="checkbox" checked={sandboxEnabled} onchange={(e) => sandboxEnabled = e.currentTarget.checked} />
                    <span>Use Sandbox</span>
                </label>
                <button type="button" class="btn-secondary" onclick={handleClose} title="Cancel">Cancel</button>
                {#if isGitRepo}
                    <button type="button" class="btn-primary" onclick={handleSelect} title="Select this directory as project">
                        Select
                    </button>
                {:else}
                    <button type="button" class="btn-primary" onclick={handleInitGit} disabled={initGitLoading} title="Initialize git repository in this directory">
                        {initGitLoading ? 'Initializing...' : 'Initialize Git'}
                    </button>
                {/if}
                </div>
            </div>
        {/snippet}
</BaseModal>

<style>
    :global(.modal) {
        display: flex;
        flex-direction: column;
        max-height: 80vh;
    }
    :global(.modal-body) {
        flex: 1;
        overflow-y: auto;
        padding: 0 16px 16px;
        display: flex;
        flex-direction: column;
        gap: 8px;
    }
    :global(.modal-footer) {
        gap: 12px;
    }
    .body-wrapper,
    .footer-wrapper {
        display: contents;
    }
    .path-bar {
        display: flex;
        flex-direction: column;
        gap: 8px;
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
    .path-input:focus-visible {
        border-color: var(--color-accent);
        outline: none;
    }
    .error {
        color: var(--color-status-red);
        font-size: 0.875rem;
        padding: 8px;
        background: var(--color-status-red)11;
        border-radius: var(--radius-sm);
    }
    .loading {
        text-align: center;
        color: var(--color-text-secondary);
        padding: 24px;
    }
    .git-indicator {
        color: var(--color-status-green);
        font-size: 0.875rem;
        padding: 8px;
        background: var(--color-status-green)11;
        border-radius: var(--radius-sm);
        text-align: center;
    }
    .new-folder-row {
        display: flex;
        gap: 8px;
        align-items: stretch;
    }
    .folder-name-input {
        flex: 1;
        padding: 8px 12px;
        background: var(--color-surface-2);
        border: 1px solid var(--color-accent);
        border-radius: var(--radius-md);
        color: var(--color-text-primary);
        font-size: 0.875rem;
        font-family: var(--font-mono);
        outline: none;
    }
    .btn-mini {
        padding: 8px 12px;
        background: var(--color-accent-dark);
        color: white;
        border-radius: var(--radius-md);
        font-size: 0.875rem;
        font-weight: 500;
        white-space: nowrap;
    }
    .btn-mini:hover:not(:disabled) {
        background: var(--color-accent-hover);
    }
    .btn-mini:disabled {
        opacity: 0.4;
        cursor: not-allowed;
    }
    .btn-mini-cancel {
        background: var(--color-surface-3);
        color: var(--color-text-secondary);
    }
    .btn-mini-cancel:hover {
        background: var(--color-surface-4);
    }
    .icon-btn-sm {
        padding: 6px;
        background: transparent;
        border-radius: var(--radius-sm);
        color: var(--color-text-secondary);
        display: flex;
        align-items: center;
        justify-content: center;
        flex-shrink: 0;
    }
    .icon-btn-sm:hover:not(:disabled) {
        background: var(--color-surface-4);
        color: var(--color-text-primary);
    }
    .icon-btn-sm:disabled {
        opacity: 0.4;
        cursor: not-allowed;
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
    .file-entry.git-repo:hover {
        background: var(--color-status-green)11;
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
    .git-badge {
        font-size: 0.75rem;
        padding: 1px 6px;
        background: var(--color-status-green)33;
        color: var(--color-status-green);
        border-radius: 10px;
        font-weight: 600;
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
    .sandbox-toggle {
        display: flex;
        align-items: center;
        gap: 6px;
        font-size: 0.8rem;
        color: var(--color-text-secondary);
        cursor: pointer;
        user-select: none;
        white-space: nowrap;
    }
    .sandbox-toggle input[type="checkbox"] {
        cursor: pointer;
        accent-color: var(--color-primary);
    }

</style>
