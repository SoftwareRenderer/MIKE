<script lang="ts">
    import { onMount, onDestroy } from 'svelte';
    import { getGitLogFull, getGitBranches, checkoutBranch, getCommitDiffFiles } from '$lib/api';
    import { toHomeRelative } from '$lib/utils/path';
    import { useIsMobile } from '$lib/composables/use-is-mobile.svelte';
    import type { GitCommit, GitLogResponse } from '$lib/types';
    import type { DiffFile } from '$lib/types/diff';
    import Icon from './Icon.svelte';
    import commitPushIcon from '$lib/assets/icons/commit-push.svg?raw';
    import BranchFileBrowser from './BranchFileBrowser.svelte';
    import BranchGitGraph from './BranchGitGraph.svelte';
    import FileSearchPanel from './FileSearchPanel.svelte';
    import SandboxImageStatusBanner from './SandboxImageStatusBanner.svelte';
    import EditorTabs from './EditorTabs.svelte';
    import BranchSymbols from './BranchSymbols.svelte';
    import CommitDiffOverlay from './CommitDiffOverlay.svelte';
    import GitStatusPanel from './GitStatusPanel.svelte';
    import ResizeHandle from './ResizeHandle.svelte';
    import { loadExplorerLayout, saveExplorerLayout, loadUnsavedFiles, saveUnsavedFiles, loadEditorTabs, saveEditorTabs, clearEditorDraft, clearEditorHistory } from '$lib/storage';
    import type { LSPSymbol } from '$lib/utils/lsp-symbols';
    import { buildLineSelectionPrompt } from '$lib/utils/line-selection';
    import { appStore, dialogStore, uiStore } from '$lib/stores';
    import { isAnyModalOpen } from '$lib/utils/modal-state';
    import { effectiveOpenTerminalKey, effectiveToggleFullscreenKey, formatShortcut, matchShortcut } from '$lib/utils/keyboard';
    import { t } from '$lib/i18n';
    import { EDITOR_SAVE_SETTLED, SAVE_SETTLE_BACKSTOP_MS, SaveRequestGuard } from '$lib/utils/save-state';

    // Terminal button tooltip — always shows the effective shortcut (tooltips
    // keep the shortcut text even when keycap hints are hidden).
    const terminalButtonTitle = $derived.by(() => {
        const key = effectiveOpenTerminalKey(appStore.config);
        return key
            ? t('explorer.shared.terminalTitle', { shortcut: formatShortcut(key) })
            : t('explorer.shared.openTerminal');
    });

    let { projectId, projectPath, currentBranch, initialFile, sandboxEnabled, onClose, fsEventQueue, onOpenTerminal, onUpdateFile, changesOpen, onChangesOpen }: {
        projectId: string;
        projectPath: string;
        currentBranch: string;
        initialFile: string;
        sandboxEnabled: boolean;
        onClose: () => void;
        fsEventQueue: Array<{ op: 'create' | 'delete'; path: string }>;
        onOpenTerminal: () => void;
        onUpdateFile: (path: string | null) => void;
        changesOpen: boolean;
        onChangesOpen: (open: boolean) => void;
    } = $props();

    let commits = $state<GitCommit[]>([]);
    let originTip = $state(''); // commit hash where origin/<branch> points
    let branches = $state<string[]>([]);
    let selectedBranch = $state('');
    $effect(() => { selectedBranch = currentBranch; });
    let loadingCommits = $state(true);
    let loadingBranches = $state(true);
    let checkoutLoading = $state(false);

    // Commit diff overlay state
    let diffCommit = $state<GitCommit | null>(null);
    let diffCommitIndex = $state(-1);
    let diffCommitFiles = $state<DiffFile[]>([]);
    let diffLoading = $state(false);
    const EMPTY_TREE = '4b825dc642cb6eb9a060e54bf899d69f826635b6';

    // Review Changes panel state (owned by App via the URL: view=review)
    let showChanges = $derived(changesOpen);

    function handleEditFromChanges(path: string, line?: number) {
        handleFileSelected(path, line);
        onChangesOpen(false);
    }

    // Gutter line selection in the file editor
    function handleLinesSelected(sel: { relativePath: string; startLine: number; lines: string[] }) {
        uiStore.createTaskLineSelection = { prompt: buildLineSelectionPrompt(sel) };
        uiStore.creatingChildOf = null;
        uiStore.showCreateTask = true;
    }

    // Find in files panel state
    let showFind = $state(false);
    function toggleFind() {
        showFind = !showFind;
    }

    // Sync selectedBranch when parent updates currentBranch via WebSocket
    $effect(() => {
        if (currentBranch && currentBranch !== selectedBranch) {
            selectedBranch = currentBranch;
        }
    });

    // Branch selector dropdown
    let showDropdown = $state(false);
    // svelte-ignore non_reactive_update — DOM ref via bind:this
    let dropdownEl: HTMLDivElement | null = null;
    // svelte-ignore non_reactive_update — DOM ref via bind:this

    function toggleDropdown() {
        showDropdown = !showDropdown;
    }

    function handleBranchSelect(branch: string) {
        showDropdown = false;
        if (branch !== selectedBranch) {
            handleCheckout(branch);
        }
    }

    // Close dropdown on outside click
    $effect(() => {
        function handleClick(e: MouseEvent) {
            if (dropdownEl && !dropdownEl.contains(e.target as Node)) {
                showDropdown = false;
            }
        }
        if (showDropdown) {
            document.addEventListener('click', handleClick);
            return () => document.removeEventListener('click', handleClick);
        }
    });

    // Sandbox mount path for this project
    let mountPath = $derived(sandboxEnabled ? '/home/sandbox/' + projectPath.split('/').filter(Boolean).pop() : '');

    // Validate file path stays within project root (or sandbox mount)
    function isPathSafe(path: string): boolean {
        if (!path) return false;
        if (path.startsWith(projectPath)) return true;
        if (sandboxEnabled && path.startsWith(mountPath)) return true;
        return false;
    }

    // Multi-tab file state
    interface OpenFile { path: string; targetLine: number; }
    let openFiles = $state<OpenFile[]>([]);
    let activeFilePath = $state('');
    // Set of file paths that are "pinned" (not preview). Preview tabs are replaced when another file is selected.
    let pinnedFiles = $state(new Set<string>());
    let initialSafeFile = $derived(isPathSafe(initialFile) ? initialFile : '');
    // O(1) lookup — replaces Array.includes/filter in handleHasChanges and close handler
    let unsavedFiles = $state(new Set<string>());

    // Load unsaved files from IndexedDB on init
    $effect(() => {
        loadUnsavedFiles(projectId).then(paths => { unsavedFiles = new Set(paths); }).catch(() => {});
    });

    // Sync selected file from URL on mount and when initialFile updates
    $effect(() => {
        const file = initialFile;
        if (file && isPathSafe(file) && openFiles.length === 0) {
            openFiles = [{ path: file, targetLine: 0 }];
            activeFilePath = file;
        }
    });
    // Clear open files when project changes, then restore from IndexedDB
    $effect(() => {
        const _ = projectId;
        openFiles = [];
        activeFilePath = '';
        pinnedFiles = new Set();
        unsavedFiles = new Set();
        // Restore persisted tabs for this project
        loadEditorTabs(projectId).then((saved) => {
            if (!saved) return;
            scrollTops = saved.scrollTops ?? {};
            fileListScroll = saved.fileListScroll ?? 0;
            if (saved.openFiles.length > 0) {
                openFiles = saved.openFiles;
                activeFilePath = saved.activeFilePath;
                pinnedFiles = new Set(saved.pinnedFiles);
                onUpdateFile(activeFilePath || null);
            }
        }).catch(() => {});
    });
    let editorFocused = $state(false);
    let editorSaving = $state(false);
    const saveGuard = new SaveRequestGuard();
    let saveBackstop: ReturnType<typeof setTimeout> | null = null;

    /** Ask the active editor to save; EDITOR_SAVE_SETTLED frees the button. */
    function handleEditorSave() {
        if (!activeFilePath || !unsavedFiles.has(activeFilePath)) return;
        if (!saveGuard.request(activeFilePath)) return;
        editorSaving = true;
        window.dispatchEvent(new CustomEvent('editor-save-request'));
        if (saveBackstop) clearTimeout(saveBackstop);
        saveBackstop = setTimeout(() => { saveGuard.expire(); editorSaving = false; }, SAVE_SETTLE_BACKSTOP_MS);
    }

    function handleSaveSettled(e: Event) {
        if (!saveGuard.settled((e as CustomEvent<{ path?: string }>).detail?.path)) return;
        if (saveBackstop) { clearTimeout(saveBackstop); saveBackstop = null; }
        editorSaving = false;
    }

    // Persist unsaved files to IndexedDB (convert Set → array for stable JSON format)
    $effect(() => {
        unsavedFiles;
        saveUnsavedFiles(projectId, [...unsavedFiles]).catch(() => {});
    });

    // ---- Scroll persistence (editor + file list) ----
    // svelte-ignore non_reactive_update — identity-stable on purpose (EditorTabs prop churn)
    let scrollTops: Record<string, number> = {};
    // svelte-ignore non_reactive_update — same as scrollTops
    let fileListScroll = 0;
    let tabSaveTimer: ReturnType<typeof setTimeout> | null = null;
    // Reactive: the file-list scroll $effect must re-run when the element binds.
    let fileBrowserEl = $state<HTMLDivElement | null>(null);

    function buildTabState() {
        return {
            openFiles: toPlainOpenFiles(openFiles),
            activeFilePath,
            pinnedFiles: [...pinnedFiles],
            scrollTops,
            fileListScroll,
        };
    }

    function queueTabSave() {
        if (tabSaveTimer) return;
        tabSaveTimer = setTimeout(() => {
            tabSaveTimer = null;
            saveEditorTabs(projectId, buildTabState()).catch(() => {});
        }, 300);
    }

    function handleEditorScrollTop(path: string, top: number) {
        scrollTops[path] = top;
        queueTabSave();
    }

    // Persist editor tabs (incl. scroll positions) to IndexedDB
    $effect(() => {
        openFiles;
        activeFilePath;
        pinnedFiles;
        queueTabSave();
    });

    // File-list scroll
    let fileListScrollArmed = true;
    $effect(() => {
        const el = fileBrowserEl;
        if (!el) return;
        el.addEventListener('scroll', handleFileListScrollEvent, true);
        if (!fileListScrollArmed) {
            return () => el.removeEventListener('scroll', handleFileListScrollEvent, true);
        }
        fileListScrollArmed = false;
        let attempts = 0;
        const timer = setInterval(() => {
            attempts++;
            const list = el.querySelector<HTMLElement>('.file-browser-list');
            if (list && fileListScroll > 0 && list.scrollHeight > list.clientHeight) {
                clearInterval(timer);
                list.scrollTop = Math.min(fileListScroll, list.scrollHeight - list.clientHeight);
            } else if (attempts >= 20) {
                clearInterval(timer);
            }
        }, 100);
        return () => {
            clearInterval(timer);
            el.removeEventListener('scroll', handleFileListScrollEvent, true);
        };
    });

    function handleFileListScrollEvent() {
        const list = fileBrowserEl?.querySelector<HTMLElement>('.file-browser-list');
        if (!list) return;
        fileListScroll = list.scrollTop;
        queueTabSave();
    }

    /** Convert openFiles array to plain objects (avoid Svelte 5 proxy issues). */
    function toPlainOpenFiles(files: OpenFile[]): OpenFile[] {
        return files.map(f => ({ path: f.path, targetLine: f.targetLine }));
    }

    // Mobile layout
    const isMobileCtx = useIsMobile();
    let activeTab = $state<'files' | 'editor' | 'symbols' | 'changes'>('files');
    let editorFullscreen = $state(false);

    function toggleEditorFullscreen() {
        editorFullscreen = !editorFullscreen;
    }
    let currentFolderPath = $state('');
    // The browsed folder belongs to a project, and this component outlives a project switch.
    // Without the reset, /api/fs (which resolves the project from the path) is asked for the old
    // project's folder: refused or wrong, and the panel sits on an error nothing retries.
    let seededForProject = $state<string | null>(null);
    $effect(() => {
        if (projectPath && seededForProject !== projectPath) {
            seededForProject = projectPath;
            currentFolderPath = '';
        }
        if (!currentFolderPath) {
            currentFolderPath = initialSafeFile ? initialSafeFile.substring(0, initialSafeFile.lastIndexOf('/')) : (sandboxEnabled ? mountPath : projectPath);
        }
    });

    // Symbol outline state
    let symbols = $state<LSPSymbol[]>([]);

    // Clear symbols when no file is selected
    $effect(() => {
        if (!activeFilePath) {
            symbols = [];
        }
    });

    // Column widths (resizable) — loaded from IndexedDB with defaults
    const DEFAULT_LAYOUT = { leftWidth: 250, rightWidth: 180, splitRatio: 0.5 };
    let leftWidth = $state(DEFAULT_LAYOUT.leftWidth);
    let rightWidth = $state(DEFAULT_LAYOUT.rightWidth);
    const MIN_LEFT = 180;
    const MAX_LEFT = 500;
    const MIN_RIGHT = 120;
    const MAX_RIGHT = 400;

    // Resize state for left column split (file browser / git graph)
    let splitRatio = $state(DEFAULT_LAYOUT.splitRatio);

    // Load saved explorer layout from IndexedDB on init
    loadExplorerLayout().then(layout => {
        leftWidth = layout.leftWidth;
        rightWidth = layout.rightWidth;
        splitRatio = layout.splitRatio;
    }).catch(() => {});

    /** Persist explorer layout to IndexedDB */
    function persistExplorerLayout() {
        saveExplorerLayout({ leftWidth, rightWidth, splitRatio }).catch(() => {});
    }

    const COMMIT_BATCH_SIZE = 30;
    let commitOffset = $state(0);
    let hasMoreCommits = $state(false);
    let loadingMoreCommits = $state(false);

    async function loadCommits(branch?: string, path?: string) {
        loadingCommits = true;
        commitOffset = 0;
        try {
            const resp: GitLogResponse = await getGitLogFull(projectId, { branch, path, limit: COMMIT_BATCH_SIZE, offset: 0 });
            commits = resp.commits;
            commitOffset = resp.commits.length;
            originTip = resp.origin_tip || '';
            hasMoreCommits = resp.has_more || false;
        } catch (e) {
            console.error('Failed to load commits:', e);
            commits = [];
            originTip = '';
            hasMoreCommits = false;
        } finally {
            loadingCommits = false;
        }
    }

    // Reload history when focused file or branch changes
    $effect(() => {
        const branch = selectedBranch;
        let relPath: string | undefined;
        if (activeFilePath) {
            if (activeFilePath.startsWith(projectPath)) {
                relPath = activeFilePath.slice(projectPath.length);
            } else if (sandboxEnabled && mountPath && activeFilePath.startsWith(mountPath)) {
                relPath = activeFilePath.slice(mountPath.length);
            }
            // Strip leading slash
            if (relPath && relPath.startsWith('/')) relPath = relPath.slice(1);
        }
        if (branch) {
            loadCommits(branch, relPath);
        }
    });

    async function loadMoreCommits() {
        if (loadingMoreCommits || !hasMoreCommits) return;
        loadingMoreCommits = true;
        try {
            let relPath: string | undefined;
            if (activeFilePath) {
                if (activeFilePath.startsWith(projectPath)) {
                    relPath = activeFilePath.slice(projectPath.length);
                } else if (sandboxEnabled && mountPath && activeFilePath.startsWith(mountPath)) {
                    relPath = activeFilePath.slice(mountPath.length);
                }
                if (relPath && relPath.startsWith('/')) relPath = relPath.slice(1);
            }
            const resp: GitLogResponse = await getGitLogFull(projectId, { branch: selectedBranch, path: relPath, limit: COMMIT_BATCH_SIZE, offset: commitOffset });
            commits = [...commits, ...resp.commits];
            commitOffset += resp.commits.length;
            hasMoreCommits = resp.has_more || false;
        } catch (e) {
            console.error('Failed to load more commits:', e);
        } finally {
            loadingMoreCommits = false;
        }
    }

    async function loadBranches() {
        loadingBranches = true;
        try {
            const allBranches = await getGitBranches(projectId);
            branches = allBranches.filter(b => !b.includes('/'));
        } catch (e) {
            console.error('Failed to load branches:', e);
            branches = [];
        } finally {
            loadingBranches = false;
        }
    }

    async function handleCheckout(branch: string) {
        if (branch === selectedBranch || checkoutLoading) return;
        checkoutLoading = true;
        try {
            await checkoutBranch(projectId, branch);
            selectedBranch = branch;
            // Commits will reload via $effect watching selectedBranch
        } catch (e) {
            console.error('Failed to checkout branch:', e);
        } finally {
            checkoutLoading = false;
        }
    }

    function handleFileSelected(path: string, line?: number, pin: boolean = false) {
        if (!isPathSafe(path)) return;
        closeCommitDiff();
        const existing = openFiles.findIndex(f => f.path === path);
        if (existing >= 0) {
            // File already open — switch to its tab
            activeFilePath = path;
            if (pin) pinTab(path);
        } else {
            // If there's an unpinned (preview) tab, replace it
            const previewIdx = openFiles.findIndex(f => !pinnedFiles.has(f.path));
            if (previewIdx >= 0) {
                const next = [...openFiles];
                next[previewIdx] = { path, targetLine: line || 0 };
                openFiles = next;
            } else {
                // No preview tab — add a new one
                openFiles = [...openFiles, { path, targetLine: line || 0 }];
            }
            activeFilePath = path;
            if (pin) pinTab(path);
        }
        onUpdateFile(path);
        if (isMobileCtx.isMobile) activeTab = 'editor';
    }

    function pinTab(path: string) {
        const next = new Set(pinnedFiles);
        next.add(path);
        pinnedFiles = next;
    }

    function handleSelectTab(path: string) {
        activeFilePath = path;
    }

    async function handleCloseTab(path: string) {
        const idx = openFiles.findIndex(f => f.path === path);
        if (idx < 0) return;

        // Warn about unsaved changes
        if (unsavedFiles.has(path)) {
            const fileName = path.split('/').pop() || path;
            const confirmed = await dialogStore.showConfirm({
                title: t('explorer.shared.unsavedChanges'),
                message: t('explorer.shared.unsavedDiscardMessage', { file: fileName }),
                confirmText: t('explorer.shared.discard'),
                cancelText: t('explorer.shared.cancel'),
                variant: 'destructive',
            });
            if (!confirmed) return;
            // Clear cached draft and history so changes don't reappear on reopen
            clearEditorDraft(path).catch(() => {});
            clearEditorHistory(path).catch(() => {});
        }

        closeTab(path, idx);
    }

    function closeTab(path: string, idx: number) {
        const next = openFiles.filter(f => f.path !== path);
        openFiles = next;
        const nextUnsaved = new Set(unsavedFiles);
        nextUnsaved.delete(path);
        unsavedFiles = nextUnsaved;
        const nextPinned = new Set(pinnedFiles);
        nextPinned.delete(path);
        pinnedFiles = nextPinned;
        if (path === activeFilePath) {
            if (next.length === 0) {
                activeFilePath = '';
                onUpdateFile(null);
                if (isMobileCtx.isMobile) activeTab = 'files';
                editorFullscreen = false;
            } else {
                // Switch to adjacent tab (prefer left neighbor)
                const newIdx = Math.min(idx, next.length - 1);
                activeFilePath = next[newIdx].path;
                onUpdateFile(activeFilePath);
            }
        }
    }

    // Commit diff overlay handlers
    async function handleCommitSelected(commit: GitCommit, index: number) {
        diffCommit = commit;
        diffCommitIndex = index;
        diffLoading = true;
        diffCommitFiles = [];
        if (isMobileCtx.isMobile) activeTab = 'editor';
        try {
            // Base: previous commit in log, or the commit's first parent, or empty tree
            let baseHash: string;
            if (index > 0) {
                baseHash = commits[index - 1].hash;
            } else if (commit.parents && commit.parents.length > 0) {
                baseHash = commit.parents[0];
            } else {
                baseHash = EMPTY_TREE;
            }
            diffCommitFiles = await getCommitDiffFiles(projectId, baseHash, commit.hash);
        } catch (e) {
            console.error('Failed to fetch commit diff:', e);
            diffCommitFiles = [];
        } finally {
            diffLoading = false;
        }
    }

    function closeCommitDiff() {
        diffCommit = null;
        diffCommitIndex = -1;
        diffCommitFiles = [];
    }

    function handleDiffNavigate(direction: 'prev' | 'next') {
        const newIndex = diffCommitIndex + (direction === 'next' ? -1 : 1);
        if (newIndex >= 0 && newIndex < commits.length) {
            handleCommitSelected(commits[newIndex], newIndex);
        }
    }

    function handleSymbolsUpdate(newSymbols: LSPSymbol[]) {
        symbols = newSymbols;
    }

    function handleEditorFocused(focused: boolean) {
        editorFocused = focused;
    }

    function handleHasChanges(filePath: string, hasChanges: boolean) {
        const currentlyUnsaved = unsavedFiles.has(filePath);
        if (hasChanges && !currentlyUnsaved) {
            const next = new Set(unsavedFiles);
            next.add(filePath);
            unsavedFiles = next;
        } else if (!hasChanges && currentlyUnsaved) {
            const next = new Set(unsavedFiles);
            next.delete(filePath);
            unsavedFiles = next;
        }
    }

    // Consume fs events from the shared queue
    let pendingFsEvent = $state<{ op: 'create' | 'delete'; path: string } | null>(null);
    let lastQueueLen = $state(0);
    $effect(() => {
        const len = fsEventQueue.length;
        // Reset when queue is cleared (project switch)
        if (len === 0) {
            lastQueueLen = 0;
            return;
        }
        if (len > lastQueueLen) {
            // Use the latest event (most relevant)
            pendingFsEvent = fsEventQueue[len - 1];
            lastQueueLen = len;
        }
    });

    // Handle Escape key — close panels in order: find → changes → commit diff → branch view
    // Handle Ctrl+Shift+F — toggle find panel
    function handleKeydown(e: KeyboardEvent) {
        if (e.key === 'Escape') {
            // A modal (e.g. the floating line-task modal) owns Escape while open
            if (isAnyModalOpen()) return;
            if (showFind) {
                showFind = false;
            } else if (showChanges) {
                onChangesOpen(false);
            } else if (diffCommit) {
                closeCommitDiff();
            } else if (!editorFocused) {
                onClose();
            }
        }
        if ((e.ctrlKey || e.metaKey) && e.shiftKey && e.key === 'F') {
            e.preventDefault();
            toggleFind();
        }
        // Toggle-fullscreen shortcut (default Ctrl+Up): maximize/minimize the
        // editor when it is the focused area (file open, no commit diff).
        if (e.key === 'ArrowUp' && !e.repeat && !e.defaultPrevented && !diffCommit && openFiles.length > 0) {
            const key = effectiveToggleFullscreenKey(appStore.config);
            if (key && matchShortcut(key, e)) {
                const active = document.activeElement;
                const center = document.querySelector('.explorer-center-column');
                if (active instanceof HTMLElement && center?.contains(active)) {
                    e.preventDefault();
                    toggleEditorFullscreen();
                }
            }
        }
    }

    // Resize handlers
    function handleLeftDragStart(_e: MouseEvent | TouchEvent, startX: number) {
        leftDragStartX = startX;
    }
    let leftDragStartX = 0;
    function handleLeftDragMove(pointerX: number) {
        const body = document.querySelector('.explorer-body');
        if (!body) return;
        const posX = pointerX - body.getBoundingClientRect().left;
        leftWidth = Math.max(MIN_LEFT, Math.min(MAX_LEFT, posX));
    }
    function handleLeftDragEnd() { persistExplorerLayout(); }

    function handleRightDragStart(_e: MouseEvent | TouchEvent, startX: number) {
        rightDragStartX = startX;
    }
    let rightDragStartX = 0;
    function handleRightDragMove(pointerX: number) {
        const body = document.querySelector('.explorer-body');
        if (!body) return;
        const posX = body.getBoundingClientRect().right - pointerX;
        rightWidth = Math.max(MIN_RIGHT, Math.min(MAX_RIGHT, posX));
    }
    function handleRightDragEnd() { persistExplorerLayout(); }

    function handleSplitDragStart(_e: MouseEvent | TouchEvent, startY: number) {
        splitDragStartY = startY;
    }
    let splitDragStartY = 0;
    function handleSplitDragMove(pointerY: number) {
        const container = document.querySelector('.explorer-split-container');
        if (!container) return;
        const posY = pointerY - container.getBoundingClientRect().top;
        splitRatio = Math.max(0.2, Math.min(0.8, posY / container.getBoundingClientRect().height));
    }
    function handleSplitDragEnd() { persistExplorerLayout(); }

    $effect(() => {
        const mobile = isMobileCtx.isMobile;
        if (mobile) {
            splitRatio = 0.75;
        }
    });

    function getFolderName(path: string): string {
        const parts = path.split('/');
        return parts[parts.length - 1] || '/';
    }

    onMount(() => {
        loadCommits();
        loadBranches();
        // If a file is already open, switch to editor tab
        if (openFiles.length > 0) {
            activeTab = 'editor';
        }
        window.addEventListener('keydown', handleKeydown);
        window.addEventListener('open-file', handleOpenFile);
        window.addEventListener(EDITOR_SAVE_SETTLED, handleSaveSettled);
    });

    // open-file dispatch (go-to-definition / search results)
    function handleOpenFile(e: Event) {
        const { path, line, taskId } = (e as CustomEvent<{ path: string; line?: number; taskId?: string }>).detail;
        if (taskId) return;
        handleFileSelected(path, line);
    }

    onDestroy(() => {
        window.removeEventListener('keydown', handleKeydown);
        window.removeEventListener('open-file', handleOpenFile);
        window.removeEventListener(EDITOR_SAVE_SETTLED, handleSaveSettled);
        if (saveBackstop) { clearTimeout(saveBackstop); saveBackstop = null; }
        // Flush a pending (throttled) tab+scroll save.
        if (tabSaveTimer) {
            clearTimeout(tabSaveTimer);
            tabSaveTimer = null;
            saveEditorTabs(projectId, buildTabState()).catch(() => {});
        }
    });
