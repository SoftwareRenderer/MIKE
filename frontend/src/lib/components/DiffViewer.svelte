<script lang="ts">
    import type { DiffFile } from '$lib/types/diff';
    import type { DiffDisplayItem, CollapsedBlock } from '$lib/utils/diff-parser';
    import { buildDisplayItems, truncatePath } from '$lib/utils/diff-parser';

    let props = $props();
    let files = $derived(props.files || []);
    let isLoading = $derived(props.loading || false);

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
</script>

{#if isLoading}
    <div class="diff-loading">Loading diff...</div>
{:else if files.length === 0}
    <div class="diff-empty">
        <svg width="24" height="24" viewBox="0 0 16 16" fill="none">
            <path d="M4 2v12M12 2v12M4 6h3l-1 2 1 2H4M12 6H9l1 2-1 2h3" stroke="currentColor" stroke-width="1" stroke-linecap="round" stroke-linejoin="round" opacity="0.3"/>
        </svg>
        <p>No changes yet</p>
    </div>
{:else}
    <div class="diff-scroll">
        {#each files as file (file.path)}
            {@const isFileExpanded = expandedFiles[file.path] ?? true}
            <section class="diff-file-section">
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
            </section>
        {/each}
    </div>
{/if}

<style>
    .diff-scroll {
        flex: 1;
        overflow-y: auto;
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
        text-align: center;
    }
    .diff-empty p {
        font-size: 0.875rem;
        margin: 0;
        font-weight: 500;
    }

    .diff-loading {
        display: flex;
        align-items: center;
        justify-content: center;
        flex: 1;
        color: var(--color-text-tertiary);
        font-size: 0.875rem;
    }

    .diff-file-section {
        margin-bottom: 8px;
    }

    .diff-file-header {
        display: flex;
        align-items: center;
        gap: 6px;
        width: 100%;
        padding: 6px 8px;
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        border-radius: 6px 6px 0 0;
        cursor: pointer;
        text-align: left;
        font: inherit;
        color: var(--color-text-primary);
        transition: background 0.1s ease;
    }
    .diff-file-header:hover {
        background: var(--color-surface-3);
    }
    .diff-file-header + .diff-file-content {
        border-radius: 0 0 6px 6px;
    }

    .diff-chevron {
        flex-shrink: 0;
        color: var(--color-text-tertiary);
    }

    .diff-file-path {
        flex: 1;
        min-width: 0;
        font-size: 0.75rem;
        font-family: var(--font-mono);
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }

    .diff-file-stats {
        display: flex;
        align-items: center;
        gap: 6px;
        flex-shrink: 0;
        font-size: 0.75rem;
        font-weight: 500;
    }
    .diff-added {
        color: #3fb950;
    }
    .diff-removed {
        color: #f85149;
    }

    .diff-file-content {
        border: 1px solid var(--color-border);
        border-top: none;
        background: var(--color-surface-1);
        overflow: hidden;
    }

    .diff-row {
        display: grid;
        grid-template-columns: 2rem 1fr;
        font-size: 0.75rem;
        font-family: var(--font-mono);
        line-height: 1.5;
        min-height: 1.5em;
    }

    .diff-line-num {
        display: flex;
        justify-content: flex-end;
        padding-right: 8px;
        user-select: none;
        color: var(--color-text-tertiary);
        background: var(--color-surface-2);
        border-right: 1px solid var(--color-border);
    }
    .diff-line-num-text {
        font-size: 0.7rem;
        opacity: 0.6;
    }

    .diff-line-content {
        display: flex;
        align-items: flex-start;
        padding: 0 8px;
        white-space: pre;
        overflow-x: auto;
    }

    .diff-prefix {
        flex-shrink: 0;
        user-select: none;
        font-weight: 600;
    }

    .diff-text {
        white-space: pre;
    }

    /* Row variants */
    .diff-row-added {
        background: rgba(63, 185, 80, 0.15);
    }
    .diff-row-added .diff-prefix {
        color: #3fb950;
    }

    .diff-row-removed {
        background: rgba(248, 81, 73, 0.15);
    }
    .diff-row-removed .diff-prefix {
        color: #f85149;
    }

    .diff-row-context {
        background: transparent;
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
        padding: 3px 8px;
        background: var(--color-surface-0);
        border: none;
        cursor: pointer;
        text-align: left;
        font-size: 0.7rem;
        font-family: var(--font-mono);
        color: var(--color-text-tertiary);
        transition: background 0.1s ease;
    }
    .diff-collapse-btn:hover {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }
</style>
