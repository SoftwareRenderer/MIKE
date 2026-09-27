<script lang="ts">
    import type { Extension } from '$lib/types';
    import { t } from '$lib/i18n';
    import ToggleSwitch from '../ToggleSwitch.svelte';
    import ExtensionBuildProgress from './ExtensionBuildProgress.svelte';
    import ExtensionLocalPath from './ExtensionLocalPath.svelte';
    import type { OperationProgress } from './ExtensionBuildProgress.svelte';
    import {
        agentMountDeclaration,
        agentMountGrant,
        needsAgentMountGrant,
        needsDependencyGrant,
    } from '$lib/utils/extension-permissions';
    import { capabilityDescriptions, capabilityKeys, formatBuiltAt, packLocale, typeBadge } from '$lib/utils/extension-manifest';
    import { identityKey } from '$lib/utils/extension-identity';
    import { safeUrl } from '$lib/utils/safe-url';

    /**
     * One installed extension: what it is, whether it is switched on, what it may reach, and the two
     * host folders it was promised. The card itself grants nothing — the folders shown here were
     * approved in the Edit dialog or from the install review, and the only action here that changes a
     * grant is the one that clears it.
     */
    let {
        ext,
        progress = null,
        updating = false,
        setupFocus = false,
        revealed = false,
        onreveal,
        oncaps,
        onedit,
        ontoggle,
        ondelete,
        onupdateimage,
        onrevoke,
    }: {
        ext: Extension;
        progress?: OperationProgress | null;
        updating?: boolean;
        setupFocus?: boolean;
        revealed?: boolean;
        onreveal: () => void;
        oncaps: (ext: Extension) => void;
        onedit: (ext: Extension) => void;
        ontoggle: (ext: Extension) => void;
        ondelete: (id: string, name: string) => void;
        onupdateimage: (id: string, name: string) => void;
        onrevoke: (ext: Extension) => void;
    } = $props();

    let isLocal = $derived(ext.install_source === 'local');
    let capKeys = $derived(capabilityKeys(ext));
    let declaredMounts = $derived(agentMountDeclaration(ext.manifest.capabilities));
    let granted = $derived(agentMountGrant(ext));
    let needsGrant = $derived(needsDependencyGrant(ext) || needsAgentMountGrant(ext));
    let capDescriptions = $derived(capabilityDescriptions());
</script>

<div
    class="extension-card"
    data-identity={identityKey(ext.author, ext.name)}
    class:disabled={!ext.enabled}
    class:unpublished-card={isLocal}
    class:card-setup-focus={setupFocus}
