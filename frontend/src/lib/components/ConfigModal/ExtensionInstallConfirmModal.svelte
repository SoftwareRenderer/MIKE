<script lang="ts">
    import type { RegistryEntry } from '$lib/types';
    import { t } from '$lib/i18n';
    import BaseModal from '../BaseModal.svelte';
    import ExtensionPermissionRow from './ExtensionPermissionRow.svelte';
    import PermissionWarningIcon from './PermissionWarningIcon.svelte';
    import {
        dependencyCacheDeclaration,
        sortPermissions,
        type AgentMountProposal,
    } from '$lib/utils/extension-permissions';
    import { capabilityDescriptions } from '$lib/utils/extension-manifest';

    /**
     * Shown before installing an extension that asks for network access, for permissions that escape the
     * sandbox, or for a host folder. Closing it installs nothing.
     *
     * The folder rows are the only place the user is asked about a folder, and the manifest's path is a
     * suggestion — an extension's `sandbox.mount` is a request, not a grant.
     */
    let { entry, agentMounts, onconfirm, onclose }: {
        entry: RegistryEntry;
        agentMounts: AgentMountProposal[];
        onconfirm: (mount: boolean) => void;
        onclose: () => void;
    } = $props();

    const network = $derived(entry.capabilities?.network);
    const declaredDep = $derived(dependencyCacheDeclaration(entry.capabilities));
    const permissions = $derived(sortPermissions((entry.capabilities?.ui?.permissions ?? []) as string[]));
    const descriptions = $derived(capabilityDescriptions());
</script>

{#snippet installConfirmFooter()}
    <button type="button" class="btn" onclick={onclose}>{t('settings.cancel')}</button>
    {#if agentMounts.length > 0}
        <button type="button" class="btn" onclick={() => onconfirm(false)}>{t('settings.extensions.agentMountDecline')}</button>
        <button type="button" class="btn btn-primary" onclick={() => onconfirm(true)}>{t('settings.extensions.agentMountGrant')}</button>
    {:else}
        <button type="button" class="btn btn-primary" onclick={() => onconfirm(false)}>{t('settings.extensions.install')}</button>
    {/if}
{/snippet}

<BaseModal
    title={t('settings.extensions.installTitle', { name: entry.display_name || entry.name })}
    width="480px"
    modalClass="install-confirm-modal"
    footer={installConfirmFooter}
    onClose={onclose}
>
    <p class="install-confirm-intro">{t('settings.extensions.installIntro')}</p>
    <div class="cap-permission-list">
        {#if network}
            <div class="cap-permission">
                <span class="cap-permission-name">
                    {descriptions.network.label}
                    <PermissionWarningIcon />
                </span>
                <span class="cap-permission-desc">{descriptions.network.description}</span>
                {#if network.allowed_domains?.length}
                    <div class="cap-detail-tags">
                        {#each network.allowed_domains as domain}
                            <span class="cap-detail-tag">{domain}</span>
                        {/each}
                    </div>
                {/if}
            </div>
        {/if}
        {#if declaredDep}
            <!-- This is not a sandbox escape. Installing approves nothing; it only means the user is
                 asked about a folder afterwards. -->
            <div class="cap-permission">
                <span class="cap-permission-name">{t('settings.extensions.depPermission')}</span>
                <span class="cap-permission-desc">
                    {t('settings.extensions.depPermissionDesc.before')} <code>{declaredDep.env}={declaredDep.container_path}</code> {t('settings.extensions.depPermissionDesc.after')}
                </span>
            </div>
        {/if}
        {#if agentMounts.length > 0}
            <div class="cap-permission">
                <span class="cap-permission-name">{t('settings.extensions.agentMountPermission')}</span>
                <span class="cap-permission-desc">
                    {t('settings.extensions.agentMountPermissionDesc.before')} <code>{agentMounts[0].container_path}</code> {t('settings.extensions.agentMountPermissionDesc.after')}
                </span>
                <p class="field-desc">{t('settings.extensions.agentMountAsk')}</p>
                {#each agentMounts as proposal}
                    <div class="cap-detail-tags">
                        <span class="cap-detail-tag">
                            {t('settings.extensions.agentMountProposal', { host: proposal.host_path, container: proposal.container_path, mode: proposal.mode || 'rw' })}
                        </span>
                    </div>
                {/each}
            </div>
        {/if}
        {#each permissions as perm}
            <ExtensionPermissionRow {perm} />
        {/each}
    </div>
</BaseModal>

<style>
    .install-confirm-intro {
        margin: 0 0 0.6rem;
        font-size: var(--text-base);
        color: var(--color-text-secondary);
    }

    .field-desc {
        color: var(--color-text-tertiary);
        font-size: var(--text-xs);
        margin: 0;
    }
</style>
