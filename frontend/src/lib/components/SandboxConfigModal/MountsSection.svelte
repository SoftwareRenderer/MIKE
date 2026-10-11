<script lang="ts">
    /**
     * Host paths a project lends its containers. What the field refuses here is pre-validation only:
     * the same rules run again on `PUT` (a row can predate them) and again at container creation, and
     * a borrowed root is mounted read-only whatever this row offers.
     */
    import { t } from '$lib/i18n';
    import { editInPlace, onActivate, LITERAL_TEXT_FIELD } from '$lib/utils/row-form';
    import CancelEditButton from './CancelEditButton.svelte';
    import ExpandableSection from './ExpandableSection.svelte';
    import type { SandboxDraftOwner } from './draft.svelte';

    let { draft, locked, onBrowse }: {
        draft: SandboxDraftOwner;
        locked: boolean;
        /** Open the host-directory picker; the path it answers with lands in this section's field. */
        onBrowse: () => void;
    } = $props();

    let formEl: HTMLDivElement | null = $state(null);

    const rowForm = editInPlace({
        form: () => formEl,
        edit: (i) => draft.editMount(i),
        add: () => draft.addMount(),
    });
</script>

<ExpandableSection contentClass="mounts-content" bind:expanded={draft.mountsExpanded}
                   label={t('sandbox.customMounts')} desc={t('sandbox.mountsDesc')} ariaLabel={t('sandbox.mountsAria')}
                   icon={mountsIcon}>
    {#if draft.mounts.length > 0}
        <div class="mount-list">
            {#each draft.mounts as mount, i}
                <div class="mount-item" class:editing={draft.editingMountIndex === i} onclick={() => rowForm.start(i)} role="button" tabindex="0" onkeydown={onActivate(() => rowForm.start(i))}>
                    <div class="mount-info">
                        <span class="mount-path" title={mount.host_path}>{mount.host_path}</span>
                        <svg class="mount-arrow" width="12" height="12" viewBox="0 0 12 12" fill="none" aria-hidden="true">
                            <path d="M4 3L8 6L4 9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                        </svg>
                        <span class="mount-container" title={mount.container_path}>{mount.container_path}</span>
                    </div>
                    <div class="mount-actions">
                        <span class="sandbox-mode" class:sandbox-mode-rw={mount.mode === 'rw'} class:sandbox-mode-ro={mount.mode === 'ro'} title={mount.mode === 'rw' ? t('sandbox.mountReadWrite') : t('sandbox.mountReadOnly')}>{mount.mode}</span>
                        <button type="button" class="btn-remove-mount" onclick={(e) => { e.stopPropagation(); draft.removeMount(i); }} disabled={locked} title={t('sandbox.remove')}>
                            {@render closeIcon()}
                        </button>
                    </div>
                </div>
            {/each}
        </div>
    {/if}

    <div class="mount-add-row" bind:this={formEl}>
        <!-- Path shapes to type, not copy; the names are the aria-labels, and the visible label once
             a field owns a line. -->
        <span class="field-label">{t('sandbox.hostPath')}</span>
        <input type="text" class="mount-input mount-input-host" bind:value={draft.newMountHostPath} placeholder="/host/path"
               disabled={locked} aria-label={t('sandbox.hostPath')} onkeydown={rowForm.onFieldKeydown}
               {...LITERAL_TEXT_FIELD} />
        <button type="button" class="btn-browse-mount" onclick={onBrowse} disabled={locked} title={t('sandbox.browseHostDirectory')}>
            <svg width="14" height="14" viewBox="0 0 14 14" fill="none" aria-hidden="true">
                <path d="M2 4h4l1.5 1.5H12V12H2V4z" stroke="currentColor" stroke-width="1.2" fill="none"/>
            </svg>
        </button>
        <svg class="mount-arrow" width="12" height="12" viewBox="0 0 12 12" fill="none" aria-hidden="true">
            <path d="M4 3L8 6L4 9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
        </svg>
        <span class="field-label">{t('sandbox.containerPath')}</span>
        <input type="text" class="mount-input" bind:value={draft.newMountContainerPath} placeholder="/container/path"
               disabled={locked} aria-label={t('sandbox.containerPath')} onkeydown={rowForm.onFieldKeydown}
               {...LITERAL_TEXT_FIELD} />
        <div class="add-row-actions">
            {#if draft.editingMountIndex !== null}
                <CancelEditButton onclick={() => draft.cancelMountEdit()} />
            {/if}
            <div class="radio-group radio-group-small">
                <label class="radio-label" class:active={draft.newMountMode === 'rw'} title={t('sandbox.mountReadWrite')}>
                    <input type="radio" name="mount-mode" value="rw" checked={draft.newMountMode === 'rw'} onchange={() => draft.newMountMode = 'rw'} disabled={locked} />
                    <span class="radio-text">rw</span>
                </label>
                <label class="radio-label" class:active={draft.newMountMode === 'ro'} title={t('sandbox.mountReadOnly')}>
                    <input type="radio" name="mount-mode" value="ro" checked={draft.newMountMode === 'ro'} onchange={() => draft.newMountMode = 'ro'} disabled={locked} />
                    <span class="radio-text">ro</span>
                </label>
            </div>
            <button type="button" class="btn-add-mount" class:pending={draft.hasPendingMount} onclick={() => draft.addMount()} disabled={locked} title={draft.editingMountIndex !== null ? t('sandbox.saveMount') : t('sandbox.addMount')}>
                {@render addIcon(draft.editingMountIndex !== null)}
            </button>
        </div>
    </div>

    {#if draft.mountError}
        <div class="mount-error">{draft.mountError}</div>
    {/if}
</ExpandableSection>

{#snippet mountsIcon()}
    <svg width="20" height="20" viewBox="0 0 20 20" fill="none" aria-hidden="true">
        <path d="M2 4h5l1.5 1.5H18V16H2V4z" stroke="currentColor" stroke-width="1.3" fill="none"/>
        <path d="M10 8v6M8 10l2-2 2 2" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
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

    .mount-actions {
        display: flex;
        align-items: center;
        gap: 6px;
        flex-shrink: 0;
    }

    .mount-arrow {
        color: var(--color-text-secondary);
        flex-shrink: 0;
    }
</style>
