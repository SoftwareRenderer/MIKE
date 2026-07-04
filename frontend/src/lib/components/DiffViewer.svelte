<script lang="ts">
    import type { DiffFile, DiffRow } from '$lib/types/diff';
    import type { DiffDisplayItem, CollapsedBlock } from '$lib/utils/diff-parser';
    import { buildDisplayItems, truncatePath } from '$lib/utils/diff-parser';
    import { highlightLines } from '$lib/highlighter';
    import { loadCollapsedDiffFiles, saveCollapsedDiffFiles, loadAppSetting, saveAppSetting } from '$lib/storage';
import { slide } from 'svelte/transition';


    let { stagedFiles = [], unstagedFiles = [], stagedUntracked = [], unstagedUntracked = [], onExpandFile, onExpandUntracked, onLineClick, onStageFile, onStageAll, onUnstageAll }: { stagedFiles: DiffFile[]; unstagedFiles: DiffFile[]; stagedUntracked: Array<{ path: string; fileSize?: number; isBinary?: boolean; file: DiffFile; truncated?: boolean; origAdded?: number; loading?: boolean }>; unstagedUntracked: Array<{ path: string; fileSize?: number; isBinary?: boolean; file: DiffFile; truncated?: boolean; origAdded?: number; loading?: boolean }>; onExpandFile?: (path: string) => void; onExpandUntracked?: (path: string) => void; onLineClick?: (filePath: string, lineNumber: number) => void; onStageFile?: (path: string, stage: boolean) => void; onStageAll?: () => void; onUnstageAll?: () => void } = $props();
    let hasContent = $derived(stagedFiles.length > 0 || unstagedFiles.length > 0 || stagedUntracked.length > 0 || unstagedUntracked.length > 0);

    // Sort files: directories first (by path), then root files
    let sortedStagedFiles = $derived(sortFilesByPath(stagedFiles));
    let sortedUnstagedFiles = $derived(sortFilesByPath(unstagedFiles));
    let sortedStagedUntracked = $derived(sortUntrackedByPath(stagedUntracked));
    let sortedUnstagedUntracked = $derived(sortUntrackedByPath(unstagedUntracked));

    // Map all untracked entries by path for lookup
    let untrackedMap = $state(new Map<string, { path: string; fileSize?: number; isBinary?: boolean; file: DiffFile; truncated?: boolean; origAdded?: number; loading?: boolean }>());
    $effect(() => {
        const map = new Map<string, { path: string; fileSize?: number; isBinary?: boolean; file: DiffFile; truncated?: boolean; origAdded?: number; loading?: boolean }>();
        for (const entry of [...stagedUntracked, ...unstagedUntracked]) {
            map.set(entry.path, entry);
        }
        untrackedMap = map;
    });

    // Track which collapsed blocks are expanded
    let expandedBlocks = $state<Record<string, boolean>>({});

    // Track which file sections are expanded (file headers)
    // Persisted to IndexedDB so collapsed state survives page refreshes.
    // Single global key — stale paths are cleaned up by the $effect below.
    let collapsedFiles = $state(new Set<string>());

    // Section-level collapse (Staged / Unstaged)
    // Persisted via app-settings so state survives page refreshes.
    let collapsedSections = $state<Set<string>>(new Set());

    loadAppSetting('kanban:diff:collapsedSections', [] as string[]).then(ids => {
        collapsedSections = new Set(ids);
    }).catch(() => {});

    async function saveCollapsedSections(ids: Set<string>): Promise<void> {
        await saveAppSetting('kanban:diff:collapsedSections', [...ids]);
    }

    function toggleSection(id: string) {
        const next = new Set(collapsedSections);
        if (next.has(id)) {
            next.delete(id);
        } else {
            next.add(id);
        }
        collapsedSections = next;
        saveCollapsedSections(next).catch(() => {});
    }

    // Derived counts for section headers
    let stagedCount = $derived(sortedStagedFiles.length + sortedStagedUntracked.length);
    let unstagedCount = $derived(sortedUnstagedFiles.length + sortedUnstagedUntracked.length);
    let stagedAdditions = $derived(sortedStagedFiles.reduce((s, f) => s + (f.origAdded ?? f.added ?? 0), 0) + sortedStagedUntracked.reduce((s, e) => s + (e.file.origAdded ?? e.file.added ?? 0), 0));
    let stagedDeletions = $derived(sortedStagedFiles.reduce((s, f) => s + (f.origRemoved ?? f.removed ?? 0), 0));
    let unstagedAdditions = $derived(sortedUnstagedFiles.reduce((s, f) => s + (f.origAdded ?? f.added ?? 0), 0) + sortedUnstagedUntracked.reduce((s, e) => s + (e.file.origAdded ?? e.file.added ?? 0), 0));
    let unstagedDeletions = $derived(sortedUnstagedFiles.reduce((s, f) => s + (f.origRemoved ?? f.removed ?? 0), 0));

    // Load collapsed files from IndexedDB on init
    loadCollapsedDiffFiles().then(ids => { collapsedFiles = ids; }).catch(() => {});

    /** Save collapsed files to IndexedDB. */
    async function saveCollapsedFiles(collapsed: Set<string>): Promise<void> {
        await saveCollapsedDiffFiles(collapsed);
    }

    // Clean up stale entries: remove paths that no longer exist in the current diff
    $effect(() => {
        const _stagedFiles = stagedFiles;
        const _unstagedFiles = unstagedFiles;
        const _stagedUntracked = stagedUntracked;
        const _unstagedUntracked = unstagedUntracked;
        const currentPaths = new Set<string>();
        for (const f of [..._stagedFiles, ..._unstagedFiles]) currentPaths.add(f.path);
        for (const u of [..._stagedUntracked, ..._unstagedUntracked]) currentPaths.add(u.path);

        let changed = false;
        const next = new Set<string>();
        for (const path of collapsedFiles) {
            if (currentPaths.has(path)) {
                next.add(path);
            } else {
                changed = true;
            }
        }
        if (changed) {
            collapsedFiles = next;
            saveCollapsedFiles(next).catch(() => {});
        }
    });

    function isFileOpen(path: string): boolean {
        return !collapsedFiles.has(path);
    }

    // Track hovered row index per file for gutter highlight sync
    let hoveredRow = $state<Record<string, number>>({});

    // Reset hover state when diff data changes (e.g. expand, reload)
    $effect(() => {
        const _stagedFiles = stagedFiles;
        const _unstagedFiles = unstagedFiles;
        const _stagedUntracked = stagedUntracked;
        const _unstagedUntracked = unstagedUntracked;
        hoveredRow = {};
    });

    // Syntax highlight cache: filePath -> { lineNumKey: highlightedHtml }
    let highlightCache = $state<Record<string, Record<string, string>>>({});

    $effect(() => {
        // Read props to establish reactive dependencies
        const _stagedFiles = stagedFiles;
        const _unstagedFiles = unstagedFiles;
        const _stagedUntracked = stagedUntracked;
        const _unstagedUntracked = unstagedUntracked;

        const result: typeof highlightCache = {};

        for (const file of [..._stagedFiles, ..._unstagedFiles]) {
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

        for (const entry of [..._stagedUntracked, ..._unstagedUntracked]) {
            const ufile = entry.file;
            if (ufile.rows.length === 0 || entry.fileSize !== undefined) continue;
            const texts = ufile.rows.map(r => r.text ?? '');
            const fileResult: Record<string, string> = {};
            const htmls = highlightLines(texts, entry.path);
            let hasAnyHighlight = false;
            ufile.rows.forEach((row, i) => {
                if (htmls[i]) {
                    fileResult[String(row.lineNumber)] = htmls[i];
                    hasAnyHighlight = true;
                }
            });
            if (hasAnyHighlight) result[entry.path] = fileResult;
        }

        highlightCache = result;
    });

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
        saveCollapsedFiles(next);
    }

    /** Sort files by path: files with directory prefix first (alphabetical), then root files. */
    function sortFilesByPath(files: DiffFile[]): DiffFile[] {
        return [...files].sort((a, b) => {
            const aHasDir = a.path.includes('/');
            const bHasDir = b.path.includes('/');
            if (aHasDir && !bHasDir) return -1;
            if (!aHasDir && bHasDir) return 1;
            return a.path.localeCompare(b.path);
        });
    }

    /** Sort untracked entries by path: files with directory prefix first, then root files. */
    function sortUntrackedByPath(entries: typeof untracked): typeof untracked {
        return [...entries].sort((a, b) => {
            const aHasDir = a.path.includes('/');
            const bHasDir = b.path.includes('/');
            if (aHasDir && !bHasDir) return -1;
            if (!aHasDir && bHasDir) return 1;
            return a.path.localeCompare(b.path);
        });
    }

    function getDisplayItems(rows: DiffRow[]): DiffDisplayItem[] {
        return buildDisplayItems(rows);
    }

    function formatFileSize(bytes: number): string {
        if (bytes < 1024) return bytes + ' B';
        if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + ' KB';
        return (bytes / (1024 * 1024)).toFixed(1) + ' MB';
    }

    function handleRowClick(filePath: string, lineNumber: number | null) {
        if (onLineClick && lineNumber != null) {
            onLineClick(filePath, lineNumber);
        }
    }

    /** Compute VS Code-style status letter for a tracked file. */
    function getStatusBadge(file: DiffFile): { letter: string; color: string } {
        if (file.origPath) return { letter: 'R', color: '#d29922' }; // Renamed — yellow
        if (file.isBinary) return { letter: 'M', color: '#569cd6' }; // Binary — blue
        const added = file.origAdded ?? file.added ?? 0;
        const removed = file.origRemoved ?? file.removed ?? 0;
        if (removed > 0 && added === 0) return { letter: 'D', color: '#f85149' }; // Deleted — red
        return { letter: 'M', color: '#569cd6' }; // Modified — blue
    }

