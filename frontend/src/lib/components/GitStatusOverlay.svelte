<script lang="ts">
    import { onMount, onDestroy } from 'svelte';
    import type { DiffFile, DiffRow } from '$lib/types/diff';
    import type { DiffDisplayItem, CollapsedBlock } from '$lib/utils/diff-parser';
    import { parseUnifiedDiff, buildDisplayItems, truncatePath } from '$lib/utils/diff-parser';
    import { getGitStatus, getProjectDiff } from '$lib/api/git';
    import { highlightLines } from '$lib/highlighter';
    import { slide } from 'svelte/transition';
    import '$lib/styles/diff-viewer.css';

    let { projectId, projectPath, onClose, activeTab, isMobile }: {
        projectId: string;
        projectPath: string;
        onClose: () => void;
        activeTab: 'files' | 'diff';
        isMobile: boolean;
    } = $props();

    // Parsed status entries
    let stagedFiles = $state<StatusEntry[]>([]);
    let unstagedFiles = $state<StatusEntry[]>([]);
    let loading = $state(true);

    // Diff data: parsed DiffFile[] for staged and unstaged
    let stagedDiffFiles = $state<DiffFile[]>([]);
    let unstagedDiffFiles = $state<DiffFile[]>([]);

    // Selected file (for sync between columns)
    let selectedPath = $state('');
    let highlightTimer: ReturnType<typeof setTimeout> | null = null;

    // Expand/collapse state
    let expandedBlocks = $state<Record<string, boolean>>({});
    let collapsedFiles = $state(new Set<string>());
    let collapsedSections = $state(new Set<string>());

    // Hover state for gutter sync
    let hoveredRow = $state<Record<string, number>>({});

    // Highlight cache
    let highlightCache = $state<Record<string, Record<string, string>>>({});

    // Right column scroll container ref
    let diffScrollEl: HTMLDivElement | null = null;

    interface StatusEntry {
        path: string;
        status: string; // M, A, D, ?, R, U, C
        staged: boolean;
        origPath?: string;
    }

    async function loadData() {
        loading = true;
        try {
            // Fetch git status
            const { status } = await getGitStatus(projectId);
            const staged: StatusEntry[] = [];
            const unstaged: StatusEntry[] = [];

            for (const line of status.split('\n').filter(l => l.trim())) {
                const indexChar = line[0];
                const workChar = line[1];
                const pathPart = line.substring(3).trim();

                // Handle renames
                const arrowIdx = pathPart.indexOf(' -> ');
                let filePath: string, origPath: string | undefined;
                if (arrowIdx >= 0) {
                    origPath = pathPart.substring(0, arrowIdx);
                    filePath = pathPart.substring(arrowIdx + 4);
                } else {
                    filePath = pathPart;
                }

                const entry: StatusEntry = { path: filePath, staged: false, origPath };

                // Index changes (staged)
                if (indexChar !== ' ' && indexChar !== '?') {
                    entry.status = indexChar;
                    entry.staged = true;
                    staged.push(entry);
                }
                // Working tree changes (unstaged)
                if (workChar !== ' ' && workChar !== '?' && workChar !== indexChar) {
                    // If already staged with same status, don't duplicate
                    const existing = staged.find(s => s.path === filePath);
                    if (!existing) {
                        entry.status = workChar;
                        entry.staged = false;
                        unstaged.push(entry);
                    }
                } else if (workChar === '?' && indexChar === ' ') {
                    // Untracked file
                    entry.status = '?';
                    entry.staged = false;
                    unstaged.push(entry);
                }
            }

            stagedFiles = staged;
            unstagedFiles = unstaged;

            // Fetch diffs
            const [stagedDiff, unstagedDiff] = await Promise.all([
                getProjectDiff(projectId, { cached: true }).catch(() => ''),
                getProjectDiff(projectId).catch(() => ''),
            ]);

            stagedDiffFiles = parseUnifiedDiff(stagedDiff);
            unstagedDiffFiles = parseUnifiedDiff(unstagedDiff);

            // Build highlight cache
            buildHighlightCache();
        } catch (e) {
            console.error('Failed to load git status:', e);
        } finally {
            loading = false;
        }
    }

    // Build syntax highlight cache for all diff files
    function buildHighlightCache() {
        const result: typeof highlightCache = {};
        for (const file of [...stagedDiffFiles, ...unstagedDiffFiles]) {
            if (file.rows.length === 0 || file.fileSize) continue;
            const texts = file.rows.map(r => r.text ?? '');
            const fileResult: Record<string, string> = {};
            const htmls = highlightLines(texts, file.path);
            let hasAnyHighlight = false;
            file.rows.forEach((row, i) => {
                if (htmls[i]) {
                    fileResult[String(row.lineNumber)] = htmls[i];
                    hasAnyHighlight = true;
                }
            });
            if (hasAnyHighlight) result[file.path] = fileResult;
        }
        highlightCache = result;
    }

    function getHighlight(filePath: string, lineNumber: number | null): string | undefined {
        return highlightCache[filePath]?.[String(lineNumber)];
    }

    function toggleBlock(id: string) {
        expandedBlocks = { ...expandedBlocks, [id]: !expandedBlocks[id] };
    }

    function toggleFile(path: string) {
        const next = new Set(collapsedFiles);
        if (next.has(path)) {
            next.delete(path);
        } else {
            next.add(path);
        }
        collapsedFiles = next;
    }

    function isFileOpen(path: string): boolean {
        return !collapsedFiles.has(path);
    }

    function toggleSection(id: string) {
        const next = new Set(collapsedSections);
        if (next.has(id)) next.delete(id);
        else next.add(id);
        collapsedSections = next;
    }

    /** Handle click on a file in the left column — scroll to it in the right column. */
    function handleFileClick(path: string) {
        selectedPath = path;
        // Ensure file is expanded
        if (collapsedFiles.has(path)) {
            const next = new Set(collapsedFiles);
            next.delete(path);
            collapsedFiles = next;
        }
        // Ensure the section is expanded
        const isStaged = stagedDiffFiles.some(f => f.path === path);
        if (isStaged && collapsedSections.has('staged')) {
            const next = new Set(collapsedSections);
            next.delete('staged');
            collapsedSections = next;
        } else if (!isStaged && collapsedSections.has('unstaged')) {
            const next = new Set(collapsedSections);
            next.delete('unstaged');
            collapsedSections = next;
        }
        // Scroll to the file in the right column after a brief delay for DOM update
        if (highlightTimer) clearTimeout(highlightTimer);
        highlightTimer = setTimeout(() => {
            const el = document.getElementById(`diff-file-${path.replace(/[^a-zA-Z0-9]/g, '_')}`);
            if (el) {
                el.scrollIntoView({ behavior: 'smooth', block: 'start' });
                el.classList.add('diff-file-highlight');
                setTimeout(() => el.classList.remove('diff-file-highlight'), 1500);
            }
        }, 100);
    }

    function handleRowClick(filePath: string, lineNumber: number | null) {
        // No-op for project-level (no editor line jump)
    }

    function getDisplayItems(rows: DiffRow[]): DiffDisplayItem[] {
        return buildDisplayItems(rows);
    }

    function formatFileSize(bytes: number): string {
        if (bytes < 1024) return bytes + ' B';
        if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + ' KB';
        return (bytes / (1024 * 1024)).toFixed(1) + ' MB';
    }

    function getStatusBadge(file: DiffFile): { letter: string; color: string } {
        if (file.origPath) return { letter: 'R', color: '#d29922' };
        if (file.isBinary) return { letter: 'M', color: '#569cd6' };
        const added = file.origAdded ?? file.added ?? 0;
        const removed = file.origRemoved ?? file.removed ?? 0;
        if (removed > 0 && added === 0) return { letter: 'D', color: '#f85149' };
        return { letter: 'M', color: '#569cd6' };
    }

    function getStatusBadgeFromEntry(entry: StatusEntry): { letter: string; color: string } {
        switch (entry.status) {
            case 'M': return { letter: 'M', color: '#569cd6' };
            case 'A': return { letter: 'A', color: '#3fb950' };
            case 'D': return { letter: 'D', color: '#f85149' };
            case '?': return { letter: '??', color: '#8b949e' };
            case 'R': return { letter: 'R', color: '#d29922' };
            case 'U': return { letter: 'U', color: '#d29922' };
            case 'C': return { letter: 'C', color: '#58a6ff' };
            default: return { letter: entry.status, color: '#8b949e' };
        }
    }

    function getStatusLabel(char: string): string {
        switch (char) {
            case 'M': return 'Modified';
            case 'A': return 'Added';
            case 'D': return 'Deleted';
            case '?': return 'Untracked';
            case 'R': return 'Renamed';
            case 'U': return 'Unmerged';
            case 'C': return 'Copied';
            default: return char;
        }
    }

    // Sort files by path
    function sortFiles(files: DiffFile[]): DiffFile[] {
        return [...files].sort((a, b) => {
            const aHasDir = a.path.includes('/');
            const bHasDir = b.path.includes('/');
            if (aHasDir && !bHasDir) return -1;
            if (!aHasDir && bHasDir) return 1;
            return a.path.localeCompare(b.path);
        });
    }

    function sortEntries(entries: StatusEntry[]): StatusEntry[] {
        return [...entries].sort((a, b) => a.path.localeCompare(b.path));
    }

    let sortedStagedFiles = $derived(sortFiles(stagedDiffFiles));
    let sortedUnstagedFiles = $derived(sortFiles(unstagedDiffFiles));
    let sortedStagedEntries = $derived(sortEntries(stagedFiles));
    let sortedUnstagedEntries = $derived(sortEntries(unstagedFiles));

    let hasContent = $derived(stagedDiffFiles.length > 0 || unstagedDiffFiles.length > 0 || stagedFiles.length > 0 || unstagedFiles.length > 0);

    // Derived stats
    let stagedAdditions = $derived(sortedStagedFiles.reduce((s, f) => s + (f.origAdded ?? f.added ?? 0), 0));
    let stagedDeletions = $derived(sortedStagedFiles.reduce((s, f) => s + (f.origRemoved ?? f.removed ?? 0), 0));
    let unstagedAdditions = $derived(sortedUnstagedFiles.reduce((s, f) => s + (f.origAdded ?? f.added ?? 0), 0));
    let unstagedDeletions = $derived(sortedUnstagedFiles.reduce((s, f) => s + (f.origRemoved ?? f.removed ?? 0), 0));

    // Keyboard handler
    function handleKeydown(e: KeyboardEvent) {
        if (e.key === 'Escape') {
            onClose();
        }
    }

    onMount(() => {
        loadData();
        window.addEventListener('keydown', handleKeydown);
    });

    onDestroy(() => {
        window.removeEventListener('keydown', handleKeydown);
        if (highlightTimer) clearTimeout(highlightTimer);
    });
