<script lang="ts">
    import { onMount, onDestroy } from 'svelte';
    import { getGitLogFull, getGitBranches, checkoutBranch } from '$lib/api';
    import type { GitCommit } from '$lib/types';
    import BranchFileBrowser from './BranchFileBrowser.svelte';
    import BranchGitGraph from './BranchGitGraph.svelte';
    import BranchEditor from './BranchEditor.svelte';
    import BranchSymbols from './BranchSymbols.svelte';

    let { projectId, projectPath, currentBranch, onClose, fsEventQueue }: {
        projectId: string;
        projectPath: string;
        currentBranch: string;
        onClose: () => void;
        fsEventQueue: Array<{ op: 'create' | 'delete'; path: string }>;
    } = $props();

    let commits = $state<GitCommit[]>([]);
    let branches = $state<string[]>([]);
    let selectedBranch = $state(currentBranch);
    let loadingCommits = $state(true);
    let loadingBranches = $state(true);
    let checkoutLoading = $state(false);

    // Selected file state
    let selectedFilePath = $state('');
    let unsavedFiles = $state<string[]>([]);
    let editorFocused = $state(false);

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
        selectedFilePath = path;
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

    onMount(() => {
        loadCommits();
        loadBranches();
        window.addEventListener('keydown', handleKeydown);
        document.addEventListener('mousemove', handleMouseMove);
        document.addEventListener('mouseup', handleMouseUp);
        document.addEventListener('selectstart', handleSelectStart);
    });

    onDestroy(() => {
        window.removeEventListener('keydown', handleKeydown);
        document.removeEventListener('mousemove', handleMouseMove);
        document.removeEventListener('mouseup', handleMouseUp);
        document.removeEventListener('selectstart', handleSelectStart);
    });
</script>

<div class="branch-view" role="dialog" aria-label="Branch view">
    <!-- Header bar -->
    <div class="branch-header">
        <div class="branch-header-left">
            <span class="branch-view-title">Explorer</span>
        </div>
        <button class="close-btn" onclick={onClose} aria-label="Close branch view" title="Close (Escape)">
            <svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" aria-hidden="true">
                <line x1="3" y1="3" x2="13" y2="13"/>
                <line x1="13" y1="3" x2="3" y2="13"/>
            </svg>
        </button>
    </div>

    <!-- 3-column layout -->
    <div class="branch-body">
        <!-- Left column: file browser + git graph -->
        <div class="branch-left-column" style="width: {leftWidth}px">
            <div class="branch-split-container">
                <!-- Top: File browser -->
                <div class="branch-file-browser" style="height: {splitRatio * 100}%">
                    <BranchFileBrowser
                        projectPath={projectPath}
                        unsavedFiles={unsavedFiles}
                        pendingFsEvent={pendingFsEvent}
                        onFileSelected={handleFileSelected}
                    />
                </div>

                <!-- Drag handle (vertical split) -->
                <div
                    class="branch-split-handle"
                    class:dragging={activeResize === 'split'}
                    onmousedown={(e) => handleResizeStart('split', e)}
                    role="separator"
                    aria-label="Resize panels"
                    tabindex="0"
                ></div>

                <!-- Bottom: Git graph -->
                <div class="branch-git-graph" style="height: {(1 - splitRatio) * 100}%">
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
        <div class="branch-center-column">
            <BranchEditor
                filePath={selectedFilePath}
                onFocused={handleEditorFocused}
                onSymbolsUpdate={handleSymbolsUpdate}
                onHasChanges={handleHasChanges}
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
        <div class="branch-right-column" style="width: {rightWidth}px">
            <BranchSymbols
                symbols={symbols}
                onSymbolClick={(line: number) => {
                    const editorEl = document.querySelector('.branch-editor');
                    if (editorEl) {
                        editorEl.dispatchEvent(new CustomEvent('jump-to-line', { detail: { line } }));
                    }
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
        justify-content: space-between;
        padding: 6px 12px;
        border-bottom: 1px solid var(--color-border);
        background: var(--color-surface-1);
        flex-shrink: 0;
    }

    .branch-header-left {
        display: flex;
        align-items: center;
        gap: 8px;
    }

    .branch-view-title {
        font-size: 0.8125rem;
        font-weight: 600;
        color: var(--color-text-secondary);
        text-transform: uppercase;
        letter-spacing: 0.05em;
    }

    .close-btn {
        padding: 4px 8px;
        border-radius: var(--radius-sm);
        background: transparent;
        color: var(--color-text-secondary);
        display: flex;
        align-items: center;
        justify-content: center;
    }
    .close-btn:hover {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
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
</style>
