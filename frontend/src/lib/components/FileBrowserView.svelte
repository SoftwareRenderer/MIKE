<script lang="ts">
    import { onMount, tick } from 'svelte';
    import {
        copyFsPaths, deleteFsPaths, downloadURL, listFsDirectory, moveFsPaths, renameFsPath,
        createFsDirectory, writeFsFile,
        type FsOpsResult, type FsEntry,
    } from '$lib/api';
    import { dialogStore } from '$lib/stores';
    import { useFileDropUpload } from '$lib/composables/use-file-drop-upload.svelte';
    import { useFileSelection } from '$lib/composables/use-file-selection.svelte';
    import FileContextMenu, { type FileMenuItem } from '$lib/components/FileContextMenu.svelte';
    import FileDropProgress from '$lib/components/FileDropProgress.svelte';
    import EmptyState from '$lib/components/EmptyState.svelte';
    import Icon from '$lib/components/Icon.svelte';
    import SelectionActionBar from '$lib/components/SelectionActionBar.svelte';
    import folderIcon from '$lib/assets/icons/folder.svg?raw';
    import fileIcon from '$lib/assets/icons/file.svg?raw';
    import checkIcon from '$lib/assets/icons/check-circle.svg?raw';
    import closeIcon from '$lib/assets/icons/close.svg?raw';
    import editIcon from '$lib/assets/icons/edit.svg?raw';
    import { t, type MessageKey } from '$lib/i18n';
    import { explorerPathHref } from '$lib/utils/app-link';
    import { confirmList, pasteTarget, rowIsSelectable } from '$lib/utils/file-selection';
    import { baseName, folderName } from '$lib/utils/path';
    import { errorText } from '$lib/utils/error-text';
    import type { FsEvent } from '$lib/utils/fs-event';

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
        retryMissingDir = false,
        pinOnDblclick = false,
        unsavedFiles,
        pendingFsEvent,
        mapFsEvent,
        onFsEvent,
        onMutated,
        onDeleted,
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
        /** Paths that no longer exist, so the editor can close any tab open on them. */
        onDeleted?: (paths: string[]) => void;
        extraEntryInfo?: (entry: FsEntry) => EntryBadge | null;
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

    let entries = $state<FsEntry[]>([]);
    let currentPath = $state('');
    let loading = $state(true);
    let firstLoadComplete = $state(false);
    let error = $state('');
    let missingDirRetries = 0;
    let rootEl = $state<HTMLDivElement | null>(null);
    let listEl = $state<HTMLDivElement | null>(null);

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

    // Never listed: build output, caches and tool state — a dot prefix is not what puts a name here.
    // The add-project picker (`FileBrowser.svelte`) is the only listing that hides dot names, and it
    // has its own option for showing them.
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

    async function loadDir(path: string) {
        // Clamp to root: nothing above it is listed, whatever a row, a crumb or a keystroke asked for.
        const target = path.startsWith(rootPath) ? path : rootPath;
        // Reloading the current directory keeps the selection; opening a different one clears it.
        const sameDir = target === currentPath;
        const heldKeyboard = rootEl?.contains(document.activeElement) ?? false;
        menu = null;
        loading = true;
        error = '';
        currentPath = target;
        onPathChange(target);
        let retrying = false;
        try {
            const listing = await listFsDirectory({ path: target, files: true, project: projectId, task: taskId });
            entries = listing.entries
                .filter(e => {
                    if (e.name === '..') return false; // the listing prepends its own
                    if (e.isDir && ignoredDirs.has(e.name)) return false;
                    return true;
                })
                .sort((a, b) => {
                    // Directories first, then files, alphabetical within each group
                    if (a.isDir !== b.isDir) return a.isDir ? -1 : 1;
                    return a.name.localeCompare(b.name);
                });
            if (target !== rootPath) {
                entries = [{ name: '..', isDir: true, path: target.split('/').slice(0, -1).join('/'), isGitRepo: false }, ...entries];
            }
            missingDirRetries = 0;
            firstLoadComplete = true;
            if (!sameDir) selection.clear();
        } catch (e) {
            // The server's own error string: a wire value this guard compares against, not copy.
            if (retryMissingDir && e instanceof Error && e.message === 'directory not found'
                && missingDirRetries < MISSING_DIR_RETRIES) {
                missingDirRetries++;
                setTimeout(() => loadDir(target), MISSING_DIR_RETRY_MS);
                retrying = true;
            }
            if (!retrying) {
                error = errorText(e, t('explorer.shared.errReadDir'));
                entries = [];
            }
        } finally {
            if (!retrying) loading = false;
        }
        if (!retrying && heldKeyboard) await reclaimKeyboard();
    }

    /**
     * A folder change can remove the row that holds focus, and a keystroke then reaches no handler at all.
     * Once the new listing has rendered and this panel holds no focus, the listing takes it.
     */
    async function reclaimKeyboard(): Promise<void> {
        await tick();
        if (!rootEl || rootEl.contains(document.activeElement)) return;
        listEl?.focus({ preventScroll: true });
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

    /**
     * The row's buttons cancel `mousedown` so the field keeps focus: leaving it discards the name, and a
     * blur unmounts the button before its `click` arrives.
     */
    function keepFieldFocused(e: MouseEvent): void {
        e.preventDefault();
    }

    function cancelNewFile(): void {
        showNewFile = false;
        newFileName = '';
    }

    function cancelNewFolder(): void {
        showNewFolder = false;
        newFolderName = '';
    }

    async function handleCreateFile() {
        const name = newFileName.trim();
        if (!name || creating) return;
        creating = true;
        createError = '';
        try {
            await writeFsFile({
                project: projectId,
                task: taskId,
                path: currentPath + '/' + name,
                content: '',
                fallback: t('explorer.shared.errCreateFile'),
            });
            showNewFile = false;
            newFileName = '';
            loadDir(currentPath);
            onMutated?.();
        } catch (e) {
            createError = errorText(e, t('explorer.shared.errCreateFile'));
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
            await createFsDirectory({ path: newPath, project: projectId, task: taskId });
            showNewFolder = false;
            newFolderName = '';
            await loadDir(newPath); // a new folder is opened into
            onMutated?.();
        } catch (e) {
            createFolderError = errorText(e, t('explorer.shared.errCreateFolder'));
        } finally {
            creatingFolder = false;
        }
    }

    function openEntry(entry: FsEntry, pin: boolean) {
        if (entry.isDir) {
            loadDir(entry.path);
        } else {
            onFileSelected(entry.path, undefined, pin && pinOnDblclick);
        }
    }

    /**
     * The row in a window of its own: the same deep link the app writes for this explorer, opened on
     * this path. A popup rather than a tab because a window is what was asked for.
     */
    function openInNewWindow(entry: FsEntry): void {
        const width = Math.round(window.innerWidth * 0.8);
        const height = Math.round(window.innerHeight * 0.8);
        const left = Math.max(0, Math.round((window.screen.availWidth - width) / 2));
        const top = Math.max(0, Math.round((window.screen.availHeight - height) / 2));
        window.open(
            explorerPathHref(projectId, taskId, entry.path, entry.isDir),
            '_blank',
            `popup=yes,width=${width},height=${height},left=${left},top=${top}`,
        );
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

    // ── Selection, the context menu, and the clipboard ──
    //
    // The clipboard belongs to this panel alone: a cut must not survive a project switch.

    let clipboard = $state<{ mode: 'copy' | 'cut'; paths: string[] } | null>(null);
    let menu = $state<{ x: number; y: number; entry: FsEntry | null } | null>(null);
    let renaming = $state<string | null>(null);
    let renameValue = $state('');
    let renameInput = $state<HTMLInputElement | null>(null);
    // True while a delete, paste or rename is in flight; blocks a second one.
    let mutating = $state(false);

    let rows = $derived(entries.map(e => ({ path: e.path, selectable: rowIsSelectable(e) })));
    const selection = useFileSelection({ rows: () => rows });
    let selectedPaths = $derived(selection.selectedPaths);

    function opsScope() {
        return { projectId, taskId: taskId || undefined };
    }

    function isCut(path: string): boolean {
        return clipboard?.mode === 'cut' && clipboard.paths.includes(path);
    }

    function handleRowClick(e: MouseEvent, entry: FsEntry, index: number): void {
        if (renaming === entry.path) return;
        if (selection.handleClick(e, index)) openEntry(entry, false);
    }

    // A double tap must not open what its two taps only toggled.
    function handleRowDblClick(entry: FsEntry): void {
        if (!selection.tapMode) openEntry(entry, true);
    }

    // `mousedown` rather than `auxclick`: it is the event that carries the user activation a popup
    // needs, and cancelling it is what stops the browser's own middle-button autoscroll.
    function handleRowMiddleClick(e: MouseEvent, entry: FsEntry): void {
        if (e.button !== 1 || renaming === entry.path || !rowIsSelectable(entry)) return;
        e.preventDefault();
        openInNewWindow(entry);
    }

    // suppress native right-click menu
    function openMenuAt(e: MouseEvent | KeyboardEvent, x: number, y: number, entry: FsEntry | null, index: number): void {
        if (renaming) return;
        if (selection.handleContextmenu(e)) return;
        e.preventDefault();
        // Right-clicking a row outside the selection selects it first.
        if (entry) selection.selectRow(index);
        menu = { x, y, entry };
    }

    // Right-clicking the listing below the rows opens the same menu with nothing selected.
    function handleListContextmenu(e: MouseEvent): void {
        if ((e.target as HTMLElement).closest('.file-browser-item')) return;
        openMenuAt(e, e.clientX, e.clientY, null, -1);
    }

    const menuItems = $derived.by((): FileMenuItem[] => {
        const asked = menu;
        if (!asked) return [];
        const entry = asked.entry;
        const paths = selectedPaths;
        const items: FileMenuItem[] = [];
        if (entry) {
            items.push({ label: t('explorer.shared.menuOpen'), icon: 'open', run: () => openEntry(entry, false) });

            if (rowIsSelectable(entry)) {
                items.push({ label: t('explorer.shared.menuOpenWindow'), icon: 'window', run: () => openInNewWindow(entry) });
                const targets = paths.length > 0 ? paths : [entry.path];
                items.push({
                    label: downloadLabel(targets),
                    icon: 'download',
                    disabled: mutating,
                    run: () => startDownload(targets),
                });
            }
            if (paths.length > 0) {
                items.push({ label: t('explorer.shared.menuCut'), icon: 'cut', disabled: mutating, run: () => setClipboard('cut', paths) });
                items.push({ label: t('explorer.shared.menuCopy'), icon: 'copy', disabled: mutating, run: () => setClipboard('copy', paths) });
                if (paths.length === 1) items.push({ label: t('explorer.shared.menuRename'), icon: 'rename', disabled: mutating, run: () => startRename(paths[0]) });
                items.push({ label: t('explorer.shared.menuDelete'), icon: 'delete', destructive: true, disabled: mutating, run: () => void deletePaths(paths) });
            }
        } else {
            items.push({ label: t('explorer.shared.newFileTitle'), icon: 'newfile', run: () => void toggleNewFile() });
            items.push({ label: t('explorer.shared.newFolder'), icon: 'newfolder', run: () => void toggleNewFolder() });
        }
        if (clipboard) {
            items.push({
                label: t('explorer.shared.menuPaste'),
                icon: 'paste',
                disabled: mutating,
                run: () => void pasteInto(pasteTarget(asked.entry, currentPath)),
            });
        }
        return items;
    });

    function setClipboard(mode: 'copy' | 'cut', paths: string[]): void {
        if (paths.length === 0) return;
        clipboard = { mode, paths };
    }

    /**
     * Whether the download these rows would produce is an archive. `GET /api/fs/download` decides that,
     * and the label has to agree with it.
     */
    function downloadIsArchive(paths: string[]): boolean {
        if (paths.length !== 1) return true;
        return entries.find(e => e.path === paths[0])?.isDir ?? false;
    }

    function downloadLabel(paths: string[]): string {
        return t(downloadIsArchive(paths) ? 'explorer.shared.menuDownloadArchive' : 'explorer.shared.menuDownload');
    }

    /**
     * Hand the URL to the browser's own download. Blank target because a refusal arrives as a page, and
     * no `download` attribute because the server names the file.
     */
    function startDownload(paths: string[]): void {
        if (paths.length === 0) return;
        const link = document.createElement('a');
        link.href = downloadURL({ project: projectId, task: taskId || undefined, paths });
        link.target = '_blank';
        link.rel = 'noopener';
        document.body.appendChild(link);
        link.click();
        link.remove();
    }

    // One toast for what landed, one for what failed, whatever the batch size.
    function reportOps(res: FsOpsResult, doneKey: MessageKey): void {
        if (res.done.length > 0) dialogStore.showToast(t(doneKey, { count: res.done.length }), 'success');
        if (res.failed.length > 0) {
            const reason = res.failed[0].reason || '';
            dialogStore.showToast(t('explorer.shared.opsFailed', { count: res.failed.length, error: reason }), 'error');
        }
    }

    function reportFailure(e: unknown): void {
        dialogStore.showToast(e instanceof Error ? e.message : String(e), 'error');
    }

    async function afterMutation(): Promise<void> {
        await loadDir(currentPath);
        onMutated?.();
    }

    /** The keyboard names the folder being listed; the menu's Paste names the folder it was opened on (`pasteTarget`). */
    async function pasteInto(dest: string): Promise<void> {
        const clip = clipboard;
        if (!clip || mutating) return;
        mutating = true;
        try {
            const res = clip.mode === 'copy'
                ? await copyFsPaths(opsScope(), clip.paths, dest)
                : await moveFsPaths(opsScope(), clip.paths, dest);
            reportOps(res, 'explorer.shared.pastedCount');
            await afterMutation();
            const renamed = res.done.filter(d => d.renamed && d.to);
            if (renamed.length > 0) {
                await dialogStore.showNotice({
                    title: t('explorer.shared.pasteRenamedTitle'),
                    message: t('explorer.shared.pasteRenamedMessage'),
                    items: renamed.map(d => t('explorer.shared.pasteRenamedEntry', { from: baseName(d.from), to: baseName(d.to || '') })),
                });
            }
            if (clip.mode === 'cut') {
                clipboard = null;
                onDeleted?.(res.done.map(d => d.from));
            }
        } catch (e) {
            reportFailure(e);
        } finally {
            mutating = false;
        }
    }

    async function deletePaths(paths: string[]): Promise<void> {
        if (paths.length === 0 || mutating) return;
        const names = paths.map(baseName);
        const confirmed = await dialogStore.showConfirm({
            title: t('explorer.shared.deleteTitle'),
            message: t('explorer.shared.deleteMessage', { count: paths.length, name: names[0] }),
            items: confirmList(names, count => t('explorer.shared.deleteMore', { count })),
            confirmText: t('explorer.shared.menuDelete'),
            cancelText: t('explorer.shared.cancel'),
            variant: 'destructive',
        });
        if (!confirmed) return;
        mutating = true;
        try {
            const res = await deleteFsPaths(opsScope(), paths);
            selection.clear();
            reportOps(res, 'explorer.shared.deletedCount');
            onDeleted?.(res.done.map(d => d.from));
            await afterMutation();
        } catch (e) {
            reportFailure(e);
        } finally {
            mutating = false;
        }
    }

    function startRename(path: string): void {
        renaming = path;
        renameValue = baseName(path);
        void tick().then(() => {
            renameInput?.focus();
            // Select the stem, so the extension is left alone.
            const dot = renameValue.lastIndexOf('.');
            renameInput?.setSelectionRange(0, dot > 0 ? dot : renameValue.length);
        });
    }

    function handleRenameKey(e: KeyboardEvent): void {
        if (e.key === 'Enter') {
            e.preventDefault();
            void commitRename();
        } else if (e.key === 'Escape') {
            e.preventDefault();
            e.stopPropagation();
            renaming = null;
        }
    }

    async function commitRename(): Promise<void> {
        const path = renaming;
        renaming = null;
        if (!path) return;
        const name = renameValue.trim();
        if (!name || name === baseName(path)) return;
        try {
            const moved = await renameFsPath(opsScope(), path, name);
            dialogStore.showToast(t('explorer.shared.renamedTo', { name: baseName(moved) }), 'success');
            onDeleted?.([path]);
            await afterMutation();
        } catch (e) {
            reportFailure(e);
        }
    }

    // The panel carries these keys rather than the listing, because a key bound to the listing only
    // arrives while one of its rows holds DOM focus.
    function keyIsOurs(e: KeyboardEvent): boolean {
        if (menu) return false;
        const target = e.target;
        return target instanceof HTMLElement && !target.closest('input, textarea, [contenteditable]');
    }

    function handleKeydown(e: KeyboardEvent): void {
        if (renaming || !keyIsOurs(e)) return;
        const mod = e.ctrlKey || e.metaKey;

        const key = selection.handleKeydown(e);
        if (key?.kind === 'handled') return;
        if (key?.kind === 'menu') {
            openMenuAt(e, key.at.x, key.at.y, entries[key.index], key.index);
            return;
        }
        const focused = selection.focusedRowPath(e);
        const paths = selectedPaths.length > 0 ? selectedPaths : focused ? [focused] : [];

        if (mod && (e.key === 'x' || e.key === 'X')) {
            e.preventDefault();
            setClipboard('cut', paths);
            return;
        }
        if (mod && (e.key === 'c' || e.key === 'C')) {
            // With nothing selected, let the browser copy the text under the cursor.
            if (paths.length === 0) return;
            e.preventDefault();
            setClipboard('copy', paths);
            return;
        }
        if (mod && (e.key === 'v' || e.key === 'V')) {
            e.preventDefault();
            void pasteInto(currentPath);
            return;
        }
        if (e.key === 'F2' && paths.length === 1) {
            e.preventDefault();
            startRename(paths[0]);
            return;
        }
        if ((e.key === 'Delete' || e.key === 'Backspace') && paths.length > 0) {
            e.preventDefault();
            void deletePaths(paths);
        }
    }
</script>

<!-- svelte-ignore a11y_no_static_element_interactions — the panel carries the listing's clipboard keys -->
<div class="file-browser" bind:this={rootEl} onkeydown={handleKeydown}>
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
                    enterkeyhint="done"
                    autocapitalize="none"
                    spellcheck={false}
                    oninput={(e) => newFileName = (e.target as HTMLInputElement).value}
                    onkeydown={(e) => { if (e.key === 'Enter') void handleCreateFile(); if (e.key === 'Escape') cancelNewFile(); }}
                    onblur={cancelNewFile}
                    aria-label={t('explorer.shared.newFileNameAria')}
                />
                <button
                    type="button"
                    class="name-row-btn is-confirm"
                    onclick={() => void handleCreateFile()}
                    onmousedown={keepFieldFocused}
                    disabled={creating || !newFileName.trim()}
                    title={t('explorer.shared.confirmCreate')}
                    aria-label={t('explorer.shared.confirmCreate')}
                ><Icon src={checkIcon} width={14} height={14} /></button>
                <button
                    type="button"
                    class="name-row-btn"
                    onclick={cancelNewFile}
                    onmousedown={keepFieldFocused}
                    title={t('explorer.shared.cancel')}
                    aria-label={t('explorer.shared.cancel')}
                ><Icon src={closeIcon} width={14} height={14} /></button>
            </div>
        {:else}
            <div class="new-file-input-row">
                <input
                    type="text"
                    class="new-file-input"
                    bind:this={newFolderInput}
                    placeholder={t('explorer.shared.folderNameLowerPlaceholder')}
                    value={newFolderName}
                    enterkeyhint="done"
                    autocapitalize="none"
                    spellcheck={false}
                    oninput={(e) => newFolderName = (e.target as HTMLInputElement).value}
                    onkeydown={(e) => { if (e.key === 'Enter') void handleCreateFolder(); if (e.key === 'Escape') cancelNewFolder(); }}
                    onblur={cancelNewFolder}
                    aria-label={t('explorer.shared.newFolderNameAria')}
                />
                <button
                    type="button"
                    class="name-row-btn is-confirm"
                    onclick={() => void handleCreateFolder()}
                    onmousedown={keepFieldFocused}
                    disabled={creatingFolder || !newFolderName.trim()}
                    title={t('explorer.shared.confirmCreate')}
                    aria-label={t('explorer.shared.confirmCreate')}
                ><Icon src={checkIcon} width={14} height={14} /></button>
                <button
                    type="button"
                    class="name-row-btn"
                    onclick={cancelNewFolder}
                    onmousedown={keepFieldFocused}
                    title={t('explorer.shared.cancel')}
                    aria-label={t('explorer.shared.cancel')}
                ><Icon src={closeIcon} width={14} height={14} /></button>
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
                enterkeyhint="go"
                autocapitalize="none"
                spellcheck={false}
                oninput={(e) => { currentPath = (e.target as HTMLInputElement).value; onPathChange(currentPath); }}
                aria-label={t('explorer.shared.filePathAria')}
                onkeydown={(e) => e.key === 'Enter' && handlePathInput()}
                onblur={handlePathInput}
                title={currentPath}
            />
        </div>
    {:else if getBreadcrumbParts().length > 0}
        <div class="breadcrumb">
            <button class="breadcrumb-item" onclick={() => loadDir(rootPath)}>{folderName(rootPath)}</button>
            {#each getBreadcrumbParts() as part, index}
                <span class="breadcrumb-sep">/</span>
                <button class="breadcrumb-item" class:active={index === getBreadcrumbParts().length - 1} onclick={() => handleBreadcrumbClick(index)}>
                    {part}
                </button>
            {/each}
        </div>
    {/if}

    <!-- The listing is the drop zone, so it renders whatever the directory answered. Its `tabindex` makes
         it the focus target `reclaimKeyboard` falls back to. -->
    <div
        class="file-browser-list {listingClass}"
        class:is-drop-target={dropUpload.isOver}
        role="tree"
        tabindex="-1"
        aria-label={treeAriaLabel}
        bind:this={listEl}
        ondragenter={dropUpload.handleDragEnter}
        ondragover={dropUpload.handleDragOver}
        ondragleave={dropUpload.handleDragLeave}
        ondrop={dropUpload.handleDrop}
        oncontextmenu={handleListContextmenu}
    >
        <FileDropProgress drop={dropUpload} />
        {#if loading && entries.length === 0}
            <div class="file-browser-loading">{firstLoadComplete ? t('explorer.shared.loading') : pendingMessage}</div>
        {:else if !error && entries.length === 0}
            <EmptyState compact title={emptyMessage} />
        {/if}
        {#each entries as entry, i}
            {@const badge = extraEntryInfo ? extraEntryInfo(entry) : null}
            {@const isSelected = selection.isSelected(entry.path)}
            {#if renaming === entry.path}
                <div class="file-browser-item is-renaming" role="treeitem" aria-selected="true">
                    {#if entry.isDir}
                        <Icon src={folderIcon} color="currentColor" width={14} height={14} class="file-icon folder-icon" />
                    {:else}
                        <Icon src={fileIcon} color="var(--color-text-muted)" width={14} height={14} class="file-icon" />
                    {/if}
                    <input
                        type="text"
                        class="file-rename-input"
                        bind:this={renameInput}
                        value={renameValue}
                        enterkeyhint="done"
                        autocapitalize="none"
                        spellcheck={false}
                        oninput={(e) => renameValue = (e.target as HTMLInputElement).value}
                        onkeydown={handleRenameKey}
                        onblur={() => renaming = null}
                        aria-label={t('explorer.shared.renameAria', { name: entry.name })}
                    />
                    <button
                        type="button"
                        class="name-row-btn is-confirm"
                        onclick={() => void commitRename()}
                        onmousedown={keepFieldFocused}
                        disabled={!renameValue.trim()}
                        title={t('explorer.shared.menuRename')}
                        aria-label={t('explorer.shared.menuRename')}
                    ><Icon src={checkIcon} width={14} height={14} /></button>
                    <button
                        type="button"
                        class="name-row-btn"
                        onclick={() => renaming = null}
                        onmousedown={keepFieldFocused}
                        title={t('explorer.shared.cancel')}
                        aria-label={t('explorer.shared.cancel')}
                    ><Icon src={closeIcon} width={14} height={14} /></button>
                </div>
            {:else}
                <button
                    class="file-browser-item {rowClass}"
                    class:dir={entry.isDir}
                    class:selected={isSelected}
                    class:is-cut={isCut(entry.path)}
                    role="treeitem"
                    aria-selected={isSelected}
                    title={entry.path}
                    data-index={i}
                    onclick={(e) => handleRowClick(e, entry, i)}
                    ondblclick={() => handleRowDblClick(entry)}
                    onmousedown={(e) => handleRowMiddleClick(e, entry)}
                    oncontextmenu={(e) => openMenuAt(e, e.clientX, e.clientY, entry, i)}
                    onpointerdown={(e) => selection.handlePointerDown(e, i)}
                    onpointermove={selection.handlePointerMove}
                    onpointerup={selection.cancelLongPress}
                    onpointercancel={selection.cancelLongPress}
                >
                    {#if entry.isDir}
                        <Icon src={folderIcon} color="currentColor" width={14} height={14} class="file-icon folder-icon" />
                    {:else}
                        <Icon src={fileIcon} color="var(--color-text-muted)" width={14} height={14} class="file-icon" />
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
            {/if}
        {/each}
    </div>

    <!-- Touch equivalent of the context menu: a phone can select rows but has no right button. -->
    {#if selection.showActionBar}
        <SelectionActionBar count={selection.selectedPaths.length} onClear={selection.clear}>
            {#snippet actions()}
                <button
                    type="button"
                    class="selection-btn btn-ghost"
                    disabled={mutating}
                    title={t('explorer.shared.menuCut')}
                    aria-label={t('explorer.shared.menuCut')}
                    onclick={() => setClipboard('cut', selectedPaths)}
                >
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                        <circle cx="6" cy="6" r="3"/><circle cx="6" cy="18" r="3"/>
                        <line x1="20" y1="4" x2="8.12" y2="15.88"/><line x1="14.47" y1="14.48" x2="20" y2="20"/><line x1="8.12" y1="8.12" x2="12" y2="12"/>
                    </svg>
                </button>
                <button
                    type="button"
                    class="selection-btn btn-ghost"
                    disabled={mutating}
                    title={t('explorer.shared.menuCopy')}
                    aria-label={t('explorer.shared.menuCopy')}
                    onclick={() => setClipboard('copy', selectedPaths)}
                >
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                        <rect width="14" height="14" x="8" y="8" rx="2"/><path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2"/>
                    </svg>
                </button>
                <button
                    type="button"
                    class="selection-btn btn-ghost"
                    disabled={mutating}
                    title={downloadLabel(selectedPaths)}
                    aria-label={downloadLabel(selectedPaths)}
                    onclick={() => startDownload(selectedPaths)}
                >
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                        <path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/>
                        <path d="m7 10 5 5 5-5"/><path d="M12 15V3"/>
                    </svg>
                </button>
                {#if selectedPaths.length === 1}
                    <button
                        type="button"
                        class="selection-btn btn-ghost"
                        disabled={mutating}
                        title={t('explorer.shared.menuRename')}
                        aria-label={t('explorer.shared.menuRename')}
                        onclick={() => startRename(selectedPaths[0])}
                    ><Icon src={editIcon} width={14} height={14} /></button>
                {/if}
                <button
                    type="button"
                    class="selection-btn btn-ghost destructive"
                    disabled={mutating}
                    title={t('explorer.shared.menuDelete')}
                    aria-label={t('explorer.shared.menuDelete')}
                    onclick={() => void deletePaths(selectedPaths)}
                >
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                        <path d="M3 6h18"/><path d="M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6"/>
                        <path d="M10 11v6M14 11v6"/><path d="M9 6V4a1 1 0 0 1 1-1h4a1 1 0 0 1 1 1v2"/>
                    </svg>
                </button>
            {/snippet}
        </SelectionActionBar>
    {/if}

    {#if menu}
        <FileContextMenu
            x={menu.x}
            y={menu.y}
            items={menuItems}
            ariaLabel={t('explorer.shared.menuAria')}
            onClose={() => menu = null}
        />
    {/if}
</div>

<style>
    @import '$lib/styles/file-browser.css';
    @import '$lib/styles/file-drop.css';
</style>
