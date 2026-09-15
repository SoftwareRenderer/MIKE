<script lang="ts">
    import { onMount, tick } from 'svelte';
    import { ensureCsrfToken } from '$lib/api';
    import { useFileDropUpload } from '$lib/composables/use-file-drop-upload.svelte';
    import FileDropProgress from '$lib/components/FileDropProgress.svelte';
    import Icon from '$lib/components/Icon.svelte';
    import folderIcon from '$lib/assets/icons/folder.svg?raw';
    import fileIcon from '$lib/assets/icons/file.svg?raw';
    import { t } from '$lib/i18n';
    import type { FsEvent } from '$lib/utils/fs-event';

    type Entry = { name: string; isDir: boolean; path: string };

    /** A row's extra badge — the project explorer's git status letter and nothing else. */
    type EntryBadge = { char: string; cls: string; title: string; ariaLabel: string };

    /** How many times the listing asks again for a worktree the server has not created yet. */
    const MISSING_DIR_RETRIES = 20;
    const MISSING_DIR_RETRY_MS = 250;

    let {
        projectId,
        taskId = '',
        rootPath,
        initialPath = '',
        navMode = 'breadcrumb',
        showDotFiles = true,
        retryMissingDir = false,
        pinOnDblclick = false,
        unsavedFiles,
        pendingFsEvent,
        mapFsEvent,
        onFsEvent,
        onMutated,
        extraEntryInfo,
        listingClass = '',
        rowClass = '',
        emptyMessage,
        pendingMessage,
        treeAriaLabel,
        onFileSelected,
        onPathChange,
        onToggleFind,
    }: {
        projectId: string;
        /** The worktree the listing is scoped to; empty for a project's own checkout. Sent as `task` on every request this component makes, upload included. */
        taskId?: string;
        /** The directory nothing is listed or written above. */
        rootPath: string;
        /** Where the first listing opens, when that is not the root. */
        initialPath?: string;
        /** A path the user types, or the trail of folders under the root. */
        navMode?: 'path' | 'breadcrumb';
        showDotFiles?: boolean;
        /** A task worktree appears only once the task starts, so its listing asks again. */
        retryMissingDir?: boolean;
        /** Double-click pins the file in the project explorer; the task explorer has no pins. */
        pinOnDblclick?: boolean;
        unsavedFiles: string[];
        pendingFsEvent: FsEvent | null;
        /** Resolves a watcher path to one under `rootPath`, or returns null when it belongs elsewhere. */
        mapFsEvent?: (event: FsEvent) => string | null;
        /** A change the watcher reported, whatever this listing made of it. */
        onFsEvent?: (op: 'create' | 'delete', relPath: string) => void;
        /** This listing wrote something: a file, a folder, or an uploaded run. */
        onMutated?: () => void;
        extraEntryInfo?: (entry: Entry) => EntryBadge | null;
        /** Names the listing and its rows are addressed by outside this file. */
        listingClass?: string;
        rowClass?: string;
        emptyMessage: string;
        pendingMessage: string;
        treeAriaLabel?: string;
        onFileSelected: (path: string, line?: number, pin?: boolean) => void;
        onPathChange: (path: string) => void;
        onToggleFind?: () => void;
    } = $props();

    let entries = $state<Entry[]>([]);
    let currentPath = $state('');
    let loading = $state(true);
    let firstLoadComplete = $state(false);
    let error = $state('');
    let missingDirRetries = 0;

    // New file and new folder: one row under the header each, one failure each.
    let showNewFile = $state(false);
    let newFileName = $state('');
    let newFileInput: HTMLInputElement | null = $state(null);
    let creating = $state(false);
    let createError = $state('');
    let showNewFolder = $state(false);
    let newFolderName = $state('');
    let newFolderInput: HTMLInputElement | null = $state(null);
    let creatingFolder = $state(false);
    let createFolderError = $state('');

    /** Everything the one error surface shows: a failed listing, a failed file, a failed folder. */
    let problems = $derived([error, createError, createFolderError].filter(Boolean));

    // O(1) lookups — the template asks these per row.
    let unsavedPaths = $derived(new Set(unsavedFiles));
    let unsavedDirPrefixes = $derived.by(() => {
        const prefixes = new Set<string>();
        for (const p of unsavedFiles) {
            const lastSlash = p.lastIndexOf('/');
            if (lastSlash >= 0) prefixes.add(p.substring(0, lastSlash));
        }
        return prefixes;
    });
    let entryPaths = $derived(new Set(entries.map(e => e.path)));

    // Directories never shown: build output, caches, tool state. Dot files are otherwise visible,
    // unless the host asks for the task tree, which hides them all.
    const ignoredDirs = new Set([
        'node_modules', '.next', 'dist', 'build', '.cache', '__pycache__',
        '.venv', 'target', 'vendor', '.gradle', '.mypy_cache', '.pytest_cache',
        'coverage', '.nuxt', '.output', '.svelte-kit', '.turbo', '.vercel',
        '.docker', '.git', '.vscode', '.idea',
    ]);

    $effect(() => {
        if (!currentPath) {
            currentPath = initialPath || rootPath;
        }
    });

    /** The query every `/api/fs` call carries: the project, and the task when there is one. */
    function fsQuery() {
        const params = new URLSearchParams({ project: projectId });
        if (taskId) params.set('task', taskId);
        return params.toString();
    }

    async function loadDir(path: string) {
        // Clamp to root: nothing above it is listed, whatever a row, a crumb or a keystroke asked for.
        const target = path.startsWith(rootPath) ? path : rootPath;
        loading = true;
        error = '';
        currentPath = target;
        onPathChange(target);
        let retrying = false;
        try {
            const res = await fetch(`/api/fs?path=${encodeURIComponent(target)}&files=1&${fsQuery()}`);
            if (!res.ok) {
                const err = await res.json().catch(() => ({}));
                const msg = err.error || t('explorer.shared.errReadDir');
                // 'directory not found' is the server's own error string — a wire value
                // this retry guard compares against, never display copy, so it is not a key.
                if (retryMissingDir && msg === 'directory not found' && missingDirRetries < MISSING_DIR_RETRIES) {
                    missingDirRetries++;
                    setTimeout(() => loadDir(target), MISSING_DIR_RETRY_MS);
                    retrying = true;
                }
                throw new Error(msg);
            }
            const data = await res.json();
            entries = (data.entries || [])
                .filter((e: any) => {
                    if (e.name === '..') return false; // the listing prepends its own
                    if (!showDotFiles && e.name.startsWith('.')) return false;
                    if (e.isDir && ignoredDirs.has(e.name)) return false;
                    return true;
                })
                .sort((a: any, b: any) => {
                    // Directories first, then files, alphabetical within each group
                    if (a.isDir !== b.isDir) return a.isDir ? -1 : 1;
                    return a.name.localeCompare(b.name);
                });
            if (target !== rootPath) {
                entries = [{ name: '..', isDir: true, path: target.split('/').slice(0, -1).join('/') }, ...entries];
            }
            missingDirRetries = 0;
            firstLoadComplete = true;
        } catch (e) {
            if (!retrying) {
                error = String(e);
                entries = [];
            }
        } finally {
            if (!retrying) loading = false;
        }
    }

    /** One POST to `/api/fs/<route>`, and the server's own reason when it refuses. */
    async function postFs(route: 'write' | 'mkdir', body: Record<string, unknown>, fallback: string) {
        const csrfToken = await ensureCsrfToken();
        const res = await fetch(`/api/fs/${route}?${fsQuery()}`, {
            method: 'POST',
            headers: {
                'Content-Type': 'application/json',
                ...(csrfToken ? { 'X-CSRF-Token': csrfToken } : {}),
            },
            body: JSON.stringify(body),
            credentials: 'include',
        });
        if (!res.ok) {
            const err = await res.json().catch(() => ({}));
            throw new Error(err.error || fallback);
        }
    }

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
            await postFs('write', { path: currentPath + '/' + name, content: '' }, t('explorer.shared.errCreateFile'));
            showNewFile = false;
            newFileName = '';
            loadDir(currentPath);
            onMutated?.();
        } catch (e) {
            createError = String(e);
        } finally {
            creating = false;
        }
    }

    async function handleCreateFolder() {
        const name = newFolderName.trim();
        if (!name || creatingFolder) return;
        creatingFolder = true;
        createFolderError = '';
        try {
            const newPath = currentPath + '/' + name;
            await postFs('mkdir', { path: newPath }, t('explorer.shared.errCreateFolder'));
            showNewFolder = false;
            newFolderName = '';
            await loadDir(newPath); // a new folder is opened into
            onMutated?.();
        } catch (e) {
            createFolderError = String(e);
        } finally {
            creatingFolder = false;
        }
    }

    function openEntry(entry: Entry, pin: boolean) {
        if (entry.isDir) {
            loadDir(entry.path);
        } else {
            onFileSelected(entry.path, undefined, pin && pinOnDblclick);
        }
    }

    /** Resolve a typed path against the root, so `..` cannot walk out of the project. */
    function resolveWithinRoot(typed: string) {
        const rootParts = rootPath.replace(/\/$/, '').split('/').filter(Boolean);
        const stack = [...rootParts];
        for (const part of typed.replace(/\/+/g, '/').replace(/\/$/, '').split('/').filter(Boolean)) {
            if (part === '..') {
                if (stack.length > rootParts.length) stack.pop();
            } else if (part !== '.') {
                stack.push(part);
            }
        }
        return stack.join('/') || rootPath;
    }

    function handlePathInput() {
        const p = currentPath.trim();
        if (p) loadDir(resolveWithinRoot(p));
    }

    /** The task worktree's own folder name, as the first crumb. */
    function getFolderName(path: string): string {
        const parts = path.split('/');
        return parts[parts.length - 1] || '/';
    }

    function getBreadcrumbParts(): string[] {
        if (currentPath === rootPath) return [];
        return currentPath.substring(rootPath.length).replace(/^\//, '').split('/');
    }

    function handleBreadcrumbClick(index: number) {
        loadDir(rootPath + '/' + getBreadcrumbParts().slice(0, index + 1).join('/'));
    }

    // A watcher event is relative to whoever watched: the project root, or one task's worktree.
    // The listing refreshes only when the event touches the directory it shows.
    function handleFsEvent(op: 'create' | 'delete', relPath: string) {
        onFsEvent?.(op, relPath);

        const taskRelPath = mapFsEvent ? mapFsEvent({ op, path: relPath }) : relPath;
        if (taskRelPath === null) return;

        const eventFullPath = rootPath + '/' + taskRelPath;
        const eventDir = eventFullPath.substring(0, eventFullPath.lastIndexOf('/'));

        if (eventDir === currentPath) {
            // Always refresh on delete (an entry went away); on create only when it is not
            // already in the listing, which covers the write this component made itself.
            if (op === 'delete' || !entryPaths.has(eventFullPath)) {
                loadDir(currentPath);
            }
            return;
        }

        // A new directory that is a direct child of what is shown belongs in it.
        const childName = eventFullPath.substring((currentPath + '/').length);
        if (op === 'create' && childName && !childName.includes('/') && !entries.some(e => e.name === childName && e.isDir)) {
            loadDir(currentPath);
        }
    }

    // Consume pending fs events from the parent.
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

    // Dropping files onto the listing uploads them into the directory it shows — `task` is what
    // /api/fs/upload resolves the target through, so a worktree and a checkout need no branching.
    const dropUpload = useFileDropUpload({
        projectId: () => projectId,
        taskId: () => taskId,
        dir: () => currentPath,
        onUploaded: () => {
            loadDir(currentPath);
            onMutated?.();
        },
    });

    // The pointer-free half of the upload: a phone's file sheet or a keyboard, neither of which can
    // start the file drag the zone waits for. Only the button and the input live here; the run is
    // dropUpload's. Why the input cannot move into a child of this component: see file-drop.css.
    let uploadInput: HTMLInputElement | null = $state(null);
</script>

<div class="file-browser">
    <div class="file-browser-header">
        <span class="file-browser-label">{t('explorer.shared.filesLabel')}</span>
        {#if !showNewFile && !showNewFolder}
            <div class="file-actions">
                {#if onToggleFind}
                    <button class="new-file-btn" onclick={onToggleFind} title={t('explorer.shared.findInFilesTitle')} aria-label={t('explorer.shared.findInFiles')}>
                        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                            <circle cx="11" cy="11" r="8"/>
                            <line x1="21" y1="21" x2="16.65" y2="16.65"/>
                        </svg>
                    </button>
                {/if}
                <button class="new-file-btn upload-btn" onclick={() => uploadInput?.click()} title={t('explorer.shared.uploadTitle')} aria-label={t('explorer.shared.uploadAria')}>
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                        <path d="M8 10.5V2.5"/>
                        <path d="M4.75 5.75 8 2.5l3.25 3.25"/>
                        <path d="M2.5 13.5h11"/>
                    </svg>
                </button>
                <button class="new-file-btn" onclick={toggleNewFile} title={t('explorer.shared.newFileTitle')} aria-label={t('explorer.shared.createFileAria')}>
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                        <line x1="8" y1="3" x2="8" y2="13"/>
                        <line x1="3" y1="8" x2="13" y2="8"/>
                    </svg>
                </button>
                <button class="new-file-btn" onclick={toggleNewFolder} title={t('explorer.shared.newFolder')} aria-label={t('explorer.shared.createFolderAria')}>
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
                    placeholder={t('explorer.shared.filenamePlaceholder')}
                    value={newFileName}
                    oninput={(e) => newFileName = (e.target as HTMLInputElement).value}
                    onkeydown={(e) => { if (e.key === 'Enter') handleCreateFile(); if (e.key === 'Escape') { showNewFile = false; newFileName = ''; } }}
                    onblur={() => { showNewFile = false; newFileName = ''; }}
                    aria-label={t('explorer.shared.newFileNameAria')}
                />
            </div>
        {:else}
            <div class="new-file-input-row">
                <input
                    type="text"
                    class="new-file-input"
                    bind:this={newFolderInput}
                    placeholder={t('explorer.shared.folderNameLowerPlaceholder')}
                    value={newFolderName}
                    oninput={(e) => newFolderName = (e.target as HTMLInputElement).value}
                    onkeydown={(e) => { if (e.key === 'Enter') handleCreateFolder(); if (e.key === 'Escape') { showNewFolder = false; newFolderName = ''; } }}
                    onblur={() => { showNewFolder = false; newFolderName = ''; }}
                    aria-label={t('explorer.shared.newFolderNameAria')}
                />
            </div>
        {/if}
    </div>
    <!-- The picker a keyboard or a phone file sheet uses; the header's upload button opens it. -->
    <input
        type="file"
        multiple
        class="upload-input"
        bind:this={uploadInput}
        onchange={dropUpload.handlePicked}
        aria-label={t('explorer.shared.uploadAria')}
    />

    {#each problems as problem}
        <div class="file-browser-error">{problem}</div>
    {/each}

    {#if navMode === 'path'}
        <div class="file-browser-path">
            <input
                type="text"
                class="file-browser-path-input"
                value={currentPath}
                oninput={(e) => { currentPath = (e.target as HTMLInputElement).value; onPathChange(currentPath); }}
                aria-label={t('explorer.shared.filePathAria')}
                onkeydown={(e) => e.key === 'Enter' && handlePathInput()}
                onblur={handlePathInput}
                title={currentPath}
            />
        </div>
    {:else if getBreadcrumbParts().length > 0}
        <div class="breadcrumb">
            <button class="breadcrumb-item" onclick={() => loadDir(rootPath)}>{getFolderName(rootPath)}</button>
            {#each getBreadcrumbParts() as part, index}
                <span class="breadcrumb-sep">/</span>
                <button class="breadcrumb-item" class:active={index === getBreadcrumbParts().length - 1} onclick={() => handleBreadcrumbClick(index)}>
                    {part}
                </button>
            {/each}
        </div>
    {/if}

    <!-- The listing is the drop zone, so it renders whatever the directory answered. -->
    <!-- svelte-ignore a11y_interactive_supports_focus — the listing is a drop target, not an
         activation target: its treeitems are what a keyboard reaches. -->
    <div
        class="file-browser-list {listingClass}"
        class:is-drop-target={dropUpload.isOver}
        role="tree"
        aria-label={treeAriaLabel}
        ondragenter={dropUpload.handleDragEnter}
        ondragover={dropUpload.handleDragOver}
        ondragleave={dropUpload.handleDragLeave}
        ondrop={dropUpload.handleDrop}
    >
        <FileDropProgress drop={dropUpload} />
        {#if loading && entries.length === 0}
            <div class="file-browser-loading">{firstLoadComplete ? t('explorer.shared.loading') : pendingMessage}</div>
        {:else if !error && entries.length === 0}
            <div class="file-browser-empty">{emptyMessage}</div>
        {/if}
        {#each entries as entry}
            {@const badge = extraEntryInfo ? extraEntryInfo(entry) : null}
            <button
                class="file-browser-item {rowClass}"
                class:dir={entry.isDir}
                role="treeitem"
                aria-selected="false"
                title={entry.path}
                onclick={() => openEntry(entry, false)}
                ondblclick={() => openEntry(entry, true)}
            >
                {#if entry.isDir}
                    <Icon src={folderIcon} color="currentColor" width={14} height={14} class="file-icon folder-icon" />
                {:else}
                    <Icon src={fileIcon} color="#6E7681" width={14} height={14} class="file-icon" />
                {/if}
                <span class="file-name">{entry.name}</span>
                {#if badge}
                    <span class="git-status-badge {badge.cls}" title={badge.title} aria-label={badge.ariaLabel}>{badge.char}</span>
                {/if}
                {#if (!entry.isDir && unsavedPaths.has(entry.path)) || (entry.isDir && unsavedDirPrefixes.has(entry.path))}
                    <span class="unsaved-dot" title={t('explorer.shared.unsavedChanges')}>
                        <svg width="8" height="8" viewBox="0 0 8 8" aria-hidden="true">
                            <circle cx="4" cy="4" r="3.5" fill="currentColor"/>
                        </svg>
                    </span>
                {/if}
            </button>
        {/each}
    </div>
</div>

<style>
    @import '$lib/styles/file-browser.css';
    @import '$lib/styles/file-drop.css';
</style>
