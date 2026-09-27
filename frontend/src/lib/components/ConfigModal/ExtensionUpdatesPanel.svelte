<script lang="ts">
    import { t } from '$lib/i18n';
    import { isDangerousPermission, permissionDescriptions, sortPermissions } from '$lib/utils/extension-permissions';
    import PermissionWarningIcon from './PermissionWarningIcon.svelte';
    import type { ExtensionUpdate } from '$lib/types';

    /**
     * "Updates Available": one row per extension the registry has newer than the commit pinned here,
     * with the changelog entries since that commit and whatever new permissions or domains the newer
     * manifest declares.
     *
     * A row that adds domains is a decision, not an update: `Update all` leaves it listed and the
     * dialog about the new domains comes from the tab, which owns the answer the server asks for.
     */
    let { updates, hasChecked, updatingAll, awaitingReview, onupdate, onupdateall }: {
        updates: ExtensionUpdate[];
        hasChecked: boolean;
        updatingAll: boolean;
        awaitingReview: string[];
        onupdate: (update: ExtensionUpdate) => void;
        onupdateall: () => void;
    } = $props();

    const descriptions = $derived(permissionDescriptions());
</script>

{#if hasChecked && updates.length === 0}
    <div class="updates-section updates-none">
        <h3>{t('settings.extensions.noUpdates')}</h3>
        <p class="updates-no-result">{t('settings.extensions.upToDate')}</p>
    </div>
{:else if updates.length > 0}
    <div class="updates-section">
        <div class="updates-header">
            <h3>{t('settings.extensions.updatesAvailable')}</h3>
            <button class="btn btn-primary btn-small" onclick={onupdateall} disabled={updatingAll}>
                {updatingAll ? t('settings.extensions.updating') : t('settings.extensions.updateAll')}
            </button>
        </div>
        {#if awaitingReview.length > 0}
            <div class="update-new-perms">
                <div class="update-new-perms-title">{t('settings.extensions.updateAllSkipped', { names: awaitingReview.join(', ') })}</div>
            </div>
        {/if}
        {#each updates as update}
            {@const newPerms = update.new_permissions ?? []}
            <div class="update-item" class:has-new-perms={newPerms.length > 0 || !!update.domains_added}>
                <div class="update-info">
                    <span class="update-name" title={update.name}>{update.name}</span>
                    <span class="update-versions">{update.current_version} → {update.available_version}</span>
                </div>
                {#if (update.changelog ?? []).length > 0}
                    <div class="update-changelog">
                        <div class="update-changelog-title">{t('settings.extensions.changesSince', { version: update.current_version })}</div>
                        <ul class="update-changelog-list">
                            {#each update.changelog as change}
                                <li class="update-changelog-item">
                                    <span class="update-changelog-desc">{change.description}</span>
                                    {#if change.hash}<code class="update-changelog-hash" title={change.hash}>{change.hash.slice(0, 7)}</code>{/if}
                                </li>
                            {/each}
                        </ul>
                    </div>
                {/if}
                {#if newPerms.length > 0}
                    <div class="update-new-perms">
                        <div class="update-new-perms-title">{t('settings.extensions.newPermsTitle')}</div>
                        <ul class="update-new-perms-list">
                            {#each sortPermissions(newPerms) as perm}
                                {@const info = descriptions[perm]}
                                <li class="update-new-perm">
                                    <span class="update-new-perm-name">
                                        {info?.label || perm}
                                        {#if isDangerousPermission(perm)}
                                            <PermissionWarningIcon />
                                        {/if}
                                    </span>
                                    {#if info?.description}<span class="update-new-perm-desc">{info.description}</span>{/if}
                                </li>
                            {/each}
                        </ul>
                    </div>
                {/if}
                {#if update.domains_added}
                    <div class="update-new-perms">
                        <div class="update-new-perms-title">{t('settings.extensions.updateDomainsNote')}</div>
                    </div>
                {/if}
                <button class="btn btn-primary btn-small update-action" onclick={() => onupdate(update)}>{t('settings.extensions.update')}</button>
            </div>
        {/each}
    </div>
{/if}

<style>
    /* Updates section */
    .updates-section {
        background: rgba(88, 166, 255, 0.08);
        border: 1px solid rgba(88, 166, 255, 0.2);
        border-radius: var(--radius-lg);
        padding: 1rem;
    }

    .updates-section h3 {
        margin: 0;
        font-size: var(--text-base);
        color: var(--color-accent);
    }

    .updates-header {
        display: flex;
        justify-content: space-between;
        align-items: center;
        margin-bottom: 0.5rem;
        gap: 1rem;
    }

    .updates-none .updates-no-result {
        margin: 0.25rem 0 0;
        font-size: var(--text-base);
        color: var(--color-text-secondary);
    }

    .update-item {
        display: flex;
        flex-wrap: wrap;
        justify-content: space-between;
        align-items: center;
        gap: 0.5rem 1rem;
        padding: 0.5rem 0;
    }

    .update-item:not(:last-child) {
        border-bottom: 1px solid rgba(88, 166, 255, 0.15);
    }

    .update-item.has-new-perms {
        padding: 0.75rem;
        margin: 0.5rem 0;
        background: rgba(255, 166, 0, 0.1);
        border: 1px solid rgba(255, 166, 0, 0.4);
        border-radius: var(--radius-lg);
    }

    .update-info {
        display: flex;
        align-items: baseline;
        flex: 1 1 0;
        min-width: 0;
    }

    .update-name {
        font-weight: 600;
        color: var(--color-text-primary);
        min-width: 0;
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
    }

    .update-versions {
        font-size: var(--text-sm);
        color: var(--color-text-tertiary);
        margin-left: 0.5rem;
        flex-shrink: 0;
    }

    .update-action {
        margin-left: auto;
        flex-shrink: 0;
    }

    .update-new-perms {
        flex-basis: 100%;
        margin: 0.25rem 0 0.5rem;
        padding: 0.6rem 0.75rem;
        background: rgba(255, 166, 0, 0.12);
        border: 1px solid rgba(255, 166, 0, 0.35);
        border-radius: var(--radius-md);
    }

    .update-new-perms-title {
        font-size: var(--text-sm);
        font-weight: 700;
        color: var(--color-warning, #e0a030);
        margin-bottom: 0.4rem;
    }

    .update-new-perms-list {
        list-style: none;
        margin: 0;
        padding: 0;
        display: flex;
        flex-direction: column;
        gap: 0.4rem;
    }

    .update-new-perm {
        display: flex;
        flex-direction: column;
        gap: 0.05rem;
    }

    .update-new-perm-name {
        display: inline-flex;
        align-items: center;
        gap: 4px;
        font-size: var(--text-sm);
        font-weight: 600;
        color: var(--color-text-primary);
    }

    .update-new-perm-desc {
        font-size: var(--text-xs);
        color: var(--color-text-secondary);
        line-height: 1.4;
    }

    .update-changelog {
        flex-basis: 100%;
        margin: 0.25rem 0 0.5rem;
        padding: 0.6rem 0.75rem;
        background: rgba(88, 166, 255, 0.08);
        border: 1px solid rgba(88, 166, 255, 0.25);
        border-radius: var(--radius-md);
    }

    .update-changelog-title {
        font-size: var(--text-sm);
        font-weight: 700;
        color: var(--color-text-secondary);
        margin-bottom: 0.4rem;
    }

    .update-changelog-list {
        list-style: none;
        margin: 0;
        padding: 0;
        display: flex;
        flex-direction: column;
        gap: 0.3rem;
    }

    .update-changelog-item {
        display: flex;
        align-items: baseline;
        gap: 0.5rem;
    }

    .update-changelog-desc {
        font-size: var(--text-sm);
        color: var(--color-text-primary);
        line-height: 1.4;
    }

    .update-changelog-hash {
        font-size: var(--text-xs);
        color: var(--color-text-tertiary);
        background: rgba(88, 166, 255, 0.12);
        padding: 0.05rem 0.3rem;
        border-radius: var(--radius-sm);
        white-space: nowrap;
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
</style>
