<script lang="ts">
    import type { GitCommit } from '$lib/types';
    import type { DiffFile, DiffRow } from '$lib/types/diff';
    import { parseUnifiedDiff, buildSideBySideRows } from '$lib/utils/diff-parser';
    import type { SideBySideRow } from '$lib/utils/diff-parser';

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
    }

    let { commit, diff, loading, projectPath, onClose, onNavigate, onFileSelected, hasPrev, hasNext }: Props = $props();

    // Parse the unified diff into structured files
    let diffFiles = $derived<DiffFile[]>(parseUnifiedDiff(diff));
    let hasContent = $derived(diffFiles.length > 0);

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

    // Build side-by-side rows for a given file's rows
    function getSideBySideRows(rows: DiffRow[]): SideBySideRow[] {
        return buildSideBySideRows(rows);
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
                <span class="diff-commit-subject" title={commit.subject}>{commit.subject}</span>
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
            <button
                class="nav-btn"
                class:disabled={!hasPrev}
                onclick={() => hasPrev && onNavigate('prev')}
                title="Previous commit"
                disabled={!hasPrev}
            >
                <svg width="12" height="12" viewBox="0 0 12 12" fill="none">
                    <path d="M7 2L3 6l4 4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                </svg>
            </button>
            <button
                class="nav-btn"
                class:disabled={!hasNext}
                onclick={() => hasNext && onNavigate('next')}
                title="Next commit"
                disabled={!hasNext}
            >
                <svg width="12" height="12" viewBox="0 0 12 12" fill="none">
                    <path d="M5 2l4 4-4 4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                </svg>
            </button>
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
                        <!-- File header (collapsible) -->
                        <button
                            type="button"
                            class="diff-file-header"
                            aria-expanded={!collapsed}
                            onclick={() => toggleFile(file.path)}
                        >
                            {#if collapsed}
                                <svg class="diff-chevron" width="12" height="12" viewBox="0 0 16 16" fill="none">
                                    <path d="M6 4l4 4-4 4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                                </svg>
                            {:else}
                                <svg class="diff-chevron" width="12" height="12" viewBox="0 0 16 16" fill="none">
                                    <path d="M4 6l4 4 4-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                                </svg>
                            {/if}
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
                                <div class="diff-sbs-wrapper" onclick={() => handleFileClick(file.path)}>
                                    <div class="diff-sbs-grid">
                                        <!-- Column headers -->
                                        <div class="diff-sbs-header-row">
                                            <div class="diff-sbs-col-header">
                                                <span class="diff-sbs-label">Old</span>
                                            </div>
                                            <div class="diff-sbs-col-header">
                                                <span class="diff-sbs-label">New</span>
                                            </div>
                                        </div>

                                        <!-- Side-by-side rows -->
                                        {#each getSideBySideRows(file.rows) as row}
                                            <div class="diff-sbs-row">
                                                <!-- Left column (old) -->
                                                <div class="diff-sbs-cell diff-sbs-left">
                                                    <span class="diff-line-num">{row.leftLineNum ?? ''}</span>
                                                    <span class="diff-line-content" class:removed={row.leftVariant === 'removed'} class:context={row.leftVariant === 'context'}>
                                                        {row.leftText}
                                                    </span>
                                                </div>
                                                <!-- Right column (new) -->
                                                <div class="diff-sbs-cell diff-sbs-right">
                                                    <span class="diff-line-num">{row.rightLineNum ?? ''}</span>
                                                    <span class="diff-line-content" class:added={row.rightVariant === 'added'} class:context={row.rightVariant === 'context'}>
                                                        {row.rightText}
                                                    </span>
                                                </div>
                                            </div>
                                        {/each}
                                    </div>
                                </div>
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
    }
    .diff-commit-subject {
        font-size: 0.8125rem;
        font-weight: 500;
        color: var(--color-text-primary);
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
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
        padding: 12px 16px 24px 16px;
    }

    /* File section — card-like */
    .diff-file-section {
        margin-bottom: 16px;
    }

    /* File header — card header with rounded top */
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
        z-index: 10;
        opacity: 1;
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

    /* Side-by-side wrapper — card body with rounded bottom */
    .diff-sbs-wrapper {
        margin-top: 2px;
        border: 1px solid var(--color-border);
        border-top: none;
        border-radius: 0 0 6px 6px;
        overflow-x: auto;
        overflow-y: hidden;
        cursor: pointer;
    }
    .diff-sbs-wrapper:hover .diff-line-content {
        opacity: 0.85;
    }

    /* Single grid for header + rows — columns stay aligned */
    .diff-sbs-grid {
        display: grid;
        grid-template-columns: 1fr 1fr;
        min-inline-size: max-content;
    }

    /* Column headers row */
    .diff-sbs-header-row {
        display: contents;
    }
    .diff-sbs-col-header {
        padding: 5px 12px;
        font-size: 0.75rem;
        font-weight: 600;
        color: var(--color-text-tertiary);
        text-transform: uppercase;
        letter-spacing: 0.05em;
        border-right: 1px solid var(--color-border);
        border-bottom: 1px solid var(--color-border);
        background: var(--color-surface-0);
    }
    .diff-sbs-col-header:last-child {
        border-right: none;
    }

    /* Side-by-side rows */
    .diff-sbs-row {
        display: contents;
    }
    .diff-sbs-cell {
        display: flex;
        font-size: 0.8125rem;
        font-family: var(--font-mono, monospace);
        line-height: 1.75rem;
        border-right: 1px solid var(--color-border);
    }
    .diff-sbs-cell:last-child {
        border-right: none;
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
        transition: opacity 0.1s ease;
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
</style>
