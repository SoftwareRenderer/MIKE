<script lang="ts">
    import type { Thread } from '$lib/types';

    interface Props {
        timestamp?: string;
        threadId: string;
        threads: Thread[];
        activeThreadId: string;
        onSwitch?: (threadId: string) => void;
        isPending?: boolean;
        commitLabel?: string;
        commitHash?: string;
        onViewChanges?: (commitHash: string) => void;
    }

    let { timestamp, threadId, threads, activeThreadId, onSwitch, isPending = false, commitLabel, commitHash, onViewChanges }: Props = $props();

    // Filter threads to only show those relevant at this checkpoint
    let relevantThreads = $derived(threads.filter(t => {
        const ct = t.checkpoint_message_time;
        if (!ct) return true; // root thread (Main)
        if (ct.startsWith('0001-') || ct.startsWith('1970-')) return true; // zero/epoch
        return !timestamp || ct <= timestamp;
    }));

    let showMenu = $state(false);
    let menuRef: HTMLElement | null = null;

    function formatTime(isoStr: string): string {
        if (!isoStr) return '';
        const date = new Date(isoStr);
        if (isNaN(date.getTime())) return '';
        return date.toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' });
    }

    function handleSwitch(threadId: string) {
        if (threadId !== activeThreadId) {
            onSwitch?.(threadId);
        }
        showMenu = false;
    }

    function toggleMenu() {
        showMenu = !showMenu;
    }

    // Close menu on outside click
    function handleClickOutside(e: MouseEvent) {
        if (menuRef && !menuRef.contains(e.target as Node)) {
            showMenu = false;
        }
    }

    $effect(() => {
        if (showMenu) {
            document.addEventListener('click', handleClickOutside);
        } else {
            document.removeEventListener('click', handleClickOutside);
        }
        return () => document.removeEventListener('click', handleClickOutside);
    });
</script>

<div class="checkpoint-divider">
    <div class="divider-line"></div>
    <div class="divider-content" bind:this={menuRef}>
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
            {commitLabel || 'Checkpoint'}
        </span>
        <div class="divider-actions">
            {#if onViewChanges && commitHash}
                <button class="view-changes-btn" onclick={() => onViewChanges(commitHash || '')} title="View changes up to this checkpoint">
                    <svg width="11" height="11" viewBox="0 0 16 16" fill="none"><rect x="2" y="2" width="5" height="12" rx="1" stroke="currentColor" stroke-width="1.2"/><rect x="9" y="2" width="5" height="12" rx="1" stroke="currentColor" stroke-width="1.2"/><path d="M4 5h1M4 7h1M4 9h1" stroke="currentColor" stroke-width="1" stroke-linecap="round"/><path d="M11 5h1M11 7l1 1M11 9l1-1" stroke="currentColor" stroke-width="1" stroke-linecap="round" stroke-linejoin="round"/></svg>
                </button>
            {/if}
            {#if relevantThreads.length > 1}
                <button class="thread-btn" onclick={toggleMenu} aria-label="Switch thread" aria-expanded={showMenu} title="Switch thread">
                    <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                        <circle cx="12" cy="12" r="10"/>
                        <path d="M2 12h20"/>
                        <path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z"/>
                    </svg>
                    {relevantThreads.find(t => t.id === activeThreadId)?.name || 'Main'}
                    <svg class="chevron" width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                        <polyline points="6 9 12 15 18 9"/>
                    </svg>
                </button>
            {/if}
        </div>
    </div>

    {#if showMenu && relevantThreads.length > 1}
        <div class="thread-menu" role="menu">
            {#each relevantThreads as thread (thread.id)}
                <button
                    class="thread-menu-item"
                    class:active={thread.is_active}
                    onclick={() => handleSwitch(thread.id)}
                    role="menuitem"
                >
                    <span class="thread-name">{thread.name}</span>
                    {#if thread.is_active}
                        <span class="thread-active">● Active</span>
                    {/if}
                </button>
            {/each}
        </div>
    {/if}
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
        opacity: 0.6;
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
        opacity: 0.5;
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
        gap: 3px;
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
    .thread-btn {
        display: inline-flex;
        align-items: center;
        gap: 4px;
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
    .thread-btn:hover {
        background: var(--color-surface-4);
        color: var(--color-text-primary);
    }
    .chevron {
        transition: transform 0.15s ease;
    }
    .thread-menu {
        position: absolute;
        top: 100%;
        right: 0;
        margin-top: 4px;
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-md);
        padding: 4px;
        min-width: 160px;
        z-index: 100;
        box-shadow: 0 4px 12px rgba(0, 0, 0, 0.15);
    }
    .thread-menu-item {
        display: flex;
        align-items: center;
        justify-content: space-between;
        width: 100%;
        padding: 6px 10px;
        border-radius: var(--radius-sm);
        font-size: 0.75rem;
        background: none;
        border: none;
        color: var(--color-text-secondary);
        cursor: pointer;
        transition: all 0.1s ease;
        text-align: left;
    }
    .thread-menu-item:hover {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }
    .thread-menu-item.active {
        background: color-mix(in srgb, var(--color-accent) 8%, transparent);
        color: var(--color-accent);
    }
    .thread-name {
        font-weight: 500;
    }
    .thread-active {
        font-size: 0.75rem;
        color: var(--color-accent);
        opacity: 0.7;
    }
</style>
