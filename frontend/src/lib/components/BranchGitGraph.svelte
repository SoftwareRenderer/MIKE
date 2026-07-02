<script lang="ts">
    import type { GitCommit } from '$lib/types';

    let { projectId, currentBranch, commits, originTip, loading, onCommitSelected, selectedCommitHash }: {
        projectId: string;
        currentBranch: string;
        commits: GitCommit[];
        originTip: string; // full commit hash where origin/<branch> points
        loading: boolean;
        onCommitSelected?: (commit: GitCommit, index: number) => void;
        selectedCommitHash?: string;
    } = $props();

    // Set of commit hashes that are pushed (at or before origin tip)
    let pushedHashes = $derived.by(() => {
        const s = new Set<string>();
        if (originTip) {
            const tipIndex = commits.findIndex(c => c.hash === originTip);
            if (tipIndex >= 0) {
                for (let i = tipIndex; i < commits.length; i++) {
                    s.add(commits[i]!.hash);
                }
            }
        }
        return s;
    });

    function formatDate(dateStr: string): string {
        if (!dateStr) return '';
        try {
            const date = new Date(dateStr);
            const now = new Date();
            const diffMs = now.getTime() - date.getTime();
            const diffMinutes = Math.floor(diffMs / (1000 * 60));
            if (diffMinutes < 1) return 'just now';
            if (diffMinutes < 60) return `${diffMinutes}m ago`;
            const diffHours = Math.floor(diffMs / (1000 * 60 * 60));
            if (diffHours < 24) return `${diffHours}h ago`;
            const diffDays = Math.floor(diffMs / (1000 * 60 * 60 * 24));
            if (diffDays === 1) return 'yesterday';
            if (diffDays < 7) return `${diffDays}d ago`;
            if (diffDays < 30) return `${Math.floor(diffDays / 7)}w ago`;
            if (diffDays < 365) return `${Math.floor(diffDays / 30)}mo ago`;
            return `${Math.floor(diffDays / 365)}y ago`;
        } catch {
            return dateStr.slice(0, 10);
        }
    }

    /** Get the primary branch name for a commit (current branch first, then first in list). */
    function getPrimaryBranch(commit: GitCommit): string | null {
        if (!commit.branches || commit.branches.length === 0) return null;
        const current = commit.branches.find(b => b === currentBranch);
        if (current) return current;
        return commit.branches[0] || null;
    }

    /** Check if this commit is the origin tip. */
    function isOriginTip(commit: GitCommit): boolean {
        return originTip && commit.hash === originTip;
    }

    /** Check if this commit is unpushed (not at or before origin tip, or no origin configured). */
    function isUnpushed(commit: GitCommit): boolean {
        if (!originTip) return true;
        return !pushedHashes.has(commit.hash);
    }

    /** Check if this commit has the current branch. */
    function hasCurrentBranch(commit: GitCommit): boolean {
        return commit.branches?.includes(currentBranch) ?? false;
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
                    class:unpushed={isUnpushed(commit)}
                    role={onCommitSelected ? 'button' : 'listitem'}
                    tabindex={onCommitSelected ? 0 : undefined}
                    onclick={() => onCommitSelected?.(commit, i)}
                    onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onCommitSelected?.(commit, i); } }}
                >
                    <!-- Timeline line -->
                    <div class="git-timeline">
                        <div
                            class="git-dot"
                            class:unpushed={isUnpushed(commit)}
                            title={commit.short}
                        ></div>
                    </div>

                    <!-- Commit info -->
                    <div class="git-commit-info">
                        <!-- Subject + badges on one line -->
                        <div class="git-commit-line">
                            <span class="git-commit-subject" title={commit.subject}>{commit.subject}</span>

                            <!-- Origin tip badge — shown before branch badge -->
                            {#if isOriginTip(commit)}
                                <span class="git-badge origin-badge" title="Pushed to origin">
                                    <svg width="8" height="8" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                                        <circle cx="6" cy="6" r="2"/>
                                        <circle cx="10" cy="10" r="2"/>
                                        <line x1="7.5" y1="7.5" x2="8.5" y2="8.5"/>
                                        <line x1="6" y1="4" x2="6" y2="2"/>
                                        <line x1="10" y1="12" x2="10" y2="14"/>
                                    </svg>
                                    origin
                                </span>
                            {/if}

                            <!-- Branch badge -->
                            {#if getPrimaryBranch(commit)}
                                <span class="git-badge branch-badge" class:current={hasCurrentBranch(commit)} title="Branch">
                                    <svg width="8" height="8" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                                        <circle cx="6" cy="6" r="2"/>
                                        <circle cx="10" cy="10" r="2"/>
                                        <line x1="7.5" y1="7.5" x2="8.5" y2="8.5"/>
                                        <line x1="6" y1="4" x2="6" y2="2"/>
                                        <line x1="10" y1="12" x2="10" y2="14"/>
                                    </svg>
                                    {getPrimaryBranch(commit)}
                                </span>
                            {/if}
                        </div>

                        <div class="git-commit-meta">
                            {#if commit.author}
                                <span class="git-commit-author" title={commit.author}>{commit.author}</span>
                            {/if}
                            {#if commit.date}
                                <span class="git-commit-date">{formatDate(commit.date)}</span>
                            {/if}
                            <span class="git-commit-hash" title={commit.hash}>{commit.short}</span>
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
    .git-commit-row.selected .git-commit-subject {
        color: var(--color-accent);
        font-weight: 500;
    }

    /* Timeline */
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
    /* Blue timeline line for unpushed rows */
    .git-commit-row.unpushed .git-timeline::before {
        background: rgba(76, 154, 255, 0.3);
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
    .git-dot.unpushed {
        background: var(--color-status-blue);
    }
    .git-commit-row.selected .git-dot,
    .git-commit-row.selected .git-dot.unpushed {
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

    /* Subject + badges on one line */
    .git-commit-line {
        display: flex;
        align-items: center;
        gap: 4px;
        min-width: 0;
    }

    .git-commit-subject {
        font-size: 0.75rem;
        color: var(--color-text-primary);
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
        line-height: 1.4;
        flex: 1 1 auto;
        min-width: 0;
    }

    /* Badges — inline, never wrap */
    .git-badge {
        display: inline-flex;
        align-items: center;
        gap: 2px;
        padding: 1px 5px;
        border-radius: 9999px;
        font-size: 0.625rem;
        font-family: var(--font-mono);
        flex-shrink: 0;
        line-height: 1;
    }

    /* Branch badge — default grey */
    .git-badge.branch-badge {
        color: var(--color-text-secondary);
        background: color-mix(in srgb, white 8%, transparent);
    }
    /* Branch badge — current branch is blue */
    .git-badge.branch-badge.current {
        color: var(--color-status-blue);
        background: color-mix(in srgb, var(--color-status-blue) 15%, transparent);
    }

    /* Origin badge — purple with branch icon */
    .git-badge.origin-badge {
        color: var(--color-status-purple);
        background: color-mix(in srgb, var(--color-status-purple) 15%, transparent);
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
        margin-left: auto;
        flex-shrink: 0;
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
    .git-commit-author::after {
        content: '·';
        margin-left: 6px;
        opacity: 0.5;
    }

    .git-commit-date {
        font-size: 0.75rem;
        color: var(--color-text-tertiary);
        opacity: 0.7;
    }
</style>
