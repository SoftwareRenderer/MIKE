<script lang="ts">
    import type { RegistryEntry } from '$lib/types';
    import { t } from '$lib/i18n';
    import ExtensionBuildProgress from './ExtensionBuildProgress.svelte';
    import type { OperationProgress } from './ExtensionBuildProgress.svelte';
    import ExtensionLocalPath from './ExtensionLocalPath.svelte';
    import {
        agentMountDeclaration,
        dependencyCacheDeclaration,
        requiresSetupAfterInstall,
    } from '$lib/utils/extension-permissions';
    import { capabilityDescriptions, packLocale, registryCapabilityKeys, typeBadge } from '$lib/utils/extension-manifest';
    import { identityKey } from '$lib/utils/extension-identity';
    import { safeUrl } from '$lib/utils/safe-url';

    /**
     * One entry of the Browse list: a published registry entry, or an unpublished folder from the local
     * extensions directory. Nothing is installed yet, so every state marker here is a label rather than a
     * control — and Install is the one button, which the tab may stop at the consent dialog.
     */
    let {
        entry,
        progress = null,
        installing = false,
        installed = false,
        unpublished = false,
        revealed = false,
        onreveal,
        oncaps,
        oninstall,
    }: {
        entry: RegistryEntry;
        progress?: OperationProgress | null;
        installing?: boolean;
        installed?: boolean;
        unpublished?: boolean;
        revealed?: boolean;
        onreveal: () => void;
        oncaps: (entry: RegistryEntry) => void;
        oninstall: (entry: RegistryEntry, unpublished: boolean) => void;
    } = $props();

    let identity = $derived(identityKey(entry.author, entry.name));
    let capKeys = $derived(registryCapabilityKeys(entry));
    let capDescriptions = $derived(capabilityDescriptions());
    let needsSetup = $derived(requiresSetupAfterInstall(entry.capabilities));
    let setupDep = $derived(dependencyCacheDeclaration(entry.capabilities));
    let setupAgentMounts = $derived(agentMountDeclaration(entry.capabilities));
</script>

<!-- Two extensions can display one name (author is part of the identity, not of the display name), so a
     card is addressed by `data-identity`, never by its text. -->
