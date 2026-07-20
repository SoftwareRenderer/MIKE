<script lang="ts">
    import type { TaskCommit } from '$lib/types';
    import { formatRelativeTime } from '$lib/utils/format-time';
    import DiffSelect from '../DiffSelect.svelte';

    let { commits, baseCommit, diffFrom, diffTo, onFromChange, onToChange, onToggleFullscreen, isFullscreen, taskStatus }: {
        commits: TaskCommit[];
        baseCommit: TaskCommit | null;
        diffFrom: string;
        diffTo: string;
        onFromChange: (value: string) => void;
        onToChange: (value: string) => void;
        onToggleFullscreen: () => void;
        isFullscreen: boolean;
        taskStatus?: string;
    } = $props();

    function getCommitSubject(commit: TaskCommit): string {
        return commit.subject.split('\n')[0].trim();
    }

    function getBaseCommitLabel(): string {
        if (!baseCommit) return 'Original branch';
        const subject = getCommitSubject(baseCommit);
        const truncated = subject.length > 20 ? subject.slice(0, 20) + '…' : subject;
        return truncated;
    }

    // Sorted commits: newest first
    let sortedCommits = $derived(
        [...commits].sort((a, b) => new Date(b.date).getTime() - new Date(a.date).getTime())
    );

    // Options for the From dropdown: commits newest→oldest, then Origin at bottom
    let fromOptions = $derived.by(() => {
        const opts: { value: string; label: string; meta?: string }[] = [];
        for (const c of sortedCommits) {
            opts.push({
                value: c.hash,
                label: getCommitSubject(c),
                meta: `${c.short.slice(0, 7)} (${formatRelativeTime(c.date)})`,
            });
        }
        if (baseCommit) {
            opts.push({
                value: '',
                label: getBaseCommitLabel(),
                meta: `${baseCommit.short.slice(0, 7)} (Origin)`,
            });
        } else {
            opts.push({ value: '', label: 'Original branch' });
        }
        return opts;
    });

    // Options for the To dropdown: Working tree (or HEAD for done tasks) at top, then commits newest→oldest
    let toOptions = $derived.by(() => {
        const opts: { value: string; label: string; meta?: string }[] = [
            { value: 'working', label: taskStatus === 'done' ? 'HEAD' : 'Working tree' },
        ];
        for (const c of sortedCommits) {
            opts.push({
                value: c.hash,
                label: getCommitSubject(c),
                meta: `${c.short.slice(0, 7)} (${formatRelativeTime(c.date)})`,
            });
        }
        return opts;
    });
</script>

<div class="diff-header">
    <DiffSelect options={fromOptions} value={diffFrom} onChange={onFromChange} ariaLabel="From" />
    <span class="diff-arrow">→</span>
    <DiffSelect options={toOptions} value={diffTo} onChange={onToChange} ariaLabel="To" />
    <button type="button" class="diff-fullscreen-btn" onclick={onToggleFullscreen} title="Toggle fullscreen">
        {#if isFullscreen}
            <svg width="14" height="14" viewBox="0 0 16 16" fill="none"><path d="M10 2V6H14M6 14V10H2" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
        {:else}
            <svg width="14" height="14" viewBox="0 0 16 16" fill="none"><path d="M10 2h4v4M6 14H2v-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
        {/if}
    </button>
</div>

<style>
    .diff-header {
        display: flex;
        align-items: center;
        gap: 6px;
        padding: 6px 8px;
        border-bottom: 1px solid var(--color-border);
        background: var(--color-surface-1);
        flex-shrink: 0;
    }
    .diff-arrow {
        font-size: 0.875rem;
        color: var(--color-text-tertiary);
        padding: 0 2px;
        user-select: none;
        flex-shrink: 0;
    }
    .diff-fullscreen-btn {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 28px;
        height: 28px;
        border: none;
        background: transparent;
        color: var(--color-text-tertiary);
        cursor: pointer;
        border-radius: 4px;
        flex-shrink: 0;
        margin-left: auto;
    }
    .diff-fullscreen-btn:hover {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }
</style>
