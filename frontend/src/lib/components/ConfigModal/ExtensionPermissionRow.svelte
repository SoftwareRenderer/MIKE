<script lang="ts">
    import { t } from '$lib/i18n';
    import { isDangerousPermission, permissionDescriptions } from '$lib/utils/extension-permissions';
    import PermissionWarningIcon from './PermissionWarningIcon.svelte';

    /**
     * One permission the manifest declared. Nothing here interprets the name: the label and the
     * explanation come from the catalog of declared permissions, and a name the catalog does not hold
     * is shown as itself and admitted to be unexplained.
     */
    let { perm }: { perm: string } = $props();

    const info = $derived(permissionDescriptions()[perm]);
</script>

<div class="cap-permission">
    <span class="cap-permission-name">
        {info?.label || perm}
        {#if isDangerousPermission(perm)}
            <PermissionWarningIcon />
        {/if}
    </span>
    {#if info?.description}
        <span class="cap-permission-desc">{info.description}</span>
    {:else}
        <span class="cap-permission-desc">{t('settings.extensions.permUndocumented')}</span>
    {/if}
</div>

<style>
    .cap-permission {
        display: flex;
        flex-direction: column;
        gap: 0.1rem;
        padding: 0.4rem 0.55rem;
        background: var(--color-bg-input);
        border: 1px solid var(--color-border-subtle);
        border-radius: 6px;
    }

    .cap-permission-name {
        display: inline-flex;
        align-items: center;
        gap: 4px;
        font-size: 0.8rem;
        font-weight: 600;
        color: var(--color-text-primary);
    }

    .cap-permission-desc {
        font-size: 0.78rem;
        color: var(--color-text-secondary);
        line-height: 1.4;
    }
</style>
