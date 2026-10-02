<script lang="ts">
    import type { Snippet } from 'svelte';
    import type { ExplorerCore } from '$lib/services/explorer-core.svelte';
    import type { ExplorerLayout } from '$lib/services/explorer-layout.svelte';
    import BranchGitGraph from './BranchGitGraph.svelte';
    import BranchSymbols from './BranchSymbols.svelte';
    import EditorTabs from './EditorTabs.svelte';
    import EmptyState from './EmptyState.svelte';
    import FileSearchPanel from './FileSearchPanel.svelte';
    import GitStatusPanel from './GitStatusPanel.svelte';
    import ResizeHandle from './ResizeHandle.svelte';
    import SandboxImageStatusBanner from './SandboxImageStatusBanner.svelte';
    import { useLazyComponent } from '$lib/composables/use-lazy-component.svelte';
    import { t } from '$lib/i18n';
    import { baseName, folderName } from '$lib/utils/path';

    /**
     * Everything an explorer screen shows below its header. The file browser is the one piece each
     * screen still writes for itself, so it arrives as a snippet.
     */
    let { core, layout, isMobile, branch, projectId, projectPath, taskId,
        changesOpen, onChangesOpen, folderPath, onReviewUpdated, scrollTops, onScrollTop, browser }: {
        core: ExplorerCore;
        layout: ExplorerLayout;
        isMobile: boolean;
        branch: string;
        projectId: string;
        projectPath: string;
        taskId?: string;
        changesOpen: boolean;
        onChangesOpen: (open: boolean) => void;
        /** What the mobile tab bar labels the listing tab. */
        folderPath: string;
        onReviewUpdated?: () => void;
        scrollTops?: Record<string, number>;
        onScrollTop?: (path: string, top: number) => void;
        browser: Snippet;
    } = $props();

    const commitDiff = useLazyComponent(() => import('./CommitDiffOverlay.svelte'));

    $effect(() => {
        if (core.diffCommit) commitDiff.load();
    });

</script>