>
    <div class="card-header">
        <div class="ext-icon" class:ext-lsp={ext.type === 'lsp'} class:ext-ui={ext.type === 'ui'} class:ext-css={ext.type === 'css'} class:ext-agent={ext.type === 'agent'} class:ext-llm={ext.type === 'llm'} class:ext-i18n={ext.type === 'i18n'}>
            {typeBadge(ext.type)}
        </div>
        <div class="ext-info">
            <div class="ext-name-row">
                <span class="ext-name">{ext.manifest.display_name || ext.name}</span>
                {#if isLocal}
                    <button
                        class="unpublished-badge"
                        type="button"
                        title={t('settings.extensions.localInstalledTitle')}
                        aria-expanded={revealed}
                        onclick={(e) => { e.stopPropagation(); onreveal(); }}
                    >{t('settings.extensions.local')}</button>
                {/if}
                {#if needsGrant}
                    <!-- Same attention color as the dot on the Edit button, and it opens the same
                         dialog: one state, said twice because a dot on an icon is easy to look
                         straight past. -->
                    <button
                        class="needs-setup-badge"
                        type="button"
                        id={`ext-card-${ext.id}`}
                        title={needsAgentMountGrant(ext)
                            ? t('settings.extensions.needsSetupAgentTitle')
                            : t('settings.extensions.needsSetupTitle')}
                        onclick={(e) => { e.stopPropagation(); onedit(ext); }}
                    >{t('settings.extensions.needsSetup')}</button>
                {/if}
                {#if capKeys.length > 0}
                    <div class="capability-badges" role="group" aria-label={t('settings.extensions.capabilitiesGroup')}>
                        {#each capKeys as capKey}
                            <button class="cap-badge" class:cap-lsp={capKey === 'lsp'} class:cap-ui={capKey === 'ui'} class:cap-css={capKey === 'css'} class:cap-agent={capKey === 'agent'} class:cap-network={capKey === 'network'} class:cap-llm={capKey === 'llm'} class:cap-i18n={capKey === 'i18n'} aria-label={t('settings.extensions.capAria', { label: capDescriptions[capKey]?.label || capKey, name: ext.manifest.display_name || ext.name })} onclick={() => oncaps(ext)}>
                                {capDescriptions[capKey]?.label || capKey}
                            </button>
                        {/each}
                    </div>
                {/if}
            </div>
            <span class="ext-meta">
                {#if ext.type === 'ui' || ext.type === 'css'}
                    v{ext.version}
                {:else}
                    {formatBuiltAt(ext.built_at)}
                {/if}
                {#if ext.manifest.author}
                    · {ext.manifest.author}
                {/if}
                {#if ext.manifest.license_url} · <a href={safeUrl(ext.manifest.license_url)} target="_blank" rel="noopener noreferrer" class="ext-link">{t('settings.extensions.license')}</a>{/if}
                {#if ext.manifest.homepage} · <a href={safeUrl(ext.manifest.homepage)} target="_blank" rel="noopener noreferrer" class="ext-link">{t('settings.extensions.website')}</a>{/if}
                {#if ext.type === 'i18n' && packLocale(ext.manifest.capabilities)} · <span class="ext-locale" title={t('settings.extensions.localeHint')}>{packLocale(ext.manifest.capabilities)}</span>{/if}
            </span>
        </div>
        <div class="card-actions">
            <button class="btn btn-icon btn-danger" title={t('settings.extensions.delete')} onclick={() => ondelete(ext.id, ext.name)}>
                <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                    <path d="M2 4h12M5 4V3a1 1 0 011-1h4a1 1 0 011 1v1M6 7v5M10 7v5" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                    <path d="M3 4l1 9a1 1 0 001 1h6a1 1 0 001-1l1-9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                </svg>
            </button>
            {#if ext.type === 'lsp'}
            <button class="btn btn-icon" title={t('settings.extensions.updateImageTitle')} onclick={() => onupdateimage(ext.id, ext.name)} disabled={updating}>
                {#if updating}
                    <span class="spinner"></span>
                {:else}
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                        <path d="M21 2v6h-6"/>
                        <path d="M3 12a9 9 0 0 1 15-6.7L21 8"/>
                        <path d="M3 22v-6h6"/>
                        <path d="M21 12a9 9 0 0 1-15 6.7L3 16"/>
                    </svg>
                {/if}
            </button>
            {/if}
            {#if ext.manifest.config_schema || ext.type === 'lsp'}
            <button
                class="btn btn-icon"
                title={needsGrant ? t('settings.extensions.editNeedsSetup') : t('settings.extensions.edit')}
                aria-label={needsGrant ? t('settings.extensions.editNeedsSetup') : t('settings.extensions.edit')}
                onclick={() => onedit(ext)}
            >
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                    <path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/>
                    <path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"/>
                </svg>
                {#if needsGrant}
                    <span class="ext-needs-dot" aria-hidden="true"></span>
                {/if}
            </button>
            {/if}
            <ToggleSwitch class="card-toggle" checked={ext.enabled} onChange={() => ontoggle(ext)} />
        </div>
    </div>
    <!-- Shows the approved folders. Changing them happens in the Edit dialog. -->
    {#if declaredMounts.length > 0}
        <!-- An agent has no container of its own, so clearing an approval here interrupts nothing. -->
        <div class="ext-agent-row">
            {#if granted.length > 0}
                <!-- This button clears every approved folder, so it must not look like it belongs to
                     one row. -->
                <div class="ext-agent-paths">
                    {#each granted as entry}
                        <div class="ext-agent-path">
                            <span class="ext-dep-state-label">{t('settings.extensions.agentMounted', { mode: entry.mode || 'ro' })}</span>
                            <code class="ext-dep-path" title={t('settings.extensions.agentMountedTitle', { host: entry.host_path, container: entry.container_path })}>{entry.host_path}</code>
                        </div>
                    {/each}
                </div>
                <button
                    type="button"
                    class="btn-secondary btn-small"
                    title={t('settings.extensions.agentCardUnmountHint')}
                    onclick={() => onrevoke(ext)}
                >{t('settings.extensions.agentCardUnmount')}</button>
            {:else}
                <span class="ext-dep-none">{t('settings.extensions.agentCardNone')}</span>
            {/if}
        </div>
    {/if}
    {#if progress}
        <ExtensionBuildProgress progress={progress.progress} message={progress.message} error={progress.error} />
    {/if}
    {#if isLocal && revealed}
        <ExtensionLocalPath path={ext.local_path} />
    {/if}
    {#if ext.manifest.description}
        <p class="ext-description">{ext.manifest.description}</p>
    {/if}
    {#if ext.manifest.capabilities?.network?.allowed_domains?.length}
        <div class="ext-proxy-domains">
            <span class="ext-proxy-label">{t('settings.extensions.connectsTo')}</span>
            <div class="cap-detail-tags">
                {#each ext.manifest.capabilities.network.allowed_domains as domain}
                    <button class="cap-detail-tag" onclick={() => oncaps(ext)}>{domain}</button>
                {/each}
            </div>
        </div>
    {/if}
    {#if ext.type === 'agent'}
        <p class="ext-note">
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="10"/><line x1="12" y1="16" x2="12" y2="12"/><line x1="12" y1="8" x2="12.01" y2="8"/></svg>
            {t('settings.extensions.agentNote.before')} {ext.manifest.display_name} {t('settings.extensions.agentNote.after')} <code>{t('settings.tab.providers')}</code>
        </p>
    {/if}
</div>

<style>
    .card-actions .btn-icon {
        position: relative;
    }

    .ext-needs-dot {
        position: absolute;
        top: 1px;
        right: 1px;
        width: 7px;
        height: 7px;
        border-radius: 50%;
        background: var(--color-status-yellow);
        pointer-events: none;
    }

    /* The agent row on a card: the approved folders and the button that clears them. */
    .ext-agent-row {
        display: flex;
        align-items: center;
        gap: 0.45rem;
        flex-wrap: wrap;
        min-width: 0;
        margin-top: 0.5rem;
    }

    .ext-agent-paths {
        display: flex;
        flex-direction: column;
        gap: 0.15rem;
        min-width: 0;
    }

    .ext-agent-path {
        display: flex;
        align-items: center;
        gap: 0.45rem;
        min-width: 0;
    }

    /* A card whose install left a question unanswered. */
    .extension-card.card-setup-focus {
        border-color: var(--color-accent);
    }

    .card-header {
        display: flex;
        align-items: flex-start;
        gap: 0.75rem;
        margin-bottom: 0.5rem;
    }

    .card-actions {
        display: flex;
        align-items: center;
        gap: 0.5rem;
        flex-shrink: 0;
    }

    :global(.card-toggle) {
        margin-left: auto;
    }

    .btn {
        padding: 0.4rem 0.75rem;
        border-radius: var(--radius-md);
        font-size: var(--text-sm);
        cursor: pointer;
        border: 1px solid transparent;
        transition: all 0.15s;
    }

    .btn:disabled {
        opacity: 0.5;
        cursor: not-allowed;
    }

    .btn-secondary {
        background: var(--color-btn-secondary-bg);
        border-color: var(--color-border-subtle);
        color: var(--color-text-primary);
    }

    .btn-secondary:hover:not(:disabled) {
        background: var(--color-border-subtle);
    }

    .btn-icon {
        background: none;
        border: none;
        color: var(--color-text-tertiary);
        padding: 0.25rem;
        font-size: var(--text-lg);
    }

    .btn-icon:hover {
        color: var(--color-text-primary);
    }

    .btn-danger:hover {
        color: var(--color-status-red);
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
</style>
