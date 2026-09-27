<script lang="ts">
    import { dialogStore } from '$lib/stores';
    import PromiseDialogShell from '../PromiseDialogShell.svelte';
    import ContextDonut from './ContextDonut.svelte';
    import { formatTokens, formatCost } from '$lib/utils/format';
    import { t } from '$lib/i18n';
    import { usePromiseDialog } from '$lib/composables/use-promise-dialog.svelte';

    // Only the absence of a caller's label is copy; usePromiseDialog says why the default resolves
    // in a derived rather than into state.
    let confirmOverride = $state<string | null>(null);
    let cancelOverride = $state<string | null>(null);
    let confirmText = $derived(confirmOverride ?? t('chat.compress.confirm'));
    let cancelText = $derived(cancelOverride ?? t('chat.common.cancel'));

    /** Resolve a stat that may be a snapshot number or a live getter. */
    function resolveStat(value: number | (() => number) | undefined): number {
        const n = typeof value === 'function' ? value() : (value ?? 0);
        return Number.isFinite(n) ? n : 0;
    }

    // Derived from the dialog store so the donut + stats track live sources
    // (e.g. stream state) in real time while the dialog is open.
    let used = $derived(resolveStat(dialogStore.compressContextDialogSlot.pending?.used));
    let limit = $derived(resolveStat(dialogStore.compressContextDialogSlot.pending?.limit));
    let sessionCost = $derived(resolveStat(dialogStore.compressContextDialogSlot.pending?.sessionCost));

    let percentage = $derived(limit > 0 ? Math.round((used / limit) * 100) : 0);

    const dialog = usePromiseDialog({
        slot: {
            get: () => dialogStore.compressContextDialogSlot.pending,
            clear: () => { dialogStore.compressContextDialogSlot.clear(); },
        },
        arm: (value) => {
            confirmOverride = value.confirmText ?? null;
            cancelOverride = value.cancelText ?? null;
        },
    });
</script>

<PromiseDialogShell
    open={dialog.open}
    title={t('chat.compress.title')}
    width="340px"
    bodyPadding="16px"
    cancelLabel={cancelText}
    confirmLabel={confirmText}
    onConfirm={dialog.confirm}
    onCancel={dialog.cancel}
>
    <p class="dialog-message">
        {t('chat.compress.body')}
    </p>

    <div class="dialog-stats">
        <ContextDonut {used} {limit} size={56} />

        <div class="stats-values">
            <div class="stat-row">
                <span class="stat-label">{t('chat.compress.contextUsed')}</span>
                <span class="stat-value">{formatTokens(used)} / {formatTokens(limit)}</span>
            </div>
            <div class="stat-row">
                <span class="stat-label">{t('chat.compress.percentage')}</span>
                <span class="stat-value">{percentage}%</span>
            </div>
            {#if sessionCost > 0}
                <div class="stat-row">
                    <span class="stat-label">{t('chat.context.sessionCost')}</span>
                    <span class="stat-value">{formatCost(sessionCost)}</span>
                </div>
            {/if}
        </div>
    </div>
</PromiseDialogShell>

<style>
    .dialog-message {
        font-size: var(--text-sm);
        color: var(--color-text-secondary);
        margin: 0 0 16px 0;
        line-height: 1.5;
    }

    .dialog-stats {
        display: flex;
        align-items: center;
        gap: 16px;
        padding: 12px;
        border-radius: var(--radius-lg);
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
        font-size: var(--text-xs);
        color: var(--color-text-tertiary);
    }

    .stat-value {
        font-size: var(--text-sm);
        font-weight: 600;
        color: var(--color-text-primary);
        font-variant-numeric: tabular-nums;
    }
</style>
