<script lang="ts">
    /**
     * The environment the container is given. Every declaration is a name and a value: the container
     * receives only what was configured here, so a name with no value is refused rather than becoming
     * a bare `-e NAME` that inherits whatever the server process happens to hold.
     */
    import { t } from '$lib/i18n';
    import { editInPlace, onActivate, LITERAL_TEXT_FIELD } from '$lib/utils/row-form';
    import CancelEditButton from './CancelEditButton.svelte';
    import ExpandableSection from './ExpandableSection.svelte';
    import Icon from '$lib/components/Icon.svelte';
    import closeIcon from '$lib/assets/icons/close.svg?raw';
    import type { SandboxDraftOwner } from './draft.svelte';
    import AddRowIcon from './AddRowIcon.svelte';

    let { draft, locked }: { draft: SandboxDraftOwner; locked: boolean } = $props();

    let formEl: HTMLDivElement | null = $state(null);

    const rowForm = editInPlace({
        form: () => formEl,
        edit: (i) => draft.editEnv(i),
        add: () => draft.addEnv(),
    });
</script>

<ExpandableSection contentClass="envs-content" bind:expanded={draft.envsExpanded}
                   label={t('sandbox.environmentVariables')} desc={t('sandbox.envsDesc')} ariaLabel={t('sandbox.envsAria')}
                   icon={envsIcon}>
    {#if draft.envs.length > 0}
        <div class="env-list">
            {#each draft.envs as env, i}
                <div class="env-item" class:editing={draft.editingEnvIndex === i} onclick={() => rowForm.start(i)} role="button" tabindex="0" onkeydown={onActivate(() => rowForm.start(i))}>
                    <div class="env-info">
                        <span class="env-key" title={env.key}>{env.key}</span>
                        <span class="env-value" title={env.value}>{env.value || t('sandbox.empty')}</span>
                    </div>
                    <button type="button" class="btn-remove-env" onclick={(e) => { e.stopPropagation(); draft.removeEnv(i); }} disabled={locked} title={t('app.remove')}>
                        <Icon src={closeIcon} width={12} height={12} />
                    </button>
                </div>
            {/each}
        </div>
    {/if}

    <div class="env-add-row" bind:this={formEl}>
        <span class="field-label">{t('sandbox.envNameAria')}</span>
        <input type="text" class="env-input env-input-key" bind:value={draft.newEnvKey} placeholder={t('sandbox.envKeyPlaceholder')}
               disabled={locked} aria-label={t('sandbox.envNameAria')} onkeydown={rowForm.onFieldKeydown}
               {...LITERAL_TEXT_FIELD} />
        <span class="field-label">{t('sandbox.envValueAria')}</span>
        <input type="text" class="env-input env-input-value" bind:value={draft.newEnvValue} placeholder={t('sandbox.envValueInputPlaceholder')}
               disabled={locked} aria-label={t('sandbox.envValueAria')} onkeydown={rowForm.onFieldKeydown}
               {...LITERAL_TEXT_FIELD} />
        <div class="add-row-actions">
            {#if draft.editingEnvIndex !== null}
                <CancelEditButton onclick={() => draft.cancelEnvEdit()} />
            {/if}
            <button type="button" class="btn-add-env" class:pending={draft.hasPendingEnv} onclick={() => draft.addEnv()} disabled={locked} title={draft.editingEnvIndex !== null ? t('sandbox.saveVariable') : t('sandbox.addVariable')}>
                <AddRowIcon editing={draft.editingEnvIndex !== null} />
            </button>
        </div>
    </div>

    {#if draft.envError}
        <div class="env-error">{draft.envError}</div>
    {/if}
</ExpandableSection>

{#snippet envsIcon()}
    <svg width="20" height="20" viewBox="0 0 20 20" fill="none" aria-hidden="true">
        <path d="M3 6h14M3 10h14M3 14h10" stroke="currentColor" stroke-width="1.3" stroke-linecap="round"/>
    </svg>
{/snippet}


<style>
    @import '$lib/styles/sandbox-settings.css';
    @import '$lib/styles/sandbox-config.css';
</style>