</script>

<div class="git-status-root">
{#if loading}
    <div class="git-status-loading">
        <span class="spinner"></span>
        <span>Loading changes...</span>
    </div>
{:else if !hasContent}
    <div class="git-status-empty">
        <svg width="32" height="32" viewBox="0 0 16 16" fill="none">
            <path d="M3 2h6l4 4v8a1 1 0 0 1-1 1H3a1 1 0 0 1-1-1V3a1 1 0 0 1 1-1z" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round" opacity="0.3"/>
            <path d="M9 2v4h4" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round" opacity="0.3"/>
        </svg>
        <p>No changes</p>
    </div>
{:else}
    <div class="git-status-body">
        <!-- Left column: File list -->
        <div class="git-status-file-list" class:hidden={isMobile && activeTab !== 'files'}>
            <div class="git-status-file-list-header">
                <span class="git-status-file-list-label">Changes</span>
                <button class="git-status-refresh-btn" onclick={loadData} title="Refresh" aria-label="Refresh git status">
                    <svg width="12" height="12" viewBox="0 0 16 16" fill="none">
                        <path d="M2 8a6 6 0 0 1 10.47-4M14 8a6 6 0 0 1-10.47 4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
                        <path d="M12 1v3h-3M4 15v-3h3" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                    </svg>
                </button>
            </div>

            <div class="git-status-file-list-scroll">
                <!-- Staged section -->
                {#if sortedStagedEntries.length > 0}
                <div class="git-status-section">
                    <button class="git-status-section-header git-status-section-staged" type="button" onclick={() => toggleSection('staged-files')} aria-expanded={!collapsedSections.has('staged-files')}>
                        <svg class="diff-section-chevron" class:expanded={!collapsedSections.has('staged-files')} width="10" height="10" viewBox="0 0 16 16" fill="none">
                            <path d="M6 4l4 4-4 4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                        </svg>
                        <span>Staged</span>
                        <span class="git-status-section-count">{sortedStagedEntries.length}</span>
                    </button>
                    {#if !collapsedSections.has('staged-files')}
                    <div class="git-status-section-body" transition:slide={{ duration: 150 }}>
                        {#each sortedStagedEntries as entry}
                            <button
                                class="git-status-file-item"
                                class:selected={selectedPath === entry.path}
                                onclick={() => handleFileClick(entry.path)}
                                title={entry.path}
                            >
                                <span class="git-status-entry-badge" style="color: {getStatusBadgeFromEntry(entry).color}" title={getStatusLabel(entry.status)}>
                                    {entry.status}
                                </span>
                                <span class="git-status-entry-name">{truncatePath(entry.path, 36)}</span>
                            </button>
                        {/each}
                    </div>
                    {/if}
                </div>
                {/if}

                <!-- Unstaged section -->
                {#if sortedUnstagedEntries.length > 0}
                <div class="git-status-section">
                    <button class="git-status-section-header git-status-section-unstaged" type="button" onclick={() => toggleSection('unstaged-files')} aria-expanded={!collapsedSections.has('unstaged-files')}>
                        <svg class="diff-section-chevron" class:expanded={!collapsedSections.has('unstaged-files')} width="10" height="10" viewBox="0 0 16 16" fill="none">
                            <path d="M6 4l4 4-4 4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                        </svg>
                        <span>Unstaged</span>
                        <span class="git-status-section-count">{sortedUnstagedEntries.length}</span>
                    </button>
                    {#if !collapsedSections.has('unstaged-files')}
                    <div class="git-status-section-body" transition:slide={{ duration: 150 }}>
                        {#each sortedUnstagedEntries as entry}
                            <button
                                class="git-status-file-item"
                                class:selected={selectedPath === entry.path}
                                onclick={() => handleFileClick(entry.path)}
                                title={entry.path}
                            >
                                <span class="git-status-entry-badge" style="color: {getStatusBadgeFromEntry(entry).color}" title={getStatusLabel(entry.status)}>
                                    {entry.status === '?' ? '??' : entry.status}
                                </span>
                                <span class="git-status-entry-name">{truncatePath(entry.path, 36)}</span>
                            </button>
                        {/each}
                    </div>
                    {/if}
                </div>
                {/if}
            </div>
        </div>

        <!-- Right column: Diff viewer -->
        <div class="git-status-diff" class:hidden={isMobile && activeTab !== 'diff'} bind:this={diffScrollEl}>
            <div class="diff-scroll">
                <div class="diff-content-wrapper">

                <!-- Staged diff section -->
                {#if sortedStagedFiles.length > 0}
                <div class="diff-section">
                    <button class="diff-section-header diff-section-staged" type="button" onclick={() => toggleSection('staged')} aria-expanded={!collapsedSections.has('staged')}>
                        <svg class="diff-section-chevron" class:expanded={!collapsedSections.has('staged')} width="12" height="12" viewBox="0 0 16 16" fill="none">
                            <path d="M6 4l4 4-4 4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                        </svg>
                        <span class="diff-section-title">Staged</span>
                        <span class="diff-section-stats">
                            <span class="diff-added">+{stagedAdditions}</span>
                            <span class="diff-removed">-{stagedDeletions}</span>
                        </span>
                        <span class="diff-section-count">{sortedStagedFiles.length} file{sortedStagedFiles.length !== 1 ? 's' : ''}</span>
                    </button>
                    {#if !collapsedSections.has('staged')}
                    <div class="diff-section-body" transition:slide={{ duration: 200 }}>
                        {#each sortedStagedFiles as file (file.path)}
                            {@const isFileExpanded = isFileOpen(file.path)}
                            {@const badge = getStatusBadge(file)}
                            <section class="diff-file-section diff-file-enter" id="diff-file-{file.path.replace(/[^a-zA-Z0-9]/g, '_')}">
                                <div class="diff-file-header-row">
                                    <button type="button" class="diff-file-header diff-file-header-staged" aria-expanded={isFileExpanded} onclick={() => toggleFile(file.path)}>
                                        <svg class="diff-chevron" class:expanded={isFileExpanded} width="12" height="12" viewBox="0 0 16 16" fill="none">
                                            <path d="M6 4l4 4-4 4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                                        </svg>
                                        <span class="diff-file-path" title={file.path}>
                                            {#if file.origPath}
                                                {truncatePath(file.origPath)} → {truncatePath(file.path)}
                                            {:else}
                                                {truncatePath(file.path)}
                                            {/if}
                                        </span>
                                        {#if !file.isBinary}
                                            <span class="diff-file-stats">
                                                <span class="diff-added">+{file.origAdded ?? file.added}</span>
                                                <span class="diff-removed">-{file.origRemoved ?? file.removed}</span>
                                            </span>
                                        {/if}
                                    </button>
                                    <span class="diff-status-badge" style="color: {badge.color}" title={badge.letter === 'M' ? 'Modified' : badge.letter === 'R' ? 'Renamed' : badge.letter === 'D' ? 'Deleted' : 'Added'}>{badge.letter}</span>
                                </div>
                                {#if isFileExpanded}
                                    <div class="diff-file-slide" transition:slide={{ duration: 200 }}>
                                        {#if file.fileSize}
                                            <div class="diff-file-content">
                                                <div class="diff-gutter"><div class="diff-gutter-spacer"></div></div>
                                                <div class="diff-line-scroll">
                                                    <div class="diff-line-content-wrapper">
                                                        {#if file.isBinary}
                                                            <div class="diff-binary-notice">
                                                                <svg width="14" height="14" viewBox="0 0 16 16" fill="none"><path d="M4 2h8a2 2 0 012 2v8a2 2 0 01-2 2H4a2 2 0 01-2-2V4a2 2 0 012-2z" stroke="currentColor" stroke-width="1.2"/><path d="M6 6l4 4M10 6l-4 4" stroke="currentColor" stroke-width="1.2" stroke-linecap="round"/></svg>
                                                                <span>Binary file — {formatFileSize(file.fileSize)}</span>
                                                            </div>
                                                        {:else}
                                                            <div class="diff-large-file-notice">
                                                                <svg width="14" height="14" viewBox="0 0 16 16" fill="none"><path d="M3 2h10l1 1v11a1 1 0 01-1 1H3a1 1 0 01-1-1V3a1 1 0 011-1z" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round" opacity="0.5"/><path d="M6 6h4M6 8h4M6 10h2" stroke="currentColor" stroke-width="1" stroke-linecap="round" opacity="0.4"/></svg>
                                                                <span>Large file — {formatFileSize(file.fileSize)}</span>
                                                            </div>
                                                        {/if}
                                                    </div>
                                                </div>
                                            </div>
                                        {:else if file.rows.length === 0}
                                            <div class="diff-file-content">
                                                <div class="diff-line-scroll"><div class="diff-line-content-wrapper">
                                                    <div class="diff-empty-file-notice">
                                                        <svg width="14" height="14" viewBox="0 0 16 16" fill="none"><path d="M3 2h6l4 4v8a1 1 0 0 1-1 1H3a1 1 0 0 1-1-1V3a1 1 0 0 1 1-1z" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round" opacity="0.3"/><path d="M9 2v4h4" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round" opacity="0.3"/></svg>
                                                        <span>Empty file</span>
                                                    </div>
                                                </div></div>
                                            </div>
                                        {:else}
                                            {@const items = getDisplayItems(file.rows)}
                                            <div class="diff-file-content">
                                                <div class="diff-file-body">
                                                    <div class="diff-gutter">
                                                        {#each items as item, i}
                                                            {#if item.type === 'row'}
                                                                <div class={`diff-gutter-row diff-row-${item.row.variant}`} class:diff-gutter-hover={hoveredRow[file.path] === i}>
                                                                    <span class="diff-line-num"><span class="diff-line-num-text">{item.row.lineNumber || ''}</span></span>
                                                                </div>
                                                            {:else if item.type === 'collapsed'}
                                                                <div class="diff-gutter-row diff-gutter-collapse" class:diff-gutter-hover={hoveredRow[file.path] === i}></div>
                                                                {#if expandedBlocks[item.block.id]}
                                                                    {#each item.block.rows as row, ji}
                                                                        <div class="diff-gutter-row diff-row-context" class:diff-gutter-hover={hoveredRow[file.path] === i + 1 + ji}>
                                                                            <span class="diff-line-num"><span class="diff-line-num-text">{row.lineNumber || ''}</span></span>
                                                                        </div>
                                                                    {/each}
                                                                {/if}
                                                            {/if}
                                                        {/each}
                                                    </div>
                                                    <div class="diff-line-scroll">
                                                        <div class="diff-line-content-wrapper">
                                                            {#each items as item, i}
                                                                {#if item.type === 'row'}
                                                                    <div class={`diff-row diff-row-${item.row.variant}`} onmouseenter={() => { hoveredRow = { ...hoveredRow, [file.path]: i } }} onmouseleave={() => { const n = { ...hoveredRow }; delete n[file.path]; hoveredRow = n; }}>
                                                                        <span class="diff-line-content">
                                                                            {#if item.row.variant === 'added'}<span class="diff-prefix">+</span>
                                                                            {:else if item.row.variant === 'removed'}<span class="diff-prefix">-</span>
                                                                            {:else}<span class="diff-prefix"> </span>{/if}
                                                                            {#if item.row.segments}
                                                                                <span class="diff-text">{#each item.row.segments as seg}<span class={`diff-segment diff-segment-${seg.tone}`}>{seg.text}</span>{/each}</span>
                                                                            {:else if getHighlight(file.path, item.row.lineNumber)}
                                                                                <span class="diff-text">{@html getHighlight(file.path, item.row.lineNumber)!}</span>
                                                                            {:else}
                                                                                <span class="diff-text">{item.row.text || ' '}</span>
                                                                            {/if}
                                                                        </span>
                                                                    </div>
                                                                {:else if item.type === 'collapsed'}
                                                                    <button type="button" class="diff-collapse-btn" aria-expanded={expandedBlocks[item.block.id]} onclick={() => toggleBlock(item.block.id)} onmouseenter={() => { hoveredRow = { ...hoveredRow, [file.path]: i } }} onmouseleave={() => { const n = { ...hoveredRow }; delete n[file.path]; hoveredRow = n; }} title={expandedBlocks[item.block.id] ? `Hide ${item.block.count} lines` : `Show ${item.block.count} lines`}>
                                                                        {#if expandedBlocks[item.block.id]}
                                                                            <svg width="10" height="10" viewBox="0 0 16 16" fill="none"><path d="M4 6l4 4 4-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
                                                                            Hide {item.block.count} unmodified lines
                                                                        {:else}
                                                                            <svg width="10" height="10" viewBox="0 0 16 16" fill="none"><path d="M4 4h8M4 8h8M4 12h8" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg>
                                                                            Show {item.block.count} unmodified lines
                                                                        {/if}
                                                                    </button>
                                                                    {#if expandedBlocks[item.block.id]}
                                                                        {#each item.block.rows as row, ji}
                                                                            <div class="diff-row diff-row-context" onmouseenter={() => { hoveredRow = { ...hoveredRow, [file.path]: i + 1 + ji } }} onmouseleave={() => { const n = { ...hoveredRow }; delete n[file.path]; hoveredRow = n; }}>
                                                                                <span class="diff-line-content">
                                                                                    <span class="diff-prefix"> </span>
                                                                                    {#if row.segments}
                                                                                        <span class="diff-text">{#each row.segments as seg}<span class={`diff-segment diff-segment-${seg.tone}`}>{seg.text}</span>{/each}</span>
                                                                                    {:else if getHighlight(file.path, row.lineNumber)}
                                                                                        <span class="diff-text">{@html getHighlight(file.path, row.lineNumber)!}</span>
                                                                                    {:else}
                                                                                        <span class="diff-text">{row.text || ' '}</span>
                                                                                    {/if}
                                                                                </span>
                                                                            </div>
                                                                        {/each}
                                                                    {/if}
                                                                {/if}
                                                            {/each}
                                                        </div>
                                                    </div>
                                                </div>
                                            </div>
                                        {/if}
                                    </div>
                                {/if}
                            </section>
                        {/each}
                    </div>
                    {/if}
                </div>
                {/if}

                <!-- Unstaged diff section -->
                {#if sortedUnstagedFiles.length > 0}
                <div class="diff-section">
                    <button class="diff-section-header diff-section-unstaged" type="button" onclick={() => toggleSection('unstaged')} aria-expanded={!collapsedSections.has('unstaged')}>
                        <svg class="diff-section-chevron" class:expanded={!collapsedSections.has('unstaged')} width="12" height="12" viewBox="0 0 16 16" fill="none">
                            <path d="M6 4l4 4-4 4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                        </svg>
                        <span class="diff-section-title">Unstaged</span>
                        <span class="diff-section-stats">
                            <span class="diff-added">+{unstagedAdditions}</span>
                            <span class="diff-removed">-{unstagedDeletions}</span>
                        </span>
                        <span class="diff-section-count">{sortedUnstagedFiles.length} file{sortedUnstagedFiles.length !== 1 ? 's' : ''}</span>
                    </button>
                    {#if !collapsedSections.has('unstaged')}
                    <div class="diff-section-body" transition:slide={{ duration: 200 }}>
                        {#each sortedUnstagedFiles as file (file.path)}
                            {@const isFileExpanded = isFileOpen(file.path)}
                            {@const badge = getStatusBadge(file)}
                            <section class="diff-file-section diff-file-enter" id="diff-file-{file.path.replace(/[^a-zA-Z0-9]/g, '_')}">
                                <div class="diff-file-header-row">
                                    <button type="button" class="diff-file-header diff-file-header-unstaged" aria-expanded={isFileExpanded} onclick={() => toggleFile(file.path)}>
                                        <svg class="diff-chevron" class:expanded={isFileExpanded} width="12" height="12" viewBox="0 0 16 16" fill="none">
                                            <path d="M6 4l4 4-4 4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                                        </svg>
                                        <span class="diff-file-path" title={file.path}>
                                            {#if file.origPath}
                                                {truncatePath(file.origPath)} → {truncatePath(file.path)}
                                            {:else}
                                                {truncatePath(file.path)}
                                            {/if}
                                        </span>
                                        {#if !file.isBinary}
                                            <span class="diff-file-stats">
                                                <span class="diff-added">+{file.origAdded ?? file.added}</span>
                                                <span class="diff-removed">-{file.origRemoved ?? file.removed}</span>
                                            </span>
                                        {/if}
                                    </button>
                                    <span class="diff-status-badge" style="color: {badge.color}" title={badge.letter === 'M' ? 'Modified' : badge.letter === 'R' ? 'Renamed' : badge.letter === 'D' ? 'Deleted' : 'Added'}>{badge.letter}</span>
                                </div>
                                {#if isFileExpanded}
                                    <div class="diff-file-slide" transition:slide={{ duration: 200 }}>
                                        {#if file.fileSize}
                                            <div class="diff-file-content">
                                                <div class="diff-gutter"><div class="diff-gutter-spacer"></div></div>
                                                <div class="diff-line-scroll">
                                                    <div class="diff-line-content-wrapper">
                                                        {#if file.isBinary}
                                                            <div class="diff-binary-notice">
                                                                <svg width="14" height="14" viewBox="0 0 16 16" fill="none"><path d="M4 2h8a2 2 0 012 2v8a2 2 0 01-2 2H4a2 2 0 01-2-2V4a2 2 0 012-2z" stroke="currentColor" stroke-width="1.2"/><path d="M6 6l4 4M10 6l-4 4" stroke="currentColor" stroke-width="1.2" stroke-linecap="round"/></svg>
                                                                <span>Binary file — {formatFileSize(file.fileSize)}</span>
                                                            </div>
                                                        {:else}
                                                            <div class="diff-large-file-notice">
                                                                <svg width="14" height="14" viewBox="0 0 16 16" fill="none"><path d="M3 2h10l1 1v11a1 1 0 01-1 1H3a1 1 0 01-1-1V3a1 1 0 011-1z" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round" opacity="0.5"/><path d="M6 6h4M6 8h4M6 10h2" stroke="currentColor" stroke-width="1" stroke-linecap="round" opacity="0.4"/></svg>
                                                                <span>Large file — {formatFileSize(file.fileSize)}</span>
                                                            </div>
                                                        {/if}
                                                    </div>
                                                </div>
                                            </div>
                                        {:else if file.rows.length === 0}
                                            <div class="diff-file-content">
                                                <div class="diff-line-scroll"><div class="diff-line-content-wrapper">
                                                    <div class="diff-empty-file-notice">
                                                        <svg width="14" height="14" viewBox="0 0 16 16" fill="none"><path d="M3 2h6l4 4v8a1 1 0 0 1-1 1H3a1 1 0 0 1-1-1V3a1 1 0 0 1 1-1z" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round" opacity="0.3"/><path d="M9 2v4h4" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round" opacity="0.3"/></svg>
                                                        <span>Empty file</span>
                                                    </div>
                                                </div></div>
                                            </div>
                                        {:else}
                                            {@const items = getDisplayItems(file.rows)}
                                            <div class="diff-file-content">
                                                <div class="diff-file-body">
                                                    <div class="diff-gutter">
                                                        {#each items as item, i}
                                                            {#if item.type === 'row'}
                                                                <div class={`diff-gutter-row diff-row-${item.row.variant}`} class:diff-gutter-hover={hoveredRow[file.path] === i}>
                                                                    <span class="diff-line-num"><span class="diff-line-num-text">{item.row.lineNumber || ''}</span></span>
                                                                </div>
                                                            {:else if item.type === 'collapsed'}
                                                                <div class="diff-gutter-row diff-gutter-collapse" class:diff-gutter-hover={hoveredRow[file.path] === i}></div>
                                                                {#if expandedBlocks[item.block.id]}
                                                                    {#each item.block.rows as row, ji}
                                                                        <div class="diff-gutter-row diff-row-context" class:diff-gutter-hover={hoveredRow[file.path] === i + 1 + ji}>
                                                                            <span class="diff-line-num"><span class="diff-line-num-text">{row.lineNumber || ''}</span></span>
                                                                        </div>
                                                                    {/each}
                                                                {/if}
                                                            {/if}
                                                        {/each}
                                                    </div>
                                                    <div class="diff-line-scroll">
                                                        <div class="diff-line-content-wrapper">
                                                            {#each items as item, i}
                                                                {#if item.type === 'row'}
                                                                    <div class={`diff-row diff-row-${item.row.variant}`} onmouseenter={() => { hoveredRow = { ...hoveredRow, [file.path]: i } }} onmouseleave={() => { const n = { ...hoveredRow }; delete n[file.path]; hoveredRow = n; }}>
                                                                        <span class="diff-line-content">
                                                                            {#if item.row.variant === 'added'}<span class="diff-prefix">+</span>
                                                                            {:else if item.row.variant === 'removed'}<span class="diff-prefix">-</span>
                                                                            {:else}<span class="diff-prefix"> </span>{/if}
                                                                            {#if item.row.segments}
                                                                                <span class="diff-text">{#each item.row.segments as seg}<span class={`diff-segment diff-segment-${seg.tone}`}>{seg.text}</span>{/each}</span>
                                                                            {:else if getHighlight(file.path, item.row.lineNumber)}
                                                                                <span class="diff-text">{@html getHighlight(file.path, item.row.lineNumber)!}</span>
                                                                            {:else}
                                                                                <span class="diff-text">{item.row.text || ' '}</span>
                                                                            {/if}
                                                                        </span>
                                                                    </div>
                                                                {:else if item.type === 'collapsed'}
                                                                    <button type="button" class="diff-collapse-btn" aria-expanded={expandedBlocks[item.block.id]} onclick={() => toggleBlock(item.block.id)} onmouseenter={() => { hoveredRow = { ...hoveredRow, [file.path]: i } }} onmouseleave={() => { const n = { ...hoveredRow }; delete n[file.path]; hoveredRow = n; }} title={expandedBlocks[item.block.id] ? `Hide ${item.block.count} lines` : `Show ${item.block.count} lines`}>
                                                                        {#if expandedBlocks[item.block.id]}
                                                                            <svg width="10" height="10" viewBox="0 0 16 16" fill="none"><path d="M4 6l4 4 4-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
                                                                            Hide {item.block.count} unmodified lines
                                                                        {:else}
                                                                            <svg width="10" height="10" viewBox="0 0 16 16" fill="none"><path d="M4 4h8M4 8h8M4 12h8" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg>
                                                                            Show {item.block.count} unmodified lines
                                                                        {/if}
                                                                    </button>
                                                                    {#if expandedBlocks[item.block.id]}
                                                                        {#each item.block.rows as row, ji}
                                                                            <div class="diff-row diff-row-context" onmouseenter={() => { hoveredRow = { ...hoveredRow, [file.path]: i + 1 + ji } }} onmouseleave={() => { const n = { ...hoveredRow }; delete n[file.path]; hoveredRow = n; }}>
                                                                                <span class="diff-line-content">
                                                                                    <span class="diff-prefix"> </span>
                                                                                    {#if row.segments}
                                                                                        <span class="diff-text">{#each row.segments as seg}<span class={`diff-segment diff-segment-${seg.tone}`}>{seg.text}</span>{/each}</span>
                                                                                    {:else if getHighlight(file.path, row.lineNumber)}
                                                                                        <span class="diff-text">{@html getHighlight(file.path, row.lineNumber)!}</span>
                                                                                    {:else}
                                                                                        <span class="diff-text">{row.text || ' '}</span>
                                                                                    {/if}
                                                                                </span>
                                                                            </div>
                                                                        {/each}
                                                                    {/if}
                                                                {/if}
                                                            {/each}
                                                        </div>
                                                    </div>
                                                </div>
                                            </div>
                                        {/if}
                                    </div>
                                {/if}
                            </section>
                        {/each}
                    </div>
                    {/if}
                </div>
                {/if}

                </div>
            </div>
        </div>
    </div>
{/if}
</div>

<style>
    .git-status-root {
        display: flex;
        flex-direction: column;
        flex: 1;
        min-height: 0;
    }

    .git-status-loading,
    .git-status-empty {
        display: flex;
        flex-direction: column;
        align-items: center;
        justify-content: center;
        gap: 12px;
        padding: 48px 24px;
        color: var(--color-text-secondary);
        font-size: 0.875rem;
        flex: 1;
        min-height: 0;
    }
    .git-status-loading .spinner {
        width: 20px;
        height: 20px;
        border: 2px solid var(--color-text-secondary);
        border-top-color: transparent;
        border-radius: 50%;
        animation: spin 0.6s linear infinite;
    }
    @keyframes spin { to { transform: rotate(360deg); } }

    .git-status-body {
        display: flex;
        flex: 1;
        overflow: hidden;
        min-height: 0;
    }

    /* Left column: File list */
    .git-status-file-list {
        width: 240px;
        flex-shrink: 0;
        border-right: 1px solid var(--color-border);
        background: var(--color-surface-1);
        display: flex;
        flex-direction: column;
        overflow: hidden;
    }

    .git-status-file-list-header {
        display: flex;
        align-items: center;
        gap: 6px;
        padding: 6px 10px;
        border-bottom: 1px solid var(--color-border);
        flex-shrink: 0;
    }

    .git-status-file-list-label {
        font-size: 0.75rem;
        font-weight: 600;
        color: var(--color-text-secondary);
        text-transform: uppercase;
        letter-spacing: 0.05em;
    }

    .git-status-refresh-btn {
        margin-left: auto;
        display: flex;
        align-items: center;
        justify-content: center;
        width: 22px;
        height: 22px;
        padding: 0;
        border: none;
        background: transparent;
        color: var(--color-text-secondary);
        cursor: pointer;
        border-radius: var(--radius-sm);
    }
    .git-status-refresh-btn:hover {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }

    .git-status-file-list-scroll {
        flex: 1;
        overflow-y: auto;
        overflow-x: hidden;
        padding: 4px 0;
    }

    .git-status-section {
        margin-bottom: 2px;
    }

    .git-status-section-header {
        display: flex;
        align-items: center;
        gap: 4px;
        width: 100%;
        padding: 4px 10px;
        border: none;
        background: transparent;
        color: var(--color-text-secondary);
        font-size: 0.6875rem;
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.04em;
        cursor: pointer;
        text-align: left;
    }
    .git-status-section-header:hover {
        color: var(--color-text-primary);
    }

    .git-status-section-staged {
        color: var(--color-status-green, #3fb950);
    }
    .git-status-section-unstaged {
        color: var(--color-status-yellow, #d29922);
    }

    .git-status-section-count {
        margin-left: auto;
        font-size: 0.625rem;
        color: var(--color-text-tertiary);
        font-weight: 400;
    }

    .git-status-section-body {
        padding: 2px 0;
    }

    .git-status-file-item {
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
    .git-status-file-item:hover {
        background: var(--color-surface-2);
    }
    .git-status-file-item.selected {
        background: var(--color-surface-3);
        color: var(--color-accent);
    }

    .git-status-entry-badge {
        flex-shrink: 0;
        font-size: 0.6875rem;
        font-family: var(--font-mono);
        font-weight: 600;
        width: 18px;
        text-align: center;
    }

    .git-status-entry-name {
        flex: 1;
        min-width: 0;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }

    /* Right column: Diff viewer */
    .git-status-diff {
        flex: 1;
        min-width: 0;
        display: flex;
        flex-direction: column;
        overflow: hidden;
    }

    /* Diff highlight animation */
    .diff-file-highlight {
        animation: diff-highlight-pulse 1.5s ease-out;
    }
    @keyframes diff-highlight-pulse {
        0% { outline: 2px solid var(--color-accent); outline-offset: -2px; border-radius: var(--radius-sm); }
        100% { outline: 2px solid transparent; outline-offset: -2px; }
    }

    /* Responsive */
    @media (max-width: 768px) {
        .git-status-file-list {
            width: 100%;
        }
        .git-status-file-list.hidden,
        .git-status-diff.hidden {
            display: none;
        }
        .git-status-diff {
            width: 100%;
        }
    }
</style>
