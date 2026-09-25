<script lang="ts">
    /**
     * The switch that decides where a task runs. It is the only place that decision is made — the
     * server stores it and never re-reads it — and turning it off is the one edit that needs the
     * person's answer again before it is written.
     */
    import { t } from '$lib/i18n';
    import Icon from '$lib/components/Icon.svelte';
    import shieldIcon from '$lib/assets/icons/shield.svg?raw';
    import shieldOffIcon from '$lib/assets/icons/shield-off.svg?raw';
    import ToggleSwitch from '../ToggleSwitch.svelte';
    import type { SandboxDraftOwner } from './draft.svelte';

    let { draft, locked }: { draft: SandboxDraftOwner; locked: boolean } = $props();
</script>

<div class="config-row">
    <div class="config-info">
        <div class="config-icon">
            {#if draft.sandboxEnabled}
                <Icon src={shieldIcon} width={20} height={20} />
            {:else}
                <Icon src={shieldOffIcon} color="var(--color-status-red)" width={20} height={20} />
            {/if}
        </div>
        <div class="config-text">
            <span class="config-label">{t('sandbox.isolation')}</span>
            <span class="config-desc">{t('sandbox.isolationDesc')}</span>
        </div>
    </div>
    <ToggleSwitch bind:checked={draft.sandboxEnabled} disabled={locked} ariaLabel={t('sandbox.isolationAria')}
                  onChange={() => draft.onSandboxEnabledChanged()} />
</div>

<style>
    @import '$lib/styles/sandbox-config.css';
</style>
