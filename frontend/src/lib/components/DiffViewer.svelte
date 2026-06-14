<script lang="ts">
    import type { DiffFile } from '$lib/types/diff';
    import type { DiffDisplayItem, CollapsedBlock } from '$lib/utils/diff-parser';
    import { buildDisplayItems, truncatePath } from '$lib/utils/diff-parser';


    let props = $props();
    let files = $derived(props.files || []);
    let untracked = $derived(props.untracked || []);
    let hasContent = $derived(files.length > 0 || untracked.length > 0);

    $effect(() => {
        console.log('[DiffViewer] re-render: files=', files.length, 'untracked=', untracked.length);
    });

    // Track which collapsed blocks are expanded
    let expandedBlocks = $state<Record<string, boolean>>({});

    // Track which file sections are expanded (file headers)
    let expandedFiles = $state<Record<string, boolean>>({});

    function toggleBlock(id: string) {
        expandedBlocks = { ...expandedBlocks, [id]: !expandedBlocks[id] };
    }

    function toggleFile(path: string) {
        expandedFiles = { ...expandedFiles, [path]: !(expandedFiles[path] ?? true) };
    }

    function getDisplayItems(rows: DiffRow[]): DiffDisplayItem[] {
        return buildDisplayItems(rows);
    }

    function formatFileSize(bytes: number): string {
        if (bytes < 1024) return bytes + ' B';
        if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + ' KB';
        return (bytes / (1024 * 1024)).toFixed(1) + ' MB';
    }
</script>

