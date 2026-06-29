<script lang="ts">
    import { onMount, onDestroy } from 'svelte';
    import { confirmation, type ConfirmOptions } from '$lib/store';

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

    function handleOverlayClick(e: MouseEvent) {
        if (e.target === e.currentTarget) {
            handleCancel();
        }
    }

    function handleKeydown(e: KeyboardEvent) {
        if (!open) return;
        if (e.key === 'Escape') {
            e.preventDefault();
            handleCancel();
        } else if (e.key === 'Enter') {
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
    <div class="confirm-overlay" role="dialog" aria-modal="true" aria-labelledby="confirm-dialog-title" tabindex="-1" onmousedown={handleOverlayClick}>
        <div class="confirm-dialog">
            <h3 id="confirm-dialog-title">{title}</h3>
            <p>{message}</p>
            <div class="confirm-actions">
                <button class="btn-cancel" onclick={handleCancel}>{cancelText}</button>
                <button class:btn-confirm={true} class:destructive={variant === 'destructive'} onclick={handleConfirm}>
                    {confirmText}
                </button>
            </div>
        </div>
    </div>
{/if}

<style>
    .confirm-overlay {
        position: fixed;
        inset: 0;
        background: rgba(0, 0, 0, 0.6);
        display: flex;
        align-items: center;
        justify-content: center;
        z-index: 1000;
        animation: fade-in 0.15s ease-out;
    }

    .confirm-dialog {
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-lg);
        padding: 20px 24px;
        max-width: 360px;
        width: 90%;
        animation: scale-in 0.15s ease-out;
    }

    .confirm-dialog h3 {
        font-size: 0.875rem;
        font-weight: 600;
        margin: 0 0 8px;
        color: var(--color-text-primary);
    }

    .confirm-dialog p {
        font-size: 0.8125rem;
        color: var(--color-text-secondary);
        margin: 0 0 16px;
        line-height: 1.5;
        white-space: pre-wrap;
    }

    .confirm-actions {
        display: flex;
        gap: 8px;
        justify-content: flex-end;
    }

    .btn-cancel {
        padding: 6px 14px;
        border-radius: var(--radius-sm);
        border: 1px solid var(--color-border);
        background: var(--color-surface-2);
        color: var(--color-text-primary);
        font-size: 0.8125rem;
        cursor: pointer;
        transition: background 0.15s;
    }

    .btn-cancel:hover {
        background: var(--color-surface-3);
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
        background: var(--color-status-red);
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
