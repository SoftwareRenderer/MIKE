<script lang="ts">
    import { onDestroy } from 'svelte';
    import { dialogStore } from '$lib/stores';
    import BaseModal from '../BaseModal.svelte';
    import ContextDonut from './ContextDonut.svelte';

    let open = $state(false);
    let confirmText = $state('Compress');
    let cancelText = $state('Cancel');
    let resolveFn: ((value: boolean) => void) | null = $state(null);

    /** Resolve a stat that may be a snapshot number or a live getter. */
    function resolveStat(value: number | (() => number) | undefined): number {
        const n = typeof value === 'function' ? value() : (value ?? 0);
        return Number.isFinite(n) ? n : 0;
    }

    // Derived from the dialog store so the donut + stats track live sources
    // (e.g. stream state) in real time while the dialog is open.
    let used = $derived(resolveStat(dialogStore.compressContextDialog?.used));
    let limit = $derived(resolveStat(dialogStore.compressContextDialog?.limit));
    let sessionCost = $derived(resolveStat(dialogStore.compressContextDialog?.sessionCost));

    let percentage = $derived(limit > 0 ? Math.round((used / limit) * 100) : 0);

    function dismiss(value: boolean) {
        open = false;
        if (resolveFn) {
            resolveFn(value);
            resolveFn = null;
        }
        dialogStore.compressContextDialog = null;
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

    function formatTokens(n: number): string {
        if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M';
        if (n >= 1_000) return (n / 1_000).toFixed(1) + 'K';
        return n.toString();
    }

    function formatCost(cost: number): string {
        if (cost <= 0) return '';
        const formatted = cost < 1 ? cost.toFixed(4) : cost.toFixed(2);
        return '$' + Number(formatted).toLocaleString(undefined, {
            minimumFractionDigits: cost < 1 ? 4 : 2,
            maximumFractionDigits: cost < 1 ? 4 : 2,
        });
    }

    $effect(() => {
        const value = dialogStore.compressContextDialog;
        if (value) {
            confirmText = value.confirmText ?? 'Compress';
            cancelText = value.cancelText ?? 'Cancel';
            resolveFn = value._resolve ?? null;
            open = true;
        }
    });

    $effect(() => {
        window.addEventListener('keydown', handleKeydown);
        return () => window.removeEventListener('keydown', handleKeydown);
    });

    onDestroy(() => {
        // Ensure any pending promise resolves if the dialog unmounts.
        if (resolveFn) {
            resolveFn(false);
            resolveFn = null;
        }
    });
</script>

{#if open}
    {#snippet header({ title }: { title: string })}
        <h2 style="font-size: 0.9375rem;">{title}</h2>
    {/snippet}

    {#snippet footer()}
        <button type="button" class="btn-cancel" onclick={handleCancel}>{cancelText}</button>
        <button type="button" class="btn-confirm" onclick={handleConfirm}>{confirmText}</button>
    {/snippet}

    <BaseModal
        title="Compress Context"
        width="340px"
        zIndex={1000}
        header={header}
        footer={footer}
        onClose={handleCancel}
        headerStyle="padding: 12px 16px;"
        bodyStyle="padding: 16px; flex: 0 0 auto; overflow: visible;"
        footerStyle="margin-top: 12px; padding: 12px 16px; gap: 6px;"
    >
        <p class="dialog-message">
            This will create a summary of the conversation to make more efficient use of context
            space. You can delete the summary to restore the full context.
        </p>

        <div class="dialog-stats">
            <ContextDonut {used} {limit} size={56} />

            <div class="stats-values">
                <div class="stat-row">
                    <span class="stat-label">Context used</span>
                    <span class="stat-value">{formatTokens(used)} / {formatTokens(limit)}</span>
                </div>
                <div class="stat-row">
                    <span class="stat-label">Percentage</span>
                    <span class="stat-value">{percentage}%</span>
                </div>
                {#if sessionCost > 0}
                    <div class="stat-row">
                        <span class="stat-label">Session cost</span>
                        <span class="stat-value">{formatCost(sessionCost)}</span>
                    </div>
                {/if}
            </div>
        </div>
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

    .dialog-message {
        font-size: 0.8125rem;
        color: var(--color-text-secondary);
        margin: 0 0 16px 0;
        line-height: 1.5;
    }

    .dialog-stats {
        display: flex;
        align-items: center;
        gap: 16px;
        padding: 12px;
        border-radius: 12px;
        background: var(--color-surface-2);
        border: 1px solid var(--color-border);
    }

    .stats-values {
        display: flex;
        flex-direction: column;
        gap: 6px;
        flex: 1;
        min-width: 0;
    }

    .stat-row {
        display: flex;
        align-items: baseline;
        justify-content: space-between;
        gap: 8px;
    }

    .stat-label {
        font-size: 0.75rem;
        color: var(--color-text-tertiary);
    }

    .stat-value {
        font-size: 0.8125rem;
        font-weight: 600;
        color: var(--color-text-primary);
        font-variant-numeric: tabular-nums;
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