<div class="extension-card registry-card" data-identity={identity} class:unpublished-card={unpublished}>
    <div class="card-header">
        <div class="ext-icon" class:ext-lsp={entry.type === 'lsp'} class:ext-ui={entry.type === 'ui'} class:ext-css={entry.type === 'css'} class:ext-agent={entry.type === 'agent'} class:ext-llm={entry.type === 'llm'} class:ext-i18n={entry.type === 'i18n'}>
            {typeBadge(entry.type)}
        </div>
        <div class="ext-info">
            <div class="ext-name-row">
                <span class="ext-name">{entry.display_name || entry.name}</span>
                {#if entry.featured}
                    <span class="featured-badge">{t('settings.extensions.featured')}</span>
                {/if}
                {#if unpublished}
                    <button
                        class="unpublished-badge"
                        type="button"
                        title={t('settings.extensions.localUnpublishedTitle')}
                        aria-expanded={revealed}
                        onclick={(e) => { e.stopPropagation(); onreveal(); }}
                    >{t('settings.extensions.local')}</button>
                {/if}
                {#if needsSetup}
                    <!-- A plain label, not a button: nothing is installed yet, so there is no record to open. -->
                    <span
                        class="needs-setup-badge"
                        title={t('settings.extensions.needsSetupRegistryTitle', { env: setupDep?.env, container: setupDep?.container_path })}
                    >{t('settings.extensions.needsSetup')}</span>
                {/if}
                {#if capKeys.length > 0}
                    <div class="capability-badges" role="group" aria-label={t('settings.extensions.capabilitiesGroup')}>
                        {#each capKeys as regCapKey}
                            {@const capInfo = capDescriptions[regCapKey]}
                            <button class="cap-badge" class:cap-lsp={regCapKey === 'lsp'} class:cap-ui={regCapKey === 'ui'} class:cap-css={regCapKey === 'css'} class:cap-agent={regCapKey === 'agent'} class:cap-network={regCapKey === 'network'} class:cap-llm={regCapKey === 'llm'} class:cap-i18n={regCapKey === 'i18n'} aria-label={t('settings.extensions.capAriaFull', { label: capInfo.label, name: entry.display_name || entry.name, description: capInfo.description })} onclick={() => oncaps(entry)}>
                                {capInfo.label}
                            </button>
                        {/each}
                    </div>
                {/if}
            </div>
            <span class="ext-meta">
                v{entry.version}
                {#if entry.author}
                    · {entry.author}
                {/if}
                {#if entry.license_url} · <a href={safeUrl(entry.license_url)} target="_blank" rel="noopener noreferrer" class="ext-link">{t('settings.extensions.license')}</a>{/if}
                {#if entry.homepage} · <a href={safeUrl(entry.homepage)} target="_blank" rel="noopener noreferrer" class="ext-link">{t('settings.extensions.website')}</a>{/if}
                <!-- The locale an i18n pack declares, shown in its own language: the one thing a
                     person needs to pick the right pack out of the list. -->
                {#if entry.type === 'i18n' && packLocale(entry.capabilities)} · <span class="ext-locale" title={t('settings.extensions.localeHint')}>{packLocale(entry.capabilities)}</span>{/if}
            </span>
        </div>
        <div class="ext-action">
            {#if installed}
                <span class="installed-badge">{t('settings.extensions.installed')}</span>
            {:else}
                <button class="btn btn-primary btn-small" onclick={() => oninstall(entry, unpublished)} disabled={installing}>
                    {#if installing}
                        <span class="spinner"></span>
                        {t('settings.extensions.installingNow')}
                    {:else}
                        {t('settings.extensions.install')}
                    {/if}
                </button>
            {/if}
        </div>
    </div>
    {#if progress}
        <ExtensionBuildProgress progress={progress.progress} message={progress.message} error={progress.error} />
    {/if}
    {#if unpublished && revealed}
        <ExtensionLocalPath path={entry.local_path} />
    {/if}
    {#if entry.description}
        <p class="ext-description">{entry.description}</p>
    {/if}
    {#if needsSetup}
        <!-- Not a permission warning: installing grants nothing. The step is a folder to pick. -->
        <p class="ext-note ext-needs-setup-note">
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="10"/><line x1="12" y1="16" x2="12" y2="12"/><line x1="12" y1="8" x2="12.01" y2="8"/></svg>
            {#if setupDep}
                <span>{t('settings.extensions.needsSetupNote.before')} <code>{setupDep?.env}={setupDep?.container_path}</code> {t('settings.extensions.needsSetupNote.after')}</span>
            {:else}
                <span>{t('settings.extensions.agentSetupNote.before')} <code>{setupAgentMounts[0]?.container_path}</code> {t('settings.extensions.agentSetupNote.after')}</span>
            {/if}
        </p>
    {/if}
    {#if entry.capabilities?.network?.allowed_domains?.length}
        <div class="ext-proxy-domains">
            <span class="ext-proxy-label">{t('settings.extensions.connectsTo')}</span>
            <div class="cap-detail-tags">
                {#each entry.capabilities.network.allowed_domains as domain}
                    <button class="cap-detail-tag" onclick={() => oncaps(entry)}>{domain}</button>
                {/each}
            </div>
        </div>
    {/if}
    {#if entry.type === 'agent'}
        <p class="ext-note">
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="10"/><line x1="12" y1="16" x2="12" y2="12"/><line x1="12" y1="8" x2="12.01" y2="8"/></svg>
            {t('settings.extensions.agentNote.before')} {entry.display_name} {t('settings.extensions.agentNote.after')} <code>{t('settings.tab.providers')}</code>
        </p>
    {/if}
</div>

<style>
    .card-header {
        display: flex;
        align-items: flex-start;
        gap: 0.75rem;
        margin-bottom: 0.5rem;
    }

    .ext-action {
        flex-shrink: 0;
        display: flex;
        align-items: flex-start;
    }

    .btn {
        padding: 0.4rem 0.75rem;
        border-radius: var(--radius-md);
        font-size: var(--text-sm);
        cursor: pointer;
        border: 1px solid transparent;
        transition: all 0.15s;
    }

    .btn-primary {
        background: var(--color-btn-primary-bg);
        color: var(--color-btn-text);
    }

    .btn-primary:hover:not(:disabled) {
        background: var(--color-btn-primary-hover);
    }

    .btn-small {
        padding: 0.25rem 0.5rem;
        font-size: var(--text-xs);
    }

    /* Spinner */
    .spinner {
        display: inline-block;
        width: 12px;
        height: 12px;
        border: 2px solid rgba(255, 255, 255, 0.3);
        border-top-color: var(--color-btn-text);
        border-radius: 50%;
        animation: spin 0.6s linear infinite;
        vertical-align: middle;
        margin-right: 0.35rem;
    }

    .installed-badge {
        font-size: var(--text-sm);
        color: var(--color-status-green);
        padding: 0.4rem 0.75rem;
        background: rgba(63, 185, 80, 0.1);
        border-radius: var(--radius-md);
    }
</style>