{#if isMobile && !changesOpen && !core.editorFullscreen}
    <div class="mobile-tab-bar">
        <button
            class="tab-btn {core.activeTab === 'files' ? 'active' : ''}"
            onclick={() => core.activeTab = 'files'}
            title={t('explorer.shared.fileBrowser')}
        >
            <svg width="12" height="12" viewBox="0 0 16 16" fill="none"><path d="M2 3h4l1.5 2H13l1.5 2.5V13a1 1 0 0 1-1 1H2a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1z" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round"/></svg>
            <span class="tab-label">{folderName(folderPath)}</span>
        </button>
        <button
            class="tab-btn {core.activeTab === 'editor' ? 'active' : ''}"
            onclick={() => core.activeTab = 'editor'}
            title={t('explorer.shared.editor')}
        >
            <svg width="12" height="12" viewBox="0 0 16 16" fill="none"><path d="M3 2h6l4 4v8a1 1 0 0 1-1 1H3a1 1 0 0 1-1-1V3a1 1 0 0 1 1-1z" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round"/><path d="M9 2v4h4" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round"/></svg>
            {core.activeFilePath ? baseName(core.activeFilePath) : t('explorer.shared.editor')}
        </button>
        <button
            class="tab-btn {core.activeTab === 'symbols' ? 'active' : ''}"
            onclick={() => core.activeTab = 'symbols'}
            title={t('explorer.shared.symbols')}
        >
            <svg width="12" height="12" viewBox="0 0 16 16" fill="none"><path d="M3 4h10M3 8h10M3 12h6" stroke="currentColor" stroke-width="1.2" stroke-linecap="round"/></svg>
            {t('explorer.shared.symbols')}
        </button>
    </div>
{/if}

<!-- Multi-column layout -->
<div class="explorer-body">
    {#if changesOpen}
    <div class="explorer-changes-column">
        <GitStatusPanel
            projectId={projectId}
            projectPath={projectPath}
            onClose={() => onChangesOpen(false)}
            isMobile={isMobile}
            onEditFile={core.handleEditFromChanges}
            onCommitted={onReviewUpdated}
            onUpdated={onReviewUpdated}
        />
    </div>
    {:else}
        <!-- Column 1: File browser + Git graph (or Find panel) -->
        <div class="explorer-left-column" class:hidden={isMobile && core.activeTab !== 'files'} style="width: {layout.leftWidth}px">
            <!-- The listing request is blocked until the project's sandbox image exists, so its
                 build/pull progress belongs here. -->
            <SandboxImageStatusBanner />
            {#if core.showFind}
                <FileSearchPanel
                    projectId={projectId}
                    taskId={taskId}
                    onFileSelected={core.handleFileSelected}
                    onClose={core.closeFind}
                />
            {:else}
                <div class="explorer-split-container">
                    <!-- Top: File browser -->
                    <div class="explorer-file-browser" class:mobile={isMobile} style:height={isMobile ? undefined : layout.splitRatio * 100 + '%'} bind:this={core.fileBrowserEl}>
                        {@render browser()}
                    </div>

                    <!-- Drag handle (file browser / git graph split) -->
                    {#if !isMobile}
                    <ResizeHandle
                        axis="horizontal"
                        ariaLabel={t('explorer.shared.resizeSplitAria')}
                        onDragStart={layout.handleSplitDragStart}
                        onDragMove={layout.handleSplitDragMove}
                        onDragEnd={layout.handleSplitDragEnd}
                    />
                    {/if}

                    <!-- Bottom: Git graph -->
                    <div class="explorer-git-graph" class:mobile={isMobile} style:height={isMobile ? undefined : (1 - layout.splitRatio) * 100 + '%'}>
                        <BranchGitGraph
                            currentBranch={branch}
                            commits={core.commits}
                            originTip={core.originTip}
                            loading={core.loadingCommits || core.loadingMoreCommits}
                            hasMore={core.hasMoreCommits}
                            onCommitSelected={core.handleCommitSelected}
                            onLoadMore={core.loadMoreCommits}
                            selectedCommitHash={core.diffCommit?.hash}
                        />
                    </div>
                </div>
            {/if}
        </div>

        <!-- Resize handle: left column -->
        {#if !isMobile}
        <ResizeHandle
            ariaLabel={t('explorer.shared.resizeLeftAria')}
            onDragStart={layout.handleLeftDragStart}
            onDragMove={layout.handleLeftDragMove}
            onDragEnd={layout.handleLeftDragEnd}
        />
        {/if}

        <!-- Column 2: Commit diff or file editor -->
        <div class="explorer-center-column" class:hidden={isMobile && core.activeTab !== 'editor'}>
            {#if core.diffCommit}
                {#if commitDiff.component}
                    {@const CommitDiffOverlay = commitDiff.component}
                    <CommitDiffOverlay
                        commit={core.diffCommit}
                        files={core.diffCommitFiles}
                        loading={core.diffLoading}
                        projectId={projectId}
                        taskId={taskId}
                        onClose={core.closeCommitDiff}
                        onNavigate={core.handleDiffNavigate}
                        onFileSelected={core.handleFileSelected}
                        hasPrev={core.diffCommitIndex < core.commits.length - 1}
                        hasNext={core.diffCommitIndex > 0}
                        isMobile={isMobile}
                    />
                {:else}
                    <EmptyState class="pane-fill" title={t('git.loadingDiffViewer')} />
                {/if}
            {:else}
                {#if core.openFiles.length > 0}
                    <EditorTabs
                        projectId={projectId}
                        projectPath={projectPath}
                        taskId={taskId}
                        openFiles={core.openFiles}
                        activeFilePath={core.activeFilePath}
                        unsavedFiles={core.unsavedFiles}
                        pinnedFiles={core.pinnedFiles}
                        onFocused={core.handleEditorFocused}
                        onSymbolsUpdate={core.handleSymbolsUpdate}
                        onHasChanges={core.handleHasChanges}
                        onSelectTab={core.handleSelectTab}
                        onCloseTab={core.handleCloseTab}
                        onPinTab={core.pinTab}
                        onSave={core.handleEditorSave}
                        saving={core.editorSaving}
                        onToggleFullscreen={core.toggleEditorFullscreen}
                        mobileFullscreen={core.editorFullscreen}
                        pendingFsEvent={core.pendingFsEvent}
                        onLinesSelected={core.handleLinesSelected}
                        {scrollTops}
                        {onScrollTop}
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
        {#if !isMobile && core.symbols.length > 0 && !core.diffCommit}
        <ResizeHandle
            ariaLabel={t('explorer.shared.resizeRightAria')}
            onDragStart={layout.handleRightDragStart}
            onDragMove={layout.handleRightDragMove}
            onDragEnd={layout.handleRightDragEnd}
        />
        {/if}

        <!-- Column 3: Symbol outline (hidden when no symbols or diff is open, always visible on mobile via tab) -->
        <div class="explorer-right-column" class:hidden={(core.symbols.length === 0 && !isMobile) || (isMobile && core.activeTab !== 'symbols') || !!core.diffCommit} style="width: {layout.rightWidth}px">
            <BranchSymbols
                symbols={core.symbols}
                filePath={core.activeFilePath}
                onSymbolClick={core.revealSymbol}
            />
        </div>
    {/if}
</div>
