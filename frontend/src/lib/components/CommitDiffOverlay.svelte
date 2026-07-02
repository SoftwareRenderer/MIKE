<script lang="ts">
    import type { GitCommit } from '$lib/types';
    import type { DiffFile, DiffRow } from '$lib/types/diff';
    import { parseUnifiedDiff, buildSideBySideRows } from '$lib/utils/diff-parser';
    import type { SideBySideRow } from '$lib/utils/diff-parser';
    import { highlightLines } from '$lib/highlighter';

    interface Props {
        commit: GitCommit;
        diff: string;
        loading: boolean;
        projectPath: string;
        onClose: () => void;
        onNavigate: (direction: 'prev' | 'next') => void;
        onFileSelected: (path: string) => void;
        hasPrev: boolean;
        hasNext: boolean;
        onToggleFullscreen?: () => void;
        mobileFullscreen?: boolean;
        isMobile?: boolean;
    }

    let { commit, diff, loading, projectPath, onClose, onNavigate, onFileSelected, hasPrev, hasNext, onToggleFullscreen, mobileFullscreen, isMobile }: Props = $props();

    // Parse the unified diff into structured files
    let diffFiles = $derived<DiffFile[]>(parseUnifiedDiff(diff));
    let hasContent = $derived(diffFiles.length > 0);

    // Syntax highlight cache: path -> Map<lineText, highlightedHtml>
    let highlightMap = $derived<Record<string, Map<string, string>>>(
        (() => {
            const result: Record<string, Map<string, string>> = {};
            for (const file of diffFiles) {
                const cleanPath = formatPath(file.path);
                // Collect old lines (removed + context in order)
                const oldLines: string[] = [];
                const newLines: string[] = [];
                for (const row of file.rows) {
                    if (row.variant === 'removed') oldLines.push(row.text);
                    else if (row.variant === 'context') { oldLines.push(row.text); newLines.push(row.text); }
                    else if (row.variant === 'added') newLines.push(row.text);
                }
                const oldHtmls = oldLines.length ? highlightLines(oldLines, cleanPath) : [];
                const newHtmls = newLines.length ? highlightLines(newLines, cleanPath) : [];
                const map = new Map<string, string>();
                for (let i = 0; i < oldLines.length; i++) {
                    if (oldHtmls[i]) map.set(oldLines[i], oldHtmls[i]);
                }
                for (let i = 0; i < newLines.length; i++) {
                    if (newHtmls[i]) map.set(newLines[i], newHtmls[i]);
                }
                result[cleanPath] = map;
            }
            return result;
        })()
    );

    function getHighlighted(path: string, text: string): string {
        if (!text) return '';
        return highlightMap[formatPath(path)]?.get(text) || '';
    }

    // Max change lines to show before truncating a file
    const TRUNCATE_AFTER = 60;

    /** Build side-by-side rows for a file, optionally truncated to first N change lines. */
    function buildRowsForFile(rows: DiffRow[], expanded: boolean): SideBySideRow[] {
        if (expanded) return buildSideBySideRows(rows);
        // Truncate: keep rows until we've seen TRUNCATE_AFTER change lines
        const truncated: DiffRow[] = [];
        let changeCount = 0;
        for (const row of rows) {
            if (row.variant === 'added' || row.variant === 'removed') {
                changeCount++;
                if (changeCount > TRUNCATE_AFTER) break;
            }
            truncated.push(row);
        }
        return buildSideBySideRows(truncated);
    }

    // Pre-compute side-by-side rows for all files.
    // Recomputes when diff or expandedFiles changes.
    let fileRows = $derived.by(() => {
        const result: Record<string, SideBySideRow[]> = {};
        for (const file of diffFiles) {
            result[file.path] = buildRowsForFile(file.rows, isExpanded(file.path));
        }
        return result;
    });

    /** Check if a file has more change lines than the threshold. */
    function isTruncated(rows: DiffRow[]): boolean {
        return rows.filter(r => r.variant === 'added' || r.variant === 'removed').length > TRUNCATE_AFTER;
    }

    /** Count remaining change lines after the threshold. */
    function remainingChanges(rows: DiffRow[]): number {
        return rows.filter(r => r.variant === 'added' || r.variant === 'removed').length - TRUNCATE_AFTER;
    }

    // Expanded file state — track which truncated files are fully expanded
    let expandedFiles = $state<Set<string>>(new Set());

    function toggleExpand(path: string) {
        const next = new Set(expandedFiles);
        if (next.has(path)) {
            next.delete(path);
        } else {
            next.add(path);
        }
        expandedFiles = next;
    }

    function isExpanded(path: string): boolean {
        return expandedFiles.has(path);
    }

    // Collapsed file state — track which files are collapsed
    let collapsedFiles = $state<Set<string>>(new Set());

    function toggleFile(path: string) {
        const next = new Set(collapsedFiles);
        if (next.has(path)) {
            next.delete(path);
        } else {
            next.add(path);
        }
        collapsedFiles = next;
    }

    function isFileCollapsed(path: string): boolean {
        return collapsedFiles.has(path);
    }

    // Format file path for display (strip a/b/ prefix)
    function formatPath(path: string): string {
        return path.replace(/^[ab]\//, '');
    }

    function handleFileClick(path: string) {
        onFileSelected(formatPath(path));
    }

    function formatDate(dateStr: string): string {
        if (!dateStr) return '';
        try {
            return new Date(dateStr).toLocaleDateString();
        } catch {
            return dateStr;
        }
    }

    function formatDateTime(dateStr: string): string {
        if (!dateStr) return '';
        try {
            return new Date(dateStr).toLocaleString();
        } catch {
            return dateStr;
        }
    }

    // Handle keyboard navigation
    function handleKeydown(e: KeyboardEvent) {
        if (e.key === 'ArrowLeft' && hasNext) {
            e.preventDefault();
            onNavigate('next');
        } else if (e.key === 'ArrowRight' && hasPrev) {
            e.preventDefault();
            onNavigate('prev');
        }
    }

    // Compute total stats
    let totalAdded = $derived(diffFiles.reduce((sum, f) => sum + f.added, 0));
    let totalRemoved = $derived(diffFiles.reduce((sum, f) => sum + f.removed, 0));

    // Full message tooltip — shown when subject is clicked
    let showFullMessage = $state(false);
    let subjectBtnEl: HTMLButtonElement | null = null;
    let hasBody = $derived.by(() => commit.body && commit.body.length > 0);
    let fullMessage = $derived.by(() => {
        const parts = [commit.subject];
        if (commit.body && commit.body.length > 0) parts.push('', commit.body);
        return parts.join('\n');
    });

    // Close tooltip on outside click
    $effect(() => {
        function handleClick(e: MouseEvent) {
            if (subjectBtnEl && !subjectBtnEl.contains(e.target as Node)) {
                showFullMessage = false;
            }
        }
        if (showFullMessage) {
            document.addEventListener('click', handleClick);
            return () => document.removeEventListener('click', handleClick);
        }
    });
</script>

<div class="commit-diff-overlay" onkeydown={handleKeydown}>
    <!-- Header bar -->
    <div class="diff-header">
        <div class="diff-header-left">
            <button class="close-btn" onclick={onClose} title="Close diff" aria-label="Close diff">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
                    <path d="M4 4l8 8M12 4l-8 8" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
                </svg>
            </button>
            <div class="diff-commit-info">
                {#if hasBody}
                    <button
                        type="button"
                        class="diff-commit-subject-btn"
                        class:expanded={showFullMessage}
                        bind:this={subjectBtnEl}
                        onclick={() => { showFullMessage = !showFullMessage; }}
                        title="Click to view full commit message"
                    >
                        <span class="diff-commit-subject" title={commit.subject}>{commit.subject}</span>
                        <svg class="diff-subject-caret" width="12" height="12" viewBox="0 0 16 16" fill="none">
                            <path d="M4 6l4 4 4-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                        </svg>
                    </button>
                    {#if showFullMessage}
                        <div class="diff-commit-body-tooltip">
                            <pre>{fullMessage}</pre>
                        </div>
                    {/if}
                {:else}
                    <span class="diff-commit-subject" title={commit.subject}>{commit.subject}</span>
                {/if}
                <span class="diff-commit-hash" title={commit.hash}>{commit.short}</span>
                {#if commit.date}
                    <span class="diff-commit-date" title={formatDateTime(commit.date)}>{formatDate(commit.date)}</span>
                {/if}
            </div>
            {#if hasContent}
                <div class="diff-summary">
                    {#if totalAdded > 0}
                        <span class="diff-stat-added">+{totalAdded}</span>
                    {/if}
                    {#if totalRemoved > 0}
                        <span class="diff-stat-removed">-{totalRemoved}</span>
                    {/if}
                    <span class="diff-file-count">{diffFiles.length} file{diffFiles.length !== 1 ? 's' : ''}</span>
                </div>
            {/if}
        </div>
        <div class="diff-header-right">
            {#if onToggleFullscreen}
                <button type="button" class="nav-btn diff-fullscreen-toggle" onclick={onToggleFullscreen} title="Toggle fullscreen">
                    {#if mobileFullscreen}
                        <svg width="14" height="14" viewBox="0 0 16 16" fill="none"><path d="M10 2V6H14M6 14V10H2" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
                    {:else}
                        <svg width="14" height="14" viewBox="0 0 16 16" fill="none"><path d="M10 2h4v4M6 14H2v-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
                    {/if}
                </button>
            {/if}
        </div>
    </div>

    <!-- Content area -->
    <div class="diff-body">
        {#if loading}
            <div class="diff-loading-center">Loading diff...</div>
        {:else if !hasContent}
            <div class="diff-empty-center">No changes in this commit</div>
        {:else}
            <div class="diff-files-scroll">
                {#each diffFiles as file}
                    {@const collapsed = isFileCollapsed(file.path)}
                    {@const cleanPath = formatPath(file.path)}
                    <section class="diff-file-section">
                        <!-- File header (collapsible, sticky) -->
                        <button
                            type="button"
                            class="diff-file-header"
                            aria-expanded={!collapsed}
                            onclick={() => toggleFile(file.path)}
                        >
                            <svg class="diff-chevron" class:expanded={!collapsed} width="12" height="12" viewBox="0 0 16 16" fill="none">
                                <path d="M6 4l4 4-4 4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                            </svg>
                            <span class="diff-file-path" title={cleanPath}>{cleanPath}</span>
                            <span class="diff-file-stats">
                                {#if file.added > 0}
                                    <span class="diff-stat-added">+{file.added}</span>
                                {/if}
                                {#if file.removed > 0}
                                    <span class="diff-stat-removed">-{file.removed}</span>
                                {/if}
                            </span>
                        </button>

                        <!-- File content (when expanded) -->
                        {#if !collapsed}
                            {#if file.rows.length === 0}
                                <div class="diff-binary-notice">Binary file</div>
                            {:else}
                                {@const rows = fileRows[file.path]}
                                <div class="diff-sbs-body">
                                    <!-- Column headers -->
                                    <div class="diff-sbs-header-row">
                                        <div class="diff-sbs-col-header">
                                            <span class="diff-sbs-label">Old</span>
                                        </div>
                                        <div class="diff-sbs-col-header">
                                            <span class="diff-sbs-label">New</span>
                                        </div>
                                    </div>
                                    <!-- Scrollable columns -->
                                    <div class="diff-sbs-columns" onclick={() => handleFileClick(file.path)}>
                                    <!-- Left column (old) -->
                                    <div class="diff-sbs-column diff-sbs-left">
                                        <div class="diff-sbs-scroll">
                                            {#each rows as row}
                                                <div class="diff-sbs-line">
                                                    <span class="diff-line-num">{row.leftLineNum ?? ''}</span>
                                                    <span class="diff-line-content" class:removed={row.leftVariant === 'removed'} class:context={row.leftVariant === 'context'}>
                                                        {@html getHighlighted(file.path, row.leftText) || row.leftText}
                                                    </span>
                                                </div>
                                            {/each}
                                        </div>
                                    </div>
                                    <!-- Right column (new) -->
                                    <div class="diff-sbs-column diff-sbs-right">
                                        <div class="diff-sbs-scroll">
                                            {#each rows as row}
                                                <div class="diff-sbs-line">
                                                    <span class="diff-line-num">{row.rightLineNum ?? ''}</span>
                                                    <span class="diff-line-content" class:added={row.rightVariant === 'added'} class:context={row.rightVariant === 'context'}>
                                                        {@html getHighlighted(file.path, row.rightText) || row.rightText}
                                                    </span>
                                                </div>
                                            {/each}
                                        </div>
                                    </div>
                                </div>
                                </div>
                                {#if isTruncated(file.rows) && !isExpanded(file.path)}
                                    <button type="button" class="diff-expand-btn" onclick={(e) => { e.stopPropagation(); toggleExpand(file.path); }}>
                                        Show all changes
                                    </button>
                                {/if}
                            {/if}
                        {/if}
                    </section>
                {/each}
            </div>
        {/if}
    </div>
</div>

<style>
    .commit-diff-overlay {
        display: flex;
        flex-direction: column;
        height: 100%;
        width: 100%;
        background: var(--color-surface-1);
        overflow: hidden;
    }

    /* Header */
    .diff-header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 8px 16px;
        border-bottom: 1px solid var(--color-border);
        flex-shrink: 0;
        background: var(--color-surface-2);
        gap: 12px;
    }
    .diff-header-left {
        flex: 1;
        min-width: 0;
        display: flex;
        align-items: center;
        gap: 12px;
    }
    .diff-header-right {
        display: flex;
        align-items: center;
        gap: 4px;
        flex-shrink: 0;
    }
    .diff-commit-info {
        display: flex;
        align-items: center;
        gap: 8px;
        min-width: 0;
        flex-wrap: wrap;
        position: relative;
    }
    .diff-commit-subject-btn {
        display: inline-flex;
        align-items: center;
        gap: 4px;
        padding: 2px 4px;
        border: none;
        border-radius: 4px;
        background: transparent;
        cursor: pointer;
        text-align: left;
        flex-shrink: 0;
        max-width: 300px;
        min-width: 0;
    }
    .diff-commit-subject-btn:hover {
        background: var(--color-surface-3);
    }
    .diff-commit-subject-btn.expanded {
        background: var(--color-surface-3);
    }
    .diff-commit-subject {
        font-size: 0.8125rem;
        font-weight: 500;
        color: var(--color-text-primary);
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
    }
    .diff-subject-caret {
        flex-shrink: 0;
        transition: transform 0.15s ease;
        color: var(--color-text-tertiary);
    }
    .diff-commit-subject-btn.expanded .diff-subject-caret {
        transform: rotate(180deg);
    }
    /* Tooltip for full commit message */
    .diff-commit-body-tooltip {
        position: absolute;
        top: calc(100% + 6px);
        left: 0;
        z-index: 100;
        min-width: 280px;
        max-width: 480px;
        padding: 8px 12px;
        background: var(--color-surface-3);
        border: 1px solid var(--color-border);
        border-radius: 6px;
        box-shadow: 0 8px 24px rgba(0,0,0,0.3);
    }
    .diff-commit-body-tooltip::before {
        content: '';
        position: absolute;
        top: -6px;
        left: 16px;
        width: 10px;
        height: 10px;
        background: var(--color-surface-3);
        border-left: 1px solid var(--color-border);
        border-top: 1px solid var(--color-border);
        transform: rotate(45deg);
    }
    .diff-commit-body-tooltip pre {
        margin: 0;
        font-size: 0.75rem;
        font-family: var(--font-mono, monospace);
        color: var(--color-text-secondary);
        line-height: 1.5;
        white-space: pre-wrap;
        word-break: break-word;
        max-height: 200px;
        overflow-y: auto;
    }
    .diff-commit-hash {
        font-size: 0.75rem;
        font-family: var(--font-mono, monospace);
        color: var(--color-text-tertiary);
        flex-shrink: 0;
    }
    .diff-commit-date {
        font-size: 0.75rem;
        color: var(--color-text-tertiary);
        opacity: 0.7;
        flex-shrink: 0;
    }
    .diff-summary {
        display: flex;
        align-items: center;
        gap: 6px;
        flex-shrink: 0;
        font-size: 0.75rem;
        font-weight: 500;
    }
    .diff-stat-added {
        color: var(--color-status-green);
    }
    .diff-stat-removed {
        color: var(--color-status-red);
    }
    .diff-file-count {
        color: var(--color-text-tertiary);
        font-weight: 400;
    }
    .nav-btn {
        width: 24px;
        height: 24px;
        display: flex;
        align-items: center;
        justify-content: center;
        border: none;
        background: transparent;
        color: var(--color-text-secondary);
        border-radius: 4px;
        cursor: pointer;
        padding: 0;
    }
    .nav-btn:hover:not(.disabled) {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }
    .nav-btn:focus-visible:not(.disabled) {
        outline: 2px solid var(--color-accent);
        outline-offset: 1px;
    }
    .nav-btn.disabled {
        opacity: 0.3;
        cursor: default;
    }
    /* Fullscreen toggle: visible only on tablets (768–1024px) */
    .diff-fullscreen-toggle {
        display: none;
    }
    @media (min-width: 768px) and (max-width: 1024px) {
        .diff-fullscreen-toggle {
            display: flex;
        }
    }

    .close-btn {
        width: 24px;
        height: 24px;
        display: flex;
        align-items: center;
        justify-content: center;
        border: none;
        background: transparent;
        color: var(--color-text-secondary);
        border-radius: 4px;
        cursor: pointer;
        padding: 0;
        flex-shrink: 0;
    }
    .close-btn:hover {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }
    .close-btn:focus-visible {
        outline: 2px solid var(--color-accent);
        outline-offset: 1px;
    }

    /* Body */
    .diff-body {
        flex: 1;
        display: flex;
        overflow: hidden;
    }
    .diff-loading-center,
    .diff-empty-center {
        flex: 1;
        display: flex;
        align-items: center;
        justify-content: center;
        padding: 20px;
        color: var(--color-text-tertiary);
        font-size: 0.8125rem;
    }

    /* Files scroll area */
    .diff-files-scroll {
        flex: 1;
        overflow-y: auto;
        overflow-x: hidden;
        padding: 0 16px 24px 16px;
    }

    /* File section — card-like */
    .diff-file-section {
        margin-bottom: 16px;
        margin-top: 12px;
    }
    .diff-file-section:last-child {
        margin-bottom: 0;
    }

    /* File header — card header with rounded top, sticky */
    .diff-file-header {
        display: flex;
        align-items: center;
        gap: 8px;
        width: 100%;
        padding: 8px 12px;
        background: var(--color-surface-2);
        border: 1px solid var(--color-border);
        border-radius: 6px 6px 0 0;
        cursor: pointer;
        font-size: 0.8125rem;
        color: var(--color-text-secondary);
        text-align: left;
        position: sticky;
        top: 0;
        z-index: 5;
        opacity: 1;
    }

    /* Diff body: column headers + columns, shares border with file header */
    .diff-sbs-body {
        border: 1px solid var(--color-border);
        border-top: none;
        border-radius: 0 0 6px 6px;
        overflow: hidden;
    }
    .diff-file-header:hover {
        background: var(--color-surface-3);
    }
    .diff-file-header:focus-visible {
        outline: 2px solid var(--color-accent);
        outline-offset: -2px;
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
        flex-shrink: 0;
        font-size: 0.75rem;
        font-weight: 500;
    }
    .diff-stat-added {
        color: var(--color-status-green);
    }
    .diff-stat-removed {
        color: var(--color-status-red);
    }

    /* Binary notice */
    .diff-binary-notice {
        margin-top: 2px;
        border: 1px solid var(--color-border);
        border-top: none;
        border-radius: 0 0 6px 6px;
        padding: 12px 12px 12px 42px;
        color: var(--color-text-tertiary);
        font-size: 0.8125rem;
        font-style: italic;
    }

    /* Column headers row */
    .diff-sbs-header-row {
        display: flex;
        border-bottom: 1px solid var(--color-border);
        background: var(--color-surface-0);
    }
    .diff-sbs-col-header {
        flex: 1;
        padding: 5px 12px;
        font-size: 0.75rem;
        font-weight: 600;
        color: var(--color-text-tertiary);
        text-transform: uppercase;
        letter-spacing: 0.05em;
        background: var(--color-surface-0);
    }

    /* Two columns side by side */
    .diff-sbs-columns {
        display: flex;
        overflow: hidden;
        cursor: pointer;
    }
    .diff-sbs-column {
        flex: 1;
        min-width: 0;
        border-right: 1px solid var(--color-border);
        overflow: hidden;
    }
    .diff-sbs-column:last-child {
        border-right: none;
    }

    /* Scrollable content area per column */
    .diff-sbs-scroll {
        overflow-x: auto;
    }

    /* Individual line within a column */
    .diff-sbs-line {
        display: flex;
        font-size: 0.8125rem;
        font-family: var(--font-mono, monospace);
        line-height: 1.75rem;
    }
    .diff-line-num {
        width: 3.5rem;
        flex-shrink: 0;
        text-align: right;
        padding: 0 10px;
        color: var(--color-text-tertiary);
        opacity: 0.5;
        user-select: none;
        border-right: 1px solid var(--color-border);
    }
    .diff-line-content {
        flex: 1;
        min-width: 0;
        padding: 0 12px;
        white-space: pre;
        color: var(--color-text-secondary);
        cursor: text;
    }
    .diff-line-content.added {
        background: rgba(63, 185, 80, 0.15);
        color: var(--color-text-primary);
    }
    .diff-line-content.removed {
        background: rgba(248, 81, 73, 0.15);
        color: var(--color-text-primary);
    }
    .diff-line-content.context {
        color: var(--color-text-secondary);
    }

    /* Expand button for truncated files */
    .diff-expand-btn {
        display: block;
        width: 100%;
        padding: 8px 12px;
        background: var(--color-surface-2);
        border: none;
        border-radius: 0 0 6px 6px;
        color: var(--color-accent);
        font-size: 0.8125rem;
        font-weight: 500;
        cursor: pointer;
        text-align: center;
    }
    .diff-expand-btn:hover {
        background: var(--color-surface-3);
    }
    .diff-expand-btn:focus-visible {
        outline: 2px solid var(--color-accent);
        outline-offset: -2px;
    }

    @media (max-width: 768px) {
        .diff-header {
            flex-wrap: wrap;
            gap: 6px;
            padding: 6px 8px;
        }
        .diff-commit-date {
            display: none;
        }
    }

    /* Syntax highlighting — GitHub Dark palette (matches DiffViewer) */
    :global(.diff-line-content .hl-keyword) { color: #ff7b72; }
    :global(.diff-line-content .hl-constantkeyword) { color: #79c0ff; }
    :global(.diff-line-content .hl-definitionvariable,
        .diff-line-content .hl-variable,
        .diff-line-content .hl-classname,
        .diff-line-content .hl-typename) { color: #ffa657; }
    :global(.diff-line-content .hl-function) { color: #d2a8ff; }
    :global(.diff-line-content .hl-property,
        .diff-line-content .hl-attribute) { color: #79c0ff; }
    :global(.diff-line-content .hl-string,
        .diff-line-content .hl-attributevalue,
        .diff-line-content .hl-regexp) { color: #a5d6ff; }
    :global(.diff-line-content .hl-number) { color: #79c0ff; }
    :global(.diff-line-content .hl-comment) { color: #8b949e; font-style: italic; }
    :global(.diff-line-content .hl-operator) { color: #c9d1d9; }
    :global(.diff-line-content .hl-punctuation) { color: #c9d1d9; }
    :global(.diff-line-content .hl-tagname) { color: #7ee787; }
    :global(.diff-line-content .hl-meta) { color: #c9d1d9; }
    :global(.diff-line-content .hl-invalid) { color: #f85149; }
</style>
