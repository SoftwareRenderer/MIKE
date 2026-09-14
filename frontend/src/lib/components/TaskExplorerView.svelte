<script lang="ts">
    import { onMount, onDestroy } from 'svelte';
    import { getTaskGitLog, getTaskCommitDiffFiles, initTask } from '$lib/api';
    import { toHomeRelative, toExplorerPath } from '$lib/utils/path';
    import { useIsMobile } from '$lib/composables/use-is-mobile.svelte';
    import type { GitCommit, GitLogResponse } from '$lib/types';
    import type { DiffFile } from '$lib/types/diff';
    import TaskFileBrowser from './TaskFileBrowser.svelte';
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
    import { revealFileTab, mergeRestoredTabs, toPersistedTabs, fromPersistedTabs, type OpenFileTab } from '$lib/utils/editor-tabs';

    // Terminal button tooltip — always shows the effective shortcut (tooltips
    // keep the shortcut text even when keycap hints are hidden).
    const terminalButtonTitle = $derived.by(() => {
        const key = effectiveOpenTerminalKey(appStore.config);
        return key
            ? t('explorer.shared.terminalTitle', { shortcut: formatShortcut(key) })
            : t('explorer.shared.openTerminal');
    });

    let { projectId, projectName, taskId, taskPath, taskBranch, taskStatus, sandboxEnabled, onClose, fsEventQueue, onOpenTerminal, onUpdateFile, changesOpen, onChangesOpen }: {
        projectId: string;
        projectName: string;
        taskId: string;
        taskPath: string;
        taskBranch: string;
        taskStatus: string;
        sandboxEnabled: boolean;
        onClose: () => void;
        fsEventQueue: Array<{ op: 'create' | 'delete'; path: string }>;
        onOpenTerminal: () => void;
        onUpdateFile: (path: string | null) => void;
        changesOpen: boolean;
        onChangesOpen: (open: boolean) => void;
    } = $props();

    let commits = $state<GitCommit[]>([]);
    let originTip = $state('');
    let selectedBranch = $state('');
    $effect(() => { selectedBranch = taskBranch; });

    // Status color for branch badge
    let statusColor = $derived(
        taskStatus === 'backlog' ? 'var(--color-column-backlog)' :
        taskStatus === 'in_progress' ? 'var(--color-column-in-progress)' :
        taskStatus === 'review' ? 'var(--color-column-review)' :
        taskStatus === 'done' ? 'var(--color-column-done)' :
        'var(--color-text-secondary)'
    );

    let loadingCommits = $state(true);

    // Commit diff overlay state
    let diffCommit = $state<GitCommit | null>(null);
    let diffCommitIndex = $state(-1);
    let diffCommitFiles = $state<DiffFile[]>([]);
    let diffLoading = $state(false);
    const EMPTY_TREE = '4b825dc642cb6eb9a060e54bf899d69f826635b6';

    // Review Changes panel state
    let showChanges = $derived(changesOpen);

    /** Open a file from Review Changes in the usual file editor. */
    function handleEditFromChanges(path: string, line?: number) {
        handleFileSelected(path, line);
        onChangesOpen(false);
    }

    // Gutter line selection in the file editor → Create Task modal prefilled with the selected lines
    function handleLinesSelected(sel: { relativePath: string; startLine: number; lines: string[] }) {
        uiStore.createTaskLineSelection = { prompt: buildLineSelectionPrompt(sel) };
        uiStore.creatingChildOf = taskId;
        uiStore.showCreateTask = true;
    }

    // Find in files panel state
    let showFind = $state(false);
    function toggleFind() {
        showFind = !showFind;
    }

    // Sync selectedBranch when parent updates
    $effect(() => {
        if (taskBranch && taskBranch !== selectedBranch) {
            selectedBranch = taskBranch;
        }
    });

    // Sandbox mount path for this task: /home/sandbox/<projectName>
    let mountPath = $derived(sandboxEnabled ? '/home/sandbox/' + projectName : '');

    // Validate file path stays within task root
    function isPathSafe(path: string): boolean {
        if (!path) return false;
        if (path.startsWith(taskPath)) return true;
        if (sandboxEnabled && path.startsWith(mountPath)) return true;
        return false;
    }

    // Multi-tab file state
    let openFiles = $state<OpenFileTab[]>([]);
    let activeFilePath = $state('');
    let pinnedFiles = $state(new Set<string>());
    let unsavedFiles = $state(new Set<string>());

    // Load unsaved files from IndexedDB
    $effect(() => {
        loadUnsavedFiles(projectId).then(paths => { unsavedFiles = new Set(paths); }).catch(() => {});
    });

    // Clear open files when task changes
    $effect(() => {
        const _ = taskId;
        openFiles = [];
        activeFilePath = '';
        pinnedFiles = new Set();
        unsavedFiles = new Set();
        // Restore persisted tabs for this project
        loadEditorTabs(projectId).then((saved) => {
            if (saved && saved.openFiles.length > 0) {
                openFiles = mergeRestoredTabs(openFiles, fromPersistedTabs(saved.openFiles.filter(f => isPathSafe(f.path))));
                if (!activeFilePath) {
                    activeFilePath = saved.openFiles.find(f => f.path === saved.activeFilePath && isPathSafe(f.path))?.path || '';
                    onUpdateFile(activeFilePath || null);
                }
                pinnedFiles = new Set(saved.pinnedFiles.filter(p => openFiles.some(f => f.path === p)));
                scrollTops = { ...saved.scrollTops, ...scrollTops };
                fileListScroll = fileListScroll || (saved.fileListScroll ?? 0);
            }
        }).catch(() => {});
    });

    let scrollTops: Record<string, number> = {};
    let fileListScroll = 0;
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

    // Persist unsaved files
    $effect(() => {
        unsavedFiles;
        saveUnsavedFiles(projectId, [...unsavedFiles]).catch(() => {});
    });

    // Persist editor tabs
    $effect(() => {
        openFiles;
        activeFilePath;
        pinnedFiles;
        saveEditorTabs(projectId, {
            openFiles: toPersistedTabs(openFiles),
            activeFilePath,
            pinnedFiles: [...pinnedFiles],
            scrollTops,
            fileListScroll,
        }).catch(() => {});
    });

    // Mobile layout
    const isMobileCtx = useIsMobile();
    let activeTab = $state<'files' | 'editor' | 'symbols' | 'changes'>('files');
    let editorFullscreen = $state(false);

    function toggleEditorFullscreen() {
        editorFullscreen = !editorFullscreen;
    }
    let currentFolderPath = $state('');
    $effect(() => {
        if (!currentFolderPath) {
            currentFolderPath = sandboxEnabled ? mountPath : taskPath;
        }
    });

    // Symbol outline state
    let symbols = $state<LSPSymbol[]>([]);

    $effect(() => {
        if (!activeFilePath) {
            symbols = [];
        }
    });

    // Column widths
    const DEFAULT_LAYOUT = { leftWidth: 250, rightWidth: 180, splitRatio: 0.5 };
    let leftWidth = $state(DEFAULT_LAYOUT.leftWidth);
    let rightWidth = $state(DEFAULT_LAYOUT.rightWidth);
    const MIN_LEFT = 180;
    const MAX_LEFT = 500;
    const MIN_RIGHT = 120;
    const MAX_RIGHT = 400;

    let splitRatio = $state(DEFAULT_LAYOUT.splitRatio);

    loadExplorerLayout().then(layout => {
        leftWidth = layout.leftWidth;
        rightWidth = layout.rightWidth;
        splitRatio = layout.splitRatio;
    }).catch(() => {});

    function persistExplorerLayout() {
        saveExplorerLayout({ leftWidth, rightWidth, splitRatio }).catch(() => {});
    }

    const COMMIT_BATCH_SIZE = 30;
    let commitOffset = $state(0);
    let hasMoreCommits = $state(false);
    let loadingMoreCommits = $state(false);

    async function loadCommits(branch?: string) {
        loadingCommits = true;
        commitOffset = 0;
        try {
            const resp: GitLogResponse = await getTaskGitLog(projectId, taskId, { branch, limit: COMMIT_BATCH_SIZE, offset: 0 });
            commits = resp.commits;
            commitOffset = resp.commits.length;
            originTip = resp.origin_tip || '';
            hasMoreCommits = resp.has_more || false;
        } catch (e) {
            console.error('Failed to load task commits:', e);
            commits = [];
            originTip = '';
            hasMoreCommits = false;
        } finally {
            loadingCommits = false;
        }
    }

    async function loadMoreCommits() {
        if (loadingMoreCommits || !hasMoreCommits) return;
        loadingMoreCommits = true;
        try {
            const resp: GitLogResponse = await getTaskGitLog(projectId, taskId, { limit: COMMIT_BATCH_SIZE, offset: commitOffset });
            commits = [...commits, ...resp.commits];
            commitOffset += resp.commits.length;
            hasMoreCommits = resp.has_more || false;
        } catch (e) {
            console.error('Failed to load more task commits:', e);
        } finally {
            loadingMoreCommits = false;
        }
    }

    async function handleFileSelected(path: string, line?: number, pin: boolean = false) {
        if (!isPathSafe(path)) return;
        // Initialize task worktree if not yet set up (no branch assigned)
        if (!taskBranch) {
            try {
                await initTask(projectId, taskId);
            } catch (e) {
                console.error('[task-explorer] Failed to initialize task:', e);
                return;
            }
        }
        closeCommitDiff();
        openFiles = revealFileTab(openFiles, path, line || 0, p => pinnedFiles.has(p));
        activeFilePath = path;
        if (pin) pinTab(path);
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
                const newIdx = Math.min(idx, next.length - 1);
                activeFilePath = next[newIdx].path;
                onUpdateFile(activeFilePath);
            }
        }
    }

    async function handleCommitSelected(commit: GitCommit, index: number) {
        diffCommit = commit;
        diffCommitIndex = index;
        diffLoading = true;
        diffCommitFiles = [];
        if (isMobileCtx.isMobile) activeTab = 'editor';
        try {
            let baseHash: string;
            if (index > 0) {
                baseHash = commits[index - 1].hash;
            } else if (commit.parents && commit.parents.length > 0) {
                baseHash = commit.parents[0];
            } else {
                baseHash = EMPTY_TREE;
            }
            diffCommitFiles = await getTaskCommitDiffFiles(projectId, taskId, baseHash, commit.hash);
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

    // Consume fs events
    let pendingFsEvent = $state<{ op: 'create' | 'delete'; path: string } | null>(null);
    let lastQueueLen = $state(0);
    $effect(() => {
        const len = fsEventQueue.length;
        if (len === 0) {
            lastQueueLen = 0;
            return;
        }
        if (len > lastQueueLen) {
            pendingFsEvent = fsEventQueue[len - 1];
            lastQueueLen = len;
        }
    });

    // Handle Escape key
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
    function handleLeftDragStart(_e: MouseEvent | TouchEvent, _startX: number) {}
    function handleLeftDragMove(pointerX: number) {
        const body = document.querySelector('.explorer-body');
        if (!body) return;
        const posX = pointerX - body.getBoundingClientRect().left;
        leftWidth = Math.max(MIN_LEFT, Math.min(MAX_LEFT, posX));
    }
    function handleLeftDragEnd() { persistExplorerLayout(); }

    function handleRightDragStart(_e: MouseEvent | TouchEvent, _startX: number) {}
    function handleRightDragMove(pointerX: number) {
        const body = document.querySelector('.explorer-body');
        if (!body) return;
        const posX = body.getBoundingClientRect().right - pointerX;
        rightWidth = Math.max(MIN_RIGHT, Math.min(MAX_RIGHT, posX));
    }
    function handleRightDragEnd() { persistExplorerLayout(); }

    function handleSplitDragStart(_e: MouseEvent | TouchEvent, _startY: number) {}
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
        window.addEventListener('keydown', handleKeydown);
        window.addEventListener('open-file', handleOpenFile);
        window.addEventListener(EDITOR_SAVE_SETTLED, handleSaveSettled);
        void (async () => {
            // Initialize task worktree if not yet set up
            if (!taskBranch) {
                try {
                    await initTask(projectId, taskId);
                } catch (e) {
                    console.error('[task-explorer] Failed to initialize task:', e);
                }
            }
            loadCommits();
            if (openFiles.length > 0) {
                activeTab = 'editor';
            }
        })();
    });

    // open-file dispatch (go-to-definition / search results, and a tool call's file)
    function handleOpenFile(e: Event) {
        const detail = (e as CustomEvent<{ path: string; line?: number; taskId?: string }>).detail;
        if (detail.taskId && detail.taskId !== taskId) return;
        handleFileSelected(toExplorerPath(detail.path, sandboxEnabled ? mountPath : taskPath), detail.line);
    }

    onDestroy(() => {
        window.removeEventListener('keydown', handleKeydown);
        window.removeEventListener('open-file', handleOpenFile);
        window.removeEventListener(EDITOR_SAVE_SETTLED, handleSaveSettled);
        if (saveBackstop) { clearTimeout(saveBackstop); saveBackstop = null; }
    });