</script>


{#if !hasContent}
    <div class="diff-empty">
        <svg width="24" height="24" viewBox="0 0 16 16" fill="none">
            <path d="M3 2h6l4 4v8a1 1 0 0 1-1 1H3a1 1 0 0 1-1-1V3a1 1 0 0 1 1-1z" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round" opacity="0.3"/>
            <path d="M9 2v4h4" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round" opacity="0.3"/>
        </svg>
        <p>No changes since last commit</p>
    </div>
{:else}
    <div class="diff-scroll">
        <div class="diff-content-wrapper">

        <!-- Staged section -->
        {#if stagedCount > 0}
        <div class="diff-section">
            <button class="diff-section-header diff-section-staged" type="button" aria-expanded={!collapsedSections.has('staged')} onclick={() => toggleSection('staged')} title={collapsedSections.has('staged') ? 'Expand staged changes' : 'Collapse staged changes'}>
                <svg class="diff-section-chevron" class:expanded={!collapsedSections.has('staged')} width="12" height="12" viewBox="0 0 16 16" fill="none">
                    <path d="M6 4l4 4-4 4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                </svg>
                <span class="diff-section-title">Staged</span>
                <span class="diff-section-stats">
                    <span class="diff-added">+{stagedAdditions}</span>
                    <span class="diff-removed">-{stagedDeletions}</span>
                </span>
                <span class="diff-section-count">{stagedCount} file{stagedCount !== 1 ? 's' : ''}</span>
                {#if onUnstageAll}
                <button type="button" class="diff-section-action" onclick={(e) => { e.stopPropagation(); onUnstageAll(); }} title="Unstage all changes">
                    <svg width="12" height="12" viewBox="0 0 16 16" fill="none"><path d="M4 8h8" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg>
                </button>
                {/if}
            </button>
            {#if !collapsedSections.has('staged')}
            <div class="diff-section-body" transition:slide={{ duration: 200 }}>
                <!-- Staged tracked files -->
                {#each sortedStagedFiles as file (file.path)}
            {@const isFileExpanded = isFileOpen(file.path)}
            {@const badge = getStatusBadge(file)}
            <section class="diff-file-section diff-file-enter">
                <div class="diff-file-header-row">
                    <button
                        type="button"
                        class="diff-file-header diff-file-header-staged"
                        aria-expanded={isFileExpanded}
                        onclick={() => toggleFile(file.path)}
                    >
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
                    {#if onStageFile}
                        <button type="button" class="diff-stage-btn" onclick={() => onStageFile(file.path, false)} title="Unstage changes">
                            <svg width="12" height="12" viewBox="0 0 16 16" fill="none"><path d="M4 8h8" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg>
                        </button>
                    {/if}
                </div>
                {#if isFileExpanded}
                    <div class="diff-file-slide" transition:slide={{ duration: 200 }}>
                        {#if file.fileSize}
                        <div class="diff-file-content">
                            <div class="diff-gutter">
                                <div class="diff-gutter-spacer"></div>
                            </div>
                            <div class="diff-line-scroll">
                                <div class="diff-line-content-wrapper">
                                    {#if file.isBinary}
                                    <div class="diff-binary-notice">
                                        <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                                            <path d="M4 2h8a2 2 0 012 2v8a2 2 0 01-2 2H4a2 2 0 01-2-2V4a2 2 0 012-2z" stroke="currentColor" stroke-width="1.2"/>
                                            <path d="M6 6l4 4M10 6l-4 4" stroke="currentColor" stroke-width="1.2" stroke-linecap="round"/>
                                        </svg>
                                        <span>Binary file — {formatFileSize(file.fileSize)}</span>
                                    </div>
                                    {:else}
                                    <div class="diff-large-file-notice">
                                        <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                                            <path d="M3 2h10l1 1v11a1 1 0 01-1 1H3a1 1 0 01-1-1V3a1 1 0 011-1z" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round" opacity="0.5"/>
                                            <path d="M6 6h4M6 8h4M6 10h2" stroke="currentColor" stroke-width="1" stroke-linecap="round" opacity="0.4"/>
                                        </svg>
                                        <span>Large file — {formatFileSize(file.fileSize)} (+{file.origAdded ?? file.added}/-{file.origRemoved ?? file.removed})</span>
                                    </div>
                                    {/if}
                                </div>
                            </div>
                        </div>
                    {:else}
                        {@const items = getDisplayItems(file.rows)}
                        <div class="diff-file-content">
                            <div class="diff-file-body">
                                <div class="diff-gutter">
                                    {#each items as item, i}
                                        {#if item.type === 'row'}
                                            <div class={`diff-gutter-row diff-row-${item.row.variant}`} class:diff-gutter-hover={hoveredRow[file.path] === i}>
                                                <span class="diff-line-num">
                                                    <span class="diff-line-num-text">{item.row.lineNumber || ''}</span>
                                                </span>
                                            </div>
                                        {:else if item.type === 'collapsed'}
                                            <div class="diff-gutter-row diff-gutter-collapse" class:diff-gutter-hover={hoveredRow[file.path] === i}></div>
                                            {#if expandedBlocks[item.block.id]}
                                                {#each item.block.rows as row, ji}
                                                    <div class="diff-gutter-row diff-row-context" class:diff-gutter-hover={hoveredRow[file.path] === i + 1 + ji}>
                                                        <span class="diff-line-num">
                                                            <span class="diff-line-num-text">{row.lineNumber || ''}</span>
                                                        </span>
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
                                                <div
                                                    class={`diff-row diff-row-${item.row.variant}${onLineClick && item.row.lineNumber != null ? ' diff-row-clickable' : ''}`}
                                                    role={onLineClick && item.row.lineNumber != null ? 'button' : undefined}
                                                    tabindex={onLineClick && item.row.lineNumber != null ? 0 : undefined}
                                                    onclick={() => handleRowClick(file.path, item.row.lineNumber)}
                                                    onmouseenter={() => { hoveredRow = { ...hoveredRow, [file.path]: i } }}
                                                    onmouseleave={() => { const n = { ...hoveredRow }; delete n[file.path]; hoveredRow = n; }}
                                                >
                                                    <span class="diff-line-content">
                                                        {#if item.row.variant === 'added'}
                                                            <span class="diff-prefix">+</span>
                                                        {:else if item.row.variant === 'removed'}
                                                            <span class="diff-prefix">-</span>
                                                        {:else}
                                                            <span class="diff-prefix"> </span>
                                                        {/if}
                                                        {#if item.row.segments}
                                                            <span class="diff-text">
                                                                {#each item.row.segments as seg}
                                                                    <span class={`diff-segment diff-segment-${seg.tone}`}>{seg.text}</span>
                                                                {/each}
                                                            </span>
                                                        {:else if getHighlight(file.path, item.row.lineNumber)}
                                                            <span class="diff-text">{@html getHighlight(file.path, item.row.lineNumber)!}</span>
                                                        {:else}
                                                            <span class="diff-text">{item.row.text || ' '}</span>
                                                        {/if}
                                                    </span>
                                                </div>
                                            {:else if item.type === 'collapsed'}
                                                <button
                                                    type="button"
                                                    class="diff-collapse-btn"
                                                    aria-expanded={expandedBlocks[item.block.id]}
                                                    onclick={() => toggleBlock(item.block.id)}
                                                    onmouseenter={() => { hoveredRow = { ...hoveredRow, [file.path]: i } }}
                                                    onmouseleave={() => { const n = { ...hoveredRow }; delete n[file.path]; hoveredRow = n; }}
                                                    title={expandedBlocks[item.block.id] ? `Hide ${item.block.count} lines` : `Show ${item.block.count} lines`}
                                                >
                                                    {#if expandedBlocks[item.block.id]}
                                                        <svg width="10" height="10" viewBox="0 0 16 16" fill="none">
                                                            <path d="M4 6l4 4 4-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                                                        </svg>
                                                        Hide {item.block.count} unmodified lines
                                                    {:else}
                                                        <svg width="10" height="10" viewBox="0 0 16 16" fill="none">
                                                            <path d="M4 4h8M4 8h8M4 12h8" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
                                                        </svg>
                                                        Show {item.block.count} unmodified lines
                                                    {/if}
                                                </button>
                                                {#if expandedBlocks[item.block.id]}
                                                    {#each item.block.rows as row, ji}
                                                        <div
                                                            class={`diff-row diff-row-context${onLineClick && row.lineNumber != null ? ' diff-row-clickable' : ''}`}
                                                            role={onLineClick && row.lineNumber != null ? "button" : undefined}
                                                            tabindex={onLineClick && row.lineNumber != null ? 0 : undefined}
                                                            onclick={() => handleRowClick(file.path, row.lineNumber)}
                                                            onkeydown={(e) => { if (onLineClick && row.lineNumber != null && (e.key === 'Enter' || e.key === ' ')) { e.preventDefault(); handleRowClick(file.path, row.lineNumber); } }}
                                                            onmouseenter={() => { hoveredRow = { ...hoveredRow, [file.path]: i + 1 + ji } }}
                                                            onmouseleave={() => { const n = { ...hoveredRow }; delete n[file.path]; hoveredRow = n; }}
                                                        >
                                                            <span class="diff-line-content">
                                                                <span class="diff-prefix"> </span>
                                                                {#if row.segments}
                                                                    <span class="diff-text">
                                                                        {#each row.segments as seg}
                                                                            <span class={`diff-segment diff-segment-${seg.tone}`}>{seg.text}</span>
                                                                        {/each}
                                                                    </span>
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
                            {#if file.truncated && onExpandFile}
                                <button
                                    type="button"
                                    class="diff-expand-btn"
                                    onclick={() => onExpandFile(file.path)}
                                    title="Load full diff for this file"
                                >
                                    <svg width="12" height="12" viewBox="0 0 16 16" fill="none">
                                        <path d="M4 6l4 4 4-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                                    </svg>
                                    Show full diff
                                </button>
                            {/if}
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

        <!-- Unstaged section -->
        {#if unstagedCount > 0}
        <div class="diff-section">
            <button class="diff-section-header diff-section-unstaged" type="button" aria-expanded={!collapsedSections.has('unstaged')} onclick={() => toggleSection('unstaged')} title={collapsedSections.has('unstaged') ? 'Expand unstaged changes' : 'Collapse unstaged changes'}>
                <svg class="diff-section-chevron" class:expanded={!collapsedSections.has('unstaged')} width="12" height="12" viewBox="0 0 16 16" fill="none">
                    <path d="M6 4l4 4-4 4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                </svg>
                <span class="diff-section-title">Unstaged</span>
                <span class="diff-section-stats">
                    <span class="diff-added">+{unstagedAdditions}</span>
                    <span class="diff-removed">-{unstagedDeletions}</span>
                </span>
                <span class="diff-section-count">{unstagedCount} file{unstagedCount !== 1 ? 's' : ''}</span>
                {#if onStageAll}
                <button type="button" class="diff-section-action" onclick={(e) => { e.stopPropagation(); onStageAll(); }} title="Stage all changes">
                    <svg width="12" height="12" viewBox="0 0 16 16" fill="none"><path d="M8 4v8M4 8h8" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg>
                </button>
                {/if}
            </button>
            {#if !collapsedSections.has('unstaged')}
            <div class="diff-section-body" transition:slide={{ duration: 200 }}>
                <!-- Unstaged tracked files -->
                {#each sortedUnstagedFiles as file (file.path)}
            {@const isFileExpanded = isFileOpen(file.path)}
            {@const badge = getStatusBadge(file)}
            <section class="diff-file-section diff-file-enter">
                <div class="diff-file-header-row">
                    <button
                        type="button"
                        class="diff-file-header diff-file-header-unstaged"
                        aria-expanded={isFileExpanded}
                        onclick={() => toggleFile(file.path)}
                    >
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
                    {#if onStageFile}
                        <button type="button" class="diff-stage-btn" onclick={() => onStageFile(file.path, true)} title="Stage changes">
                            <svg width="12" height="12" viewBox="0 0 16 16" fill="none"><path d="M8 4v8M4 8h8" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg>
                        </button>
                    {/if}
                </div>
                {#if isFileExpanded}
                    <div class="diff-file-slide" transition:slide={{ duration: 200 }}>
                        {#if file.fileSize}
                        <div class="diff-file-content">
                            <div class="diff-gutter">
                                <div class="diff-gutter-spacer"></div>
                            </div>
                            <div class="diff-line-scroll">
                                <div class="diff-line-content-wrapper">
                                    {#if file.isBinary}
                                    <div class="diff-binary-notice">
                                        <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                                            <path d="M4 2h8a2 2 0 012 2v8a2 2 0 01-2 2H4a2 2 0 01-2-2V4a2 2 0 012-2z" stroke="currentColor" stroke-width="1.2"/>
                                            <path d="M6 6l4 4M10 6l-4 4" stroke="currentColor" stroke-width="1.2" stroke-linecap="round"/>
                                        </svg>
                                        <span>Binary file — {formatFileSize(file.fileSize)}</span>
                                    </div>
                                    {:else}
                                    <div class="diff-large-file-notice">
                                        <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                                            <path d="M3 2h10l1 1v11a1 1 0 01-1 1H3a1 1 0 01-1-1V3a1 1 0 011-1z" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round" opacity="0.5"/>
                                            <path d="M6 6h4M6 8h4M6 10h2" stroke="currentColor" stroke-width="1" stroke-linecap="round" opacity="0.4"/>
                                        </svg>
                                        <span>Large file — {formatFileSize(file.fileSize)} (+{file.origAdded ?? file.added}/-{file.origRemoved ?? file.removed})</span>
                                    </div>
                                    {/if}
                                </div>
                            </div>
                        </div>
                    {:else}
                        {@const items = getDisplayItems(file.rows)}
                        <div class="diff-file-content">
                            <div class="diff-file-body">
                                <div class="diff-gutter">
                                    {#each items as item, i}
                                        {#if item.type === 'row'}
                                            <div class={`diff-gutter-row diff-row-${item.row.variant}`} class:diff-gutter-hover={hoveredRow[file.path] === i}>
                                                <span class="diff-line-num">
                                                    <span class="diff-line-num-text">{item.row.lineNumber || ''}</span>
                                                </span>
                                            </div>
                                        {:else if item.type === 'collapsed'}
                                            <div class="diff-gutter-row diff-gutter-collapse" class:diff-gutter-hover={hoveredRow[file.path] === i}></div>
                                            {#if expandedBlocks[item.block.id]}
                                                {#each item.block.rows as row, ji}
                                                    <div class="diff-gutter-row diff-row-context" class:diff-gutter-hover={hoveredRow[file.path] === i + 1 + ji}>
                                                        <span class="diff-line-num">
                                                            <span class="diff-line-num-text">{row.lineNumber || ''}</span>
                                                        </span>
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
                                                <div
                                                    class={`diff-row diff-row-${item.row.variant}${onLineClick && item.row.lineNumber != null ? ' diff-row-clickable' : ''}`}
                                                    role={onLineClick && item.row.lineNumber != null ? 'button' : undefined}
                                                    tabindex={onLineClick && item.row.lineNumber != null ? 0 : undefined}
                                                    onclick={() => handleRowClick(file.path, item.row.lineNumber)}
                                                    onmouseenter={() => { hoveredRow = { ...hoveredRow, [file.path]: i } }}
                                                    onmouseleave={() => { const n = { ...hoveredRow }; delete n[file.path]; hoveredRow = n; }}
                                                >
                                                    <span class="diff-line-content">
                                                        {#if item.row.variant === 'added'}
                                                            <span class="diff-prefix">+</span>
                                                        {:else if item.row.variant === 'removed'}
                                                            <span class="diff-prefix">-</span>
                                                        {:else}
                                                            <span class="diff-prefix"> </span>
                                                        {/if}
                                                        {#if item.row.segments}
                                                            <span class="diff-text">
                                                                {#each item.row.segments as seg}
                                                                    <span class={`diff-segment diff-segment-${seg.tone}`}>{seg.text}</span>
                                                                {/each}
                                                            </span>
                                                        {:else if getHighlight(file.path, item.row.lineNumber)}
                                                            <span class="diff-text">{@html getHighlight(file.path, item.row.lineNumber)!}</span>
                                                        {:else}
                                                            <span class="diff-text">{item.row.text || ' '}</span>
                                                        {/if}
                                                    </span>
                                                </div>
                                            {:else if item.type === 'collapsed'}
                                                <button
                                                    type="button"
                                                    class="diff-collapse-btn"
                                                    aria-expanded={expandedBlocks[item.block.id]}
                                                    onclick={() => toggleBlock(item.block.id)}
                                                    onmouseenter={() => { hoveredRow = { ...hoveredRow, [file.path]: i } }}
                                                    onmouseleave={() => { const n = { ...hoveredRow }; delete n[file.path]; hoveredRow = n; }}
                                                    title={expandedBlocks[item.block.id] ? `Hide ${item.block.count} lines` : `Show ${item.block.count} lines`}
                                                >
                                                    {#if expandedBlocks[item.block.id]}
                                                        <svg width="10" height="10" viewBox="0 0 16 16" fill="none">
                                                            <path d="M4 6l4 4 4-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                                                        </svg>
                                                        Hide {item.block.count} unmodified lines
                                                    {:else}
                                                        <svg width="10" height="10" viewBox="0 0 16 16" fill="none">
                                                            <path d="M4 4h8M4 8h8M4 12h8" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
                                                        </svg>
                                                        Show {item.block.count} unmodified lines
                                                    {/if}
                                                </button>
                                                {#if expandedBlocks[item.block.id]}
                                                    {#each item.block.rows as row, ji}
                                                        <div
                                                            class={`diff-row diff-row-context${onLineClick && row.lineNumber != null ? ' diff-row-clickable' : ''}`}
                                                            role={onLineClick && row.lineNumber != null ? "button" : undefined}
                                                            tabindex={onLineClick && row.lineNumber != null ? 0 : undefined}
                                                            onclick={() => handleRowClick(file.path, row.lineNumber)}
                                                            onkeydown={(e) => { if (onLineClick && row.lineNumber != null && (e.key === 'Enter' || e.key === ' ')) { e.preventDefault(); handleRowClick(file.path, row.lineNumber); } }}
                                                            onmouseenter={() => { hoveredRow = { ...hoveredRow, [file.path]: i + 1 + ji } }}
                                                            onmouseleave={() => { const n = { ...hoveredRow }; delete n[file.path]; hoveredRow = n; }}
                                                        >
                                                            <span class="diff-line-content">
                                                                <span class="diff-prefix"> </span>
                                                                {#if row.segments}
                                                                    <span class="diff-text">
                                                                        {#each row.segments as seg}
                                                                            <span class={`diff-segment diff-segment-${seg.tone}`}>{seg.text}</span>
                                                                        {/each}
                                                                    </span>
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
                            {#if file.truncated && onExpandFile}
                                <button
                                    type="button"
                                    class="diff-expand-btn"
                                    onclick={() => onExpandFile(file.path)}
                                    title="Load full diff for this file"
                                >
                                    <svg width="12" height="12" viewBox="0 0 16 16" fill="none">
                                        <path d="M4 6l4 4 4-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                                    </svg>
                                    Show full diff
                                </button>
                            {/if}
                        </div>
                        {/if}
                    </div>
                {/if}
            </section>
        {/each}

        <!-- Unstaged untracked files -->
        {#each sortedUnstagedUntracked as entry (entry.path)}
            {@const ufile = entry.file}
            {@const isUntrackedExpanded = isFileOpen(entry.path)}
            {@const isLarge = entry.fileSize !== undefined}
            {@const isBin = !!entry.isBinary}
            <section class="diff-file-section diff-file-section-untracked diff-file-enter">
                <div class="diff-file-header-row">
                    <button
                        type="button"
                        class="diff-file-header diff-file-header-unstaged"
                        aria-expanded={isUntrackedExpanded}
                        onclick={() => toggleFile(entry.path)}
                    >
                        <svg class="diff-chevron" class:expanded={isUntrackedExpanded} width="12" height="12" viewBox="0 0 16 16" fill="none">
                            <path d="M6 4l4 4-4 4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                        </svg>
                        <span class="diff-file-path" title={entry.path}>{truncatePath(entry.path)}</span>
                        {#if isLarge && !isBin}
                            <span class="diff-file-stats">
                                <span class="diff-large-size">{formatFileSize(entry.fileSize ?? 0)}</span>
                            </span>
                        {:else if !isBin && (entry.origAdded ?? ufile.added ?? 0) > 0}
                            <span class="diff-file-stats">
                                <span class="diff-added">+{entry.origAdded ?? ufile.added ?? 0}</span>
                            </span>
                        {/if}
                        <span class="diff-untracked-badge">untracked</span>
                    </button>
                    <span class="diff-status-badge diff-status-added" title="Added">A</span>
                    {#if onStageFile}
                        <button type="button" class="diff-stage-btn" onclick={() => onStageFile(entry.path, true)} title="Stage file">
                            <svg width="12" height="12" viewBox="0 0 16 16" fill="none"><path d="M8 4v8M4 8h8" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg>
                        </button>
                    {/if}
                </div>
                {#if isUntrackedExpanded}
                    <div class="diff-file-slide" transition:slide={{ duration: 200 }}>
                        {#if isLarge}
                        <div class="diff-file-content">
                            <div class="diff-gutter">
                                <div class="diff-gutter-spacer"></div>
                            </div>
                            <div class="diff-line-scroll">
                                <div class="diff-line-content-wrapper">
                                    {#if isBin}
                                    <div class="diff-binary-notice">
                                        <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                                            <path d="M4 2h8a2 2 0 012 2v8a2 2 0 01-2 2H4a2 2 0 01-2-2V4a2 2 0 012-2z" stroke="currentColor" stroke-width="1.2"/>
                                            <path d="M6 6l4 4M10 6l-4 4" stroke="currentColor" stroke-width="1.2" stroke-linecap="round"/>
                                        </svg>
                                        <span>Binary file — {formatFileSize(entry.fileSize ?? 0)}</span>
                                    </div>
                                    {:else}
                                    <div class="diff-large-file-notice">
                                        <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                                            <path d="M3 2h10l1 1v11a1 1 0 01-1 1H3a1 1 0 01-1-1V3a1 1 0 011-1z" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round" opacity="0.5"/>
                                            <path d="M6 6h4M6 8h4M6 10h2" stroke="currentColor" stroke-width="1" stroke-linecap="round" opacity="0.4"/>
                                        </svg>
                                        <span>Large file — {formatFileSize(entry.fileSize ?? 0)}</span>
                                    </div>
                                    {/if}
                                </div>
                            </div>
                        </div>
                    {:else if ufile.rows.length > 0}
                        {@const uitems = getDisplayItems(ufile.rows)}
                        <div class="diff-file-content">
                            <div class="diff-file-body">
                                <div class="diff-gutter">
                                    {#each uitems as item, i}
                                        {#if item.type === 'row'}
                                            <div class={`diff-gutter-row diff-row-${item.row.variant}`} class:diff-gutter-hover={hoveredRow[entry.path] === i}>
                                                <span class="diff-line-num">
                                                    <span class="diff-line-num-text">{item.row.lineNumber || ''}</span>
                                                </span>
                                            </div>
                                        {:else if item.type === 'collapsed'}
                                            <div class="diff-gutter-row diff-gutter-collapse" class:diff-gutter-hover={hoveredRow[entry.path] === i}></div>
                                            {#if expandedBlocks[item.block.id]}
                                                {#each item.block.rows as row, ji}
                                                    <div class="diff-gutter-row diff-row-context" class:diff-gutter-hover={hoveredRow[entry.path] === i + 1 + ji}>
                                                        <span class="diff-line-num">
                                                            <span class="diff-line-num-text">{row.lineNumber || ''}</span>
                                                        </span>
                                                    </div>
                                                {/each}
                                            {/if}
                                        {/if}
                                    {/each}
                                </div>
                                <div class="diff-line-scroll">
                                    <div class="diff-line-content-wrapper">
                                        {#each uitems as item, i}
                                            {#if item.type === 'row'}
                                                <div
                                                    class={`diff-row diff-row-${item.row.variant}${onLineClick && item.row.lineNumber != null ? ' diff-row-clickable' : ''}`}
                                                    role={onLineClick && item.row.lineNumber != null ? 'button' : undefined}
                                                    tabindex={onLineClick && item.row.lineNumber != null ? 0 : -1}
                                                    onclick={() => handleRowClick(entry.path, item.row.lineNumber)}
                                                    onmouseenter={() => { hoveredRow = { ...hoveredRow, [entry.path]: i } }}
                                                    onmouseleave={() => { const n = { ...hoveredRow }; delete n[entry.path]; hoveredRow = n; }}
                                                >
                                                    <span class="diff-line-content">
                                                        {#if item.row.variant === 'added'}
                                                            <span class="diff-prefix">+</span>
                                                        {:else if item.row.variant === 'removed'}
                                                            <span class="diff-prefix">-</span>
                                                        {:else}
                                                            <span class="diff-prefix"> </span>
                                                        {/if}
                                                        {#if item.row.segments}
                                                            <span class="diff-text">
                                                                {#each item.row.segments as seg}
                                                                    <span class={`diff-segment diff-segment-${seg.tone}`}>{seg.text}</span>
                                                                {/each}
                                                            </span>
                                                        {:else if getHighlight(entry.path, item.row.lineNumber)}
                                                            <span class="diff-text">{@html getHighlight(entry.path, item.row.lineNumber)!}</span>
                                                        {:else}
                                                            <span class="diff-text">{item.row.text || ' '}</span>
                                                        {/if}
                                                    </span>
                                                </div>
                                            {:else if item.type === 'collapsed'}
                                                <button
                                                    type="button"
                                                    class="diff-collapse-btn"
                                                    aria-expanded={expandedBlocks[item.block.id]}
                                                    onclick={() => toggleBlock(item.block.id)}
                                                    onmouseenter={() => { hoveredRow = { ...hoveredRow, [entry.path]: i } }}
                                                    onmouseleave={() => { const n = { ...hoveredRow }; delete n[entry.path]; hoveredRow = n; }}
                                                    title={expandedBlocks[item.block.id] ? `Hide ${item.block.count} lines` : `Show ${item.block.count} lines`}
                                                >
                                                    {#if expandedBlocks[item.block.id]}
                                                        <svg width="10" height="10" viewBox="0 0 16 16" fill="none">
                                                            <path d="M4 6l4 4 4-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                                                        </svg>
                                                        Hide {item.block.count} unmodified lines
                                                    {:else}
                                                        <svg width="10" height="10" viewBox="0 0 16 16" fill="none">
                                                            <path d="M4 4h8M4 8h8M4 12h8" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
                                                        </svg>
                                                        Show {item.block.count} unmodified lines
                                                    {/if}
                                                </button>
                                                {#if expandedBlocks[item.block.id]}
                                                    {#each item.block.rows as row, ji}
                                                        <div
                                                            class={`diff-row diff-row-context${onLineClick && row.lineNumber != null ? ' diff-row-clickable' : ''}`}
                                                            role={onLineClick && row.lineNumber != null ? "button" : undefined}
                                                            tabindex={onLineClick && row.lineNumber != null ? 0 : undefined}
                                                            onclick={() => handleRowClick(entry.path, row.lineNumber)}
                                                            onkeydown={(e) => { if (onLineClick && row.lineNumber != null && (e.key === 'Enter' || e.key === ' ')) { e.preventDefault(); handleRowClick(entry.path, row.lineNumber); } }}
                                                            onmouseenter={() => { hoveredRow = { ...hoveredRow, [entry.path]: i + 1 + ji } }}
                                                            onmouseleave={() => { const n = { ...hoveredRow }; delete n[entry.path]; hoveredRow = n; }}
                                                        >
                                                            <span class="diff-line-content">
                                                                <span class="diff-prefix"> </span>
                                                                {#if row.segments}
                                                                    <span class="diff-text">
                                                                        {#each row.segments as seg}
                                                                            <span class={`diff-segment diff-segment-${seg.tone}`}>{seg.text}</span>
                                                                        {/each}
                                                                    </span>
                                                                {:else if getHighlight(entry.path, row.lineNumber)}
                                                                    <span class="diff-text">{@html getHighlight(entry.path, row.lineNumber)!}</span>
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
                            {#if entry.truncated && onExpandUntracked}
                                <button
                                    type="button"
                                    class="diff-expand-btn"
                                    onclick={() => onExpandUntracked(entry.path)}
                                    title="Load full diff for this file"
                                >
                                    <svg width="12" height="12" viewBox="0 0 16 16" fill="none">
                                        <path d="M4 6l4 4 4-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                                    </svg>
                                    Show full diff
                                </button>
                            {/if}
                        </div>
                    {:else if ufile.added > 0 && !entry.loading}
                        <div class="diff-file-content">
                            <div class="diff-line-scroll">
                                <div class="diff-line-content-wrapper">
                                    <button type="button" class="diff-expand-btn diff-expand-center" onclick={() => onExpandUntracked(entry.path)}>
                                        Load {ufile.added} line{ufile.added !== 1 ? 's' : ''}
                                    </button>
                                </div>
                            </div>
                        </div>
                    {:else if entry.loading}
                        <div class="diff-file-content">
                            <div class="diff-line-scroll">
                                <div class="diff-line-content-wrapper">
                                    <div class="diff-loading">
                                        <div class="diff-loading-spinner"></div>
                                        <span>Loading...</span>
                                    </div>
                                </div>
                            </div>
                        </div>
                    {:else if ufile.added === 0 && ufile.rows.length === 0}
                        <div class="diff-file-content">
                            <div class="diff-line-scroll">
                                <div class="diff-line-content-wrapper">
                                    <div class="diff-empty-file-notice">
                                        <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                                            <path d="M3 2h6l4 4v8a1 1 0 0 1-1 1H3a1 1 0 0 1-1-1V3a1 1 0 0 1 1-1z" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round" opacity="0.3"/>
                                            <path d="M9 2v4h4" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round" opacity="0.3"/>
                                        </svg>
                                        <span>Empty file</span>
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
{/if}

<style>
    @keyframes diffFadeIn {
        from { opacity: 0; transform: translateY(-4px); }
        to { opacity: 1; transform: translateY(0); }
    }

    .diff-file-enter {
        animation: diffFadeIn 200ms ease-out;
    }

    .diff-scroll {
        flex: 1;
        overflow-y: auto;
        overflow-x: hidden;
        min-height: 0;
        padding: 0 8px 12px 8px;
    }

    .diff-empty {
        display: flex;
        flex-direction: column;
        align-items: center;
        justify-content: center;
        gap: 8px;
        padding: 32px 16px;
        color: var(--color-text-tertiary);
    }
    .diff-empty p {
        margin: 0;
        font-size: 0.875rem;
    }

    /* File section */
    .diff-file-section {
        margin-bottom: 8px;
    }

    .diff-file-header {
        display: flex;
        align-items: center;
        gap: 6px;
        width: 100%;
        min-width: 100%;
        padding: 0 8px;
        height: 28px;
        background: var(--color-surface-2);
        border: 1px solid var(--color-border);
        border-left: 3px solid var(--color-border);
        border-right: none;
        border-radius: 6px 6px 0 0;
        cursor: pointer;
        font-size: 0.8125rem;
        color: var(--color-text-secondary);
        text-align: left;
        position: sticky;
        top: 0;
        left: 0;
        z-index: 10;
    }
    .diff-file-header:hover {
        filter: brightness(1.2);
        opacity: 1;
    }

    /* Header row: button + badge + stage button */
    .diff-file-header-row {
        display: flex;
        align-items: center;
        gap: 0;
        width: 100%;
    }
    .diff-file-header-row .diff-file-header {
        flex: 1;
        min-width: 0;
        border-radius: 6px 0 0 0;
        border-right: none;
    }

    /* VS Code-style status badge */
    .diff-status-badge {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 1.5rem;
        height: 28px;
        font-size: 0.6875rem;
        font-weight: 700;
        font-family: var(--font-mono);
        background: var(--color-surface-2);
        border: 1px solid var(--color-border);
        border-right: none;
        border-left: none;
        color: var(--color-text-secondary);
        flex-shrink: 0;
        user-select: none;
    }
    .diff-status-badge.diff-status-added {
        color: #3fb950;
    }

    /* Stage/unstage button */
    .diff-stage-btn {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 28px;
        height: 28px;
        padding: 0;
        background: var(--color-surface-2);
        border: 1px solid var(--color-border);
        border-left: none;
        border-radius: 0 6px 0 0;
        cursor: pointer;
        color: var(--color-text-tertiary);
        flex-shrink: 0;
        transition: background 0.1s ease, color 0.1s ease;
    }
    .diff-stage-btn:hover {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }

    .diff-chevron {
        flex-shrink: 0;
        color: var(--color-text-tertiary);
        transition: transform 0.15s ease;
    }
    .diff-chevron.expanded {
        transform: rotate(90deg);
    }

    .diff-file-path {
        flex: 1;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
        font-family: var(--font-mono);
        font-size: 0.75rem;
        color: var(--color-text-primary);
    }

    .diff-file-stats {
        display: flex;
        gap: 6px;
        font-size: 0.75rem;
        font-weight: 500;
        flex-shrink: 0;
    }
    .diff-added {
        color: var(--color-status-green);
    }
    .diff-removed {
        color: var(--color-status-red);
    }

    /* File content — column: gutter+scroll body + expand button */
    .diff-file-content {
        margin-top: 2px;
        border: 1px solid var(--color-border);
        border-top: none;
        border-right: none;
        border-radius: 0 0 6px 6px;
        display: flex;
        flex-direction: column;
        min-width: 0;
        overflow: clip;
    }

    /* Gutter + scroll area row */
    .diff-file-body {
        display: flex;
        flex: 1;
        min-height: 0;
        overflow: hidden;
        overflow-x: auto;
    }

    /* Fixed gutter column */
    .diff-gutter {
        flex-shrink: 0;
        width: 2rem;
        display: flex;
        flex-direction: column;
        background: var(--color-surface-1);
        position: sticky;
        left: 0;
        z-index: 5;
    }

    .diff-gutter-row {
        font-family: var(--font-mono);
        font-size: 14px;
        line-height: 1.5;
        height: 1.5em;
        flex-shrink: 0;
        border-top: 1px solid transparent;
        border-bottom: 1px solid transparent;
    }
    .diff-gutter-hover {
        border-top-color: var(--color-accent);
        border-bottom-color: var(--color-accent);
    }

    .diff-gutter-spacer {
        line-height: 1.5;
    }

    .diff-line-num {
        display: block;
        text-align: right;
        padding-right: 8px;
        user-select: none;
        color: var(--color-text-tertiary);
        width: 100%;
        overflow: hidden;
    }
    .diff-line-num-text {
        font-size: 14px;
    }

    /* Scrollable content column */
    .diff-line-scroll {
        flex: 1;
        min-width: 0;
    }

    .diff-line-content-wrapper {
        display: flex;
        flex-direction: column;
        width: max-content;
        min-width: 100%;
    }

    /* Diff rows */
    .diff-row {
        display: block;
        font-family: var(--font-mono);
        font-size: 14px;
        line-height: 1.5;
        height: 1.5em;
        flex-shrink: 0;
        min-width: 100%;
        border-top: 1px solid transparent;
        border-bottom: 1px solid transparent;
    }
    .diff-line-content {
        display: block;
        padding: 0 8px;
        width: max-content;
        line-height: 1.5;
    }
    .diff-prefix {
        flex-shrink: 0;
        width: 1rem;
        user-select: none;
        padding-right: 8px;
        display: inline-block;
    }
    .diff-text {
        white-space: pre;
    }
    /* Ensure syntax-highlighted spans render correctly */
    .diff-text > span {
        font-family: inherit;
        font-size: inherit;
        white-space: inherit;
    }

    /* Inline word-level diff segments */
    .diff-segment {
        font-family: inherit;
        font-size: inherit;
        white-space: inherit;
    }
    .diff-segment-added {
        background: rgba(63,185,80,0.4);
    }
    .diff-segment-removed {
        background: rgba(248,81,73,0.4);
        text-decoration: line-through;
    }

    /* Lezer token highlighting — GitHub Dark palette (matches FileEditor) */
    :global(.diff-text .hl-keyword) {
        color: #F97583;
    }
    :global(.diff-text .hl-constantkeyword) {
        color: #79C0FF;
    }
    :global(.diff-text .hl-definitionvariable,
        .diff-text .hl-variable,
        .diff-text .hl-classname,
        .diff-text .hl-typename) {
        color: #FFA657;
    }
    :global(.diff-text .hl-function) {
        color: #D2A8FF;
    }
    :global(.diff-text .hl-property,
        .diff-text .hl-attribute) {
        color: #79C0FF;
    }
    :global(.diff-text .hl-string,
        .diff-text .hl-attributevalue,
        .diff-text .hl-regexp) {
        color: #A5D6FF;
    }
    :global(.diff-text .hl-number) {
        color: #79C0FF;
    }
    :global(.diff-text .hl-comment) {
        color: #8B949E;
        font-style: italic;
    }
    :global(.diff-text .hl-operator) {
        color: #FF7B72;
    }
    :global(.diff-text .hl-punctuation) {
        color: #C9D1D9;
    }
    :global(.diff-text .hl-tagname) {
        color: #7EE787;
    }
    :global(.diff-text .hl-meta) {
        color: #C9D1D9;
    }
    :global(.diff-text .hl-invalid) {
        color: #F97583;
        text-decoration: underline wavy #F97583;
    }

    /* Row variants */
    .diff-row-added {
        background: rgba(63,185,80,0.15);
    }
    .diff-row-added .diff-prefix {
        color: var(--color-status-green);
    }
    .diff-row-removed {
        background: rgba(248,81,73,0.15);
    }
    .diff-row-removed .diff-prefix {
        color: var(--color-status-red);
    }
    .diff-row-context .diff-prefix {
        color: var(--color-text-tertiary);
    }

    /* Gutter row variants */
    .diff-row-added .diff-line-num {
        background: rgba(63,185,80,0.08);
    }
    .diff-row-removed .diff-line-num {
        background: rgba(248,81,73,0.08);
    }

    /* Clickable rows */
    .diff-row-clickable {
        cursor: pointer;
    }
    .diff-row-clickable:hover {
        filter: brightness(0.95);
        border-top-color: var(--color-accent);
        border-bottom-color: var(--color-accent);
    }
    /* Collapse button */
    .diff-collapse-btn {
        display: flex;
        align-items: center;
        gap: 4px;
        width: 100%;
        padding: 4px 12px;
        background: var(--color-surface-2);
        border: none;
        border-top: 1px solid var(--color-border);
        border-bottom: 1px solid var(--color-border);
        cursor: pointer;
        font-size: 0.75rem;
        color: var(--color-text-tertiary);
        transition: background 0.1s ease;
        flex-shrink: 0;
    }
    .diff-collapse-btn:hover {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }

    /* Large/binary file notice */
    .diff-large-file-notice {
        display: flex;
        align-items: center;
        gap: 6px;
        padding: 8px 12px;
        color: var(--color-text-tertiary);
        font-size: 0.75rem;
        font-family: var(--font-mono);
    }
    .diff-large-file-notice svg {
        flex-shrink: 0;
        opacity: 0.5;
    }
    /* Binary file notice — slightly distinct from large file */
    .diff-binary-notice {
        display: flex;
        align-items: center;
        gap: 6px;
        padding: 8px 12px;
        color: var(--color-text-secondary);
        font-size: 0.75rem;
        font-family: var(--font-mono);
    }
    .diff-binary-notice svg {
        flex-shrink: 0;
        opacity: 0.6;
    }

    /* Empty file notice */
    .diff-empty-file-notice {
        display: flex;
        align-items: center;
        gap: 6px;
        padding: 8px 12px;
        color: var(--color-text-tertiary);
        font-size: 0.75rem;
        font-family: var(--font-mono);
    }
    .diff-empty-file-notice svg {
        flex-shrink: 0;
        opacity: 0.5;
    }
    .diff-large-size {
        color: var(--color-text-tertiary);
        font-size: 0.75rem;
        font-weight: 500;
    }

    /* Untracked file entries */

    .diff-untracked-badge {
        font-size: 0.75rem;
        padding: 1px 6px;
        border-radius: 4px;
        background: rgba(250,204,21,0.15);
        color: rgba(250,204,21,0.9);
        font-weight: 500;
        flex-shrink: 0;
    }

    /* Expand button for truncated files */
    .diff-expand-btn {
        display: flex;
        align-items: center;
        justify-content: flex-start;
        gap: 8px;
        width: 100%;
        padding: 12px;
        background: var(--color-surface-2);
        border: none;
        border-top: 1px dashed var(--color-border);
        cursor: pointer;
        font-size: 0.75rem;
        color: var(--color-accent);
        transition: background 0.1s ease;
        font-family: inherit;
        flex-shrink: 0;
    }
    .diff-expand-btn:hover {
        background: var(--color-surface-3);
        color: var(--color-accent-hover);
    }
    .diff-expand-center {
        justify-content: center;
        padding: 24px 12px;
        border-top: none;
    }

    /* Loading indicator */
    .diff-loading {
        display: flex;
        align-items: center;
        justify-content: center;
        gap: 8px;
        padding: 24px 12px;
        color: var(--color-text-secondary);
        font-size: 0.75rem;
    }
    .diff-loading-spinner {
        width: 14px;
        height: 14px;
        border: 2px solid var(--color-border);
        border-top-color: var(--color-accent);
        border-radius: 50%;
        animation: diff-spin 0.6s linear infinite;
    }
    @keyframes diff-spin {
        to { transform: rotate(360deg); }
    }

    /* Section grouping (Staged / Unstaged) */
    .diff-section {
        margin-bottom: 12px;
    }
    .diff-section-header {
        display: flex;
        align-items: center;
        gap: 6px;
        width: 100%;
        padding: 6px 10px;
        background: var(--color-surface-2);
        border: 1px solid var(--color-border);
        border-left: 3px solid transparent;
        border-radius: 6px;
        cursor: pointer;
        font-size: 0.8125rem;
        font-weight: 600;
        color: var(--color-text-primary);
        text-align: left;
        transition: background 0.15s ease;
        margin-bottom: 6px;
    }
    .diff-section-staged {
        border-left-color: var(--color-status-green);
        margin-top: 8px;
    }
    .diff-section-unstaged {
        border-left-color: var(--color-status-yellow, #f0c040);
    }
    .diff-file-header-staged {
        border-left-color: var(--color-status-green);
    }
    .diff-file-header-unstaged {
        border-left-color: var(--color-status-yellow, #f0c040);
    }
    .diff-section-header:hover {
        background: var(--color-surface-3);
    }
    .diff-section-chevron {
        flex-shrink: 0;
        color: var(--color-text-tertiary);
        transition: transform 0.15s ease;
    }
    .diff-section-chevron.expanded {
        transform: rotate(90deg);
    }
    .diff-section-title {
        flex: 1;
        min-width: 0;
    }
    .diff-section-count {
        font-size: 0.75rem;
        font-weight: 500;
        color: var(--color-text-tertiary);
        flex-shrink: 0;
    }
    .diff-section-stats {
        display: flex;
        gap: 6px;
        font-size: 0.75rem;
        font-weight: 500;
        flex-shrink: 0;
    }
    .diff-section-action {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 24px;
        height: 24px;
        padding: 0;
        background: var(--color-surface-3);
        border: 1px solid var(--color-border);
        border-radius: 4px;
        cursor: pointer;
        color: var(--color-text-secondary);
        flex-shrink: 0;
        transition: background 0.1s ease, color 0.1s ease;
    }
    .diff-section-action:hover {
        background: var(--color-surface-4);
        color: var(--color-text-primary);
    }
    .diff-section-body {
        display: flex;
        flex-direction: column;
        gap: 8px;
    }


    /* Tree grouping */


    /* Directory header */



</style>
