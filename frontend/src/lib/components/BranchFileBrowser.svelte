<script lang="ts">
    import { onMount } from 'svelte';
    import { ensureCsrfToken } from '$lib/api';
    import folderIcon from '$lib/assets/icons/folder.svg';
    import fileIcon from '$lib/assets/icons/file.svg';

    let { projectId, projectPath, initialPath, sandboxEnabled, unsavedFiles, pendingFsEvent, onFileSelected, onPathChange }: {
        projectId: string;
        projectPath: string;
        initialPath?: string;
        sandboxEnabled: boolean;
        unsavedFiles: string[];
        pendingFsEvent: { op: 'create' | 'delete'; path: string } | null;
        onFileSelected: (path: string) => void;
        onPathChange: (path: string) => void;
    } = $props();

    // New file creation
    let showNewFile = $state(false);
    let newFileName = $state('');
    let creating = $state(false);
    let createError = $state('');

    async function handleCreateFile() {
        const name = newFileName.trim();
        if (!name || creating) return;
        creating = true;
        createError = '';
        try {
            const csrfToken = await ensureCsrfToken();
            const res = await fetch(`/api/fs/write?project=${encodeURIComponent(projectId)}`, {
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
    let creatingFolder = $state(false);
    let createFolderError = $state('');

    async function handleCreateFolder() {
        const name = newFolderName.trim();
        if (!name || creatingFolder) return;
        creatingFolder = true;
        createFolderError = '';
        try {
            const csrfToken = await ensureCsrfToken();
            const res = await fetch(`/api/fs/mkdir?project=${encodeURIComponent(projectId)}`, {
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
            loadDir(currentPath);
        } catch (e) {
            createFolderError = String(e);
        } finally {
            creatingFolder = false;
        }
    }

    // Sandbox mount path for this project
    let mountPath = $derived(sandboxEnabled ? '/sandbox/' + projectPath.split('/').filter(Boolean).pop() : '');
    // Current path: sandbox path when sandbox enabled, host path otherwise
    let currentPath = $state('');
    // Reactive reference to projectPath for closures
    let currentProjectPath = $derived(projectPath);

    // O(1) lookup — replaces Array.includes/some in template
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
    let loading = $state(true);
    let error = $state('');
    let firstLoadComplete = $state(false);

    // Initialize currentPath once props are available
    $effect(() => {
        if (!currentPath) {
            currentPath = sandboxEnabled ? mountPath : (initialPath || projectPath);
        }
    });

    // Directories to never show (from backend ignoredDirs)
    const ignoredDirs = new Set([
        'node_modules', '.next', 'dist', 'build', '.cache', '__pycache__',
        '.venv', 'target', 'vendor', '.gradle', '.mypy_cache', '.pytest_cache',
        'coverage', '.nuxt', '.output', '.svelte-kit', '.turbo', '.vercel',
        '.docker', '.git', '.vscode', '.idea',
    ]);

    async function loadDir(path: string) {
        // Clamp to root (sandbox mount path or project path)
        const rootPath = sandboxEnabled ? mountPath : projectPath;
        if (!path.startsWith(rootPath)) {
            path = rootPath;
        }
        loading = true;
        error = '';
        currentPath = path;
        onPathChange(path);
        try {
            const res = await fetch(`/api/fs?path=${encodeURIComponent(path)}&files=1&project=${encodeURIComponent(projectId)}`);
            if (!res.ok) {
                const err = await res.json().catch(() => ({}));
                throw new Error(err.error || 'Failed to read directory');
            }
            const data = await res.json();
            entries = (data.entries || [])
                .filter((e: any) => {
                    // Keep ".." only when not at project root
                    const rootPath = sandboxEnabled ? mountPath : projectPath;
                    if (e.name === '..') return currentPath !== rootPath;
                    if (e.name.startsWith('.')) return false;
                    if (e.isDir && ignoredDirs.has(e.name)) return false;
                    return true;
                })
                .sort((a: any, b: any) => {
                    // Directories first, then files, alphabetical within each group
                    if (a.isDir !== b.isDir) return a.isDir ? -1 : 1;
                    return a.name.localeCompare(b.name);
                });
                firstLoadComplete = true;
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
            onFileSelected(entry.path);
        }
    }

    function handlePathInput() {
        const p = currentPath.trim();
        if (p) {
            // Clamp to project root - resolve any .. that would escape
            const resolved = p.replace(/\/+/g, '/').replace(/\/$/, '');
            // Use mount path as root when sandbox enabled
            const rootPath = sandboxEnabled ? mountPath : projectPath;
            const rootParts = rootPath.replace(/\/$/, '').split('/').filter(Boolean);
            const pathParts = resolved.replace(/\/$/, '').split('/').filter(Boolean);
            const stack = [...rootParts];
            for (const part of pathParts) {
                if (part === '..') {
                    if (stack.length > rootParts.length) stack.pop();
                } else if (part !== '.') {
                    stack.push(part);
                }
            }
            loadDir(stack.join('/') || (sandboxEnabled ? mountPath : projectPath));
        }
    }

    // Called from parent when fs events arrive
    function handleFsEvent(op: 'create' | 'delete', relPath: string) {
        // Only refresh if the event would change the current directory listing.
        // Skip 'create' events for files that may already be in the listing
        // (e.g. from the initial load triggering the server-side fs watcher).
        const basePath = sandboxEnabled ? mountPath : projectPath;
        const currentPrefix = currentPath + '/';
        const eventFullPath = basePath + '/' + relPath;
        const eventDir = eventFullPath.substring(0, eventFullPath.lastIndexOf('/'));

        // Event is for something directly in the current directory
        if (eventDir === currentPath) {
            // Always refresh on delete (entry removed)
            // For create, check if it's already in the listing (skip redundant refresh)
            if (op === 'delete' || !entries.some(e => e.path === eventFullPath)) {
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

    // Consume pending fs events from parent
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

    onMount(() => {
        loadDir(currentPath);
    });
</script>

<div class="file-browser">
    <div class="file-browser-header">
        <span class="file-browser-label">Files</span>
        {#if !showNewFile && !showNewFolder}
            <div class="file-actions">
                <button class="new-file-btn" onclick={() => { showNewFile = true; newFileName = ''; createError = ''; }} title="New file" aria-label="Create new file">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" aria-hidden="true">
                        <line x1="8" y1="3" x2="8" y2="13"/>
                        <line x1="3" y1="8" x2="13" y2="8"/>
                    </svg>
                </button>
                <button class="new-file-btn" onclick={() => { showNewFolder = true; newFolderName = ''; createFolderError = ''; }} title="New folder" aria-label="Create new folder">
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
                    placeholder="filename"
                    value={newFileName}
                    oninput={(e) => newFileName = (e.target as HTMLInputElement).value}
                    onkeydown={(e) => { if (e.key === 'Enter') handleCreateFile(); if (e.key === 'Escape') { showNewFile = false; newFileName = ''; } }}
                    onblur={() => { showNewFile = false; newFileName = ''; }}
                    aria-label="New file name"
                    autofocus
                />
            </div>
        {:else}
            <div class="new-file-input-row">
                <input
                    type="text"
                    class="new-file-input"
                    placeholder="folder name"
                    value={newFolderName}
                    oninput={(e) => newFolderName = (e.target as HTMLInputElement).value}
                    onkeydown={(e) => { if (e.key === 'Enter') handleCreateFolder(); if (e.key === 'Escape') { showNewFolder = false; newFolderName = ''; } }}
                    onblur={() => { showNewFolder = false; newFolderName = ''; }}
                    aria-label="New folder name"
                    autofocus
                />
            </div>
        {/if}
    </div>
    {#if createError}
        <div class="file-browser-error">{createError}</div>
    {/if}
    {#if createFolderError}
        <div class="file-browser-error">{createFolderError}</div>
    {/if}

    {#if error}
        <div class="file-browser-error">{error}</div>
    {:else if loading && entries.length === 0}
        <div class="file-browser-loading">{firstLoadComplete ? 'Loading...' : 'Initializing sandbox...'}</div>
    {:else}
        <div class="file-browser-path">
            <input
                type="text"
                class="file-browser-path-input"
                value={currentPath}
                oninput={(e) => { currentPath = (e.target as HTMLInputElement).value; onPathChange(currentPath); }}
                aria-label="File path"
                onkeydown={(e) => e.key === 'Enter' && handlePathInput()}
                onblur={handlePathInput}
                title={currentPath}
            />
        </div>
        <div class="file-browser-list" role="tree" aria-label="File browser">
            {#each entries as entry}
                <button
                    class="file-browser-item"
                    class:dir={entry.isDir}
                    onclick={() => handleClick(entry)}
                    role="treeitem"
                    aria-selected="false"
                    title={entry.path}
                >
                    {#if entry.isDir}
                        <img src={folderIcon} width="14" height="14" alt="" class="file-icon" />
                    {:else}
                        <img src={fileIcon} width="14" height="14" alt="" class="file-icon" />
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
            {#if entries.length === 0 && !loading}
                <div class="file-browser-empty">Empty directory</div>
            {/if}
        </div>
    {/if}
</div>

<style>
    .file-browser {
        display: flex;
        flex-direction: column;
        height: 100%;
        overflow: hidden;
    }

    .file-browser-header {
        display: flex;
        align-items: center;
        gap: 6px;
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

    .file-browser-error {
        padding: 8px 10px;
        color: var(--color-status-red);
        font-size: 0.75rem;
    }

    .file-browser-loading {
        padding: 8px 10px;
        color: var(--color-text-secondary);
        font-size: 0.75rem;
    }

    .file-browser-path {
        padding: 4px 6px;
        border-bottom: 1px solid var(--color-border);
        flex-shrink: 0;
    }

    .file-browser-path-input {
        width: 100%;
        padding: 2px 4px;
        font-size: 0.75rem;
        font-family: var(--font-mono);
        background: var(--color-surface-2);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        color: var(--color-text-secondary);
        outline: none;
    }
    .file-browser-path-input:focus {
        border-color: var(--color-accent);
        color: var(--color-text-primary);
    }

    .file-browser-list {
        flex: 1;
        overflow-y: auto;
        overflow-x: hidden;
        padding: 2px 0;
    }

    .file-browser-item {
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
    .file-browser-item:hover {
        background: var(--color-surface-2);
        opacity: 1;
    }
    .file-browser-item.dir {
        font-weight: 500;
    }

    .file-icon {
        flex-shrink: 0;
        opacity: 0.7;
    }

    .file-name {
        flex: 1;
        min-width: 0;
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
    }

    .unsaved-dot {
        flex-shrink: 0;
        color: var(--color-status-yellow, #f0c000);
        display: inline-flex;
        align-items: center;
        margin-left: 2px;
    }

    .file-browser-empty {
        padding: 8px 10px;
        color: var(--color-text-tertiary);
        font-size: 0.75rem;
    }

    @media (max-width: 768px) {
        .file-browser-header {
            padding: 10px 12px;
        }
        .file-browser-label {
            font-size: 0.8125rem;
        }
        .new-file-btn {
            width: 32px;
            height: 32px;
        }
        .file-browser-path {
            padding: 8px 10px;
        }
        .file-browser-path-input {
            padding: 4px 8px;
            font-size: 0.8125rem;
        }
        .file-browser-item {
            padding: 8px 12px;
            font-size: 0.9375rem;
            gap: 10px;
        }
        .file-browser-error,
        .file-browser-loading,
        .file-browser-empty {
            padding: 12px;
            font-size: 0.875rem;
        }
    }
</style>
