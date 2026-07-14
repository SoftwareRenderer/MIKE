<script lang="ts">
    import type { DiffFile, DiffRow } from '$lib/types/diff';
    import type { DiffDisplayItem, CollapsedBlock } from '$lib/utils/diff-parser';
    import { buildDisplayItems, truncatePath } from '$lib/utils/diff-parser';
    import { highlightLines } from '$lib/highlighter';
    import { loadCollapsedDiffFiles, saveCollapsedDiffFiles } from '$lib/storage';
import { slide } from 'svelte/transition';
    import '$lib/styles/diff-viewer.css';


    let { stagedFiles = [], unstagedFiles = [], stagedUntracked = [], unstagedUntracked = [], onExpandFile, onExpandUntracked, onLineClick, onStageFile, onStageAll, onUnstageAll, selectedPath }: { stagedFiles: DiffFile[]; unstagedFiles: DiffFile[]; stagedUntracked: Array<{ path: string; fileSize?: number; isBinary?: boolean; file: DiffFile; truncated?: boolean; origAdded?: number; loading?: boolean }>; unstagedUntracked: Array<{ path: string; fileSize?: number; isBinary?: boolean; file: DiffFile; truncated?: boolean; origAdded?: number; loading?: boolean }>; onExpandFile?: (path: string) => void; onExpandUntracked?: (path: string) => void; onLineClick?: (filePath: string, lineNumber: number) => void; onStageFile?: (path: string, stage: boolean) => void; onStageAll?: () => void; onUnstageAll?: () => void; selectedPath?: string } = $props();
    let hasContent = $derived(stagedFiles.length > 0 || unstagedFiles.length > 0 || stagedUntracked.length > 0 || unstagedUntracked.length > 0);

    // Sort files: directories first (by path), then root files
    let sortedStagedFiles = $derived(sortFilesByPath(stagedFiles));
    let sortedUnstagedFiles = $derived(sortFilesByPath(unstagedFiles));
    let sortedStagedUntracked = $derived(sortUntrackedByPath(stagedUntracked));
    let sortedUnstagedUntracked = $derived(sortUntrackedByPath(unstagedUntracked));

    // Map of file path -> DOM element for scroll-to-file from sidebar
    let fileSections = $state(new Map<string, HTMLElement>());
    function registerFileSection(el: HTMLElement, path: string) {
        fileSections = new Map([...fileSections, [path, el]]);
        return () => {
            const next = new Map(fileSections);
            next.delete(path);
            fileSections = next;
        };
    }

    // Scroll to selected file when selectedPath changes (from sidebar click)
    $effect(() => {
        const path = selectedPath;
        if (!path) return;
        const el = fileSections.get(path);
        if (!el) return;
        // Expand the file if collapsed
        if (collapsedFiles.has(path)) {
            toggleFile(path);
        }
        // Expand parent section files if collapsed
        const file = [...sortedStagedFiles, ...sortedUnstagedFiles, ...sortedStagedUntracked, ...sortedUnstagedUntracked].find(f => (f as any).path === path);
        if (file) {
            const isStaged = sortedStagedFiles.includes(file as any) || sortedStagedUntracked.includes(file as any);
            const sectionId = isStaged ? 'staged' : 'unstaged';
            if (isSectionCollapsed(sectionId)) {
                toggleSection(sectionId);
            }
        }
        // Use requestAnimationFrame + setTimeout to wait for slide transition
        requestAnimationFrame(() => {
            setTimeout(() => {
                el.scrollIntoView({ behavior: 'smooth', block: 'start' });
            }, 50);
        });
    });

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

    /** Get all file paths belonging to a section. */
    function getSectionPaths(id: string): string[] {
        if (id === 'staged') {
            const paths: string[] = [];
            for (const f of sortedStagedFiles) paths.push(f.path);
            for (const u of sortedStagedUntracked) paths.push(u.path);
            return paths;
        }
        if (id === 'unstaged') {
            const paths: string[] = [];
            for (const f of sortedUnstagedFiles) paths.push(f.path);
            for (const u of sortedUnstagedUntracked) paths.push(u.path);
            return paths;
        }
        return [];
    }

    /** Check if all files in a section are collapsed. */
    function isSectionCollapsed(id: string): boolean {
        const paths = getSectionPaths(id);
        if (paths.length === 0) return false;
        for (const path of paths) {
            if (!collapsedFiles.has(path)) return false;
        }
        return true;
    }

    /** Collapse or expand all files in a section. */
    function toggleSection(id: string) {
        const sectionPaths = getSectionPaths(id);
        if (sectionPaths.length === 0) return;
        const allCollapsed = isSectionCollapsed(id);
        const nextFiles = new Set(collapsedFiles);
        for (const path of sectionPaths) {
            if (allCollapsed) {
                nextFiles.delete(path);
            } else {
                nextFiles.add(path);
            }
        }
        collapsedFiles = nextFiles;
        saveCollapsedFiles(nextFiles).catch(() => {});
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

{#snippet diffFileEntry(file: DiffFile, isStaged: boolean)}
    {@const isFileExpanded = isFileOpen(file.path)}
    {@const badge = getStatusBadge(file)}
    <section class="diff-file-section diff-file-enter" use:registerFileSection={file.path}>
        <div class="diff-file-header-row">
            <button
                type="button"
                class={`diff-file-header diff-file-header-${isStaged ? 'staged' : 'unstaged'}`}
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
                <button type="button" class="diff-stage-btn" onclick={() => onStageFile(file.path, !isStaged)} title={isStaged ? 'Unstage changes' : 'Stage changes'}>
                    <svg width="12" height="12" viewBox="0 0 16 16" fill="none"><path d={isStaged ? "M4 8h8" : "M8 4v8M4 8h8"} stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg>
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
            {:else if file.rows.length === 0}
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
                                        <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
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
                                                <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
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
{/snippet}

{#snippet untrackedFileEntry(entry: { path: string; fileSize?: number; isBinary?: boolean; file: DiffFile; truncated?: boolean; origAdded?: number; loading?: boolean }, isStaged: boolean)}
    {@const ufile = entry.file}
    {@const isUntrackedExpanded = isFileOpen(entry.path)}
    {@const isLarge = entry.fileSize !== undefined}
    {@const isBin = !!entry.isBinary}
    <section class="diff-file-section diff-file-section-untracked diff-file-enter" use:registerFileSection={entry.path}>
        <div class="diff-file-header-row">
            <button
                type="button"
                class={`diff-file-header diff-file-header-${isStaged ? 'staged' : 'unstaged'}`}
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
            </button>
            <span class="diff-untracked-badge">untracked</span>
            <span class="diff-status-badge diff-status-added" title="Added">A</span>
            {#if onStageFile}
                <button type="button" class="diff-stage-btn" onclick={() => onStageFile(entry.path, !isStaged)} title={isStaged ? 'Unstage file' : 'Stage file'}>
                    <svg width="12" height="12" viewBox="0 0 16 16" fill="none"><path d={isStaged ? "M4 8h8" : "M8 4v8M4 8h8"} stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg>
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
                                        <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
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
                                                <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
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
{/snippet}


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
            <button class="diff-section-header diff-section-staged" type="button" aria-expanded={!isSectionCollapsed('staged')} onclick={() => toggleSection('staged')} title={isSectionCollapsed('staged') ? 'Expand all staged files' : 'Collapse all staged files'}>
                <svg class="diff-section-chevron" class:expanded={!isSectionCollapsed('staged')} width="12" height="12" viewBox="0 0 16 16" fill="none">
                    <path d="M6 4l4 4-4 4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                </svg>
                <span class="diff-section-title">Staged</span>
                <span class="diff-section-stats">
                    <span class="diff-added">+{stagedAdditions}</span>
                    <span class="diff-removed">-{stagedDeletions}</span>
                </span>
                <span class="diff-section-count">{stagedCount} file{stagedCount !== 1 ? 's' : ''}</span>
                {#if onUnstageAll}
                <span class="diff-section-action" role="button" tabindex="0" onclick={(e) => { e.stopPropagation(); onUnstageAll(); }} onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); e.stopPropagation(); onUnstageAll(); } }} title="Unstage all changes" aria-label="Unstage all changes">
                    <svg width="12" height="12" viewBox="0 0 16 16" fill="none" aria-hidden="true"><path d="M4 8h8" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg>
                </span>
                {/if}
            </button>
            <div class="diff-section-body">
                <!-- Staged tracked files -->
                {#each sortedStagedFiles as file (file.path)}
                    {@render diffFileEntry(file, true)}
                {/each}
            </div>
        </div>
        {/if}

        <!-- Unstaged section -->
        {#if unstagedCount > 0}
        <div class="diff-section">
            <button class="diff-section-header diff-section-unstaged" type="button" aria-expanded={!isSectionCollapsed('unstaged')} onclick={() => toggleSection('unstaged')} title={isSectionCollapsed('unstaged') ? 'Expand all unstaged files' : 'Collapse all unstaged files'}>
                <svg class="diff-section-chevron" class:expanded={!isSectionCollapsed('unstaged')} width="12" height="12" viewBox="0 0 16 16" fill="none">
                    <path d="M6 4l4 4-4 4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                </svg>
                <span class="diff-section-title">Unstaged</span>
                <span class="diff-section-stats">
                    <span class="diff-added">+{unstagedAdditions}</span>
                    <span class="diff-removed">-{unstagedDeletions}</span>
                </span>
                <span class="diff-section-count">{unstagedCount} file{unstagedCount !== 1 ? 's' : ''}</span>
                {#if onStageAll}
                <span class="diff-section-action" role="button" tabindex="0" onclick={(e) => { e.stopPropagation(); onStageAll(); }} onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); e.stopPropagation(); onStageAll(); } }} title="Stage all changes" aria-label="Stage all changes">
                    <svg width="12" height="12" viewBox="0 0 16 16" fill="none" aria-hidden="true"><path d="M8 4v8M4 8h8" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg>
                </span>
                {/if}
            </button>
            <div class="diff-section-body">
                <!-- Unstaged tracked files -->
                {#each sortedUnstagedFiles as file (file.path)}
                    {@render diffFileEntry(file, false)}
                {/each}

                <!-- Unstaged untracked files -->
                {#each sortedUnstagedUntracked as entry (entry.path)}
                    {@render untrackedFileEntry(entry, false)}
                {/each}
            </div>
        </div>
        {/if}

        </div>
    </div>
{/if}

<style>
    /* Diff viewer styles — see $lib/styles/diff-viewer.css */
</style>
