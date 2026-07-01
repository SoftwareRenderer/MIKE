<script lang="ts">
    import type { GitCommit } from '$lib/types';

    let { projectId, currentBranch, commits, loading, onCommitSelected, selectedCommitHash }: {
        projectId: string;
        currentBranch: string;
        commits: GitCommit[];
        loading: boolean;
        onCommitSelected?: (commit: GitCommit, index: number) => void;
        selectedCommitHash?: string;
    } = $props();

    function formatDate(dateStr: string): string {
        if (!dateStr) return '';
        try {
            const date = new Date(dateStr);
            const now = new Date();
            const diffMs = now.getTime() - date.getTime();
            const diffDays = Math.floor(diffMs / (1000 * 60 * 60 * 24));
            if (diffDays === 0) return 'today';
            if (diffDays === 1) return 'yesterday';
            if (diffDays < 7) return `${diffDays}d ago`;
            if (diffDays < 30) return `${Math.floor(diffDays / 7)}w ago`;
            if (diffDays < 365) return `${Math.floor(diffDays / 30)}mo ago`;
            return `${Math.floor(diffDays / 365)}y ago`;
        } catch {
            return dateStr.slice(0, 10);
        }
    }
</script>

<div class="git-graph">
    <div class="git-graph-header">
        <span class="git-graph-label">History</span>
    </div>

    <div class="git-graph-list" role="list" aria-label="Commit history">
        {#if loading}
            <div class="git-graph-loading">Loading commits...</div>
        {:else if commits.length === 0}
            <div class="git-graph-empty">No commits</div>
        {:else}
            {#each commits as commit, i}
                <div
                    class="git-commit-row"
                    class:selectable={!!onCommitSelected}
                    class:selected={commit.hash === selectedCommitHash}
                    role={onCommitSelected ? 'button' : 'listitem'}
                    tabindex={onCommitSelected ? 0 : undefined}
                    onclick={() => onCommitSelected?.(commit, i)}
                    onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onCommitSelected?.(commit, i); } }}
                >
                    <!-- Timeline line -->
                    <div class="git-timeline">
                        <div class="git-dot" title={commit.short}></div>
                    </div>

                    <!-- Commit info -->
                    <div class="git-commit-info">
                        <div class="git-commit-subject" title={commit.subject}>{commit.subject}</div>
                        <div class="git-commit-meta">
                            <span class="git-commit-hash" title={commit.hash}>{commit.short}</span>
                            {#if commit.author}
                                <span class="git-commit-author" title={commit.author}>{commit.author}</span>
                            {/if}
                            {#if commit.date}
                                <span class="git-commit-date">{formatDate(commit.date)}</span>
                            {/if}
                        </div>
                    </div>
                </div>
            {/each}
        {/if}
    </div>
</div>

<style>
    .git-graph {
        display: flex;
        flex-direction: column;
        height: 100%;
        overflow: hidden;
    }

    .git-graph-header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 6px 10px;
        border-bottom: 1px solid var(--color-border);
        flex-shrink: 0;
        gap: 6px;
    }

    .git-graph-label {
        font-size: 0.75rem;
        font-weight: 600;
        color: var(--color-text-secondary);
        text-transform: uppercase;
        letter-spacing: 0.05em;
    }

    /* Git graph list */
    .git-graph-list {
        flex: 1;
        overflow-y: auto;
        overflow-x: hidden;
        padding: 4px 0;
    }

    .git-graph-loading,
    .git-graph-empty {
        padding: 8px 10px;
        color: var(--color-text-tertiary);
        font-size: 0.75rem;
    }

    .git-commit-row {
        display: flex;
        align-items: flex-start;
        gap: 6px;
        padding: 3px 8px;
        cursor: default;
    }
    .git-commit-row:hover {
        background: var(--color-surface-2);
    }
    .git-commit-row.selectable {
        cursor: pointer;
    }
    .git-commit-row.selectable:hover {
        background: var(--color-surface-3);
    }
    .git-commit-row.selectable:focus-visible {
        outline: 2px solid var(--color-accent);
        outline-offset: -2px;
    }
    .git-commit-row.selected {
        background: var(--color-accent)15;
    }
    .git-commit-row.selected .git-dot {
        background: var(--color-accent);
        box-shadow: 0 0 0 3px var(--color-accent)33;
    }
    .git-commit-row.selected .git-commit-subject {
        color: var(--color-accent);
        font-weight: 500;
    }

    .git-timeline {
        align-self: stretch;
        display: flex;
        align-items: center;
        justify-content: center;
        position: relative;
        flex-shrink: 0;
        width: 12px;
    }
    .git-timeline::before {
        content: '';
        position: absolute;
        top: -3px;
        left: 50%;
        transform: translateX(-50%);
        width: 1.5px;
        height: calc(100% + 6px);
        background: rgba(163, 113, 247, 0.3);
    }

    .git-dot {
        width: 8px;
        height: 8px;
        border-radius: 50%;
        background: var(--color-status-purple);
        flex-shrink: 0;
        z-index: 1;
        transition: all 0.15s ease;
    }
    .git-commit-row.selected .git-dot {
        width: 0;
        height: 0;
        border-radius: 0;
        background: transparent;
        border-left: 8px solid var(--color-accent);
        border-top: 5px solid transparent;
        border-bottom: 5px solid transparent;
    }

    .git-commit-info {
        flex: 1;
        min-width: 0;
        padding-right: 4px;
    }

    .git-commit-subject {
        font-size: 0.75rem;
        color: var(--color-text-primary);
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
        line-height: 1.3;
    }

    .git-commit-meta {
        display: flex;
        align-items: center;
        gap: 6px;
        margin-top: 1px;
    }

    .git-commit-hash {
        font-size: 0.75rem;
        font-family: var(--font-mono);
        color: var(--color-text-tertiary);
    }

    .git-commit-author {
        font-size: 0.75rem;
        color: var(--color-text-tertiary);
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
        flex: 0 1 auto;
        min-width: 0;
    }
    .git-commit-author::before {
        content: '·';
        margin-right: 6px;
        opacity: 0.5;
    }

    .git-commit-date {
        font-size: 0.75rem;
        color: var(--color-text-tertiary);
        opacity: 0.7;
    }
</style>
