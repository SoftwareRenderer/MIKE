<script lang="ts">
    import { t } from '$lib/i18n';
    import type { BoardPending } from '$lib/utils/board-pending';

    /**
     * The backlog column's add-task slot while the project it belongs to has no row on the server: the
     * bar of the add in flight, the line the server last said, and once it stopped, git's own output —
     * the one thing the person can act on. Nothing here recovers the add, so the box offers the exit.
     */
    let { pending, onDismiss }: { pending: BoardPending; onDismiss?: () => void } = $props();
</script>

<div class="pending-add" class:failed={pending.failed}>
    <div class="pending-bar-row">
        <span class="pending-bar" role="progressbar" aria-label={t('board.pending.progressAria')} aria-valuemin={0} aria-valuemax={100} aria-valuenow={pending.percent}>
            <span class="pending-bar-fill" style={`width: ${pending.percent}%`}></span>
        </span>
        <span class="pending-percent">{pending.percent}%</span>
    </div>
    <p class="pending-caption" role={pending.failed ? 'alert' : undefined}>{pending.caption}</p>
    {#if pending.detail}
        <pre class="pending-detail">{pending.detail}</pre>
    {/if}
    {#if onDismiss && pending.failed}
        <button type="button" class="btn pending-dismiss" onclick={onDismiss}>{t('board.pending.dismiss')}</button>
    {/if}
</div>

<style>
    .pending-add {
        display: flex;
        flex-direction: column;
        gap: 8px;
        padding: 10px;
        border-radius: var(--radius-md);
        background: var(--color-surface-3);
        font-size: var(--text-base);
        color: var(--color-text-secondary);
    }
    .pending-bar-row {
        display: flex;
        align-items: center;
        gap: 8px;
    }
    /* Determinate: the width is the streamed percentage. */
    .pending-bar {
        flex: 1;
        min-width: 48px;
        height: 4px;
        border-radius: var(--radius-xs);
        background: var(--color-surface-4);
        overflow: hidden;
    }
    .pending-bar-fill {
        display: block;
        height: 100%;
        border-radius: inherit;
        background: var(--color-accent);
        transition: width var(--dur-md) ease;
    }
    .pending-add.failed .pending-bar-fill {
        background: var(--color-status-red);
    }
    .pending-percent {
        flex-shrink: 0;
        font-size: var(--text-xs);
        color: var(--color-text-tertiary);
        font-variant-numeric: tabular-nums;
    }
    .pending-caption {
        margin: 0;
        overflow-wrap: anywhere;
    }
    /* Scrolled rather than truncated. */
    .pending-detail {
        margin: 0;
        max-height: 9em;
        overflow: auto;
        padding: 6px 8px;
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        background: var(--color-surface-1);
        color: var(--color-text-tertiary);
        font-family: var(--font-mono);
        font-size: var(--text-xs);
        white-space: pre-wrap;
        overflow-wrap: anywhere;
    }
    .pending-dismiss {
        align-self: flex-start;
    }
</style>
