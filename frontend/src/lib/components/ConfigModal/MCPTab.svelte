<script lang="ts">
    import { t } from '$lib/i18n';
    import EmptyState from '../EmptyState.svelte';
    import MCPServerCard from './MCPServerCard.svelte';
    import type { MCPServerDraft } from './mcp-drafts';
    import { MAX_MCP_SERVERS } from './mcp-drafts';

    /**
     * Settings → MCP: the registry of Streamable-HTTP servers a project may opt into. The modal owns the
     * list; this tab edits what it is handed and reports every change as dirty. `locked` is
     * MIKE_RESTRICT_MCP: the registry is still listed — a frozen server is offered and called — but this tab
     * writes nothing. The notice is said once for the page; a disabled button carries the reason in its own
     * title, and help text that would describe a frozen edit is not shown.
     */
    let {
        servers,
        locked,
        onAdd,
        onRemove,
        onTest,
        onDirty,
    }: {
        servers: MCPServerDraft[];
        locked: boolean;
        onAdd: () => void;
        onRemove: (uid: string) => void;
        onTest: (uid: string) => void;
        onDirty: () => void;
    } = $props();

    const lockedNoticeId = 'mcp-registry-locked';
</script>

<div class="settings-section">
    <div class="mcp-head">
        <div>
            <h3 class="settings-section-title">{t('settings.mcp.title')}</h3>
            <p class="mcp-intro">{t('settings.mcp.intro')}</p>
        </div>
        <div class="settings-section-actions">
            <button type="button" class="settings-btn-small" onclick={onAdd}
                disabled={locked || servers.length >= MAX_MCP_SERVERS}
                title={locked ? t('settings.mcp.lockedHint') : t('settings.mcp.addHint')}>
                {t('settings.mcp.add')}
            </button>
        </div>
    </div>

    {#if locked}
        <p class="mcp-footnote" id={lockedNoticeId} role="note" data-testid="mcp-locked">
            {t('settings.mcp.restricted')}
        </p>
    {/if}

    {#each servers as server (server.uid)}
        <MCPServerCard
            {server}
            {locked}
            {lockedNoticeId}
            onRemove={() => onRemove(server.uid)}
            onTest={() => onTest(server.uid)}
            {onDirty}
        />
    {/each}

    {#if servers.length === 0}
        <EmptyState compact title={t('settings.mcp.empty')}
            hint={locked ? undefined : t('settings.mcp.emptyHint')} />
    {/if}

    <p class="mcp-footnote">{t('settings.mcp.projectHint')}</p>
</div>

<style>
    @import '$lib/styles/settings-section.css';

    .mcp-head {
        display: flex;
        justify-content: space-between;
        gap: 12px;
    }
    .mcp-intro,
    .mcp-footnote {
        margin: 0;
        color: var(--color-text-tertiary);
        line-height: 1.45;
    }
    .mcp-intro {
        margin-top: 6px;
        font-size: var(--text-sm);
    }
    .mcp-footnote {
        font-size: var(--text-xs);
    }
</style>
