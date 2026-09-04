<script lang="ts">
    import type { Thread } from '$lib/types';
    import { t } from '$lib/i18n';

    interface Props {
        threads: Thread[];
        activeThreadId: string;
        onSwitch: (threadId: string) => void;
        isSwitching?: boolean;
    }

    let { threads, activeThreadId, onSwitch, isSwitching = false }: Props = $props();

    let showMenu = $state(false);
    let menuRef: HTMLElement | null = null;

    function handleSwitch(threadId: string) {
        if (threadId !== activeThreadId && !isSwitching) {
            onSwitch(threadId);
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

    let activeThreadName = $derived(threads.find(t2 => t2.id === activeThreadId)?.name || t('chat.thread.main'));
</script>

<div class="thread-selector" bind:this={menuRef}>
    <button class="thread-btn" onclick={toggleMenu} aria-label={t('chat.thread.switch')} aria-expanded={showMenu} title={t('chat.thread.switch')}>
        <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
            <circle cx="12" cy="12" r="10"/>
            <path d="M2 12h20"/>
            <path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z"/>
        </svg>
        {activeThreadName}
        <svg class="chevron" width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
            <polyline points="6 9 12 15 18 9"/>
        </svg>
    </button>

    {#if showMenu}
        <div class="thread-menu" role="menu">
            {#each threads as thread (thread.id)}
                <button
                    class="thread-menu-item"
                    class:active={thread.is_active}
                    disabled={isSwitching}
                    onclick={() => handleSwitch(thread.id)}
                    role="menuitem"
                >
                    <span class="thread-name">{thread.name}</span>
                    {#if isSwitching}
                        <span class="switch-spinner"></span>
                    {/if}
                </button>
            {/each}
        </div>
    {/if}
</div>

<style>
    .thread-selector {
        position: relative;
    }
    .thread-btn {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        gap: 4px;
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
        background: color-mix(in srgb, var(--color-accent) 18%, transparent);
        color: var(--color-accent);
    }
    .thread-menu-item:disabled {
        opacity: 0.5;
        cursor: not-allowed;
    }
    .thread-name {
        font-weight: 500;
    }
    .switch-spinner {
        display: inline-block;
        width: 12px;
        height: 12px;
        border: 2px solid currentColor;
        border-top-color: transparent;
        border-radius: 50%;
        animation: spin 0.6s linear infinite;
    }
    @keyframes spin {
        to { transform: rotate(360deg); }
    }
</style>