{#if !hasContent}
    <div class="diff-empty">
        <svg width="24" height="24" viewBox="0 0 16 16" fill="none">
            <path d="M3 2h6l4 4v8a1 1 0 0 1-1 1H3a1 1 0 0 1-1-1V3a1 1 0 0 1 1-1z" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round" opacity="0.3"/>
            <path d="M9 2v4h4" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round" opacity="0.3"/>
        </svg>
        <p>No changes yet</p>
    </div>
{:else}
    <div class="diff-scroll">
        <div class="diff-content-wrapper">
        {#each untracked as entry (entry.path)}
            {@const ufile = entry.file}
            {@const isUntrackedExpanded = expandedFiles[entry.path] ?? true}
            {@const isLarge = entry.fileSize !== undefined}
            <section class="diff-file-section diff-file-section-untracked diff-file-enter">
                <button
                    type="button"
                    class="diff-file-header diff-file-header-untracked"
                    aria-expanded={isUntrackedExpanded}
                    onclick={() => toggleFile(entry.path)}
                >
                    {#if isUntrackedExpanded}
                        <svg class="diff-chevron" width="12" height="12" viewBox="0 0 16 16" fill="none">
                            <path d="M4 6l4 4 4-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                        </svg>
                    {:else}
                        <svg class="diff-chevron" width="12" height="12" viewBox="0 0 16 16" fill="none">
                            <path d="M6 4l4 4-4 4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                        </svg>
                    {/if}
                    <svg class="diff-untracked-icon" width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                        <path d="M8 1l2 3h3l-2.5 3 1 3.5L8 8 4.5 10.5l1-3.5L3 4h3z" fill="currentColor" opacity="0.6"/>
                    </svg>
                    <span class="diff-file-path" title={entry.path}>{truncatePath(entry.path)}</span>
                    {#if isLarge}
                        <span class="diff-file-stats">
                            <span class="diff-large-size">{formatFileSize(entry.fileSize)}</span>
                        </span>
                    {:else}
                        <span class="diff-file-stats">
                            <span class="diff-added">+{ufile.added}</span>
                        </span>
                    {/if}
                    <span class="diff-untracked-badge">untracked</span>
                </button>
                {#if isUntrackedExpanded}
                    {#if isLarge}
                        <div class="diff-file-content">
                            <div class="diff-large-file-notice">
                                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                                    <path d="M4 2h8a2 2 0 012 2v8a2 2 0 01-2 2H4a2 2 0 01-2-2V4a2 2 0 012-2z" stroke="currentColor" stroke-width="1.2"/>
                                    <path d="M6 6l4 4M10 6l-4 4" stroke="currentColor" stroke-width="1.2" stroke-linecap="round"/>
                                </svg>
                                <span>Binary or large file: {formatFileSize(entry.fileSize)}</span>
                            </div>
                        </div>
                    {:else if ufile.rows.length > 0}
                        {@const uitems = getDisplayItems(ufile.rows)}
                        <div class="diff-file-content">
                            {#each uitems as item}
                                {#if item.type === 'row'}
                                    <div class={`diff-row diff-row-${item.row.variant}`}>
                                        <span class="diff-line-num">
                                            <span class="diff-line-num-text">{item.row.lineNumber ?? ''}</span>
                                        </span>
                                        <span class="diff-line-content">
                                            {#if item.row.variant === 'added'}
                                                <span class="diff-prefix">+</span>
                                            {:else if item.row.variant === 'removed'}
                                                <span class="diff-prefix">-</span>
                                            {:else}
                                                <span class="diff-prefix"> </span>
                                            {/if}
                                            <span class="diff-text">{item.row.text || ' '}</span>
                                        </span>
                                    </div>
                                {:else if item.type === 'collapsed'}
                                    <button
                                        type="button"
                                        class="diff-collapse-btn"
                                        aria-expanded={expandedBlocks[item.block.id]}
                                        onclick={() => toggleBlock(item.block.id)}
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
                                        {#each item.block.rows as row}
                                            <div class="diff-row diff-row-context">
                                                <span class="diff-line-num">
                                                    <span class="diff-line-num-text">{row.lineNumber ?? ''}</span>
                                                </span>
                                                <span class="diff-line-content">
                                                    <span class="diff-prefix"> </span>
                                                    <span class="diff-text">{row.text || ' '}</span>
                                                </span>
                                            </div>
                                        {/each}
                                    {/if}
                                {/if}
                            {/each}
                        </div>
                    {/if}
                {/if}
            </section>
        {/each}
        {#each files as file (file.path)}
            {@const isFileExpanded = expandedFiles[file.path] ?? true}
            <section class="diff-file-section diff-file-enter">
                <button
                    type="button"
                    class="diff-file-header"
                    aria-expanded={isFileExpanded}
                    onclick={() => toggleFile(file.path)}
                >
                    {#if isFileExpanded}
                        <svg class="diff-chevron" width="12" height="12" viewBox="0 0 16 16" fill="none">
                            <path d="M4 6l4 4 4-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                        </svg>
                    {:else}
                        <svg class="diff-chevron" width="12" height="12" viewBox="0 0 16 16" fill="none">
                            <path d="M6 4l4 4-4 4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                        </svg>
                    {/if}
                    <span class="diff-file-path" title={file.path}>{truncatePath(file.path)}</span>
                    <span class="diff-file-stats">
                        <span class="diff-added">+{file.added}</span>
                        <span class="diff-removed">-{file.removed}</span>
                    </span>
                </button>
                {#if isFileExpanded}
                    {#if file.fileSize}
                        <div class="diff-file-content">
                            <div class="diff-large-file-notice">
                                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                                    <path d="M4 2h8a2 2 0 012 2v8a2 2 0 01-2 2H4a2 2 0 01-2-2V4a2 2 0 012-2z" stroke="currentColor" stroke-width="1.2"/>
                                    <path d="M6 6l4 4M10 6l-4 4" stroke="currentColor" stroke-width="1.2" stroke-linecap="round"/>
                                </svg>
                                <span>Binary or large file: {formatFileSize(file.fileSize)}</span>
                            </div>
                        </div>
                    {:else}
                        {@const items = getDisplayItems(file.rows)}
                        <div class="diff-file-content">
                            {#each items as item}
                                {#if item.type === 'row'}
                                    <div class={`diff-row diff-row-${item.row.variant}`}>
                                        <span class="diff-line-num">
                                            <span class="diff-line-num-text">{item.row.lineNumber ?? ''}</span>
                                        </span>
                                        <span class="diff-line-content">
                                            {#if item.row.variant === 'added'}
                                                <span class="diff-prefix">+</span>
                                            {:else if item.row.variant === 'removed'}
                                                <span class="diff-prefix">-</span>
                                            {:else}
                                                <span class="diff-prefix"> </span>
                                            {/if}
                                            <span class="diff-text">{item.row.text || ' '}</span>
                                        </span>
                                    </div>
                                {:else if item.type === 'collapsed'}
                                    <button
                                        type="button"
                                        class="diff-collapse-btn"
                                        aria-expanded={expandedBlocks[item.block.id]}
                                        onclick={() => toggleBlock(item.block.id)}
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
                                        {#each item.block.rows as row}
                                            <div class="diff-row diff-row-context">
                                                <span class="diff-line-num">
                                                    <span class="diff-line-num-text">{row.lineNumber ?? ''}</span>
                                                </span>
                                                <span class="diff-line-content">
                                                    <span class="diff-prefix"> </span>
                                                    <span class="diff-text">{row.text || ' '}</span>
                                                </span>
                                            </div>
                                        {/each}
                                    {/if}
                                {/if}
                            {/each}
                        </div>
                    {/if}
                {/if}
            </section>
        {/each}
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
        min-height: 0;
        padding: 12px 8px 12px 8px;
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
        padding: 6px 8px;
        background: var(--color-surface-2);
        border: 1px solid var(--color-border);
        border-radius: 6px;
        cursor: pointer;
        font-size: 0.8125rem;
        color: var(--color-text-secondary);
        text-align: left;
    }
    .diff-file-header:hover {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }

    .diff-chevron {
        flex-shrink: 0;
        color: var(--color-text-tertiary);
    }

    .diff-file-path {
        flex: 1;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
        font-family: var(--font-mono);
        font-size: 0.75rem;
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

    /* File content */
    .diff-file-content {
        margin-top: 2px;
        border: 1px solid var(--color-border);
        border-top: none;
        border-radius: 0 0 6px 6px;
        overflow: hidden;
    }

    /* Diff rows */
    .diff-row {
        display: grid;
        grid-template-columns: 2rem 1fr;
        font-family: var(--font-mono);
        font-size: 0.75rem;
        line-height: 1.5;
        min-height: 1.5rem;
    }
    .diff-line-num {
        display: flex;
        justify-content: flex-end;
        padding-right: 8px;
        user-select: none;
        color: var(--color-text-tertiary);
        background: var(--color-surface-1);
    }
    .diff-line-num-text {
        font-size: 0.75rem;
    }
    .diff-line-content {
        display: flex;
        padding: 0 8px;
        white-space: pre;
        overflow-x: auto;
    }
    .diff-prefix {
        flex-shrink: 0;
        width: 1rem;
        user-select: none;
        padding-right: 8px;
    }
    .diff-text {
        white-space: pre;
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
    .diff-large-size {
        color: var(--color-text-tertiary);
        font-size: 0.75rem;
        font-weight: 500;
    }

    /* Untracked file entries */
    .diff-file-header-untracked {
        background: rgba(250,204,21,0.08);
    }
    .diff-untracked-icon {
        flex-shrink: 0;
        color: rgba(250,204,21,0.8);
    }
    .diff-untracked-badge {
        font-size: 0.75rem;
        padding: 1px 6px;
        border-radius: 4px;
        background: rgba(250,204,21,0.15);
        color: rgba(250,204,21,0.9);
        font-weight: 500;
        flex-shrink: 0;
    }
</style>
