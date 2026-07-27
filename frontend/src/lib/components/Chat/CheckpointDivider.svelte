<script lang="ts">
    import type { Thread } from '$lib/types';
    import ThreadSelector from './ThreadSelector.svelte';

    interface Props {
        timestamp?: string;
        threadId: string;
        threads: Thread[];
        activeThreadId: string;
        onSwitch?: (threadId: string) => void;
        isSwitching?: boolean;
        isPending?: boolean;
        commitLabel?: string;
        commitHash?: string;
        checkpointIndex?: number;
        onViewChanges?: (commitHash: string, checkpointIndex?: number) => void;
    }

    let { timestamp, threadId, threads, activeThreadId, onSwitch, isSwitching = false, isPending = false, commitLabel, commitHash, checkpointIndex, onViewChanges }: Props = $props();

    // Filter threads to only show those relevant at this checkpoint
    let relevantThreads = $derived(threads.filter(t => {
        const ct = t.checkpoint_message_time;
        if (!ct) return true; // root thread (Main)
        if (ct.startsWith('0001-') || ct.startsWith('1970-')) return true; // zero/epoch
        return !timestamp || ct <= timestamp;
    }));
</script>

<div class="checkpoint-divider">
    <div class="divider-line"></div>
    <div class="divider-content">
        <span class="checkpoint-label" title={commitLabel || undefined}>
            {#if isPending}
                <svg class="checkpoint-spinner" width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" aria-hidden="true">
                    <path d="M12 2a10 10 0 0 1 10 10"/>
                </svg>
            {:else}
                <svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                    <path d="M19 21l-7-5-7 5V5a2 2 0 0 1 2-2h10a2 2 0 0 1 2 2z"/>
                </svg>
            {/if}
            Checkpoint
        </span>
        <div class="divider-actions">
            {#if onViewChanges && commitHash}
                <button class="view-changes-btn" onclick={() => onViewChanges(commitHash || '', checkpointIndex)} title="View changes up to this checkpoint">
                    <svg width="11" height="11" viewBox="0 0 16 16" fill="none"><rect x="2" y="2" width="5" height="12" rx="1" stroke="currentColor" stroke-width="1.2"/><rect x="9" y="2" width="5" height="12" rx="1" stroke="currentColor" stroke-width="1.2"/><path d="M4 5h1M4 7h1M4 9h1" stroke="currentColor" stroke-width="1" stroke-linecap="round"/><path d="M11 5h1M11 7l1 1M11 9l1-1" stroke="currentColor" stroke-width="1" stroke-linecap="round" stroke-linejoin="round"/></svg>
                </button>
            {/if}
            {#if relevantThreads.length > 1}
                <ThreadSelector
                    threads={relevantThreads}
                    activeThreadId={activeThreadId}
                    onSwitch={onSwitch || (() => {})}
                    isSwitching={isSwitching}
                />
            {/if}
        </div>
    </div>
</div>

<style>
    .checkpoint-divider {
        display: flex;
        align-items: center;
        gap: 10px;
        padding: 6px 0;
        position: relative;
    }
    .divider-line {
        flex: 1;
        height: 1px;
        background: var(--color-accent);
        opacity: 0.7;
    }
    .divider-content {
        display: flex;
        align-items: center;
        gap: 8px;
        flex-shrink: 0;
    }
    .checkpoint-label {
        display: inline-flex;
        align-items: center;
        gap: 4px;
        font-size: 0.75rem;
        color: var(--color-accent);
        opacity: 1;
        white-space: nowrap;
    }
    .checkpoint-label svg {
        opacity: 0.7;
    }
    .checkpoint-spinner {
        animation: checkpoint-spin 1s linear infinite;
    }
    @keyframes checkpoint-spin {
        from { transform: rotate(0deg); }
        to { transform: rotate(360deg); }
    }
    .divider-actions {
        display: flex;
        align-items: center;
        gap: 4px;
    }
    .view-changes-btn {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        gap: 3px;
        min-height: 1.375rem;
        padding: 2px 8px;
        border-radius: var(--radius-sm);
        font-size: 0.75rem;
        font-weight: 500;
        background: var(--color-surface-3);
        color: var(--color-text-secondary);
        border: 1px solid var(--color-border);
        cursor: pointer;
        transition: all 0.15s ease;
        white-space: nowrap;
    }
    .view-changes-btn:hover {
        background: color-mix(in srgb, var(--color-accent) 12%, transparent);
        color: var(--color-accent);
        border-color: color-mix(in srgb, var(--color-accent) 30%, transparent);
    }
</style>
