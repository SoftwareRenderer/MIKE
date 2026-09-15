<script lang="ts">
    import { dialogStore } from '$lib/stores';
    import { t } from '$lib/i18n';
    import { usePromiseDialog } from '$lib/composables/use-promise-dialog.svelte';
    import PromiseDialogShell from './PromiseDialogShell.svelte';

    let title = $state('');
    let message = $state('');
    let items = $state<string[]>([]);
    let variant = $state<'default' | 'destructive'>('destructive');
    // Only the absence of a caller's label is copy; usePromiseDialog says why the default resolves
    // in a derived rather than into state.
    let confirmOverride = $state<string | null>(null);
    let cancelOverride = $state<string | null>(null);
    let confirmText = $derived(confirmOverride ?? t('app.confirm.confirm'));
    let cancelText = $derived(cancelOverride ?? t('app.cancel'));

    const dialog = usePromiseDialog({
        slot: {
            get: () => dialogStore.confirmation,
            clear: () => { dialogStore.confirmation = null; },
        },
        arm: (value) => {
            title = value.title;
            message = value.message;
            items = value.items ?? [];
            confirmOverride = value.confirmText ?? null;
            cancelOverride = value.cancelText ?? null;
            variant = value.variant ?? 'destructive';
        },
    });
</script>

<PromiseDialogShell
    open={dialog.open}
    {title}
    cancelLabel={cancelText}
    confirmLabel={confirmText}
    {variant}
    onConfirm={dialog.confirm}
    onCancel={dialog.cancel}
>
    <p class="confirm-message">{message}</p>
    {#if items.length > 0}
        <ul class="confirm-list">
            {#each items as item}
                <li>{item}</li>
            {/each}
        </ul>
    {/if}
</PromiseDialogShell>

<style>
    .confirm-message {
        font-size: 0.8125rem;
        color: var(--color-text-secondary);
        margin: 0;
        line-height: 1.5;
        white-space: pre-wrap;
    }
    .confirm-list {
        margin: 8px 0 0 0;
        padding-left: 18px;
        font-size: 0.8125rem;
        color: var(--color-text-secondary);
        line-height: 1.6;
    }
</style>
