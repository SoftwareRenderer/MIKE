<script lang="ts">
    import { onMount, tick } from 'svelte';
    import { ensureCsrfToken } from '$lib/api';
    import Icon from '$lib/components/Icon.svelte';
    import folderIcon from '$lib/assets/icons/folder.svg?raw';
    import fileIcon from '$lib/assets/icons/file.svg?raw';

    let { projectId, projectName, taskId, taskPath, sandboxEnabled, unsavedFiles, pendingFsEvent, onFileSelected, onPathChange, onToggleFind }: {
        projectId: string;
        projectName: string;
        taskId: string;
        taskPath: string;
        sandboxEnabled: boolean;
        unsavedFiles: string[];
        pendingFsEvent: { op: 'create' | 'delete'; path: string } | null;
        onFileSelected: (path: string, line?: number, pin?: boolean) => void;
        onPathChange: (path: string) => void;
        onToggleFind?: () => void;
    } = $props();

    // New file creation
    let showNewFile = $state(false);
    let newFileName = $state('');
    let newFileInput: HTMLInputElement | null = $state(null);
    let creating = $state(false);
    let createError = $state('');

    async function toggleNewFile() {
        showNewFile = true;
        newFileName = '';
        createError = '';
        await tick(); // wait for the row to render
        newFileInput?.focus();
    }

    async function toggleNewFolder() {
        showNewFolder = true;
        newFolderName = '';
        createFolderError = '';
        await tick(); // wait for the row to render
        newFolderInput?.focus();
    }

    async function handleCreateFile() {
        const name = newFileName.trim();
        if (!name || creating) return;
        creating = true;
        createError = '';
        try {
            const csrfToken = await ensureCsrfToken();
            const res = await fetch(`/api/fs/write?project=${encodeURIComponent(projectId)}&task=${encodeURIComponent(taskId)}`, {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json',
                    ...(csrfToken ? { 'X-CSRF-Token': csrfToken } : {}),
                },
                body: JSON.stringify({ path: currentPath + '/' + name, content: '' }),
                credentials: 'include',
            });
            if (!res.ok) {
                const err = await res.json().catch(() => ({}));
                throw new Error(err.error || 'Failed to create file');
            }
            showNewFile = false;
            newFileName = '';
            loadDir(currentPath);
        } catch (e) {
            createError = String(e);
        } finally {
            creating = false;
        }
    }

    // New folder creation
    let showNewFolder = $state(false);
    let newFolderName = $state('');
    let newFolderInput: HTMLInputElement | null = $state(null);
    let creatingFolder = $state(false);
    let createFolderError = $state('');

    async function handleCreateFolder() {
        const name = newFolderName.trim();
        if (!name || creatingFolder) return;
        creatingFolder = true;
        createFolderError = '';
        try {
            const csrfToken = await ensureCsrfToken();
            const res = await fetch(`/api/fs/mkdir?project=${encodeURIComponent(projectId)}&task=${encodeURIComponent(taskId)}`, {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json',
                    ...(csrfToken ? { 'X-CSRF-Token': csrfToken } : {}),
                },
                body: JSON.stringify({ path: currentPath + '/' + name }),
                credentials: 'include',
            });
            if (!res.ok) {
                const err = await res.json().catch(() => ({}));
                throw new Error(err.error || 'Failed to create folder');
            }
            showNewFolder = false;
            newFolderName = '';
            const newFolderPath = currentPath + '/' + name;
            await loadDir(newFolderPath);
        } catch (e) {
            createFolderError = String(e);
        } finally {
            creatingFolder = false;
        }
    }

    // Sandbox mount path for this task
    // Sandbox mount path: /home/sandbox/<projectName>
    let mountPath = $derived(sandboxEnabled ? '/home/sandbox/' + projectName : '');
    // Current path: sandbox path when sandbox enabled, host path otherwise
    let currentPath = $state('');
    let currentTaskPath = $derived(taskPath);

    // O(1) lookup
    let unsavedPaths = $derived(new Set(unsavedFiles));
    let unsavedDirPrefixes = $derived.by(() => {
        const prefixes = new Set<string>();
        for (const p of unsavedFiles) {
            const lastSlash = p.lastIndexOf('/');
            if (lastSlash >= 0) prefixes.add(p.substring(0, lastSlash));
        }
        return prefixes;
    });

    let entries = $state<{ name: string; isDir: boolean; path: string }[]>([]);
    let entryPaths = $derived(new Set(entries.map(e => e.path)));
    let loading = $state(true);
    let error = $state('');
    let firstLoadComplete = $state(false);

    // Initialize currentPath once props are available
    $effect(() => {
        if (!currentPath) {
            currentPath = sandboxEnabled ? mountPath : (taskPath || '');
        }
    });

    // Directories to never show
    const ignoredDirs = new Set([
        'node_modules', '.next', 'dist', 'build', '.cache', '__pycache__',
        '.venv', 'target', 'vendor', '.gradle', '.mypy_cache', '.pytest_cache',
        'coverage', '.nuxt', '.output', '.svelte-kit', '.turbo', '.vercel',
        '.docker', '.git', '.vscode', '.idea',
    ]);

    async function loadDir(path: string) {
        // Clamp to root
        const rootPath = sandboxEnabled ? mountPath : taskPath;
        if (!path.startsWith(rootPath)) {
            path = rootPath;
        }
        loading = true;
        error = '';
        currentPath = path;
        onPathChange(path);
        try {
            const res = await fetch(`/api/fs?path=${encodeURIComponent(path)}&files=1&project=${encodeURIComponent(projectId)}&task=${encodeURIComponent(taskId)}`);
            if (!res.ok) {
                const err = await res.json().catch(() => ({}));
                throw new Error(err.error || 'Failed to read directory');
            }
            const data = await res.json();
            entries = (data.entries || [])
                .filter((e: any) => {
                    if (e.name === '..') return false;
                    if (e.name.startsWith('.')) return false;
                    if (e.isDir && ignoredDirs.has(e.name)) return false;
                    return true;
                })
                .sort((a: any, b: any) => {
                    if (a.isDir !== b.isDir) return a.isDir ? -1 : 1;
                    return a.name.localeCompare(b.name);
                });
            // Prepend ".." when not at root
            if (currentPath !== rootPath) {
                const parentPath = currentPath.split('/').slice(0, -1).join('/');
                entries = [{ name: '..', isDir: true, path: parentPath }, ...entries];
            }
            firstLoadComplete = true;
        } catch (e) {
            error = String(e);
            entries = [];
        } finally {
            loading = false;
        }
    }

    // Handle fs events — mirror BranchFileBrowser behavior
    // Fs events from WorktreeWatcher are relative to the task worktree (e.g. "src/main.go")
    // Fs events from ProjectWatcher are relative to project root (e.g. "worktrees/temp/bear-comet-lore/src/main.go")
    function handleFsEvent(op: 'create' | 'delete', relPath: string) {
        const basePath = sandboxEnabled ? mountPath : taskPath;
        const currentPrefix = currentPath + '/';

        // Convert project-relative path to task-relative path
        // Strip "worktrees/<projectID>/<taskId>/" prefix if present (from ProjectWatcher)
        const worktreePrefix = 'worktrees/' + projectId + '/' + taskId + '/';
        let taskRelPath = relPath;
        if (relPath.startsWith(worktreePrefix)) {
            taskRelPath = relPath.substring(worktreePrefix.length);
        } else if (relPath.startsWith('worktrees/' + projectId + '/')) {
            // Event is in the worktrees dir but not this task — skip
            return;
        }

        const eventFullPath = basePath + '/' + taskRelPath;
        const eventDir = eventFullPath.substring(0, eventFullPath.lastIndexOf('/'));

        // Only refresh directory listing when the event affects the current directory
        if (eventDir === currentPath) {
            // Always refresh on delete (entry removed)
            // For create, check if it's already in the listing (skip redundant refresh)
            if (op === 'delete' || !entryPaths.has(eventFullPath)) {
                loadDir(currentPath);
            }
            return;
        }

        // New directory created as a direct child of current directory
        const childName = eventFullPath.substring(currentPrefix.length);
        if (op === 'create' && childName && !childName.includes('/') && !entries.some(e => e.name === childName && e.isDir)) {
            loadDir(currentPath);
        }
    }

    let lastFsEventKey = $state('');
    $effect(() => {
        if (pendingFsEvent) {
            const key = pendingFsEvent.op + ':' + pendingFsEvent.path;
            if (key !== lastFsEventKey) {
                handleFsEvent(pendingFsEvent.op, pendingFsEvent.path);
                lastFsEventKey = key;
            }
        }
    });

    function handleDirEnter(path: string) {
        loadDir(path);
    }

    function handleDirBack() {
        if (currentPath !== (sandboxEnabled ? mountPath : taskPath)) {
            const parentPath = currentPath.split('/').slice(0, -1).join('/');
            loadDir(parentPath);
        }
    }

    function getBreadcrumbParts(): string[] {
        const rootPath = sandboxEnabled ? mountPath : taskPath;
        if (currentPath === rootPath) return [];
        const relPath = currentPath.substring(rootPath.length).replace(/^\//, '');
        return relPath.split('/');
    }

    function handleBreadcrumbClick(index: number) {
        const rootPath = sandboxEnabled ? mountPath : taskPath;
        const parts = getBreadcrumbParts();
        const newPath = rootPath + '/' + parts.slice(0, index + 1).join('/');
        loadDir(newPath);
    }

    function getFolderName(path: string): string {
        const parts = path.split('/');
        return parts[parts.length - 1] || '/';
    }

    onMount(() => {
        loadDir(sandboxEnabled ? mountPath : taskPath);
    });
</script>

<div class="task-file-browser">
    <!-- Header -->
    <div class="file-browser-header">
        <span class="file-browser-label">Files</span>
        {#if !showNewFile && !showNewFolder}
            <div class="file-actions">
                {#if onToggleFind}
                    <button class="new-file-btn" onclick={onToggleFind} title="Find in files (Ctrl+Shift+F)" aria-label="Find in files">
                        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                            <circle cx="11" cy="11" r="8"/>
                            <line x1="21" y1="21" x2="16.65" y2="16.65"/>
                        </svg>
                    </button>
                {/if}
                <button class="new-file-btn" onclick={toggleNewFile} title="New file" aria-label="Create new file">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" aria-hidden="true">
                        <line x1="8" y1="3" x2="8" y2="13"/>
                        <line x1="3" y1="8" x2="13" y2="8"/>
                    </svg>
                </button>
                <button class="new-file-btn" onclick={toggleNewFolder} title="New folder" aria-label="Create new folder">
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                        <path d="M3 7v10a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2h-6l-2-2H5a2 2 0 00-2 2z"/>
                        <line x1="12" y1="10" x2="12" y2="16"/>
                        <line x1="9" y1="13" x2="15" y2="13"/>
                    </svg>
                </button>
            </div>
        {:else if showNewFile}
            <div class="new-file-input-row">
                <input
                    type="text"
                    class="new-file-input"
                    bind:this={newFileInput}
                    placeholder="filename"
                    value={newFileName}
                    oninput={(e) => newFileName = (e.target as HTMLInputElement).value}
                    onkeydown={(e) => { if (e.key === 'Enter') handleCreateFile(); if (e.key === 'Escape') { showNewFile = false; newFileName = ''; } }}
                    onblur={() => { showNewFile = false; newFileName = ''; }}
                    aria-label="New file name"
                />
            </div>
        {:else}
            <div class="new-file-input-row">
                <input
                    type="text"
                    class="new-file-input"
                    bind:this={newFolderInput}
                    placeholder="folder name"
                    value={newFolderName}
                    oninput={(e) => newFolderName = (e.target as HTMLInputElement).value}
                    onkeydown={(e) => { if (e.key === 'Enter') handleCreateFolder(); if (e.key === 'Escape') { showNewFolder = false; newFolderName = ''; } }}
                    onblur={() => { showNewFolder = false; newFolderName = ''; }}
                    aria-label="New folder name"
                />
            </div>
        {/if}
    </div>

    <!-- Breadcrumb -->
    {#if getBreadcrumbParts().length > 0}
        <div class="breadcrumb">
            <button class="breadcrumb-item" onclick={() => loadDir(sandboxEnabled ? mountPath : taskPath)}>
                {getFolderName(sandboxEnabled ? mountPath : taskPath)}
            </button>
            {#each getBreadcrumbParts() as part, index}
                <span class="breadcrumb-sep">/</span>
                <button class="breadcrumb-item" class:active={index === getBreadcrumbParts().length - 1} onclick={() => handleBreadcrumbClick(index)}>
                    {part}
                </button>
            {/each}
        </div>
    {/if}

    <!-- File list -->
    <div class="file-list" role="tree">
        {#if loading && !firstLoadComplete}
            <div class="loading">Loading...</div>
        {:else if error}
            <div class="error">{error}</div>
        {:else}
            {#each entries as entry}
                <button
                    class="file-entry"
                    class:dir={entry.isDir}
                    role="treeitem"
                    aria-selected="false"
                    title={entry.path}
                    onclick={() => {
                        if (entry.isDir) {
                            handleDirEnter(entry.path);
                        } else {
                            onFileSelected(entry.path);
                        }
                    }}
                >
                    {#if entry.isDir}
                        <Icon src={folderIcon} color="#6E7681" width={14} height={14} class="file-icon" />
                    {:else}
                        <Icon src={fileIcon} color="#6E7681" width={14} height={14} class="file-icon" />
                    {/if}
                    <span class="file-name">{entry.name}</span>
                    {#if (!entry.isDir && unsavedPaths.has(entry.path)) || (entry.isDir && unsavedDirPrefixes.has(entry.path))}
                        <span class="unsaved-dot" title="Unsaved changes">
                            <svg width="8" height="8" viewBox="0 0 8 8" aria-hidden="true">
                                <circle cx="4" cy="4" r="3.5" fill="currentColor"/>
                            </svg>
                        </span>
                    {/if}
                </button>
            {/each}
            {#if entries.length === 0 && firstLoadComplete}
                <div class="empty">Empty folder</div>
            {/if}
        {/if}
    </div>
</div>

<style>
    @import '$lib/styles/shared.css';

    .task-file-browser {
        display: flex;
        flex-direction: column;
        height: 100%;
        overflow: hidden;
        background: var(--color-surface-1);
    }

    .file-browser-header {
        display: flex;
        align-items: center;
        padding: 6px 10px;
        border-bottom: 1px solid var(--color-border);
        flex-shrink: 0;
    }

    .file-browser-label {
        font-size: 0.75rem;
        font-weight: 600;
        color: var(--color-text-secondary);
        text-transform: uppercase;
        letter-spacing: 0.05em;
    }

    .file-actions {
        margin-left: auto;
        display: flex;
        gap: 2px;
    }

    .new-file-btn {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 24px;
        height: 24px;
        padding: 0;
        border: none;
        background: transparent;
        color: var(--color-text-secondary);
        cursor: pointer;
        border-radius: var(--radius-sm);
    }
    .new-file-btn:hover {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }

    .new-file-input-row {
        margin-left: auto;
    }

    .new-file-input {
        width: 120px;
        padding: 2px 6px;
        font-size: 0.75rem;
        font-family: var(--font-mono);
        background: var(--color-surface-2);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        color: var(--color-text-primary);
        outline: none;
    }
    .new-file-input:focus {
        border-color: var(--color-accent);
    }

    .breadcrumb {
        display: flex;
        align-items: center;
        padding: 4px 12px;
        font-size: 0.75rem;
        color: var(--color-text-tertiary);
        border-bottom: 1px solid var(--color-border);
        flex-shrink: 0;
        overflow-x: auto;
        white-space: nowrap;
    }

    .breadcrumb-item {
        background: none;
        border: none;
        color: var(--color-text-secondary);
        cursor: pointer;
        padding: 2px 4px;
        border-radius: var(--radius-sm);
        font-size: inherit;
        font-family: inherit;
    }
    .breadcrumb-item:hover {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }
    .breadcrumb-item.active {
        color: var(--color-text-primary);
        cursor: default;
    }
    .breadcrumb-item.active:hover {
        background: none;
    }

    .breadcrumb-sep {
        padding: 0 2px;
        color: var(--color-text-tertiary);
    }

    .file-list {
        flex: 1;
        overflow-y: auto;
        overflow-x: hidden;
        padding: 2px 0;
    }

    .file-entry {
        display: flex;
        align-items: center;
        gap: 6px;
        width: 100%;
        padding: 3px 8px 3px 10px;
        border: none;
        background: transparent;
        color: var(--color-text-primary);
        font-size: 0.75rem;
        text-align: left;
        cursor: pointer;
        border-radius: 0;
    }
    .file-entry:hover {
        background: var(--color-surface-2);
    }
    .file-entry.dir {
        font-weight: 500;
    }

    :global(.file-icon) {
        flex-shrink: 0;
        opacity: 0.7;
    }

    .file-name {
        flex: 1;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }

    .unsaved-dot {
        flex-shrink: 0;
        color: var(--color-status-yellow, #f0c000);
        display: inline-flex;
        align-items: center;
        margin-left: 2px;
    }

    .loading, .error, .empty {
        padding: 12px;
        font-size: 0.8125rem;
        color: var(--color-text-tertiary);
        text-align: center;
    }
    .error {
        color: var(--color-status-red);
    }
</style>
