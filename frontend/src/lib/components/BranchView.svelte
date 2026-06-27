<script lang="ts">
    import { onMount, onDestroy } from 'svelte';
    import { getGitLogFull, getGitBranches, checkoutBranch } from '$lib/api';
    import { getHomeRelativePath } from '$lib/utils/path';
    import type { GitCommit } from '$lib/types';
    import BranchFileBrowser from './BranchFileBrowser.svelte';
    import BranchGitGraph from './BranchGitGraph.svelte';
    import BranchEditor from './BranchEditor.svelte';
    import BranchSymbols from './BranchSymbols.svelte';

    let { projectId, projectPath, currentBranch, initialFile, onClose, fsEventQueue, onOpenTerminal, onUpdateFile }: {
        projectId: string;
        projectPath: string;
        currentBranch: string;
        initialFile: string;
        onClose: () => void;
        fsEventQueue: Array<{ op: 'create' | 'delete'; path: string }>;
        onOpenTerminal: () => void;
        onUpdateFile: (path: string | null) => void;
    } = $props();

    let commits = $state<GitCommit[]>([]);
    let branches = $state<string[]>([]);
    let selectedBranch = $state(currentBranch);
    let loadingCommits = $state(true);
    let loadingBranches = $state(true);
    let checkoutLoading = $state(false);

    // Validate file path stays within project root
    function isPathSafe(path: string): boolean {
        return path && path.startsWith(projectPath);
    }

    // Selected file state
    let initialSafeFile = isPathSafe(initialFile) ? initialFile : '';
    let selectedFilePath = $state(initialSafeFile);
    let unsavedFiles = $state<string[]>([]);
    let editorFocused = $state(false);

    // Mobile layout
    let isMobile = $state(false);
    let activeTab = $state<'files' | 'editor' | 'symbols'>('files');
    let currentFolderPath = $state(initialSafeFile ? initialSafeFile.substring(0, initialSafeFile.lastIndexOf('/')) : projectPath);

    // Symbol outline state
    let symbols = $state<Array<{ name: string; kind: string; line: number; indent: number }>>([]);

    // Column widths (resizable)
    let leftWidth = $state(250);
    let rightWidth = $state(180);
    const MIN_LEFT = 180;
    const MAX_LEFT = 500;
    const MIN_RIGHT = 120;
    const MAX_RIGHT = 400;

    // Resize state for left column split (file browser / git graph)
    let splitRatio = $state(0.5);

    // Active resize handle: 'left', 'right', 'split', or null
    let activeResize = $state<'left' | 'right' | 'split' | null>(null);

    async function loadCommits(branch?: string) {
        loadingCommits = true;
        try {
            commits = await getGitLogFull(projectId, branch);
        } catch (e) {
            console.error('Failed to load commits:', e);
            commits = [];
        } finally {
            loadingCommits = false;
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
        selectedFilePath = path;
        onUpdateFile(path);
        if (isMobile) activeTab = 'editor';
    }

    function handleSymbolsUpdate(newSymbols: Array<{ name: string; kind: string; line: number; indent: number }>) {
        symbols = newSymbols;
    }

    function handleEditorFocused(focused: boolean) {
        editorFocused = focused;
    }

    function handleHasChanges(hasChanges: boolean) {
        if (!selectedFilePath) return;
        const currentlyUnsaved = unsavedFiles.includes(selectedFilePath);
        if (hasChanges && !currentlyUnsaved) {
            unsavedFiles = [...unsavedFiles, selectedFilePath];
        } else if (!hasChanges && currentlyUnsaved) {
            unsavedFiles = unsavedFiles.filter(p => p !== selectedFilePath);
        }
    }

    // Consume fs events from the shared queue
    let pendingFsEvent = $state<{ op: 'create' | 'delete'; path: string } | null>(null);
    let lastQueueLen = $state(0);
    $effect(() => {
        const len = fsEventQueue.length;
        if (len > lastQueueLen && len > 0) {
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
    function handleResizeStart(type: 'left' | 'right' | 'split', e: MouseEvent) {
        activeResize = type;
        e.preventDefault();
    }

    function handleMouseMove(e: MouseEvent) {
        if (!activeResize) return;
        const body = document.querySelector('.branch-body');
        if (!body) return;
        const rect = body.getBoundingClientRect();

        if (activeResize === 'left') {
            const x = e.clientX - rect.left;
            leftWidth = Math.max(MIN_LEFT, Math.min(MAX_LEFT, x));
        } else if (activeResize === 'right') {
            const x = rect.right - e.clientX;
            rightWidth = Math.max(MIN_RIGHT, Math.min(MAX_RIGHT, x));
        } else if (activeResize === 'split') {
            const splitContainer = document.querySelector('.branch-split-container');
            if (!splitContainer) return;
            const splitRect = splitContainer.getBoundingClientRect();
            const y = e.clientY - splitRect.top;
            splitRatio = Math.max(0.2, Math.min(0.8, y / splitRect.height));
        }
    }

    function handleMouseUp() {
        activeResize = null;
    }

    // Prevent text selection during resize
    function handleSelectStart(e: Event) {
        if (activeResize) e.preventDefault();
    }

    function checkMobile() {
        const wasMobile = isMobile;
        isMobile = window.matchMedia('(max-width: 768px)').matches;
        if (isMobile) {
            activeTab = 'files';
            if (!wasMobile) splitRatio = 0.75;
        }
    }

    function getFolderName(path: string): string {
        const parts = path.split('/');
        return parts[parts.length - 1] || '/';
    }

    onMount(() => {
        loadCommits();
        loadBranches();
        checkMobile();
        window.addEventListener('resize', checkMobile);
        window.addEventListener('keydown', handleKeydown);
        document.addEventListener('mousemove', handleMouseMove);
        document.addEventListener('mouseup', handleMouseUp);
        document.addEventListener('selectstart', handleSelectStart);
    });

    onDestroy(() => {
        window.removeEventListener('resize', checkMobile);
        window.removeEventListener('keydown', handleKeydown);
        document.removeEventListener('mousemove', handleMouseMove);
        document.removeEventListener('mouseup', handleMouseUp);
        document.removeEventListener('selectstart', handleSelectStart);
    });
</script>

<div class="branch-view" role="dialog" aria-label="Branch view">
    <!-- Header bar (replaces top-bar when explorer is open) -->
    <div class="branch-header">
        <button class="close-btn" onclick={onClose} aria-label="Close branch view" title="Close (Escape)">
            <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                <path d="M4 4l8 8M12 4l-8 8" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
            </svg>
        </button>
        <span class="branch-view-title">Explorer</span>
        <span class="project-path">{getHomeRelativePath(projectPath)}</span>
        <div class="branch-header-spacer"></div>
        <button class="icon-btn terminal-btn" onclick={onOpenTerminal} aria-label="Open terminal" title="Open terminal">
            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                <rect x="1" y="2" width="14" height="12" rx="1.5" stroke="currentColor" stroke-width="1.3"/>
                <path d="M4 6l2 2-2 2M7 10h3" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
            </svg>
        </button>
    </div>

    <!-- Mobile Tab Bar -->
    {#if isMobile}
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
        <div class="branch-left-column" class:hidden={isMobile && activeTab !== 'files'} style="width: {leftWidth}px">
            <div class="branch-split-container">
                <!-- Top: File browser -->
                <div class="branch-file-browser" class:mobile={isMobile} style:height={isMobile ? undefined : splitRatio * 100 + '%'}>
                    <BranchFileBrowser
                        projectId={projectId}
                        projectPath={projectPath}
                        initialPath={currentFolderPath}
                        unsavedFiles={unsavedFiles}
                        pendingFsEvent={pendingFsEvent}
                        onFileSelected={handleFileSelected}
                        onPathChange={(p) => currentFolderPath = p}
                    />
                </div>

                <!-- Drag handle (vertical split) -->
                {#if !isMobile}
                <div
                    class="branch-split-handle"
                    class:dragging={activeResize === 'split'}
                    onmousedown={(e) => handleResizeStart('split', e)}
                    role="separator"
                    aria-label="Resize panels"
                    tabindex="0"
                ></div>
                {/if}

                <!-- Bottom: Git graph -->
                <div class="branch-git-graph" class:mobile={isMobile} style:height={isMobile ? undefined : (1 - splitRatio) * 100 + '%'}>
                    <BranchGitGraph
                        projectId={projectId}
                        currentBranch={selectedBranch}
                        commits={commits}
                        loading={loadingCommits}
                        branches={branches}
                        loadingBranches={loadingBranches}
                        checkoutLoading={checkoutLoading}
                        onCheckout={handleCheckout}
                        onBranchChange={(b) => { selectedBranch = b; loadCommits(b); }}
                    />
                </div>
            </div>
        </div>

        <!-- Resize handle: left column -->
        <div
            class="branch-resize-handle"
            class:active={activeResize === 'left'}
            onmousedown={(e) => handleResizeStart('left', e)}
            role="separator"
            aria-label="Resize left panel"
            tabindex="0"
        ></div>

        <!-- Center column: File editor -->
        <div class="branch-center-column" class:hidden={isMobile && activeTab !== 'editor'}>
            <BranchEditor
                projectId={projectId}
                filePath={selectedFilePath}
                onFocused={handleEditorFocused}
                onSymbolsUpdate={handleSymbolsUpdate}
                onHasChanges={handleHasChanges}
                onClose={() => { const path = selectedFilePath; selectedFilePath = ''; unsavedFiles = unsavedFiles.filter(p => p !== path); onUpdateFile(null); if (isMobile) activeTab = 'files'; }}
            />
        </div>

        <!-- Resize handle: right column -->
        <div
            class="branch-resize-handle"
            class:active={activeResize === 'right'}
            onmousedown={(e) => handleResizeStart('right', e)}
            role="separator"
            aria-label="Resize right panel"
            tabindex="0"
        ></div>

        <!-- Right column: Symbol outline -->
        <div class="branch-right-column" class:hidden={isMobile && activeTab !== 'symbols'} style="width: {rightWidth}px">
            <BranchSymbols
                symbols={symbols}
                onSymbolClick={(line: number) => {
                    window.dispatchEvent(new CustomEvent('jump-to-line', { detail: { line } }));
                }}
            />
        </div>
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

    .branch-view-title {
        font-size: 0.8125rem;
        font-weight: 600;
        color: var(--color-text-secondary);
        text-transform: uppercase;
        letter-spacing: 0.05em;
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

    .close-btn {
        background: none;
        border: none;
        color: var(--color-text-secondary);
        cursor: pointer;
        padding: 6px;
        border-radius: var(--radius-sm);
        display: flex;
        align-items: center;
        justify-content: center;
        flex-shrink: 0;
    }
    .close-btn:hover {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
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

    /* Vertical split handle (file browser / git graph) */
    .branch-split-handle {
        height: 4px;
        cursor: row-resize;
        background: var(--color-border);
        transition: background 0.15s ease;
        flex-shrink: 0;
        position: relative;
        z-index: 10;
    }
    .branch-split-handle:hover,
    .branch-split-handle.dragging {
        background: var(--color-accent);
    }
    .branch-split-handle::after {
        content: '';
        position: absolute;
        left: 50%;
        top: 50%;
        transform: translate(-50%, -50%);
        width: 24px;
        height: 2px;
        background: var(--color-text-secondary);
        border-radius: 1px;
        opacity: 0.4;
    }

    .branch-git-graph {
        overflow: hidden;
        display: flex;
        flex-direction: column;
    }

    /* Column resize handles */
    .branch-resize-handle {
        width: 4px;
        cursor: col-resize;
        background: var(--color-border);
        transition: background 0.15s ease;
        flex-shrink: 0;
        z-index: 10;
        position: relative;
    }
    .branch-resize-handle:hover,
    .branch-resize-handle.active {
        background: var(--color-accent);
    }
    .branch-resize-handle::after {
        content: '';
        position: absolute;
        left: 50%;
        top: 50%;
        transform: translate(-50%, -50%);
        width: 2px;
        height: 24px;
        background: var(--color-text-secondary);
        border-radius: 1px;
        opacity: 0.4;
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

    .mobile-tab-bar {
        display: none;
    }
    .column.hidden {
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
        .branch-resize-handle {
            display: none;
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
    }
</style>
