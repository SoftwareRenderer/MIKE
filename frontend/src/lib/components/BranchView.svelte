<script lang="ts">
    import { onMount, onDestroy } from 'svelte';
    import { getGitLogFull, getGitBranches, checkoutBranch, getCommitDiff } from '$lib/api';
    import { toHomeRelative } from '$lib/utils/path';
    import { useIsMobile } from '$lib/composables/use-is-mobile.svelte';
    import type { GitCommit, GitLogResponse } from '$lib/types';
    import BranchFileBrowser from './BranchFileBrowser.svelte';
    import BranchGitGraph from './BranchGitGraph.svelte';
    import BranchEditor from './BranchEditor.svelte';
    import BranchSymbols from './BranchSymbols.svelte';
    import CommitDiffOverlay from './CommitDiffOverlay.svelte';
    import ResizeHandle from './ResizeHandle.svelte';
    import { loadExplorerLayout, saveExplorerLayout, loadUnsavedFiles, saveUnsavedFiles } from '$lib/storage';

    let { projectId, projectPath, currentBranch, initialFile, sandboxEnabled, onClose, fsEventQueue, onOpenTerminal, onUpdateFile }: {
        projectId: string;
        projectPath: string;
        currentBranch: string;
        initialFile: string;
        sandboxEnabled: boolean;
        onClose: () => void;
        fsEventQueue: Array<{ op: 'create' | 'delete'; path: string }>;
        onOpenTerminal: () => void;
        onUpdateFile: (path: string | null) => void;
    } = $props();

    let commits = $state<GitCommit[]>([]);
    let originTip = $state(''); // commit hash where origin/<branch> points
    let branches = $state<string[]>([]);
    let selectedBranch = $state(currentBranch);
    let currentBranchRef = $derived(currentBranch);
    let loadingCommits = $state(true);
    let loadingBranches = $state(true);
    let checkoutLoading = $state(false);

    // Commit diff overlay state
    let diffCommit = $state<GitCommit | null>(null);
    let diffCommitIndex = $state(-1);
    let diffUnified = $state('');
    let diffLoading = $state(false);
    const EMPTY_TREE = '4b825dc642cb6eb9a060e54bf899d69f826635b6';

    // Sync selectedBranch when parent updates currentBranch via WebSocket
    $effect(() => {
        if (currentBranch && currentBranch !== selectedBranch) {
            selectedBranch = currentBranch;
        }
    });

    // Branch selector dropdown
    let showDropdown = $state(false);
    let dropdownEl: HTMLDivElement | null = null;

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

    // Selected file state
    let initialSafeFile = isPathSafe(initialFile) ? initialFile : '';
    let selectedFilePath = $state(initialSafeFile);
    // O(1) lookup — replaces Array.includes/filter in handleHasChanges and close handler
    let unsavedFiles = $state(new Set<string>());

    // Load unsaved files from IndexedDB on init
    loadUnsavedFiles(projectId).then(paths => { unsavedFiles = new Set(paths); }).catch(() => {});

    // Sync selected file from URL on mount and when initialFile updates
    $effect(() => {
        const file = initialFile;
        if (file && isPathSafe(file) && !selectedFilePath) {
            selectedFilePath = file;
        }
    });
    let editorFocused = $state(false);

    // Persist unsaved files to IndexedDB (convert Set → array for stable JSON format)
    $effect(() => {
        unsavedFiles;
        saveUnsavedFiles(projectId, [...unsavedFiles]).catch(() => {});
    });

    // Mobile layout
    const { isMobile } = useIsMobile();
    let activeTab = $state<'files' | 'editor' | 'symbols'>('files');
    let editorFullscreen = $state(false);
    let diffFullscreen = $state(false);

    function toggleEditorFullscreen() {
        editorFullscreen = !editorFullscreen;
    }

    function toggleDiffFullscreen() {
        diffFullscreen = !diffFullscreen;
    }
    let currentFolderPath = $state(initialSafeFile ? initialSafeFile.substring(0, initialSafeFile.lastIndexOf('/')) : (sandboxEnabled ? mountPath : projectPath));
    let projectPathRef = $derived(projectPath);

    // Symbol outline state
    let symbols = $state<Array<{ name: string; kind: string; line: number; indent: number }>>([]);

    // Clear symbols when no file is selected
    $effect(() => {
        if (!selectedFilePath) {
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

    async function loadCommits(branch?: string) {
        loadingCommits = true;
        commitOffset = 0;
        try {
            const resp: GitLogResponse = await getGitLogFull(projectId, { branch, limit: COMMIT_BATCH_SIZE, offset: 0 });
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

    async function loadMoreCommits() {
        if (loadingMoreCommits || !hasMoreCommits) return;
        loadingMoreCommits = true;
        try {
            const resp: GitLogResponse = await getGitLogFull(projectId, { limit: COMMIT_BATCH_SIZE, offset: commitOffset });
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
            await loadCommits(branch);
        } catch (e) {
            console.error('Failed to checkout branch:', e);
        } finally {
            checkoutLoading = false;
        }
    }

    function handleFileSelected(path: string) {
        if (!isPathSafe(path)) return;
        closeCommitDiff();
        selectedFilePath = path;
        onUpdateFile(path);
        if (isMobile) activeTab = 'editor';
    }

    // Commit diff overlay handlers
    async function handleCommitSelected(commit: GitCommit, index: number) {
        diffCommit = commit;
        diffCommitIndex = index;
        diffLoading = true;
        diffUnified = '';
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
            diffUnified = await getCommitDiff(projectId, baseHash, commit.hash);
        } catch (e) {
            console.error('Failed to fetch commit diff:', e);
            diffUnified = '';
        } finally {
            diffLoading = false;
        }
    }

    function closeCommitDiff() {
        diffCommit = null;
        diffCommitIndex = -1;
        diffUnified = '';
        diffFullscreen = false;
    }

    function handleDiffNavigate(direction: 'prev' | 'next') {
        const newIndex = diffCommitIndex + (direction === 'next' ? -1 : 1);
        if (newIndex >= 0 && newIndex < commits.length) {
            handleCommitSelected(commits[newIndex], newIndex);
        }
    }

    function handleSymbolsUpdate(newSymbols: Array<{ name: string; kind: string; line: number; indent: number }>) {
        symbols = newSymbols;
    }

    function handleEditorFocused(focused: boolean) {
        editorFocused = focused;
    }

    function handleHasChanges(hasChanges: boolean) {
        if (!selectedFilePath) return;
        const currentlyUnsaved = unsavedFiles.has(selectedFilePath);
        if (hasChanges && !currentlyUnsaved) {
            const next = new Set(unsavedFiles);
            next.add(selectedFilePath);
            unsavedFiles = next;
        } else if (!hasChanges && currentlyUnsaved) {
            const next = new Set(unsavedFiles);
            next.delete(selectedFilePath);
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

    // Handle Escape key — only close if editor is not focused
    function handleKeydown(e: KeyboardEvent) {
        if (e.key === 'Escape' && !editorFocused) {
            onClose();
        }
    }

    // Resize handlers
    function handleLeftDragStart(_e: MouseEvent | TouchEvent, startX: number) {
        const body = document.querySelector('.branch-body');
        if (!body) return;
        leftDragStartX = startX;
        leftDragBodyLeft = body.getBoundingClientRect().left;
    }
    let leftDragStartX = 0;
    let leftDragBodyLeft = 0;
    function handleLeftDragMove(pointerX: number) {
        const body = document.querySelector('.branch-body');
        if (!body) return;
        const posX = pointerX - body.getBoundingClientRect().left;
        leftWidth = Math.max(MIN_LEFT, Math.min(MAX_LEFT, posX));
    }
    function handleLeftDragEnd() { persistExplorerLayout(); }

    function handleRightDragStart(_e: MouseEvent | TouchEvent, startX: number) {
        const body = document.querySelector('.branch-body');
        if (!body) return;
        rightDragStartX = startX;
        rightDragBodyRight = body.getBoundingClientRect().right;
    }
    let rightDragStartX = 0;
    let rightDragBodyRight = 0;
    function handleRightDragMove(pointerX: number) {
        const body = document.querySelector('.branch-body');
        if (!body) return;
        const posX = body.getBoundingClientRect().right - pointerX;
        rightWidth = Math.max(MIN_RIGHT, Math.min(MAX_RIGHT, posX));
    }
    function handleRightDragEnd() { persistExplorerLayout(); }

    function handleSplitDragStart(_e: MouseEvent | TouchEvent, startY: number) {
        const container = document.querySelector('.branch-split-container');
        if (!container) return;
        splitDragStartY = startY;
        splitDragContainerTop = container.getBoundingClientRect().top;
        splitDragContainerHeight = container.getBoundingClientRect().height;
    }
    let splitDragStartY = 0;
    let splitDragContainerTop = 0;
    let splitDragContainerHeight = 0;
    function handleSplitDragMove(pointerY: number) {
        const container = document.querySelector('.branch-split-container');
        if (!container) return;
        const rect = container.getBoundingClientRect();
        const posY = pointerY - rect.top;
        splitRatio = Math.max(0.2, Math.min(0.8, posY / rect.height));
    }
    function handleSplitDragEnd() { persistExplorerLayout(); }

    $effect(() => {
        const mobile = isMobile;
        if (mobile) {
            activeTab = 'files';
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
        if (selectedFilePath) {
            activeTab = 'editor';
        }
        window.addEventListener('keydown', handleKeydown);
    });

    onDestroy(() => {
        window.removeEventListener('keydown', handleKeydown);
    });
</script>

<div class="branch-view" class:fullscreen={(editorFullscreen && selectedFilePath) || (diffFullscreen && diffCommit)} role="dialog" aria-label="Branch view">
    <!-- Header bar (replaces top-bar when explorer is open) -->
    <div class="branch-header">
        <button class="close-btn" onclick={() => { editorFullscreen = false; diffFullscreen = false; onClose(); }} aria-label="Close branch view" title="Close (Escape)">
            <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                <path d="M4 4l8 8M12 4l-8 8" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
            </svg>
        </button>
        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" class="branch-view-icon">
            <path d="M3 7v10a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2h-6l-2-2H5a2 2 0 00-2 2z"/>
        </svg>
        <span class="project-path">{toHomeRelative(projectPath)}</span>

        <!-- Branch selector dropdown -->
        <div class="branch-selector" class:open={showDropdown} bind:this={dropdownEl}>
            <button
                class="branch-dropdown-btn"
                onclick={toggleDropdown}
                title="Switch branch"
                disabled={checkoutLoading}
            >
                <svg width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                    <circle cx="6" cy="6" r="2.5"/>
                    <circle cx="10" cy="10" r="2.5"/>
                    <line x1="7.8" y1="7.8" x2="8.2" y2="8.2"/>
                    <line x1="6" y1="3.5" x2="6" y2="2"/>
                    <line x1="10" y1="12.5" x2="10" y2="14"/>
                </svg>
                <span class="branch-name">{selectedBranch}</span>
                <svg width="10" height="10" viewBox="0 0 10 10" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                    <polyline points="2,3.5 5,6.5 8,3.5"/>
                </svg>
            </button>

            {#if showDropdown}
                <div class="branch-dropdown-list" role="listbox">
                    {#if loadingBranches}
                        <div class="branch-dropdown-item loading">Loading...</div>
                    {:else}
                        {#each branches as branch}
                            <button
                                class="branch-dropdown-item"
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
                                    <span class="branch-check-empty"></span>
                                {/if}
                                <span>{branch}</span>
                            </button>
                        {/each}
                        {#if branches.length === 0}
                            <div class="branch-dropdown-item empty">No branches</div>
                        {/if}
                    {/if}
                </div>
            {/if}
        </div>
        <div class="branch-header-spacer"></div>
        <button class="icon-btn terminal-btn" onclick={onOpenTerminal} aria-label="Open terminal" title="Open terminal">
            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                <rect x="1" y="2" width="14" height="12" rx="1.5" stroke="currentColor" stroke-width="1.3"/>
                <path d="M4 6l2 2-2 2M7 10h3" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
            </svg>
        </button>
    </div>

    <!-- Mobile Tab Bar -->
    {#if isMobile && !diffCommit && !editorFullscreen && !diffFullscreen}
        <div class="mobile-tab-bar">
            <button
                class="tab-btn {activeTab === 'files' ? 'active' : ''}"
                onclick={() => activeTab = 'files'}
                title="File browser"
            >
                <svg width="12" height="12" viewBox="0 0 16 16" fill="none"><path d="M2 2h4l2 2v8a1 1 0 0 1-1 1H2a1 1 0 0 1-1-1V3a1 1 0 0 1 1-1z" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round"/><path d="M6 2h4l2 2" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round"/></svg>
                <span class="tab-label">{getFolderName(currentFolderPath)}</span>
            </button>
            <button
                class="tab-btn {activeTab === 'editor' ? 'active' : ''}"
                onclick={() => activeTab = 'editor'}
                title="Editor"
            >
                <svg width="12" height="12" viewBox="0 0 16 16" fill="none"><path d="M3 2h6l4 4v8a1 1 0 0 1-1 1H3a1 1 0 0 1-1-1V3a1 1 0 0 1 1-1z" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round"/><path d="M9 2v4h4" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round"/></svg>
                Editor
            </button>
            <button
                class="tab-btn {activeTab === 'symbols' ? 'active' : ''}"
                onclick={() => activeTab = 'symbols'}
                title="Symbols"
            >
                <svg width="12" height="12" viewBox="0 0 16 16" fill="none"><path d="M3 4h10M3 8h10M3 12h6" stroke="currentColor" stroke-width="1.2" stroke-linecap="round"/></svg>
                Symbols
            </button>
        </div>
    {/if}

    <!-- 3-column layout -->
    <div class="branch-body">
        <!-- Left column: file browser + git graph -->
        <div class="branch-left-column" class:hidden={isMobile && (activeTab !== 'files' || diffCommit)} style="width: {leftWidth}px">
            <div class="branch-split-container">
                <!-- Top: File browser -->
                <div class="branch-file-browser" class:mobile={isMobile} style:height={isMobile ? undefined : splitRatio * 100 + '%'}>
                    <BranchFileBrowser
                        projectId={projectId}
                        projectPath={projectPath}
                        initialPath={currentFolderPath}
                        sandboxEnabled={sandboxEnabled}
                        unsavedFiles={[...unsavedFiles]}
                        pendingFsEvent={pendingFsEvent}
                        onFileSelected={handleFileSelected}
                        onPathChange={(p) => currentFolderPath = p}
                    />
                </div>

                <!-- Drag handle (file browser / git graph split) -->
                {#if !isMobile}
                <ResizeHandle
                    axis="horizontal"
                    ariaLabel="Resize file browser and git graph panels"
                    onDragStart={handleSplitDragStart}
                    onDragMove={handleSplitDragMove}
                    onDragEnd={handleSplitDragEnd}
                />
                {/if}

                <!-- Bottom: Git graph -->
                <div class="branch-git-graph" class:mobile={isMobile} style:height={isMobile ? undefined : (1 - splitRatio) * 100 + '%'}>
                    <BranchGitGraph
                        projectId={projectId}
                        currentBranch={selectedBranch}
                        commits={commits}
                        originTip={originTip}
                        loading={loadingCommits}
                        hasMore={hasMoreCommits}
                        onCommitSelected={handleCommitSelected}
                        onLoadMore={loadMoreCommits}
                        selectedCommitHash={diffCommit?.hash}
                    />
                </div>
            </div>
        </div>

        <!-- Resize handle: left column -->
        <ResizeHandle
            ariaLabel="Resize left panel"
            onDragStart={handleLeftDragStart}
            onDragMove={handleLeftDragMove}
            onDragEnd={handleLeftDragEnd}
        />

        {#if diffCommit}
            <!-- Commit diff overlay (replaces editor + symbols columns) -->
            <div class="branch-diff-overlay">
                <CommitDiffOverlay
                    commit={diffCommit}
                    diff={diffUnified}
                    loading={diffLoading}
                    projectPath={projectPath}
                    onClose={closeCommitDiff}
                    onNavigate={handleDiffNavigate}
                    onFileSelected={handleFileSelected}
                    hasPrev={diffCommitIndex < commits.length - 1}
                    hasNext={diffCommitIndex > 0}
                    onToggleFullscreen={toggleDiffFullscreen}
                    mobileFullscreen={diffFullscreen}
                    isMobile={isMobile}
                />
            </div>
        {:else}
            <!-- Center column: File editor -->
            <div class="branch-center-column" class:hidden={isMobile && activeTab !== 'editor'}>
                <BranchEditor
                    projectId={projectId}
                    filePath={selectedFilePath}
                    onFocused={handleEditorFocused}
                    onSymbolsUpdate={handleSymbolsUpdate}
                    onHasChanges={handleHasChanges}
                    onClose={() => { const path = selectedFilePath; selectedFilePath = ''; const next = new Set(unsavedFiles); next.delete(path); unsavedFiles = next; onUpdateFile(null); if (isMobile) activeTab = 'files'; editorFullscreen = false; }}
                    onToggleFullscreen={toggleEditorFullscreen}
                    mobileFullscreen={editorFullscreen}
                    isMobile={isMobile}
                />
            </div>

            <!-- Resize handle: right column (only when symbols exist) -->
            {#if symbols.length > 0}
            <ResizeHandle
                ariaLabel="Resize right panel"
                onDragStart={handleRightDragStart}
                onDragMove={handleRightDragMove}
                onDragEnd={handleRightDragEnd}
            />
            {/if}

            <!-- Right column: Symbol outline (hidden when no symbols, always visible on mobile via tab) -->
            <div class="branch-right-column" class:hidden={(symbols.length === 0 && !isMobile) || (isMobile && activeTab !== 'symbols')} style="width: {rightWidth}px">
                <BranchSymbols
                    symbols={symbols}
                    onSymbolClick={(line: number) => {
                        window.dispatchEvent(new CustomEvent('jump-to-line', { detail: { line } }));
                    }}
                />
            </div>
        {/if}
    </div>
</div>

<style>
    .branch-view {
        display: flex;
        flex-direction: column;
        height: 100%;
        background: var(--color-surface-0);
        overflow: hidden;
    }

    .branch-header {
        display: flex;
        align-items: center;
        gap: 8px;
        padding: 6px 12px;
        border-bottom: 1px solid var(--color-border);
        background: var(--color-surface-1);
        flex-shrink: 0;
    }

    /* Fullscreen mode: hide header and tab bar, expand to cover sidebar */
    .branch-view.fullscreen {
        position: fixed;
        top: 0;
        left: 0;
        right: 0;
        bottom: 0;
        z-index: 1000;
        margin: 0;
        border-radius: 0;
    }
    .branch-view.fullscreen .branch-header {
        display: none;
    }
    .branch-view.fullscreen .mobile-tab-bar {
        display: none !important;
    }
    .branch-view.fullscreen .branch-left-column {
        display: none !important;
    }
    .branch-view.fullscreen .branch-body {
        height: 100%;
    }

    .branch-view-icon {
        color: var(--color-text-secondary);
        flex-shrink: 0;
    }

    .project-path {
        font-size: 0.8125rem;
        color: var(--color-text-primary);
        line-height: 1;
        align-self: center;
    }

    .branch-header-spacer {
        flex: 1;
    }

    @import '$lib/styles/shared.css';

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
        background: var(--color-accent);
        color: white;
    }

    /* Branch selector */
    .branch-selector {
        position: relative;
    }

    .branch-dropdown-btn {
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
    }
    .branch-dropdown-btn:hover {
        background: var(--color-surface-4);
        color: var(--color-text-primary);
        opacity: 1;
    }
    .branch-dropdown-btn:disabled {
        opacity: 0.5;
        cursor: not-allowed;
    }

    .branch-name {
        max-width: 120px;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }

    .branch-dropdown-list {
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

    .branch-dropdown-item {
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
    .branch-dropdown-item:hover {
        background: var(--color-surface-3);
        opacity: 1;
    }
    .branch-dropdown-item.active {
        color: var(--color-status-purple);
    }
    .branch-dropdown-item.loading,
    .branch-dropdown-item.empty {
        color: var(--color-text-tertiary);
        cursor: default;
    }
    .branch-check-empty {
        display: inline-block;
        width: 10px;
    }

    .branch-body {
        display: flex;
        flex: 1;
        overflow: hidden;
    }

    .branch-left-column {
        flex-shrink: 0;
        display: flex;
        flex-direction: column;
        border-right: 1px solid var(--color-border);
        background: var(--color-surface-1);
        overflow: hidden;
    }

    .branch-split-container {
        flex: 1;
        display: flex;
        flex-direction: column;
        overflow: hidden;
        position: relative;
    }

    .branch-file-browser {
        overflow: hidden;
        display: flex;
        flex-direction: column;
    }

    .branch-git-graph {
        overflow: hidden;
        display: flex;
        flex-direction: column;
    }

    .branch-center-column {
        flex: 1;
        min-width: 0;
        display: flex;
        flex-direction: column;
        overflow: hidden;
    }

    .branch-right-column {
        flex-shrink: 0;
        border-left: 1px solid var(--color-border);
        background: var(--color-surface-1);
        overflow: hidden;
        display: flex;
        flex-direction: column;
    }
    .branch-right-column.hidden {
        display: none;
    }

    .branch-diff-overlay {
        flex: 1;
        display: flex;
        overflow: hidden;
        border-left: 1px solid var(--color-border);
        background: var(--color-surface-1);
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
        .branch-left-column {
            width: 100% !important;
            border-right: none;
            flex: 1 !important;
            min-height: 0;
        }
        .branch-file-browser.mobile {
            flex: 0 0 75% !important;
            min-height: 100px;
        }
        .branch-git-graph.mobile {
            flex: 1;
            min-height: 0;
        }
        .branch-center-column {
            width: 100% !important;
            flex: 1 !important;
            min-height: 0;
        }
        .branch-right-column {
            width: 100% !important;
            border-left: none;
        }
        .branch-left-column.hidden,
        .branch-center-column.hidden,
        .branch-right-column.hidden {
            display: none;
        }
        .branch-body {
            flex-direction: column;
            min-height: 0;
        }
        .branch-diff-overlay {
            width: 100% !important;
            border-left: none;
            flex: 1 !important;
            min-height: 0;
        }
    }
</style>
