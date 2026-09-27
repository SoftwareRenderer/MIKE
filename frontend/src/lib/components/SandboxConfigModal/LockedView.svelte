<script lang="ts">
    /**
     * What the project's sandbox record says, shown instead of the form while tasks are in progress.
     * The lists of saved values are the same rows SandboxPanel shows — see SandboxSettingLists.
     */
    import { t } from '$lib/i18n';
    import { formatMemoryLimitMB } from '$lib/utils/memory-stats';
    import { formatAssignedHost, formatForwardPorts } from '$lib/utils/sandbox-ports';
    import SandboxSettingLists from '../SandboxSettingLists.svelte';
    import type { SandboxDraftOwner } from './draft.svelte';

    let { draft }: { draft: SandboxDraftOwner } = $props();
</script>

<div class="locked-view">
    <div class="locked-notice">
        <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
            <rect x="3" y="7" width="10" height="7" rx="1.5" stroke="currentColor" stroke-width="1.3"/>
            <path d="M5.5 7V5a2.5 2.5 0 015 0v2" stroke="currentColor" stroke-width="1.3" stroke-linecap="round"/>
        </svg>
        <p>{t('sandbox.lockedNotice')}</p>
    </div>

    <div class="sandbox-settings">
        <div class="sandbox-row">
            <div class="setting-label-group">
                <span class="sandbox-label">{t('sandbox.isolation')}</span>
            </div>
            <span class="sandbox-value">{draft.sandboxEnabled ? t('sandbox.enabled') : t('sandbox.disabled')}</span>
        </div>

        {#if draft.sandboxEnabled}
            <div class="sandbox-row">
                <span class="sandbox-label">{t('sandbox.networkIsolation')}</span>
                <span class="sandbox-value">{draft.networkIsolation ? t('sandbox.enabled') : t('sandbox.disabled')}</span>
            </div>

            <div class="sandbox-row">
                <span class="sandbox-label">{t('sandbox.memoryLimit')}</span>
                <span class="sandbox-value">{formatMemoryLimitMB(draft.memoryLimitMB)}</span>
            </div>

            <div class="sandbox-row">
                <span class="sandbox-label">{t('sandbox.image')}</span>
                <span class="sandbox-value sandbox-value-mono">{draft.dockerfilePath || draft.image || t('sandbox.default')}</span>
            </div>

            {#if draft.forwardPorts.length > 0}
                <div class="sandbox-row">
                    <span class="sandbox-label">{t('sandbox.forwardedPorts')}</span>
                    <span class="sandbox-value">{formatForwardPorts(draft.forwardPorts)}</span>
                </div>

                {#if draft.hasActivePorts}
                    <div class="sandbox-section sandbox-section-bounded">
                        <span class="sandbox-section-label">{t('sandbox.activePortMappings')}</span>
                        <div class="sandbox-list">
                            {#each draft.forwardPorts as port}
                                {#if draft.activeHostPorts.has(port.container_port)}
                                    <div class="sandbox-item">
                                        <span class="sandbox-item-key" title={t('sandbox.hostPort')}>{formatAssignedHost(draft.activeHostPorts.get(port.container_port) ?? 0, port.host_ip)}</span>
                                        <svg width="12" height="12" viewBox="0 0 12 12" fill="none" aria-hidden="true">
                                            <path d="M4 3L8 6L4 9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                                        </svg>
                                        <span class="sandbox-item-text" title={t('sandbox.containerPort')}>{port.container_port}</span>
                                    </div>
                                {/if}
                            {/each}
                        </div>
                    </div>
                {/if}
            {/if}

            <SandboxSettingLists mounts={draft.mounts} envs={draft.envs} capabilities={draft.capabilities} bounded />
        {/if}
    </div>
</div>

<style>
    @import '$lib/styles/sandbox-settings.css';

    .locked-view {
        display: flex;
        flex-direction: column;
        gap: 12px;
    }

    .locked-notice {
        display: flex;
        align-items: flex-start;
        gap: 10px;
        padding: 10px 14px;
        background: rgba(210, 153, 34, 0.1);
        border: 1px solid rgba(210, 153, 34, 0.3);
        border-radius: var(--radius-md);
        color: var(--color-status-yellow, #d29922);
        font-size: var(--text-sm);
    }

    .locked-notice svg {
        flex-shrink: 0;
        margin-top: 1px;
    }

    .locked-notice p {
        margin: 0;
        line-height: 1.4;
    }

    .setting-label-group {
        display: flex;
        align-items: center;
        gap: 8px;
    }
</style>
