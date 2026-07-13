<script lang="ts">
    import { onMount, onDestroy } from 'svelte';
    import { confirmation, type ConfirmOptions } from '$lib/stores/dialogs';
    import BaseModal from './BaseModal.svelte';

    let open = $state(false);
    let title = $state('');
    let message = $state('');
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
        confirmation.set(null);
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

    let unsub: (() => void) | null = null;

    onMount(() => {
        unsub = confirmation.subscribe((value) => {
            if (value) {
                title = value.title;
                message = value.message;
                confirmText = value.confirmText ?? 'Confirm';
                cancelText = value.cancelText ?? 'Cancel';
                variant = value.variant ?? 'destructive';
                resolveFn = (value as ConfirmOptions & { _resolve?: (value: boolean) => void })._resolve ?? null;
                open = true;
            }
        });

        window.addEventListener('keydown', handleKeydown);
    });

    onDestroy(() => {
        unsub?.();
        window.removeEventListener('keydown', handleKeydown);
    });
</script>

{#if open}
    {#snippet footer()}
        <button type="button" class="btn-secondary" onclick={handleCancel}>{cancelText}</button>
        <button type="button" class="btn-confirm" class:destructive={variant === 'destructive'} onclick={handleConfirm}>
            {confirmText}
        </button>
    {/snippet}

    <BaseModal title={title} width="360px" zIndex={1000} footer={footer} onClose={handleCancel}>
        <p class="confirm-message">{message}</p>
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
    :global(.modal-body) {
        padding: 0;
    }
    :global(.modal-footer) {
        margin-top: 16px;
        padding-top: 16px;
        justify-content: flex-end;
    }

    .confirm-message {
        font-size: 0.8125rem;
        color: var(--color-text-secondary);
        margin: 0;
        line-height: 1.5;
        white-space: pre-wrap;
    }

    .btn-confirm {
        padding: 6px 14px;
        border-radius: var(--radius-sm);
        border: none;
        background: var(--color-status-blue);
        color: white;
        font-size: 0.8125rem;
        font-weight: 500;
        cursor: pointer;
        transition: opacity 0.15s;
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