</script>

<div class="explorer-view" class:fullscreen={editorFullscreen && activeFilePath} role="dialog" aria-label={t('explorer.taskView.aria')}>
    <!-- Header bar -->
    <div class="explorer-header">
        <button class="close-btn" onclick={() => { if (showChanges) { onChangesOpen(false); } else { editorFullscreen = false; closeCommitDiff(); onClose(); } }} aria-label={t('explorer.taskView.closeAria')} title={t('explorer.shared.closeEscape')}>
            <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                <path d="M4 4l8 8M12 4l-8 8" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
            </svg>
        </button>
        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" class="explorer-view-icon">
            <path d="M3 7v10a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2h-6l-2-2H5a2 2 0 00-2 2z"/>
        </svg>
        <span class="project-path" title={toHomeRelative(taskPath)}>{toHomeRelative(taskPath)}</span>

        <!-- Branch badge -->
        <span class="explorer-branch-badge" style="background: {statusColor}22; color: {statusColor}; border-color: {statusColor}44" title="{selectedBranch}">
            <svg width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                <circle cx="6" cy="6" r="2.5"/>
                <circle cx="10" cy="10" r="2.5"/>
                <line x1="7.8" y1="7.8" x2="8.2" y2="8.2"/>
                <line x1="6" y1="3.5" x2="6" y2="2"/>
                <line x1="10" y1="12.5" x2="10" y2="14"/>
            </svg>
            <span class="branch-badge-label">{selectedBranch}</span>
        </span>
        <div class="explorer-header-spacer"></div>
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
        <!-- Review Changes panel -->
        {#if showChanges}
        <div class="explorer-changes-column">
            <GitStatusPanel
                projectId={projectId}
                projectPath={taskPath}
                onClose={() => onChangesOpen(false)}
                isMobile={isMobileCtx.isMobile}
                onEditFile={handleEditFromChanges}
            />
        </div>
        {:else}
            <!-- Column 1: File browser + Git graph -->
            <div class="explorer-left-column" class:hidden={isMobileCtx.isMobile && activeTab !== 'files'} style="width: {leftWidth}px">
                <!-- The task's file listing waits on the project's sandbox image; this is where
                     that build or pull becomes visible. -->
                <SandboxImageStatusBanner />
                {#if showFind}
                    <FileSearchPanel
                        projectId={projectId}
                        taskId={taskId}
                        onFileSelected={handleFileSelected}
                        onClose={() => showFind = false}
                    />
                {:else}
                    <div class="explorer-split-container">
                        <!-- Top: File browser -->
                        <div class="explorer-file-browser" class:mobile={isMobileCtx.isMobile} style:height={isMobileCtx.isMobile ? undefined : splitRatio * 100 + '%'}>
                            <TaskFileBrowser
                                projectId={projectId}
                                projectName={projectName}
                                taskId={taskId}
                                taskPath={taskPath}
                                sandboxEnabled={sandboxEnabled}
                                unsavedFiles={[...unsavedFiles]}
                                pendingFsEvent={pendingFsEvent}
                                onFileSelected={handleFileSelected}
                                onPathChange={(p) => currentFolderPath = p}
                                onToggleFind={toggleFind}
                            />
                        </div>

                        <!-- Drag handle -->
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
                        projectPath={taskPath}
                        projectId={projectId}
                        taskId={taskId}
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
                            projectPath={taskPath}
                            taskId={taskId}
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

            <!-- Resize handle: right column -->
            {#if !isMobileCtx.isMobile && symbols.length > 0 && !diffCommit}
            <ResizeHandle
                ariaLabel={t('explorer.shared.resizeRightAria')}
                onDragStart={handleRightDragStart}
                onDragMove={handleRightDragMove}
                onDragEnd={handleRightDragEnd}
            />
            {/if}

            <!-- Column 3: Symbol outline -->
            <div class="explorer-right-column" class:hidden={(symbols.length === 0 && !isMobileCtx.isMobile) || (isMobileCtx.isMobile && activeTab !== 'symbols') || !!diffCommit} style="width: {rightWidth}px">
                <BranchSymbols
                    symbols={symbols}
                    filePath={activeFilePath}
                    onSymbolClick={(line: number) => {
                        if (isMobileCtx.isMobile) {
                            activeTab = 'editor';
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
        line-height: var(--line-height-clipped);
        align-self: center;
        min-width: 0;
        overflow: hidden;
        display: -webkit-box;
        -webkit-box-orient: vertical;
        -webkit-line-clamp: 2;
        line-clamp: 2;
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

    .explorer-branch-badge {
        display: inline-flex;
        align-items: center;
        gap: 4px;
        padding: 4px 10px;
        border-radius: var(--radius-md);
        border: 1px solid;
        font-size: 0.8125rem;
        font-family: var(--font-mono);
        font-weight: 500;
        max-width: 160px;
        min-width: 0;
        overflow: hidden;
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
