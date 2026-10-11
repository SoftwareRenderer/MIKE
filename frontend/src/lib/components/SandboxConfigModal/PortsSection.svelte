<script lang="ts">
    /**
     * Ports the container publishes. Only a container port is configured: the host side is assigned
     * when a task runs and is what the active mappings show. This row exists only while Network
     * Isolation is off — an isolated container has nothing to reach these ports from.
     */
    import { t } from '$lib/i18n';
    import type { PortForward } from '$lib/types';
    import { formatAssignedHost, formatForwardPort } from '$lib/utils/sandbox-ports';
    import { editInPlace, onActivate, LITERAL_TEXT_FIELD } from '$lib/utils/row-form';
    import CancelEditButton from './CancelEditButton.svelte';
    import ExpandableSection from './ExpandableSection.svelte';
    import type { SandboxDraftOwner } from './draft.svelte';

    let { draft, locked }: { draft: SandboxDraftOwner; locked: boolean } = $props();

    let formEl: HTMLDivElement | null = $state(null);

    function hostHalf(port: PortForward): string {
        const assigned = draft.activeHostPorts.get(port.container_port);
        return assigned === undefined ? '' : formatAssignedHost(assigned, port.host_ip);
    }

    const rowForm = editInPlace({
        form: () => formEl,
        edit: (i) => draft.editPort(i),
        add: () => draft.addPortForward(),
    });
</script>

<ExpandableSection contentClass="ports-content" subsection bind:expanded={draft.portsExpanded}
                   label={t('sandbox.ports')} desc={t('sandbox.portsDesc')} ariaLabel={t('sandbox.portsAria')}
                   icon={portsIcon}>
    {#if draft.forwardPorts.length > 0}
        <div class="port-list">
            {#each draft.forwardPorts as port, i}
                <div class="port-item" class:editing={draft.editingPortIndex === i} onclick={() => rowForm.start(i)} role="button" tabindex="0" onkeydown={onActivate(() => rowForm.start(i))}>
                    <span class="port-mapping">
                        {#if hostHalf(port)}
                            <span class="port-host" title={t('sandbox.hostPort')}>{hostHalf(port)}</span>
                        {/if}
                        <span class="port-container" title={t('sandbox.containerPort')}>{hostHalf(port) ? `:${port.container_port}` : formatForwardPort(port)}</span>
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

    <div class="port-add-row" bind:this={formEl}>
        <span class="field-label">{t('sandbox.hostInterface')}</span>
        <input type="text" class="port-input port-input-bind" bind:value={draft.newHostIP} placeholder={t('sandbox.hostInterface')}
               {...LITERAL_TEXT_FIELD} disabled={locked} aria-label={t('sandbox.hostInterfaceAria')} onkeydown={rowForm.onFieldKeydown} />
        <span class="field-label">{t('sandbox.containerPort')}</span>
        <input type="number" class="port-input" bind:value={draft.newContainerPort} placeholder={t('sandbox.containerPort')} min="1" max="65535"
               {...LITERAL_TEXT_FIELD} disabled={locked} aria-label={t('sandbox.containerPort')} onkeydown={rowForm.onFieldKeydown} />
        <div class="add-row-actions">
            {#if draft.editingPortIndex !== null}
                <CancelEditButton onclick={() => draft.cancelPortEdit()} />
            {/if}
            <button type="button" class="btn-add-port" class:pending={draft.hasPendingPort} onclick={() => draft.addPortForward()} disabled={locked} title={draft.editingPortIndex !== null ? t('sandbox.savePort') : t('sandbox.addPort')}>
                {@render addIcon(draft.editingPortIndex !== null)}
            </button>
        </div>
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
</style>
