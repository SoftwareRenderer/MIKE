<script lang="ts">
    import type { GitCommit } from '$lib/types';

    let { projectId, currentBranch, commits, loading, branches, loadingBranches, checkoutLoading, onCheckout, onBranchChange }: {
        projectId: string;
        currentBranch: string;
        commits: GitCommit[];
        loading: boolean;
        branches: string[];
        loadingBranches: boolean;
        checkoutLoading: boolean;
        onCheckout: (branch: string) => void;
        onBranchChange: (branch: string) => void;
    } = $props();

    let showDropdown = $state(false);
    let dropdownEl: HTMLDivElement | null = null;

    function toggleDropdown() {
        showDropdown = !showDropdown;
    }

    function handleBranchSelect(branch: string) {
        showDropdown = false;
        if (branch !== currentBranch) {
            onCheckout(branch);
        }
    }

    // Close dropdown on outside click
    $effect(() => {
        function handleClick(e: MouseEvent) {
            if (dropdownEl && !dropdownEl.contains(e.target as Node)) {
                showDropdown = false;
            }
        }
        if (showDropdown) {
            document.addEventListener('click', handleClick);
            return () => document.removeEventListener('click', handleClick);
        }
    });

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

        <!-- Branch selector dropdown -->
        <div class="branch-selector" class:open={showDropdown} bind:this={dropdownEl}>
            <button
                class="branch-dropdown-btn"
                onclick={toggleDropdown}
                title="Switch branch"
                disabled={checkoutLoading}
            >
                <svg width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                    <circle cx="6" cy="6" r="2.5"/>
                    <circle cx="10" cy="10" r="2.5"/>
                    <line x1="7.8" y1="7.8" x2="8.2" y2="8.2"/>
                    <line x1="6" y1="3.5" x2="6" y2="2"/>
                    <line x1="10" y1="12.5" x2="10" y2="14"/>
                </svg>
                <span class="branch-name">{currentBranch}</span>
                <svg width="10" height="10" viewBox="0 0 10 10" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                    <polyline points="2,3.5 5,6.5 8,3.5"/>
                </svg>
            </button>

            {#if showDropdown}
                <div class="branch-dropdown-list" role="listbox">
                    {#if loadingBranches}
                        <div class="branch-dropdown-item loading">Loading...</div>
                    {:else}
                        {#each branches as branch}
                            <button
                                class="branch-dropdown-item"
                                class:active={branch === currentBranch}
                                onclick={() => handleBranchSelect(branch)}
                                role="option"
                                aria-selected={branch === currentBranch}
                            >
                                {#if branch === currentBranch}
                                    <svg width="10" height="10" viewBox="0 0 10 10" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                                        <polyline points="1.5,5 4,7.5 8.5,2.5"/>
                                    </svg>
                                {:else}
                                    <span class="branch-check-empty"></span>
                                {/if}
                                <span>{branch}</span>
                            </button>
                        {/each}
                        {#if branches.length === 0}
                            <div class="branch-dropdown-item empty">No branches</div>
                        {/if}
                    {/if}
                </div>
            {/if}
        </div>
    </div>

    <div class="git-graph-list" role="list" aria-label="Commit history">
        {#if loading}
            <div class="git-graph-loading">Loading commits...</div>
        {:else if commits.length === 0}
            <div class="git-graph-empty">No commits</div>
        {:else}
            {#each commits as commit, i}
                <div class="git-commit-row" role="listitem">
                    <!-- Timeline line -->
                    <div class="git-timeline">
                        <div class="git-dot" title={commit.short}></div>
                        {#if i < commits.length - 1}
                            <div class="git-line"></div>
                        {/if}
                    </div>

                    <!-- Commit info -->
                    <div class="git-commit-info">
                        <div class="git-commit-subject" title={commit.subject}>{commit.subject}</div>
                        <div class="git-commit-meta">
                            <span class="git-commit-hash" title={commit.hash}>{commit.short}</span>
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

    /* Branch selector */
    .branch-selector {
        position: relative;
    }

    .branch-dropdown-btn {
        display: flex;
        align-items: center;
        gap: 3px;
        padding: 2px 5px;
        border-radius: var(--radius-sm);
        background: var(--color-surface-3);
        color: var(--color-text-secondary);
        font-size: 0.75rem;
        font-family: var(--font-mono);
        cursor: pointer;
        border: 1px solid transparent;
    }
    .branch-dropdown-btn:hover {
        background: var(--color-surface-4);
        color: var(--color-text-primary);
        opacity: 1;
    }
    .branch-dropdown-btn:disabled {
        opacity: 0.5;
        cursor: not-allowed;
    }

    .branch-name {
        max-width: 80px;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }

    .branch-dropdown-list {
        position: absolute;
        top: 100%;
        right: 0;
        z-index: 100;
        min-width: 160px;
        max-height: 240px;
        overflow-y: auto;
        background: var(--color-surface-2);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-md);
        box-shadow: 0 4px 12px rgba(0, 0, 0, 0.3);
        margin-top: 2px;
    }

    .branch-dropdown-item {
        display: flex;
        align-items: center;
        gap: 6px;
        width: 100%;
        padding: 5px 8px;
        border: none;
        background: transparent;
        color: var(--color-text-primary);
        font-size: 0.75rem;
        text-align: left;
        cursor: pointer;
    }
    .branch-dropdown-item:hover {
        background: var(--color-surface-3);
        opacity: 1;
    }
    .branch-dropdown-item.active {
        color: var(--color-status-purple);
    }
    .branch-dropdown-item.loading,
    .branch-dropdown-item.empty {
        color: var(--color-text-tertiary);
        cursor: default;
    }
    .branch-check-empty {
        display: inline-block;
        width: 10px;
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

    .git-timeline {
        display: flex;
        flex-direction: column;
        align-items: center;
        padding-top: 3px;
        position: relative;
        flex-shrink: 0;
        width: 12px;
    }

    .git-dot {
        width: 8px;
        height: 8px;
        border-radius: 50%;
        background: var(--color-status-purple);
        flex-shrink: 0;
        z-index: 1;
    }

    .git-line {
        width: 1.5px;
        flex: 1;
        min-height: 12px;
        background: rgba(163, 113, 247, 0.3);
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
        gap: 4px;
        margin-top: 1px;
    }

    .git-commit-hash {
        font-size: 0.75rem;
        font-family: var(--font-mono);
        color: var(--color-text-tertiary);
    }

    .git-commit-date {
        font-size: 0.75rem;
        color: var(--color-text-tertiary);
        opacity: 0.7;
    }
</style>
