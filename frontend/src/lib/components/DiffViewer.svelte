<script lang="ts">
    import type { DiffFile, DiffRow, DiffImageRequest, DiffImageSection, DiffImageSource } from '$lib/types/diff';
    import type { DiffDisplayItem } from '$lib/utils/diff-parser';
    import { buildDisplayItems, jumpTargets, truncatePath, getStatusBadge } from '$lib/utils/diff-parser';
    import { storeBounded } from '$lib/utils/bounded-map';
    import { comparePathsRootLast, sortPathsRootLast } from '$lib/utils/sort-paths';
    import { isDisplayableImage, imageSides } from '$lib/utils/diff-image';
    import { highlightLinesAsync } from '$lib/highlighter';
    import { formatBytes } from '$lib/utils/format';
    import { t } from '$lib/i18n';
    import Spinner from './Spinner.svelte';
    import ImageDiffModal from './ImageDiffModal.svelte';
    import { loadCollapsedDiffFiles, saveCollapsedDiffFiles } from '$lib/storage';
import { slide } from 'svelte/transition';
    import '$lib/styles/diff-viewer.css';
    // Token colors for the highlighted lines. Separate from the viewer's own layout because
    // CommitDiffOverlay paints the same classes and must not drift from it.
    import '$lib/styles/token-colors.css';


    interface Props {
        committedFiles?: DiffFile[];
        stagedFiles: DiffFile[];
        unstagedFiles: DiffFile[];
        stagedUntracked?: UntrackedEntry[];
        unstagedUntracked?: UntrackedEntry[];
        loading?: boolean;
        /** Paths whose stage write has not been answered. Their button shows a spinner and does nothing. */
        pendingPaths?: Set<string>;
        /** Which section's whole-section action is running — 'staged' is the Staged section's Unstage All —
         *  so only that header shows a spinner. */
        busySection?: 'staged' | 'unstaged' | null;
        /** Translated text naming the wait, read out by the controls a spinner has replaced. */
        busyLabel?: string;
        onExpandFile?: (path: string) => void;
        onExpandUntracked?: (path: string) => void;
        onLineClick?: (filePath: string, lineNumber: number) => void;
        onStageFile?: (path: string, stage: boolean) => void;
        onStageAll?: () => void;
        onUnstageAll?: () => void;
        selectedPath?: string;
        mergedLabel?: string;
        imageSource?: DiffImageSource;
        /** What the unstaged section is called where this panel is not a working tree. A historical
         * comparison has no index, so it must not borrow the staging vocabulary. */
        unstagedTitle?: string;
        /** The empty state's sentence, which for a ref comparison is not "since last commit". */
        emptyTitle?: string;
        /** Which view owns this panel's collapsed files. Two panels open at once — the working diff and a
         * checkpoint's — hold different comparisons, and one shared record makes each prune the other's. */
        collapseScope?: string;
    }

    let { committedFiles = [], stagedFiles = [], unstagedFiles = [], stagedUntracked = [], unstagedUntracked = [], loading = false, pendingPaths = new Set<string>(), busySection = null, busyLabel = '', onExpandFile, onExpandUntracked, onLineClick, onStageFile, onStageAll, onUnstageAll, selectedPath, mergedLabel = t('task.diff.mergedFromSubtasks'), imageSource, unstagedTitle = t('diff.unstaged'), emptyTitle = t('diff.noChanges'), collapseScope = 'working' }: Props = $props();
    const busyId = 'diff-stage-busy';
    let hasContent = $derived(committedFiles.length > 0 || stagedFiles.length > 0 || unstagedFiles.length > 0 || stagedUntracked.length > 0 || unstagedUntracked.length > 0);

    // Tracked files rank by change status first, untracked ones by path alone; both end at the
    // same path rule ($lib/utils/sort-paths).
    let sortedStagedFiles = $derived(sortFilesByPath(stagedFiles));
    let sortedUnstagedFiles = $derived(sortFilesByPath(unstagedFiles));
    let sortedStagedUntracked = $derived(sortPathsRootLast(stagedUntracked));
    let sortedCommittedFiles = $derived(sortFilesByPath(committedFiles));
    let sortedUnstagedUntracked = $derived(sortPathsRootLast(unstagedUntracked));

    // Map of file path -> DOM element for scroll-to-file from sidebar
    let fileSections = new Map<string, HTMLElement>();
    function registerFileSection(el: HTMLElement, path: string) {
        fileSections.set(path, el);
        return {
            destroy() {
                fileSections.delete(path);
            },
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

    // Track which collapsed blocks are expanded
    let expandedBlocks = $state<Record<string, boolean>>({});

    // Track which file sections are expanded (file headers)
    // Persisted to IndexedDB so collapsed state survives page refreshes.
    // Single global key — stale paths are cleaned up by the $effect below.
    let collapsedFiles = $state(new Set<string>());

    /** Get all file paths belonging to a section. */
    function getSectionPaths(id: string): string[] {
        if (id === 'committed') {
            const paths: string[] = [];
            for (const f of sortedCommittedFiles) paths.push(f.path);
            return paths;
        }
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
    let stagedAdditions = $derived(sortedStagedFiles.reduce((s: number, f: DiffFile) => s + (f.origAdded ?? f.added ?? 0), 0) + sortedStagedUntracked.reduce((s: number, e: UntrackedEntry) => s + (e.file.origAdded ?? e.file.added ?? 0), 0));
    let stagedDeletions = $derived(sortedStagedFiles.reduce((s: number, f: DiffFile) => s + (f.origRemoved ?? f.removed ?? 0), 0));
    let unstagedAdditions = $derived(sortedUnstagedFiles.reduce((s: number, f: DiffFile) => s + (f.origAdded ?? f.added ?? 0), 0) + sortedUnstagedUntracked.reduce((s: number, e: UntrackedEntry) => s + (e.file.origAdded ?? e.file.added ?? 0), 0));
    let unstagedDeletions = $derived(sortedUnstagedFiles.reduce((s: number, f: DiffFile) => s + (f.origRemoved ?? f.removed ?? 0), 0));

    // Read this view's collapsed files back. The scope is a literal at every call site, but reading it in an
    // effect is what keeps the record and the view that owns it in step.
    $effect(() => {
        const scope = collapseScope;
        loadCollapsedDiffFiles(scope).then(ids => { collapsedFiles = ids; }).catch(() => {});
    });

    /** Save collapsed files to IndexedDB. */
    async function saveCollapsedFiles(collapsed: Set<string>): Promise<void> {
        await saveCollapsedDiffFiles(collapseScope, collapsed);
    }

    $effect(() => {
        const _committedFiles = committedFiles;
        const _stagedFiles = stagedFiles;
        const _unstagedFiles = unstagedFiles;
        const _stagedUntracked = stagedUntracked;
        const _unstagedUntracked = unstagedUntracked;
        const currentPaths = new Set<string>();
        for (const f of [..._committedFiles, ..._stagedFiles, ..._unstagedFiles]) currentPaths.add(f.path);
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
    let hoveredRowKey = $state('');

    // Reset hover state when diff data changes (e.g. expand, reload)
    $effect(() => {
        const _stagedFiles = stagedFiles;
        const _unstagedFiles = unstagedFiles;
        const _stagedUntracked = stagedUntracked;
        const _unstagedUntracked = unstagedUntracked;
        hoveredRowKey = '';
    });

    // Highlighted HTML per file, keyed by the line's text. A diff has one gutter, which numbers removed
    // rows from the old file and every other row from the new one, so a line number names two rows.
    let highlightCache = $state(new Map<string, Map<string, string>>());

    const HIGHLIGHTS_PER_FILE = 2000;

    function getHighlight(filePath: string, text: string): string | undefined {
        if (!text) return undefined;
        return highlightCache.get(filePath)?.get(text);
    }

    /** The rows one section draws for a file. A file can be listed in the committed and working sections
     *  at once, comparing different commits. */
    function drawnRows(file: DiffFile, committed: boolean): DiffRow[] {
        return committed && file.committedRows?.length ? file.committedRows : file.rows;
    }

    /** Row lists already sent to the highlighter, by array identity: a refresh hands over a new array. */
    const highlightedList = new WeakSet<DiffRow[]>();

    async function highlightFile(path: string, rows: DiffRow[]) {
        if (rows.length === 0) return;
        const htmls = await highlightLinesAsync(rows.map(r => r.text ?? ''), path);
        const byText = highlightCache.get(path) ?? new Map<string, string>();
        rows.forEach((row, i) => {
            const html = htmls[i];
            if (html) storeBounded(byText, row.text ?? '', html, HIGHLIGHTS_PER_FILE);
        });
        const next = new Map(highlightCache);
        next.set(path, byText);
        highlightCache = next;
    }

    // Every file the panel is drawing gets one highlight pass — tracked, committed and untracked alike.
    $effect(() => {
        const open: Array<{ path: string; rows: DiffRow[] }> = [];
        for (const f of sortedCommittedFiles) {
            if (f.fileSize === undefined && isFileOpen(f.path)) open.push({ path: f.path, rows: drawnRows(f, true) });
        }
        for (const f of [...sortedStagedFiles, ...sortedUnstagedFiles]) {
            if (f.fileSize === undefined && isFileOpen(f.path)) open.push({ path: f.path, rows: drawnRows(f, false) });
        }
        for (const u of [...sortedStagedUntracked, ...sortedUnstagedUntracked]) {
            if (u.fileSize === undefined && isFileOpen(u.path)) open.push({ path: u.path, rows: u.file.rows });
        }
        for (const { path, rows } of open) {
            if (highlightedList.has(rows)) continue;
            highlightedList.add(rows);
            void highlightFile(path, rows);
        }
    });

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

    /** Modified, then Added, then Deleted, then Renamed; the path rule decides within a status. */
    function sortFilesByPath(files: DiffFile[]): DiffFile[] {
        // Status sort priority: Modified (M) = 0, Added (A) = 1, Deleted (D) = 2, Renamed (R) = 3
        const statusOrder = (f: DiffFile): number => {
            const { letter } = getStatusBadge(f);
            if (letter === 'M') return 0;
            if (letter === 'A') return 1;
            if (letter === 'D') return 2;
            return 3; // R (renamed)
        };
        return [...files].sort((a, b) => (statusOrder(a) - statusOrder(b)) || comparePathsRootLast(a, b));
    }

    type UntrackedEntry = { path: string; fileSize?: number; isBinary?: boolean; file: DiffFile; truncated?: boolean; origAdded?: number; loading?: boolean };

    function handleRowClick(filePath: string, lineNumber: number | null) {
        if (onLineClick && lineNumber != null) {
            onLineClick(filePath, lineNumber);
        }
    }

    /** A whole-section action. None runs while a write is in flight here: a second batch would be judged
     *  against an index the first is still moving. */
    function runSectionAction(section: 'staged' | 'unstaged') {
        if (busySection || pendingPaths.size > 0) return;
        if (section === 'staged') onUnstageAll?.();
        else onStageAll?.();
    }

    /**
     * What one binary file's "View image" button asks for, or null when there is nothing to ask: no
     * project named by the host, a format no browser paints, or a file this change removed.
     *
     * The host decides which commits each side is read from, because only it knows what the panel is
     * comparing. The defaults are what each section means when it says nothing.
     */
    function imageRequest(path: string, file: { isNewFile?: boolean; isDeleted?: boolean; origPath?: string }, section: DiffImageSection): DiffImageRequest | null {
        if (!imageSource || !isDisplayableImage(path)) return null;
        const sides = imageSides(file);
        if (!sides) return null;
        const refs = (section === 'committed' ? (imageSource.committed ?? imageSource.working) : imageSource.working) ?? {};
        return {
            path,
            displayPath: file.origPath ? `${file.origPath} → ${path}` : path,
            oldPath: file.origPath ?? '',
            oldRef: sides === 'compare' ? (refs.old ?? 'HEAD') : '',
            newRef: refs.new ?? (section === 'committed' ? 'HEAD' : ''),
        };
    }

    let imageModal = $state<DiffImageRequest | null>(null);

</script>

<!-- One row of the diff, shared by the tracked and the untracked rendering.

     `slot` is the row's position in the gutter, which is what `hoveredRowKey` names: the gutter and
     the line column are two lists side by side, and a collapsed block holds a slot of its own, so
     its expanded rows start at the next one. `jumpTo` is the line a click opens, not the gutter number
     when the row was removed. Context rows carry no variant prefix of their own — only context lines are
     ever collapsed into a block. -->
{#snippet diffRow(path: string, row: DiffRow, slot: number, variantClass: string, jumpTo: number | null)}
    {@const highlighted = getHighlight(path, row.text ?? '')}
    <!-- The role and the tabindex are conditioned on the same test, which the a11y check cannot
         follow: a non-negative tabindex never appears without role="button" beside it. -->
    <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
    <div
        class={`diff-row ${variantClass}${onLineClick && jumpTo != null ? ' diff-row-clickable' : ''}`}
        role={onLineClick && jumpTo != null ? 'button' : undefined}
        tabindex={onLineClick && jumpTo != null ? 0 : undefined}
        onclick={() => handleRowClick(path, jumpTo)}
        onkeydown={(e) => { if (onLineClick && jumpTo != null && (e.key === 'Enter' || e.key === ' ')) { e.preventDefault(); handleRowClick(path, jumpTo); } }}
        onmouseenter={() => { hoveredRowKey = `${path}:${slot}`; }}
        onmouseleave={() => { hoveredRowKey = ''; }}
    >
        <span class="diff-line-content">
            {#if row.variant === 'added'}
                <span class="diff-prefix">+</span>
            {:else if row.variant === 'removed'}
                <span class="diff-prefix">-</span>
            {:else}
                <span class="diff-prefix"> </span>
            {/if}
            {#if row.segments}
                <span class="diff-text">
                    {#each row.segments as seg}
                        <span class={`diff-segment diff-segment-${seg.tone}`}>{seg.text}</span>
                    {/each}
                </span>
            {:else if highlighted}
                <span class="diff-text">{@html highlighted}</span>
            {:else}
                <span class="diff-text">{row.text || ' '}</span>
            {/if}
        </span>
    </div>
{/snippet}

<!-- A binary file the diff cannot show as text: its size, and the image viewer when there is one. -->
{#snippet binaryNotice(image: DiffImageRequest | null, size: number | undefined)}
    <div class="diff-binary-notice">
        <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
            <path d="M4 2h8a2 2 0 012 2v8a2 2 0 01-2 2H4a2 2 0 01-2-2V4a2 2 0 012-2z" stroke="currentColor" stroke-width="1.2"/>
            <path d="M6 6l4 4M10 6l-4 4" stroke="currentColor" stroke-width="1.2" stroke-linecap="round"/>
        </svg>
        <span>{t('diff.binaryWithSize', { size: formatBytes(size ?? 0) })}</span>
        {#if image}
            <button type="button" class="diff-view-image-btn" onclick={() => { imageModal = image }}>{t('diff.viewImage')}</button>
        {/if}
    </div>
{/snippet}

{#snippet diffFileBody(path: string, items: DiffDisplayItem[], jumps: (number | null)[], truncated: boolean | undefined, onExpand: ((path: string) => void) | undefined)}
    <div class="diff-file-content">
        <div class="diff-file-body">
            <div class="diff-gutter">
                {#each items as item, i}
                    {#if item.type === 'row'}
                        <div class={`diff-gutter-row diff-row-${item.row.variant}`} class:diff-gutter-hover={hoveredRowKey === `${path}:${i}`}>
                            <span class="diff-line-num">
                                <span class="diff-line-num-text">{item.row.lineNumber || ''}</span>
                            </span>
                        </div>
                    {:else if item.type === 'collapsed'}
                        <div class="diff-gutter-row diff-gutter-collapse" class:diff-gutter-hover={hoveredRowKey === `${path}:${i}`}></div>
                        {#if expandedBlocks[item.block.id]}
                            {#each item.block.rows as row, ji}
                                <div class="diff-gutter-row diff-row-context" class:diff-gutter-hover={hoveredRowKey === `${path}:${i + 1 + ji}`}>
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
                            {@render diffRow(path, item.row, i, `diff-row-${item.row.variant}`, jumps[item.rowIndex])}
                        {:else if item.type === 'collapsed'}
                            {@const hidden = item.block.rows.length}
                            <button
                                type="button"
                                class="diff-collapse-btn"
                                aria-expanded={expandedBlocks[item.block.id]}
                                onclick={() => toggleBlock(item.block.id)}
                                onmouseenter={() => { hoveredRowKey = `${path}:${i}`; }}
                                onmouseleave={() => { hoveredRowKey = ''; }}
                                title={expandedBlocks[item.block.id]
                                    ? t('diff.hideLines', { count: hidden })
                                    : t('diff.showLines', { count: hidden })}
                            >
                                {#if expandedBlocks[item.block.id]}
                                    <svg width="10" height="10" viewBox="0 0 16 16" fill="none">
                                        <path d="M4 6l4 4 4-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                                    </svg>
                                    {t('diff.hideLines', { count: hidden })}
                                {:else}
                                    <svg width="10" height="10" viewBox="0 0 16 16" fill="none">
                                        <path d="M4 4h8M4 8h8M4 12h8" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
                                    </svg>
                                    {t('diff.showLines', { count: hidden })}
                                {/if}
                            </button>
                            {#if expandedBlocks[item.block.id]}
                                {#each item.block.rows as row, ji}
                                    {@render diffRow(path, row, i + 1 + ji, 'diff-row-context', jumps[item.block.startIndex + ji])}
                                {/each}
                            {/if}
                        {/if}
                    {/each}
                </div>
            </div>
        </div>
        {#if truncated && onExpand}
            <button
                type="button"
                class="diff-expand-btn"
                onclick={() => onExpand(path)}
                title={t('diff.loadFullTitle')}
            >
                <svg width="12" height="12" viewBox="0 0 16 16" fill="none">
                    <path d="M4 6l4 4 4-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                </svg>
                {t('diff.showFull')}
            </button>
        {/if}
    </div>
{/snippet}

{#snippet diffFileEntry(file: DiffFile, isStaged: boolean, showStageBtn: boolean = true, section: DiffImageSection = 'working')}
    {@const isFileExpanded = isFileOpen(file.path)}
    {@const badge = getStatusBadge(file)}
    {@const image = imageRequest(file.path, file, section)}
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
            <span class="diff-status-badge" style="color: {badge.color}" title={badge.letter === 'M' ? t('git.status.modified') : badge.letter === 'R' ? t('git.status.renamed') : badge.letter === 'D' ? t('git.status.deleted') : badge.letter === 'A' ? t('git.status.added') : t('git.status.modified')}>{badge.letter}</span>
            {#if onStageFile && showStageBtn}
                {@const pending = pendingPaths.has(file.path)}
                <button type="button" class="diff-stage-btn" disabled={pending} aria-busy={pending || undefined} aria-describedby={pending ? busyId : undefined} onclick={() => onStageFile(file.path, !isStaged)} title={isStaged ? t('diff.unstageChangesTitle') : t('diff.stageChangesTitle')}>
                    {#if pending}
                        <Spinner size={12} decorative />
                    {:else}
                        <svg width="12" height="12" viewBox="0 0 16 16" fill="none"><path d={isStaged ? "M4 8h8" : "M8 4v8M4 8h8"} stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg>
                    {/if}
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
                                {@render binaryNotice(image, file.fileSize)}
                            {:else}
                            <div class="diff-large-file-notice">
                                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                                    <path d="M3 2h10l1 1v11a1 1 0 01-1 1H3a1 1 0 01-1-1V3a1 1 0 011-1z" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round" opacity="0.5"/>
                                    <path d="M6 6h4M6 8h4M6 10h2" stroke="currentColor" stroke-width="1" stroke-linecap="round" opacity="0.4"/>
                                </svg>
                                <span>{t('diff.largeWithSize', { size: formatBytes(file.fileSize), added: file.origAdded ?? file.added, removed: file.origRemoved ?? file.removed })}</span>
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
                                <span>{t('diff.emptyFile')}</span>
                            </div>
                        </div>
                    </div>
                </div>
            {:else}
                {@const items = buildDisplayItems(file.rows)}
                {@const jumps = jumpTargets(file.rows)}
                {@render diffFileBody(file.path, items, jumps, file.truncated, onExpandFile)}
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
    {@const image = isBin ? imageRequest(entry.path, { isNewFile: true }, 'working') : null}
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
                        <span class="diff-large-size">{formatBytes(entry.fileSize ?? 0)}</span>
                    </span>
                {:else if !isBin && (entry.origAdded ?? ufile.added ?? 0) > 0}
                    <span class="diff-file-stats">
                        <span class="diff-added">+{entry.origAdded ?? ufile.added ?? 0}</span>
                    </span>
                {/if}
            </button>
            <span class="diff-untracked-badge">{t('diff.untracked')}</span>
            <span class="diff-status-badge diff-status-added" title={t('git.status.added')}>{t('diff.badgeAdded')}</span>
            {#if onStageFile}
                {@const pending = pendingPaths.has(entry.path)}
                <button type="button" class="diff-stage-btn" disabled={pending} aria-busy={pending || undefined} aria-describedby={pending ? busyId : undefined} onclick={() => onStageFile(entry.path, !isStaged)} title={isStaged ? t('diff.unstageFileTitle') : t('diff.stageFileTitle')}>
                    {#if pending}
                        <Spinner size={12} decorative />
                    {:else}
                        <svg width="12" height="12" viewBox="0 0 16 16" fill="none"><path d={isStaged ? "M4 8h8" : "M8 4v8M4 8h8"} stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg>
                    {/if}
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
                                {@render binaryNotice(image, entry.fileSize)}
                            {:else}
                            <div class="diff-large-file-notice">
                                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                                    <path d="M3 2h10l1 1v11a1 1 0 01-1 1H3a1 1 0 01-1-1V3a1 1 0 011-1z" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round" opacity="0.5"/>
                                    <path d="M6 6h4M6 8h4M6 10h2" stroke="currentColor" stroke-width="1" stroke-linecap="round" opacity="0.4"/>
                                </svg>
                                <span>{t('diff.largeFile', { size: formatBytes(entry.fileSize ?? 0) })}</span>
                            </div>
                            {/if}
                        </div>
                    </div>
                </div>
            {:else if ufile.rows.length > 0}
                {@const uitems = buildDisplayItems(ufile.rows)}
                {@const ujumps = jumpTargets(ufile.rows)}
                {@render diffFileBody(entry.path, uitems, ujumps, entry.truncated, onExpandUntracked)}
            {:else if ufile.added > 0 && !entry.loading}
                <div class="diff-file-content">
                    <div class="diff-line-scroll">
                        <div class="diff-line-content-wrapper">
                            <button type="button" class="diff-expand-btn diff-expand-center" onclick={() => onExpandUntracked?.(entry.path)}>
                                {t('diff.loadLines', { count: ufile.added })}
                            </button>
                        </div>
                    </div>
                </div>
            {:else if entry.loading}
                <div class="diff-file-content">
                    <div class="diff-line-scroll">
                        <div class="diff-line-content-wrapper">
                            <div class="diff-loading">
                                <Spinner decorative />
                                <span>{t('diff.shared.loading')}</span>
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
                                <span>{t('diff.emptyFile')}</span>
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
        {#if loading}
            <div class="diff-loading diff-loading-center">
                <Spinner decorative />
                <span>{t('diff.loadingChanges')}</span>
            </div>
        {:else}
            <svg width="24" height="24" viewBox="0 0 16 16" fill="none">
                <path d="M3 2h6l4 4v8a1 1 0 0 1-1 1H3a1 1 0 0 1-1-1V3a1 1 0 0 1 1-1z" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round" opacity="0.3"/>
                <path d="M9 2v4h4" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round" opacity="0.3"/>
            </svg>
            <p>{emptyTitle}</p>
        {/if}
    </div>
{:else}
    <div class="diff-scroll">
        <div class="diff-content-wrapper">

        <!-- Loading banner (shown when refreshing, e.g. thread switch) -->
        {#if loading}
        <div class="diff-loading-banner">
            <Spinner decorative />
            <span>{t('diff.loadingChanges')}</span>
        </div>
        {/if}

        <!-- Names the wait for the controls a spinner has replaced. -->
        {#if busyLabel}
            <span id={busyId} class="sr-only">{busyLabel}</span>
        {/if}

        <!-- Staged section -->
        {#if stagedCount > 0}
        <div class="diff-section">
            <button class="diff-section-header diff-section-staged" type="button" aria-expanded={!isSectionCollapsed('staged')} onclick={() => toggleSection('staged')} title={isSectionCollapsed('staged') ? t('diff.expandSection', { name: t('diff.staged') }) : t('diff.collapseSection', { name: t('diff.staged') })}>
                <svg class="diff-section-chevron" class:expanded={!isSectionCollapsed('staged')} width="12" height="12" viewBox="0 0 16 16" fill="none">
                    <path d="M6 4l4 4-4 4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                </svg>
                <span class="diff-section-title">{t('diff.staged')}</span>
                <span class="diff-section-stats">
                    <span class="diff-added">+{stagedAdditions}</span>
                    <span class="diff-removed">-{stagedDeletions}</span>
                </span>
                <span class="diff-section-count">{t('diff.shared.file', { count: stagedCount })}</span>
                {#if onUnstageAll}
                <span class="diff-section-action" role="button" tabindex="0" aria-busy={busySection === 'staged' || undefined} aria-describedby={busySection === 'staged' ? busyId : undefined} onclick={(e) => { e.stopPropagation(); runSectionAction('staged'); }} onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); e.stopPropagation(); runSectionAction('staged'); } }} title={t('diff.unstageAllAria')} aria-label={t('diff.unstageAllAria')}>
                    {#if busySection === 'staged'}
                        <Spinner size={12} decorative />
                    {:else}
                        <svg width="12" height="12" viewBox="0 0 16 16" fill="none" aria-hidden="true"><path d="M4 8h8" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg>
                    {/if}
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
            <button class="diff-section-header diff-section-unstaged" type="button" aria-expanded={!isSectionCollapsed('unstaged')} onclick={() => toggleSection('unstaged')} title={isSectionCollapsed('unstaged') ? t('diff.expandSection', { name: unstagedTitle }) : t('diff.collapseSection', { name: unstagedTitle })}>
                <svg class="diff-section-chevron" class:expanded={!isSectionCollapsed('unstaged')} width="12" height="12" viewBox="0 0 16 16" fill="none">
                    <path d="M6 4l4 4-4 4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                </svg>
                <span class="diff-section-title">{unstagedTitle}</span>
                <span class="diff-section-stats">
                    <span class="diff-added">+{unstagedAdditions}</span>
                    <span class="diff-removed">-{unstagedDeletions}</span>
                </span>
                <span class="diff-section-count">{t('diff.shared.file', { count: unstagedCount })}</span>
                {#if onStageAll}
                <span class="diff-section-action" role="button" tabindex="0" aria-busy={busySection === 'unstaged' || undefined} aria-describedby={busySection === 'unstaged' ? busyId : undefined} onclick={(e) => { e.stopPropagation(); runSectionAction('unstaged'); }} onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); e.stopPropagation(); runSectionAction('unstaged'); } }} title={t('diff.stageAllAria')} aria-label={t('diff.stageAllAria')}>
                    {#if busySection === 'unstaged'}
                        <Spinner size={12} decorative />
                    {:else}
                        <svg width="12" height="12" viewBox="0 0 16 16" fill="none" aria-hidden="true"><path d="M8 4v8M4 8h8" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg>
                    {/if}
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

        <!-- Merged from Subtasks section -->
        {#if sortedCommittedFiles.length > 0}
        <div class="diff-section">
            <button class="diff-section-header diff-section-merged" type="button" aria-expanded={!isSectionCollapsed('committed')} onclick={() => toggleSection('committed')} title={isSectionCollapsed('committed') ? t('diff.expandSection', { name: mergedLabel }) : t('diff.collapseSection', { name: mergedLabel })}>
                <svg class="diff-section-chevron" class:expanded={!isSectionCollapsed('committed')} width="12" height="12" viewBox="0 0 16 16" fill="none">
                    <path d="M6 4l4 4-4 4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                </svg>
                <span class="diff-section-title">{mergedLabel}</span>
                <span class="diff-section-stats">
                    <span class="diff-added">+{sortedCommittedFiles.reduce((s, f) => s + (f.origAdded ?? f.added ?? 0), 0)}</span>
                    <span class="diff-removed">-{sortedCommittedFiles.reduce((s, f) => s + (f.origRemoved ?? f.removed ?? 0), 0)}</span>
                </span>
                <span class="diff-section-count">{t('diff.shared.file', { count: sortedCommittedFiles.length })}</span>
            </button>
            <div class="diff-section-body">
                {#each sortedCommittedFiles as file (file.path)}
                    {@render diffFileEntry({ ...file, rows: drawnRows(file, true) }, false, false, 'committed')}
                {/each}
            </div>
        </div>
        {/if}

        </div>
    </div>
{/if}

{#if imageModal && imageSource}
    <ImageDiffModal
        project={imageSource.project}
        task={imageSource.task ?? ''}
        path={imageModal.path}
        displayPath={imageModal.displayPath}
        oldPath={imageModal.oldPath}
        oldRef={imageModal.oldRef}
        newRef={imageModal.newRef}
        onClose={() => { imageModal = null; }}
    />
{/if}

<style>
    /* Diff viewer styles — see $lib/styles/diff-viewer.css */
</style>
