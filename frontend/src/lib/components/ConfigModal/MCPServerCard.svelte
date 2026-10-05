<script lang="ts">
    import { t } from '$lib/i18n';
    import { dialogStore } from '$lib/stores';
    import ToggleSwitch from '../ToggleSwitch.svelte';
    import Spinner from '../Spinner.svelte';
    import Icon from '../Icon.svelte';
    import closeIcon from '$lib/assets/icons/close.svg?raw';
    import type { MCPServerDraft } from './mcp-drafts';
    import { MAX_MCP_HEADERS } from './mcp-drafts';

    /**
     * One registered MCP server, edited in place. A card rather than a modal: the Test answer belongs
     * under the address it came from. `locked` is MIKE_RESTRICT_MCP — every write this card offers is
     * frozen, so the row is shown as it stands and `lockedNoticeId` says why.
     */
    let {
        server,
        locked,
        lockedNoticeId,
        onRemove,
        onTest,
        onDirty,
    }: {
        server: MCPServerDraft;
        locked: boolean;
        lockedNoticeId: string;
        onRemove: () => void;
        onTest: () => void;
        onDirty: () => void;
    } = $props();

    const frozenBy = $derived(locked ? lockedNoticeId : undefined);

    async function handleRemove() {
        const confirmed = await dialogStore.showConfirm({
            title: t('settings.mcp.removeTitle'),
            message: t('settings.mcp.removeMessage', { name: server.name || server.url }),
            confirmText: t('settings.mcp.remove'),
            cancelText: t('settings.cancel'),
            variant: 'destructive',
        });
        if (!confirmed) return;
        onRemove();
    }

    function addHeader() {
        server.headers.push({ name: '', value: '', stored: false });
        onDirty();
    }

    function removeHeader(index: number) {
        server.headers.splice(index, 1);
        onDirty();
    }
</script>

