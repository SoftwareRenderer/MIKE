<script lang="ts">
    import { t } from '$lib/i18n';

    let { activeTab, onTabChange, showCheckpointTab = false }: { activeTab: string; onTabChange: (tab: 'context' | 'chat' | 'diff' | 'checkpoint') => void; showCheckpointTab?: boolean } = $props();
</script>

<div class="mobile-tab-bar">
    <button
        class="tab-btn {activeTab === 'context' ? 'active' : ''}"
        onclick={() => onTabChange('context')}
        title={t('task.tab.contextAria')}
        aria-label={t('task.tab.contextAria')}
    >
        <svg width="20" height="20" viewBox="0 0 16 16" fill="none"><path d="M2 2h5v5H2zM9 2h5v5H9zM2 9h5v5H2zM9 9h5v5H9z" stroke="currentColor" stroke-width="1.2"/></svg>
    </button>
    <button
        class="tab-btn {activeTab === 'chat' ? 'active' : ''}"
        onclick={() => onTabChange('chat')}
        title={t('task.tab.chatAria')}
        aria-label={t('task.tab.chatAria')}
    >
        <svg width="20" height="20" viewBox="0 0 16 16" fill="none"><path d="M2 3h12v8H5l-3 3V3z" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round"/></svg>
    </button>
    {#if showCheckpointTab}
        <button
            class="tab-btn {activeTab === 'checkpoint' ? 'active' : ''}"
            onclick={() => onTabChange('checkpoint')}
            title={t('task.tab.checkpointAria')}
            aria-label={t('task.tab.checkpointAria')}
        >
            <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
                <path d="M19 21l-7-5-7 5V5a2 2 0 0 1 2-2h10a2 2 0 0 1 2 2z"/>
            </svg>
            {t('task.tabs.checkpoint')}
        </button>
    {/if}
    <button
        class="tab-btn {activeTab === 'diff' ? 'active' : ''}"
        onclick={() => onTabChange('diff')}
        title={t('task.tab.diffAria')}
        aria-label={t('task.tab.diffAria')}
    >
        <svg width="20" height="20" viewBox="0 0 16 16" fill="none"><rect x="2" y="2" width="5" height="12" rx="1" stroke="currentColor" stroke-width="1.2"/><rect x="9" y="2" width="5" height="12" rx="1" stroke="currentColor" stroke-width="1.2"/><path d="M4 5h1M4 7h1M4 9h1" stroke="currentColor" stroke-width="1" stroke-linecap="round"/><path d="M11 5h1M11 7l1 1M11 9l1-1" stroke="currentColor" stroke-width="1" stroke-linecap="round" stroke-linejoin="round"/></svg>
    </button>
</div>

<style>
    .mobile-tab-bar {
        display: flex;
        border-bottom: 1px solid var(--color-border);
        background: var(--color-surface-0);
        flex-shrink: 0;
    }
    .tab-btn {
        flex: 1;
        display: flex;
        flex-direction: column;
        align-items: center;
        gap: 2px;
        padding: 8px 4px;
        background: none;
        border: none;
        color: var(--color-text-tertiary);
        font-size: var(--text-2xs);
        cursor: pointer;
        transition: all 0.15s ease;
        border-bottom: 2px solid transparent;
    }
    .tab-btn.active {
        color: var(--color-accent);
        border-bottom-color: var(--color-accent);
    }
    .tab-btn:hover:not(.active) {
        color: var(--color-text-secondary);
        background: var(--color-surface-1);
    }
</style>
