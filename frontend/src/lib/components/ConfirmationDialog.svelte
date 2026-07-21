<script lang="ts">
    import { onDestroy } from 'svelte';
    import { dialogStore } from '$lib/stores';
    import BaseModal from './BaseModal.svelte';

    let open = $state(false);
    let title = $state('');
    let message = $state('');
    let items = $state<string[]>([]);
    let confirmText = $state('Confirm');
    let cancelText = $state('Cancel');
    let variant = $state<'default' | 'destructive'>('destructive');
    let resolveFn: ((value: boolean) => void) | null = $state(null);

    function dismiss(value: boolean) {
        open = false;
        if (resolveFn) {
            resolveFn(value);
            resolveFn = null;
        }
        dialogStore.confirmation = null;
    }

    function handleConfirm() {
        dismiss(true);
    }

    function handleCancel() {
        dismiss(false);
    }

    function handleKeydown(e: KeyboardEvent) {
        if (!open) return;
        if (e.key === 'Enter') {
            e.preventDefault();
            handleConfirm();
        }
    }

    $effect(() => {
        const value = dialogStore.confirmation;
        if (value) {
            title = value.title;
            message = value.message;
            items = value.items ?? [];
            confirmText = value.confirmText ?? 'Confirm';
            cancelText = value.cancelText ?? 'Cancel';
            variant = value.variant ?? 'destructive';
            resolveFn = value._resolve ?? null;
            open = true;
        }
    });

    $effect(() => {
        window.addEventListener('keydown', handleKeydown);
        return () => window.removeEventListener('keydown', handleKeydown);
    });
</script>

{#if open}
    {#snippet header({ title }: { title: string })}
        <h2 style="font-size: 0.9375rem;">{title}</h2>
    {/snippet}

    {#snippet footer()}
        <button type="button" class="btn-cancel" onclick={handleCancel}>{cancelText}</button>
        <button type="button" class="btn-confirm" class:destructive={variant === 'destructive'} onclick={handleConfirm}>
            {confirmText}
        </button>
    {/snippet}

    <BaseModal
        title={title}
        width="320px"
        zIndex={1000}
        header={header}
        footer={footer}
        onClose={handleCancel}
        headerStyle="padding: 12px 16px;"
        bodyStyle="padding: 12px 16px 0; flex: 0 0 auto; overflow: visible;"
        footerStyle="margin-top: 12px; padding: 12px 16px; gap: 6px;"
    >
        <p class="confirm-message">{message}</p>
        {#if items.length > 0}
            <ul class="confirm-list">
                {#each items as item}
                    <li>{item}</li>
                {/each}
            </ul>
        {/if}
    </BaseModal>
{/if}

<style>
    :global(.overlay) {
        animation: fade-in 0.15s ease-out;
    }
    :global(.modal) {
        animation: scale-in 0.15s ease-out;
        padding: 4px;
    }

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

    .btn-cancel {
        padding: 6px 14px;
        border-radius: var(--radius-md);
        border: none;
        background: var(--color-surface-3);
        color: var(--color-text-primary);
        font-size: 0.875rem;
        font-weight: 500;
        font-family: inherit;
        cursor: pointer;
        transition: background 0.15s, opacity 0.15s;
    }
    .btn-cancel:hover {
        background: var(--color-surface-4);
    }

    .btn-confirm {
        padding: 6px 14px;
        border-radius: var(--radius-md);
        border: none;
        background: var(--color-status-blue);
        color: white;
        font-size: 0.875rem;
        font-weight: 500;
        font-family: inherit;
        cursor: pointer;
        transition: background 0.15s, opacity 0.15s;
    }
    .btn-confirm:hover {
        opacity: 0.85;
    }
    .btn-confirm.destructive {
        background: var(--color-status-red-dark);
    }

    @keyframes fade-in {
        from { opacity: 0; }
        to { opacity: 1; }
    }

    @keyframes scale-in {
        from {
            opacity: 0;
            transform: scale(0.95);
        }
        to {
            opacity: 1;
            transform: scale(1);
        }
    }
</style>