</script>

<div class="explorer-view" class:fullscreen={editorFullscreen && activeFilePath} role="dialog" aria-label={t('explorer.view.aria')}>
    <!-- Header bar (replaces top-bar when explorer is open) -->
    <div class="explorer-header">
        <button class="close-btn" onclick={() => { if (showChanges) { onChangesOpen(false); } else { editorFullscreen = false; closeCommitDiff(); onClose(); } }} aria-label={t('explorer.view.closeAria')} title={t('explorer.shared.closeEscape')}>
            <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                <path d="M4 4l8 8M12 4l-8 8" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
            </svg>
        </button>
        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" class="explorer-view-icon">
            <path d="M3 7v10a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2h-6l-2-2H5a2 2 0 00-2 2z"/>
        </svg>
        <span class="project-path" title={toHomeRelative(projectPath)}>{toHomeRelative(projectPath)}</span>

        <!-- Branch selector: badge when changes panel open, dropdown otherwise -->
        {#if showChanges}
            <span class="explorer-branch-badge" title="{selectedBranch}">
                <svg width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                    <circle cx="6" cy="6" r="2.5"/>
                    <circle cx="10" cy="10" r="2.5"/>
                    <line x1="7.8" y1="7.8" x2="8.2" y2="8.2"/>
                    <line x1="6" y1="3.5" x2="6" y2="2"/>
                    <line x1="10" y1="12.5" x2="10" y2="14"/>
                </svg>
                <span class="branch-badge-label">{selectedBranch}</span>
            </span>
        {:else}
            <div class="explorer-selector" class:open={showDropdown} bind:this={dropdownEl}>
                <button
                    class="explorer-dropdown-btn"
                    onclick={toggleDropdown}
                    title={t('explorer.view.switchBranch')}
                    disabled={checkoutLoading}
                >
                    <svg width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                        <circle cx="6" cy="6" r="2.5"/>
                        <circle cx="10" cy="10" r="2.5"/>
                        <line x1="7.8" y1="7.8" x2="8.2" y2="8.2"/>
                        <line x1="6" y1="3.5" x2="6" y2="2"/>
                        <line x1="10" y1="12.5" x2="10" y2="14"/>
                    </svg>
                    <span class="explorer-name">{selectedBranch}</span>
                    <svg width="10" height="10" viewBox="0 0 10 10" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                        <polyline points="2,3.5 5,6.5 8,3.5"/>
                    </svg>
                </button>

                {#if showDropdown}
                    <div class="explorer-dropdown-list" role="listbox">
                        {#if loadingBranches}
                            <div class="explorer-dropdown-item loading">{t('explorer.shared.loading')}</div>
                        {:else}
                            {#each branches as branch}
                                <button
                                    class="explorer-dropdown-item"
                                    class:active={branch === selectedBranch}
                                    onclick={() => handleBranchSelect(branch)}
                                    role="option"
                                    aria-selected={branch === selectedBranch}
                                >
                                    {#if branch === selectedBranch}
                                        <svg width="10" height="10" viewBox="0 0 10 10" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                                            <polyline points="1.5,5 4,7.5 8.5,2.5"/>
                                        </svg>
                                    {:else}
                                        <span class="explorer-check-empty"></span>
                                    {/if}
                                    <span>{branch}</span>
                                </button>
                            {/each}
                            {#if branches.length === 0}
                                <div class="explorer-dropdown-item empty">{t('explorer.view.noBranches')}</div>
                            {/if}
                        {/if}
                    </div>
                {/if}
            </div>
        {/if}
        <div class="explorer-header-spacer"></div>
        <button class="icon-btn git-status-btn" class:active={showChanges} onclick={() => onChangesOpen(!showChanges)} aria-label={t('explorer.shared.reviewChanges')} title={t('explorer.shared.reviewChanges')}>
            <Icon src={commitPushIcon} color="currentColor" width={14} height={14} />
        </button>
        <button class="icon-btn terminal-btn" onclick={onOpenTerminal} aria-label={t('explorer.shared.openTerminal')} title={terminalButtonTitle}>
            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                <rect x="1" y="2" width="14" height="12" rx="1.5" stroke="currentColor" stroke-width="1.3"/>
                <path d="M4 6l2 2-2 2M7 10h3" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
            </svg>
        </button>
    </div>

    <!-- Mobile Tab Bar -->
    {#if isMobileCtx.isMobile && !showChanges && !editorFullscreen}
        <div class="mobile-tab-bar">
            <button
                class="tab-btn {activeTab === 'files' ? 'active' : ''}"
                onclick={() => activeTab = 'files'}
                title={t('explorer.shared.fileBrowser')}
            >
                <svg width="12" height="12" viewBox="0 0 16 16" fill="none"><path d="M2 3h4l1.5 2H13l1.5 2.5V13a1 1 0 0 1-1 1H2a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1z" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round"/></svg>
                <span class="tab-label">{getFolderName(currentFolderPath)}</span>
            </button>
            <button
                class="tab-btn {activeTab === 'editor' ? 'active' : ''}"
                onclick={() => activeTab = 'editor'}
                title={t('explorer.shared.editor')}
            >
                <svg width="12" height="12" viewBox="0 0 16 16" fill="none"><path d="M3 2h6l4 4v8a1 1 0 0 1-1 1H3a1 1 0 0 1-1-1V3a1 1 0 0 1 1-1z" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round"/><path d="M9 2v4h4" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round"/></svg>
                {activeFilePath ? activeFilePath.split('/').pop() : t('explorer.shared.editor')}
            </button>
            <button
                class="tab-btn {activeTab === 'symbols' ? 'active' : ''}"
                onclick={() => activeTab = 'symbols'}
                title={t('explorer.shared.symbols')}
            >
                <svg width="12" height="12" viewBox="0 0 16 16" fill="none"><path d="M3 4h10M3 8h10M3 12h6" stroke="currentColor" stroke-width="1.2" stroke-linecap="round"/></svg>
                {t('explorer.shared.symbols')}
            </button>
        </div>
    {/if}

    <!-- Multi-column layout -->
    <div class="explorer-body">
        <!-- Review Changes panel (takes over full width when open) -->
        {#if showChanges}
        <div class="explorer-changes-column">
            <GitStatusPanel
                projectId={projectId}
                projectPath={projectPath}
                onClose={() => onChangesOpen(false)}
                isMobile={isMobileCtx.isMobile}
                onEditFile={handleEditFromChanges}
                onCommitted={() => {
                    loadCommits();
                    loadBranches();
                }}
                onUpdated={() => {
                    loadCommits();
                    loadBranches();
                }}
            />
        </div>
        {:else}
            <!-- Column 1: File browser + Git graph (or Find panel) -->
            <div class="explorer-left-column" class:hidden={isMobileCtx.isMobile && activeTab !== 'files'} style="width: {leftWidth}px">
                <!-- The listing request below is blocked until the project's sandbox image exists,
                     so its build/pull progress belongs here. -->
                <SandboxImageStatusBanner />
                {#if showFind}
                    <!-- Find in files panel (replaces file browser + git graph) -->
                    <FileSearchPanel
                        projectId={projectId}
                        onFileSelected={handleFileSelected}
                        onClose={() => showFind = false}
                    />
                {:else}
                    <div class="explorer-split-container">
                        <!-- Top: File browser -->
                        <div class="explorer-file-browser" class:mobile={isMobileCtx.isMobile} style:height={isMobileCtx.isMobile ? undefined : splitRatio * 100 + '%'} bind:this={fileBrowserEl}>
                            <BranchFileBrowser
                                projectId={projectId}
                                projectPath={projectPath}
                                initialPath={currentFolderPath}
                                sandboxEnabled={sandboxEnabled}
                                unsavedFiles={[...unsavedFiles]}
                                pendingFsEvent={pendingFsEvent}
                                onFileSelected={handleFileSelected}
                                onPathChange={(p) => currentFolderPath = p}
                                onToggleFind={toggleFind}
                            />
                        </div>

                        <!-- Drag handle (file browser / git graph split) -->
                        {#if !isMobileCtx.isMobile}
                        <ResizeHandle
                            axis="horizontal"
                            ariaLabel={t('explorer.shared.resizeSplitAria')}
                            onDragStart={handleSplitDragStart}
                            onDragMove={handleSplitDragMove}
                            onDragEnd={handleSplitDragEnd}
                        />
                        {/if}

                        <!-- Bottom: Git graph -->
                        <div class="explorer-git-graph" class:mobile={isMobileCtx.isMobile} style:height={isMobileCtx.isMobile ? undefined : (1 - splitRatio) * 100 + '%'}>
                            <BranchGitGraph
                                currentBranch={selectedBranch}
                                commits={commits}
                                originTip={originTip}
                                loading={loadingCommits || loadingMoreCommits}
                                hasMore={hasMoreCommits}
                                onCommitSelected={handleCommitSelected}
                                onLoadMore={loadMoreCommits}
                                selectedCommitHash={diffCommit?.hash}
                            />
                        </div>
                    </div>
                {/if}
            </div>

            <!-- Resize handle: left column -->
            {#if !isMobileCtx.isMobile}
            <ResizeHandle
                ariaLabel={t('explorer.shared.resizeLeftAria')}
                onDragStart={handleLeftDragStart}
                onDragMove={handleLeftDragMove}
                onDragEnd={handleLeftDragEnd}
            />
            {/if}

            <!-- Column 2: Commit diff or file editor -->
            <div class="explorer-center-column" class:hidden={isMobileCtx.isMobile && activeTab !== 'editor'}>
                {#if diffCommit}
                    <CommitDiffOverlay
                        commit={diffCommit}
                        files={diffCommitFiles}
                        loading={diffLoading}
                        projectPath={projectPath}
                        onClose={closeCommitDiff}
                        onNavigate={handleDiffNavigate}
                        onFileSelected={handleFileSelected}
                        hasPrev={diffCommitIndex < commits.length - 1}
                        hasNext={diffCommitIndex > 0}
                        isMobile={isMobileCtx.isMobile}
                    />
                {:else}
                    {#if openFiles.length > 0}
                        <EditorTabs
                            projectId={projectId}
                            projectPath={projectPath}
                            openFiles={openFiles}
                            activeFilePath={activeFilePath}
                            unsavedFiles={unsavedFiles}
                            pinnedFiles={pinnedFiles}
                            onFocused={handleEditorFocused}
                            onSymbolsUpdate={handleSymbolsUpdate}
                            onHasChanges={handleHasChanges}
                            onSelectTab={handleSelectTab}
                            onCloseTab={handleCloseTab}
                            onPinTab={pinTab}
                            onSave={handleEditorSave}
                            saving={editorSaving}
                            onToggleFullscreen={toggleEditorFullscreen}
                            mobileFullscreen={editorFullscreen}
                            pendingFsEvent={pendingFsEvent}
                            onLinesSelected={handleLinesSelected}
                            scrollTops={scrollTops}
                            onScrollTop={handleEditorScrollTop}
                        />
                    {:else}
                        <div class="file-editor-placeholder">
                            <svg width="32" height="32" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1" opacity="0.3" aria-hidden="true">
                                <path d="M3 1h6l4 4v8a1 1 0 01-1 1H3a1 1 0 01-1-1V2a1 1 0 011-1z"/>
                                <polyline points="9,1 9,5 13,5"/>
                            </svg>
                            <p>{t('explorer.shared.selectFileToEdit')}</p>
                        </div>
                    {/if}
                {/if}
            </div>

            <!-- Resize handle: right column (only when symbols exist and no diff is open) -->
            {#if !isMobileCtx.isMobile && symbols.length > 0 && !diffCommit}
            <ResizeHandle
                ariaLabel={t('explorer.shared.resizeRightAria')}
                onDragStart={handleRightDragStart}
                onDragMove={handleRightDragMove}
                onDragEnd={handleRightDragEnd}
            />
            {/if}

            <!-- Column 3: Symbol outline (hidden when no symbols or diff is open, always visible on mobile via tab) -->
            <div class="explorer-right-column" class:hidden={(symbols.length === 0 && !isMobileCtx.isMobile) || (isMobileCtx.isMobile && activeTab !== 'symbols') || !!diffCommit} style="width: {rightWidth}px">
                <BranchSymbols
                    symbols={symbols}
                    onSymbolClick={(line: number) => {
                        if (isMobileCtx.isMobile) {
                            activeTab = 'editor';
                            // Dispatch after tab switch so editor is visible and layout is computed
                            requestAnimationFrame(() => {
                                window.dispatchEvent(new CustomEvent('jump-to-line', { detail: { line } }));
                            });
                        } else {
                            window.dispatchEvent(new CustomEvent('jump-to-line', { detail: { line } }));
                        }
                    }}
                />
            </div>
        {/if}
    </div>
</div>

<style>
    @import '$lib/styles/shared.css';

    .explorer-view {
        display: flex;
        flex-direction: column;
        height: 100%;
        background: var(--color-surface-0);
        overflow: hidden;
    }

    .explorer-header {
        display: flex;
        align-items: center;
        gap: 8px;
        padding: 6px 12px;
        border-bottom: 1px solid var(--color-border);
        background: var(--color-surface-1);
        flex-shrink: 0;
    }

    /* Fullscreen mode: hide header and tab bar, expand to cover sidebar */
    .explorer-view.fullscreen {
        position: fixed;
        top: 0;
        left: 0;
        right: 0;
        bottom: 0;
        z-index: 1000;
        margin: 0;
        border-radius: 0;
    }
    .explorer-view.fullscreen .explorer-header {
        display: none;
    }
    .explorer-view.fullscreen .mobile-tab-bar {
        display: none !important;
    }
    .explorer-view.fullscreen .explorer-left-column {
        display: none !important;
    }
    .explorer-view.fullscreen .explorer-body {
        height: 100%;
    }

    .explorer-view-icon {
        color: var(--color-text-secondary);
        flex-shrink: 0;
    }

    .project-path {
        font-size: 0.8125rem;
        color: var(--color-text-primary);
        line-height: 1;
        align-self: center;
        min-width: 0;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }

    .explorer-header-spacer {
        flex: 1;
    }

    :global(.close-btn) {
        padding: 6px;
    }

    .terminal-btn {
        padding: 6px 12px;
        border-radius: var(--radius-md);
        background: var(--color-surface-3);
        color: var(--color-text-primary);
        display: inline-flex;
        align-items: center;
        justify-content: center;
        border: none;
        cursor: pointer;
    }
    .terminal-btn:hover {
        background: var(--color-accent-dark);
        color: white;
    }

    .git-status-btn {
        padding: 6px 12px;
        border-radius: var(--radius-md);
        background: var(--color-surface-3);
        color: var(--color-text-primary);
        display: inline-flex;
        align-items: center;
        justify-content: center;
        border: none;
        cursor: pointer;
    }
    .git-status-btn:hover,
    .git-status-btn.active {
        background: var(--color-accent-dark);
        color: white;
    }

    /* Branch badge (shown when changes panel is open) */
    .explorer-branch-badge {
        display: inline-flex;
        align-items: center;
        gap: 4px;
        padding: 4px 10px;
        border-radius: var(--radius-md);
        background: var(--color-surface-3);
        color: var(--color-text-secondary);
        font-size: 0.8125rem;
        font-family: var(--font-mono);
        font-weight: 500;
        max-width: 160px;
        overflow: hidden;
        min-width: 0;
    }
    .explorer-branch-badge svg {
        flex-shrink: 0;
    }
    .branch-badge-label {
        min-width: 0;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }

    /* Branch selector */
    .explorer-selector {
        position: relative;
        min-width: 0;
    }

    .explorer-dropdown-btn {
        display: flex;
        align-items: center;
        gap: 4px;
        padding: 6px 8px;
        border-radius: var(--radius-md);
        background: var(--color-surface-3);
        color: var(--color-text-secondary);
        font-size: 14px;
        font-family: var(--font-mono);
        cursor: pointer;
        border: none;
        min-width: 0;
    }
    .explorer-dropdown-btn svg {
        flex-shrink: 0;
    }
    .explorer-dropdown-btn:hover {
        background: var(--color-surface-4);
        color: var(--color-text-primary);
        opacity: 1;
    }
    .explorer-dropdown-btn:disabled {
        opacity: 0.5;
        cursor: not-allowed;
    }

    .explorer-name {
        max-width: 120px;
        min-width: 0;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }

    .explorer-dropdown-list {
        position: absolute;
        top: 100%;
        right: 0;
        z-index: 100;
        min-width: 160px;
        max-height: 240px;
        overflow-y: auto;
        background: var(--color-surface-2);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-md);
        box-shadow: 0 4px 12px rgba(0, 0, 0, 0.3);
        margin-top: 2px;
    }

    .explorer-dropdown-item {
        display: flex;
        align-items: center;
        gap: 6px;
        width: 100%;
        padding: 6px 10px;
        border: none;
        background: transparent;
        color: var(--color-text-primary);
        font-size: 14px;
        text-align: left;
        cursor: pointer;
    }
    .explorer-dropdown-item:hover {
        background: var(--color-surface-3);
        opacity: 1;
    }
    .explorer-dropdown-item.active {
        color: var(--color-status-purple);
    }
    .explorer-dropdown-item.loading,
    .explorer-dropdown-item.empty {
        color: var(--color-text-tertiary);
        cursor: default;
    }
    .explorer-check-empty {
        display: inline-block;
        width: 10px;
    }

    .explorer-body {
        display: flex;
        flex: 1;
        overflow: hidden;
    }

    .explorer-left-column {
        flex-shrink: 0;
        display: flex;
        flex-direction: column;
        border-right: 1px solid var(--color-border);
        background: var(--color-surface-1);
        overflow: hidden;
    }

    .explorer-split-container {
        flex: 1;
        display: flex;
        flex-direction: column;
        overflow: hidden;
        position: relative;
    }

    .explorer-file-browser {
        overflow: hidden;
        display: flex;
        flex-direction: column;
    }

    .explorer-git-graph {
        overflow: hidden;
        display: flex;
        flex-direction: column;
    }

    .explorer-center-column {
        flex: 1;
        min-width: 0;
        display: flex;
        flex-direction: column;
        overflow: hidden;
    }

    .explorer-right-column {
        flex-shrink: 0;
        border-left: 1px solid var(--color-border);
        background: var(--color-surface-1);
        overflow: hidden;
        display: flex;
        flex-direction: column;
    }
    .explorer-right-column.hidden {
        display: none;
    }
    .explorer-left-column.hidden {
        display: none;
    }

    .explorer-changes-column {
        flex: 1;
        display: flex;
        flex-direction: column;
        background: var(--color-surface-1);
        overflow: hidden;
        min-width: 0;
    }

    .file-editor-placeholder {
        display: flex;
        flex-direction: column;
        align-items: center;
        justify-content: center;
        gap: 8px;
        padding: 48px 24px;
        color: var(--color-text-tertiary);
        font-size: 0.8125rem;
        flex: 1;
    }

    .mobile-tab-bar {
        display: none;
    }
    @media (max-width: 768px) {
        .mobile-tab-bar {
            display: flex;
            border-bottom: 1px solid var(--color-border);
            background: var(--color-surface-1);
            flex-shrink: 0;
        }
        .tab-btn {
            flex: 1;
            display: flex;
            align-items: center;
            justify-content: center;
            gap: 4px;
            padding: 10px 8px;
            border: none;
            background: none;
            color: var(--color-text-tertiary);
            font-size: 0.75rem;
            font-weight: 500;
            cursor: pointer;
            border-bottom: 2px solid transparent;
            transition: all 0.15s ease;
        }
        .tab-btn.active {
            color: var(--color-accent);
            border-bottom-color: var(--color-accent);
        }
        .tab-btn:hover:not(.active) {
            color: var(--color-text-primary);
        }
        .explorer-left-column {
            width: 100% !important;
            border-right: none;
            flex: 1 !important;
            min-height: 0;
        }
        .explorer-file-browser.mobile {
            flex: 0 0 75% !important;
            min-height: 100px;
        }
        .explorer-git-graph.mobile {
            flex: 1;
            min-height: 0;
        }
        .explorer-center-column {
            width: 100% !important;
            flex: 1 !important;
            min-height: 0;
        }
        .explorer-right-column {
            width: 100% !important;
            border-left: none;
        }
        .explorer-left-column.hidden,
        .explorer-center-column.hidden,
        .explorer-right-column.hidden {
            display: none;
        }
        .explorer-body {
            flex-direction: column;
            min-height: 0;
        }
        .explorer-changes-column {
            width: 100% !important;
            flex: 1 !important;
            min-height: 0;
        }
    }
</style>
