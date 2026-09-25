<script lang="ts">
    /**
     * Whether the sandbox container may reach the network. It bounds the container only: a
     * contributed extension tool runs in the MIKE server process, so its requests leave while
     * isolation is on, and that is what the notice below says out loud
     * (`frontend/src/lib/utils/server-tool-network.ts` is the rule).
     */
    import { t } from '$lib/i18n';
    import { appStore } from '$lib/stores';
    import { formatNetworkedExtensions, serverToolExtensions } from '$lib/utils/server-tool-network';
    import ToggleSwitch from '../ToggleSwitch.svelte';
    import type { SandboxDraftOwner } from './draft.svelte';

    let { draft, locked }: { draft: SandboxDraftOwner; locked: boolean } = $props();

    // See $lib/utils/server-tool-network for the rule.
    const serverToolNotice = $derived(
        serverToolExtensions(appStore.config, draft.sandboxEnabled, draft.networkIsolation)
    );
</script>

<div class="config-row" onclickcapture={() => { draft.markNetworkTouched(); }}>
    <div class="config-info">
        <div class="config-icon">
            {#if draft.networkIsolation}
                <svg width="20" height="20" viewBox="0 0 20 20" fill="none" aria-hidden="true">
                    <path d="M10 1v10M5 11h10M7 15h6M8.5 19h3" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
                </svg>
            {:else}
                <svg width="20" height="20" viewBox="0 0 20 20" fill="none" aria-hidden="true">
                    <circle cx="10" cy="17" r="1.2" fill="currentColor"/>
                    <path d="M8 13.5Q10 12 12 13.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" fill="none"/>
                    <path d="M4.5 10.5Q10 6 15.5 10.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" fill="none"/>
                    <path d="M1 7.5Q10 0.5 19 7.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" fill="none"/>
                </svg>
            {/if}
        </div>
        <div class="config-text">
            <span class="config-label">{t('sandbox.networkIsolation')}</span>
            <span class="config-desc">{t('sandbox.networkIsolationDesc')}</span>
        </div>
    </div>
    <ToggleSwitch bind:checked={draft.networkIsolation} disabled={locked} ariaLabel={t('sandbox.networkIsolationAria')} />
</div>

{#if serverToolNotice.length > 0}
    <div class="server-tool-notice">
        {t('sandbox.networkIsolationServerTools', { extensions: formatNetworkedExtensions(serverToolNotice) })}
    </div>
{/if}

<style>
    @import '$lib/styles/sandbox-config.css';

    .server-tool-notice {
        padding: 8px 12px;
        margin: -4px 0 12px;
        background: rgba(210, 153, 34, 0.08);
        border: 1px solid rgba(210, 153, 34, 0.3);
        border-radius: var(--radius-md);
        color: var(--color-text-secondary);
        font-size: 0.75rem;
        line-height: 1.45;
    }
</style>
