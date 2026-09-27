<script lang="ts">
    import { markdownToSafeHTML } from '$lib/utils/sanitize';
    import { extensionManager } from '$lib/services/extension-manager.svelte';
    import { t } from '$lib/i18n';

    let { agentName = '' } = $props();

    let agentExt = $derived(
        extensionManager.state.extensions
            .find(e => e.name === agentName && e.type === 'agent') || null
    );
    // Config only counts while the extension is enabled (the card only renders
    // then anyway, since agent providers are registry-derived).
    let agentConfig = $derived(agentExt?.enabled ? agentExt.manifest.capabilities?.agent : undefined);

    let description = $derived(agentConfig?.description || '');
    let setupInfo = $derived(agentConfig?.setup_info || '');
    let installed = $derived(!!agentConfig);
    let agentDisplayName = $derived(agentExt?.manifest.display_name || '');

    let descriptionHtml = $derived.by(() => {
        if (!description) return '';
        return markdownToSafeHTML(description);
    });

    let setupHtml = $derived.by(() => {
        if (!setupInfo) return '';
        return markdownToSafeHTML(setupInfo);
    });
</script>

{#if installed}
    <div class="agent-info-section">
        {#if description}
            <div class="agent-info-text">
                {@html descriptionHtml}
            </div>
        {/if}
        {#if setupInfo}
            <div class="agent-info-box">
                <h4 class="agent-info-box-title">{t('settings.agent.setup')}</h4>
                {@html setupHtml}
            </div>
        {/if}
        <p class="agent-managed-note">
            {t('settings.agent.managedBefore')} <strong>{agentDisplayName || agentName}</strong> {t('settings.agent.managedAfter')}
        </p>
    </div>
{:else}
    <div class="agent-info-section agent-info-missing">
        <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
            <path d="M8 1L15 14H1L8 1Z" stroke="currentColor" stroke-width="1.3" stroke-linejoin="round"/>
            <path d="M8 6v4M8 11.5v.5" stroke="currentColor" stroke-width="1.3" stroke-linecap="round"/>
        </svg>
        {t('settings.agent.notInstalled')}
    </div>
{/if}

<style>
    .agent-info-section {
        padding: 12px;
        background: var(--color-surface-2);
        border-radius: var(--radius-md);
    }
    .agent-managed-note {
        margin: 8px 0 0;
        font-size: var(--text-xs);
        color: var(--color-text-secondary);
    }
    .agent-managed-note :global(strong) {
        color: var(--color-text-primary);
        font-weight: 600;
    }
    .agent-info-missing {
        display: flex;
        align-items: center;
        gap: 6px;
        font-size: var(--text-sm);
        color: var(--color-warning);
    }
    .agent-info-missing svg {
        flex-shrink: 0;
    }
    .agent-info-text {
        font-size: var(--text-sm);
        color: var(--color-text-secondary);
        line-height: 1.5;
        margin: 0 0 12px;
    }
    .agent-info-text :global(p) {
        margin: 0 0 0.5em;
    }
    .agent-info-text :global(p:last-child) {
        margin-bottom: 0;
    }
    .agent-info-box {
        background: var(--color-surface-1);
        border-radius: var(--radius-sm);
        padding: 10px 12px;
        border: 1px solid var(--color-border);
    }
    .agent-info-box-title {
        font-size: var(--text-xs);
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.05em;
        color: var(--color-text-secondary);
        margin: 0 0 8px;
    }
    .agent-info-box :global(ul) {
        margin: 0;
        padding-left: 16px;
        font-size: var(--text-sm);
        color: var(--color-text-secondary);
        line-height: 1.6;
    }
    .agent-info-box :global(li) {
        margin-bottom: 4px;
    }
    .agent-info-box :global(code) {
        background: var(--color-surface-2);
        padding: 1px 4px;
        border-radius: var(--radius-sm);
        font-size: var(--text-xs);
    }
    .agent-info-box :global(strong) {
        font-weight: 600;
    }
</style>
