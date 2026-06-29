<script lang="ts">
    let { activeTab, onTabChange }: { activeTab: string; onTabChange: (tab: 'context' | 'chat' | 'diff') => void } = $props();
</script>

<div class="mobile-tab-bar">
    <button
        class="tab-btn {activeTab === 'context' ? 'active' : ''}"
        onclick={() => onTabChange('context')}
        title="Board overview"
    >
        <svg width="12" height="12" viewBox="0 0 16 16" fill="none"><path d="M2 2h5v5H2zM9 2h5v5H9zM2 9h5v5H2zM9 9h5v5H9z" stroke="currentColor" stroke-width="1.2"/></svg>
        Board
    </button>
    <button
        class="tab-btn {activeTab === 'chat' ? 'active' : ''}"
        onclick={() => onTabChange('chat')}
        title="Agent chat"
    >
        <svg width="12" height="12" viewBox="0 0 16 16" fill="none"><path d="M2 3h12v8H5l-3 3V3z" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round"/></svg>
        Agent
    </button>
    <button
        class="tab-btn {activeTab === 'diff' ? 'active' : ''}"
        onclick={() => onTabChange('diff')}
        title="Working changes"
    >
        <svg width="12" height="12" viewBox="0 0 16 16" fill="none"><rect x="2" y="2" width="5" height="12" rx="1" stroke="currentColor" stroke-width="1.2"/><rect x="9" y="2" width="5" height="12" rx="1" stroke="currentColor" stroke-width="1.2"/><path d="M4 5h1M4 7h1M4 9h1" stroke="currentColor" stroke-width="1" stroke-linecap="round"/><path d="M11 5h1M11 7l1 1M11 9l1-1" stroke="currentColor" stroke-width="1" stroke-linecap="round" stroke-linejoin="round"/></svg>
        Changes
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
        font-size: 0.6875rem;
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
