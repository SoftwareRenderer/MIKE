<script lang="ts">
    /**
     * Ports the container publishes. Only a container port is configured: the host side is assigned
     * when a task runs and is what the active mappings show. This row exists only while Network
     * Isolation is off — an isolated container has nothing to reach these ports from.
     */
    import { t } from '$lib/i18n';
    import { formatForwardPort } from '$lib/utils/sandbox-ports';
    import CancelEditButton from './CancelEditButton.svelte';
    import ExpandableSection from './ExpandableSection.svelte';
    import type { SandboxDraftOwner } from './draft.svelte';

    let { draft, locked }: { draft: SandboxDraftOwner; locked: boolean } = $props();
</script>

<ExpandableSection contentClass="ports-content" subsection bind:expanded={draft.portsExpanded}
                   label={t('sandbox.ports')} desc={t('sandbox.portsDesc')} ariaLabel={t('sandbox.portsAria')}
                   icon={portsIcon}>
    {#if draft.forwardPorts.length > 0}
        <div class="port-list">
            {#each draft.forwardPorts as port, i}
                <div class="port-item" class:editing={draft.editingPortIndex === i} onclick={() => draft.editPort(i)} role="button" tabindex="0" onkeydown={(e) => e.key === 'Enter' && draft.editPort(i)}>
                    <span class="port-mapping">
                        {#if draft.activeHostPorts.has(port.container_port)}
                            <span class="port-host" title={t('sandbox.hostPort')}>{draft.activeHostPorts.get(port.container_port)}</span>
                            <svg width="12" height="12" viewBox="0 0 12 12" fill="none" aria-hidden="true">
                                <path d="M4 3L8 6L4 9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                            </svg>
                        {/if}
                        <span class="port-container" title={t('sandbox.containerPort')}>{formatForwardPort(port)}</span>
                    </span>
                    <button type="button" class="btn-remove-port" onclick={(e) => { e.stopPropagation(); draft.removePortForward(i); }} disabled={locked} title={t('sandbox.remove')}>
                        {@render closeIcon()}
                    </button>
                </div>
            {/each}
        </div>
    {/if}

    {#if draft.forwardPorts.length > 0 && !draft.hasActivePorts}
        <div class="sandbox-note port-no-active">{t('sandbox.portsNoActive')}</div>
    {/if}

    <div class="port-add-row">
        {#if draft.editingPortIndex !== null}
            <CancelEditButton onclick={() => draft.cancelPortEdit()} />
        {/if}
        <input type="text" class="port-input port-input-bind" bind:value={draft.newHostIP} placeholder={t('sandbox.hostInterface')} spellcheck="false" autocomplete="off" disabled={locked} aria-label={t('sandbox.hostInterfaceAria')} />
        <input type="number" class="port-input" bind:value={draft.newContainerPort} placeholder={t('sandbox.containerPort')} min="1" max="65535" disabled={locked} aria-label={t('sandbox.containerPort')} />
        <button type="button" class="btn-add-port" class:pending={draft.hasPendingPort} onclick={() => draft.addPortForward()} disabled={locked} title={draft.editingPortIndex !== null ? t('sandbox.savePort') : t('sandbox.addPort')}>
            {@render addIcon(draft.editingPortIndex !== null)}
        </button>
    </div>

    {#if draft.portError}
        <div class="port-error">{draft.portError}</div>
    {:else}
        <div class="port-hint">{t('sandbox.hostInterfaceHint')}</div>
    {/if}
</ExpandableSection>

{#snippet portsIcon()}
    <svg width="20" height="20" viewBox="0 0 20 20" fill="none" aria-hidden="true">
        <path d="M4 7H16M4 13H16" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
        <path d="M12 4.5L16 7L12 9.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
        <path d="M8 10.5L4 13L8 15.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
    </svg>
{/snippet}

{#snippet closeIcon()}
    <svg width="12" height="12" viewBox="0 0 12 12" fill="none" aria-hidden="true">
        <path d="M3 3L9 9M9 3L3 9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round"/>
    </svg>
{/snippet}

{#snippet addIcon(editing: boolean)}
    {#if editing}
        <svg width="14" height="14" viewBox="0 0 14 14" fill="none" aria-hidden="true">
            <path d="M3 8L6 11L11 6" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
        </svg>
    {:else}
        <svg width="14" height="14" viewBox="0 0 14 14" fill="none" aria-hidden="true">
            <path d="M7 2V12M2 7H12" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
        </svg>
    {/if}
{/snippet}

<style>
    @import '$lib/styles/sandbox-settings.css';
    @import '$lib/styles/sandbox-config.css';

    .port-input {
        flex: 1;
        min-width: 120px;
        max-width: 200px;
    }

    .port-input-bind {
        max-width: 170px;
    }

    .port-hint {
        padding: 2px 0 0 36px;
        font-size: 0.6875rem;
        color: var(--color-text-muted);
    }

    .port-no-active {
        padding: 4px 8px;
        padding-left: 36px;
    }
</style>
