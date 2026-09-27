<script lang="ts">
    import type { Extension } from '$lib/types';
    import { t } from '$lib/i18n';
    import BaseModal from '../BaseModal.svelte';
    import ExtensionPermissionRow from './ExtensionPermissionRow.svelte';
    import { sortPermissions } from '$lib/utils/extension-permissions';
    import { capabilityDescriptions, capabilityKeys, injectionPointLabels, packLocale, typeBadge } from '$lib/utils/extension-manifest';

    /**
     * Everything one extension declared, in the detail dialog its badges open. Every line here is the
     * manifest's own: the domains it may reach, the injection points it registers at, the permissions it
     * asks for, the file types its server claims, the stylesheet it ships, the locale tag a pack names.
     *
     * A Browse entry has no record, so the caller hands over the same shape built from the index entry.
     */
    let { extension, onclose }: {
        extension: Extension;
        onclose: () => void;
    } = $props();

    const caps = $derived(extension.manifest.capabilities);
    const capKeys = $derived(capabilityKeys(extension));
    const descriptions = $derived(capabilityDescriptions());
    const injectionPoints = $derived(injectionPointLabels());
</script>

{#snippet capsFooter()}
    <button type="button" class="btn-secondary" onclick={onclose}>{t('settings.close')}</button>
{/snippet}

<BaseModal title={t('settings.extensions.capsTitle')} width="520px" footer={capsFooter} onClose={onclose}>
    <div class="caps-modal-body">
        <div class="caps-ext-header">
            <div class="caps-ext-icon" class:ext-lsp={extension.type === 'lsp'} class:ext-ui={extension.type === 'ui'} class:ext-css={extension.type === 'css'} class:ext-agent={extension.type === 'agent'} class:ext-llm={extension.type === 'llm'} class:ext-i18n={extension.type === 'i18n'} aria-hidden="true">
                {typeBadge(extension.type)}
            </div>
            <div class="caps-ext-info">
                <h3 class="caps-ext-name">{extension.manifest.display_name || extension.name}</h3>
                {#if extension.manifest.description}
                    <p class="caps-ext-desc">{extension.manifest.description}</p>
                {/if}
            </div>
        </div>

        <div class="caps-list" role="list" aria-label={t('settings.extensions.capsDetailsAria')}>
            {#each capKeys as capKey}
                {@const info = descriptions[capKey]}
                {#if info}
                    <div class="cap-detail" role="listitem">
                        <div class="cap-detail-header">
                            <span class="cap-detail-badge" class:cap-lsp={capKey === 'lsp'} class:cap-ui={capKey === 'ui'} class:cap-css={capKey === 'css'} class:cap-agent={capKey === 'agent'} class:cap-network={capKey === 'network'} class:cap-llm={capKey === 'llm'} class:cap-i18n={capKey === 'i18n'}>
                                {info.label}
                            </span>
                        </div>
                        <p class="cap-detail-desc">{info.description}</p>
                        <!-- Network: show allowed domains -->
                        {#if capKey === 'network' && caps?.network?.allowed_domains?.length}
                            <div class="cap-detail-extra">
                                <span class="cap-detail-label">{t('settings.extensions.allowedDomains')}</span>
                                <div class="cap-detail-tags">
                                    {#each caps.network.allowed_domains as domain}
                                        <span class="cap-detail-tag">{domain}</span>
                                    {/each}
                                </div>
                            </div>
                        {/if}
                        <!-- UI: show injection points -->
                        {#if capKey === 'ui' && caps?.ui?.injection_points?.length}
                            <div class="cap-detail-extra">
                                <span class="cap-detail-label">{t('settings.extensions.modifies')}</span>
                                <div class="cap-detail-tags">
                                    {#each caps.ui.injection_points as point}
                                        <span class="cap-detail-tag">{injectionPoints[point] || point}</span>
                                    {/each}
                                </div>
                            </div>
                        {/if}
                        <!-- UI: show declared permissions -->
                        {#if capKey === 'ui' && caps?.ui?.permissions?.length}
                            <div class="cap-detail-extra cap-detail-extra-block">
                                <span class="cap-detail-label">{t('settings.extensions.permissions')}</span>
                                <div class="cap-permission-list">
                                    {#each sortPermissions(caps.ui.permissions) as perm}
                                        <ExtensionPermissionRow {perm} />
                                    {/each}
                                </div>
                            </div>
                        {/if}
                        <!-- LSP: show language and extensions -->
                        {#if capKey === 'lsp' && caps?.lsp}
                            <div class="cap-detail-extra">
                                {#if caps.lsp.language}
                                    <span class="cap-detail-label">{t('settings.extensions.language')}</span>
                                    <span class="cap-detail-tags">
                                        <span class="cap-detail-tag">{caps.lsp.language}</span>
                                    </span>
                                {/if}
                                {#if caps.lsp.file_types?.length}
                                    <span class="cap-detail-label">{t('settings.extensions.fileTypes')}</span>
                                    <div class="cap-detail-tags">
                                        {#each caps.lsp.file_types as ft (ft.extension)}
                                            <!-- The tooltip names the language the server is told about this file type. -->
                                            <span class="cap-detail-tag" title={ft.label ? `${ft.label} · ${ft.language_id}` : ft.language_id}>
                                                .{ft.extension}
                                            </span>
                                        {/each}
                                    </div>
                                {/if}
                            </div>
                        {/if}
                        <!-- CSS: show stylesheet URL -->
                        {#if capKey === 'css' && caps?.css?.css_url}
                            <div class="cap-detail-extra">
                                <span class="cap-detail-label">{t('settings.extensions.stylesheet')}</span>
                                <code class="cap-detail-value">{caps.css.css_url}</code>
                            </div>
                        {/if}
                        <!-- Translation pack: the locale tag it declares, verbatim —
                             core holds no language names to render. -->
                        {#if capKey === 'i18n' && packLocale(caps)}
                            <div class="cap-detail-extra">
                                <span class="cap-detail-label">{t('settings.extensions.language')}</span>
                                <code class="cap-detail-value">{packLocale(caps)}</code>                            </div>
                        {/if}
                    </div>
                {/if}
            {/each}
        </div>
    </div>
</BaseModal>

<style>
    /* Capabilities modal */
    .caps-modal-body {
        display: flex;
        flex-direction: column;
        gap: 1.5rem;
        padding: 0.5rem 0;
    }

    .caps-ext-header {
        display: flex;
        align-items: center;
        gap: 0.75rem;
    }

    .caps-ext-icon {
        width: 2.5rem;
        height: 2.5rem;
        border-radius: var(--radius-md);
        display: flex;
        align-items: center;
        justify-content: center;
        font-size: var(--text-xs);
        font-weight: 700;
        flex-shrink: 0;
    }

    .caps-ext-info {
        display: flex;
        flex-direction: column;
        gap: 0.15rem;
    }

    .caps-ext-name {
        margin: 0;
        font-weight: 600;
        color: var(--color-text-primary);
        font-size: var(--text-lg);
    }

    .caps-ext-desc {
        margin: 0;
        font-size: var(--text-sm);
        color: var(--color-text-tertiary);
    }

    .caps-list {
        display: flex;
        flex-direction: column;
        gap: 1rem;
    }

    .cap-detail {
        display: flex;
        flex-direction: column;
        gap: 0.35rem;
        padding: 0.75rem;
        background: var(--color-bg-card);
        border: 1px solid var(--color-border-subtle);
        border-radius: var(--radius-lg);
    }

    .cap-detail-header {
        display: flex;
        align-items: center;
        gap: 0.5rem;
    }

    .cap-detail-badge {
        font-size: var(--text-xs);
        font-weight: 600;
        padding: 0.2rem 0.6rem;
        border-radius: var(--radius-sm);
        text-transform: uppercase;
        letter-spacing: 0.03em;
    }

    .cap-detail-desc {
        margin: 0;
        font-size: var(--text-base);
        color: var(--color-text-secondary);
        line-height: 1.5;
    }

    .cap-detail-extra {
        display: flex;
        flex-wrap: wrap;
        align-items: flex-start;
        gap: 0.3rem 0.5rem;
        margin-top: 0.5rem;
        padding-top: 0.5rem;
        border-top: 1px solid var(--color-border-subtle);
    }

    .cap-detail-label {
        font-size: var(--text-xs);
        font-weight: 600;
        color: var(--color-text-tertiary);
        text-transform: uppercase;
        letter-spacing: 0.03em;
    }

    .cap-detail-value {
        font-size: var(--text-sm);
        color: var(--color-text-secondary);
    }

    .cap-detail-extra-block {
        flex-direction: column;
        align-items: stretch;
    }

    .btn-secondary {
        background: var(--color-btn-secondary-bg);
        border-color: var(--color-border-subtle);
        color: var(--color-text-primary);
    }

    .btn-secondary:hover:not(:disabled) {
        background: var(--color-border-subtle);
    }
</style>
