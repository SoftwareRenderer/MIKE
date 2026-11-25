<script lang="ts">
    /**
     * The Remove dialog's body: the shared shell, plus the folder question a plain confirmation cannot ask.
     * The answer is a decision about a folder rather than a yes, so this dialog settles its slot itself the
     * way `Chat/CommitDialog.svelte` does.
     */
    import { onDestroy, tick } from 'svelte';
    import { dialogStore, type ProjectDeletionChoice } from '$lib/stores';
    import { t } from '$lib/i18n';
    import PromiseDialogShell from './PromiseDialogShell.svelte';

    const slot = dialogStore.deleteProjectSlot;

    let pending = $derived(slot.pending);
    let folder = $derived(pending?.folder ?? '');
    let takeFolder = $state(false);
    let typed = $state('');
    let inputEl: HTMLInputElement | null = $state(null);

    // A new request is a different row: nothing the last one left in the field may carry over.
    $effect(() => {
        if (!pending) return;
        takeFolder = false;
        typed = '';
    });

    const folderAsked = $derived(pending?.cloned === true && takeFolder);
    const named = $derived(typed === folder);

    function answer(choice: ProjectDeletionChoice | null) {
        const request = slot.pending;
        slot.clear();
        request?._resolve?.(choice);
    }

    async function chooseFolder(e: Event) {
        takeFolder = (e.target as HTMLInputElement).checked;
        if (takeFolder) {
            await tick();
            inputEl?.focus();
        }
    }

    function confirm() {
        if (folderAsked && !named) return;
        answer({ deleteData: folderAsked, confirmName: folderAsked ? typed : '' });
    }

    function onFieldKeydown(e: KeyboardEvent) {
        // Enter in the field means what the button means.
        if (e.key === 'Enter') {
            e.preventDefault();
            confirm();
        }
    }

    onDestroy(() => {
        // A caller still awaiting an answer when this unmounts has no dialog left to answer it.
        if (slot.pending) answer(null);
    });
</script>

<PromiseDialogShell
    open={pending !== null}
    title={t('app.project.removeTitle')}
    width="360px"
    bodyPadding="12px 16px 0"
    cancelLabel={t('app.cancel')}
    confirmLabel={t('app.remove')}
    variant="destructive"
    confirmDisabled={folderAsked && !named}
    onConfirm={confirm}
    onCancel={() => answer(null)}
>
    {#if pending}
        <p class="confirm-message">
            {pending.cloned
                ? t('app.project.removeMessageCloned', { name: pending.name })
                : t('app.project.removeMessage', { name: pending.name })}
        </p>
        {#if pending.cloned}
            <label class="folder-row">
                <input type="checkbox" checked={takeFolder} onchange={chooseFolder} />
                <span>{t('app.project.deleteFolder', { folder })}</span>
            </label>
            <p class="folder-path">{pending.path}</p>
            {#if takeFolder}
                <label class="folder-name">
                    <span>{t('app.project.typeFolderName', { folder })}</span>
                    <input
                        type="text"
                        bind:this={inputEl}
                        bind:value={typed}
                        spellcheck="false"
                        autocomplete="off"
                        aria-label={t('app.project.typeFolderNameAria', { folder })}
                        onkeydown={onFieldKeydown}
                    />
                </label>
            {/if}
        {/if}
    {/if}
</PromiseDialogShell>

<style>
    .confirm-message {
        font-size: var(--text-sm);
        color: var(--color-text-secondary);
        margin: 0;
        line-height: 1.5;
        white-space: pre-wrap;
    }

    .folder-row {
        display: flex;
        align-items: center;
        gap: 8px;
        margin-top: 12px;
        font-size: var(--text-sm);
        color: var(--color-text-primary);
        cursor: pointer;
    }

    .folder-row input[type='checkbox'] {
        width: 16px;
        height: 16px;
        accent-color: var(--color-status-red-dark);
        cursor: pointer;
    }

    .folder-path {
        margin: 4px 0 0 24px;
        font-size: var(--text-xs);
        color: var(--color-text-tertiary);
        font-family: var(--font-mono);
        overflow-wrap: anywhere;
    }

    .folder-name {
        display: flex;
        flex-direction: column;
        gap: 4px;
        margin-top: 12px;
        font-size: var(--text-xs);
        color: var(--color-text-secondary);
    }

    .folder-name input {
        font-family: var(--font-mono);
        font-size: var(--text-sm);
        padding: 6px 8px;
        border: 1px solid var(--color-border);
        border-radius: var(--radius-md);
        background: var(--color-surface-1);
        color: var(--color-text-primary);
    }
</style>