<div class="mcp-card">
    <div class="mcp-card-head">
        <input
            type="text"
            class="mcp-name"
            bind:value={server.name}
            oninput={onDirty}
            disabled={locked}
            placeholder={t('settings.mcp.namePlaceholder')}
            aria-label={t('settings.mcp.nameAria')}
        />
        <div class="mcp-head-actions">
            <span class="mcp-enabled-label" id={`mcp-enabled-${server.uid}`}>{t('settings.mcp.enabled')}</span>
            <ToggleSwitch
                bind:checked={server.enabled}
                disabled={locked}
                ariaLabel={t('settings.mcp.enabledAria')}
                ariaDescribedby={frozenBy || `mcp-enabled-${server.uid}`}
                onChange={onDirty}
            />
            {#if !locked}
                <button type="button" class="btn btn-secondary mcp-action" onclick={handleRemove} title={t('settings.mcp.removeHint')}>
                    {t('settings.mcp.remove')}
                </button>
            {/if}
        </div>
    </div>

    <label class="mcp-field">
        <span class="mcp-label">{t('settings.mcp.url')}</span>
        <input
            type="text"
            class="mcp-input mcp-url"
            bind:value={server.url}
            oninput={onDirty}
            disabled={locked}
            spellcheck="false"
            placeholder="https://mcp.example.com/mcp"
            aria-label={t('settings.mcp.urlAria')}
        />
        <span class="mcp-help">{t('settings.mcp.urlHelp')}</span>
    </label>

    <div class="mcp-field-row">
        <label class="mcp-field">
            <span class="mcp-label">{t('settings.mcp.timeout')}</span>
            <input
                type="number"
                class="mcp-input mcp-timeout"
                bind:value={server.timeoutSeconds}
                oninput={onDirty}
                disabled={locked}
                min="0"
                aria-label={t('settings.mcp.timeoutAria')}
            />
            <span class="mcp-help">{t('settings.mcp.timeoutHelp')}</span>
        </label>

        <button
            type="button"
            class="btn btn-secondary"
            onclick={onTest}
            disabled={locked || server.testing || !server.url.trim()}
            title={locked ? t('settings.mcp.lockedHint') : t('settings.mcp.testHint')}
        >
            {#if server.testing}
                <Spinner size={12} decorative />
            {/if}
            {t('settings.mcp.test')}
        </button>
    </div>

    {#if server.result}
        <p class="mcp-answer" class:mcp-answer-ok={server.result.ok} class:mcp-answer-fail={!server.result.ok} role="status">
            {server.result.message}
        </p>
    {/if}

    <div class="mcp-headers">
        <div class="mcp-headers-head">
            <span class="mcp-label">{t('settings.mcp.headers')}</span>
            {#if !locked}
                <button type="button" class="btn btn-secondary mcp-action" onclick={addHeader}
                    disabled={server.headers.length >= MAX_MCP_HEADERS} title={t('settings.mcp.addHeaderHint')}>
                    {t('settings.mcp.addHeader')}
                </button>
            {/if}
        </div>
        {#if !locked}
            <span class="mcp-help">{t('settings.mcp.headersHelp')}</span>
        {/if}
        {#each server.headers as header, i (i)}
            <div class="mcp-header-row">
                <input
                    type="text"
                    class="mcp-input mcp-header-name"
                    bind:value={header.name}
                    oninput={onDirty}
                    disabled={locked}
                    spellcheck="false"
                    placeholder="Authorization"
                    aria-label={t('settings.mcp.headerNameAria')}
                />
                <input
                    type="password"
                    class="mcp-input mcp-header-value"
                    value={header.value}
                    disabled={locked}
                    oninput={(e) => { header.value = (e.target as HTMLInputElement).value; onDirty(); }}
                    placeholder={header.stored ? t('settings.mcp.headerSaved') : t('settings.mcp.headerValuePlaceholder')}
                    aria-label={t('settings.mcp.headerValueAria')}
                />
                {#if !locked}
                    <button type="button" class="btn btn-secondary mcp-action" onclick={() => removeHeader(i)}
                        title={t('settings.mcp.removeHeaderHint')} aria-label={t('settings.mcp.removeHeaderAria')}>
                        <Icon src={closeIcon} width={14} height={14} />
                    </button>
                {/if}
            </div>
        {/each}
    </div>
</div>

<style>
    /* One column of fields, one input skin, one answer band. A scoped block that repeats a token set
       someone else wrote is a second copy of a rule. */
    .mcp-card,
    .mcp-field,
    .mcp-headers {
        display: flex;
        flex-direction: column;
        gap: 6px;
        min-width: 0;
    }
    .mcp-card {
        gap: 10px;
        border: 1px solid var(--color-border);
        border-radius: var(--radius-md);
        padding: 12px;
    }
    .mcp-card-head,
    .mcp-field-row,
    .mcp-header-row,
    .mcp-headers-head,
    .mcp-head-actions {
        display: flex;
        align-items: center;
        gap: 8px;
    }
    .mcp-card-head,
    .mcp-headers-head {
        justify-content: space-between;
    }
    .mcp-field-row {
        align-items: flex-end;
        flex-wrap: wrap;
    }
    .mcp-head-actions {
        flex-shrink: 0;
    }
    .mcp-name,
    .mcp-input {
        color: var(--color-text-primary);
    }
    .mcp-name {
        flex: 1;
        min-width: 0;
        background: transparent;
        border: none;
        border-bottom: 1px solid transparent;
        font-size: var(--text-base);
        font-weight: 600;
        padding: 2px 0;
    }
    .mcp-input {
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        font-family: var(--font-mono, monospace);
        font-size: var(--text-sm);
        padding: 6px 8px;
    }
    .mcp-input:focus {
        outline: none;
        border-color: var(--color-accent);
    }
    .mcp-url,
    .mcp-header-value,
    .mcp-header-name,
    .mcp-timeout {
        min-width: 0;
    }
    .mcp-url,
    .mcp-header-value {
        flex: 1;
    }
    .mcp-header-name {
        flex: 0 0 14rem;
    }
    .mcp-timeout {
        width: 7ch;
    }
    .mcp-label,
    .mcp-help,
    .mcp-enabled-label {
        font-size: var(--text-xs);
        color: var(--color-text-tertiary);
    }
    .mcp-label {
        color: var(--color-text-secondary);
        font-weight: 500;
    }
    .mcp-help {
        line-height: 1.4;
    }
    .mcp-answer {
        margin: 0;
        padding: 8px 10px;
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        background: var(--color-surface-1);
        color: var(--color-text-secondary);
        font-size: var(--text-xs);
        line-height: 1.45;
    }
    .mcp-answer-ok {
        border-color: rgb(from var(--color-status-green) r g b / 0.35);
        color: var(--color-status-green);
    }
    .mcp-answer-fail {
        border-color: rgb(from var(--color-status-red) r g b / 0.35);
    }
    .mcp-action {
        padding: 4px 10px;
        font-size: var(--text-xs);
    }
</style>
