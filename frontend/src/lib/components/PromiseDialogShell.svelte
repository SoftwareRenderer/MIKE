<script lang="ts">
    /**
     * The shell a promise-backed dialog draws: overlay, title, body, and a footer of one or two
     * buttons. Which request is pending, and what answering it resolves, is `usePromiseDialog`'s;
     * this component holds no state of its own.
     *
     * `.btn-cancel` / `.btn-confirm` stay on the buttons because the e2e specs click them by name;
     * `modalClass` carries this shell's marker so the shared stylesheet can reach its buttons without
     * claiming those two names for the whole app.
     */
    import type { Snippet } from 'svelte';
    import type { DialogVariant } from '$lib/stores';
    import BaseModal from './BaseModal.svelte';

    let {
        open,
        title,
        width = '320px',
        bodyPadding = '12px 16px 0',
        cancelLabel,
        confirmLabel,
        dismissOnly = false,
        variant = 'default',
        onConfirm,
        onCancel,
        children,
    }: {
        open: boolean;
        title: string;
        width?: string;
        /** The body's own padding; the flex and overflow every one of these wants is added here. */
        bodyPadding?: string;
        /** Ignored when `dismissOnly` is set. */
        cancelLabel?: string;
        confirmLabel: string;
        /** Show only the confirm button: the dialog reports something rather than asking. */
        dismissOnly?: boolean;
        variant?: DialogVariant;
        onConfirm: () => void;
        onCancel: () => void;
        children?: Snippet;
    } = $props();
</script>

{#if open}
    {#snippet header({ title }: { title: string })}
        <h2 style="font-size: var(--text-md);">{title}</h2>
    {/snippet}

    {#snippet footer()}
        {#if !dismissOnly}
            <button type="button" class="btn-cancel" onclick={onCancel}>{cancelLabel}</button>
        {/if}
        <button type="button" class="btn-confirm" class:destructive={variant === 'destructive'} onclick={onConfirm}>
            {confirmLabel}
        </button>
    {/snippet}

    <BaseModal
        {title}
        {width}
        zIndex={1000}
        modalClass="promise-dialog"
        {header}
        {footer}
        onClose={onCancel}
        headerStyle="padding: 12px 16px;"
        bodyStyle="padding: {bodyPadding}; flex: 0 0 auto; overflow: visible;"
        footerStyle="margin-top: 12px; padding: 12px 16px; gap: 6px;"
    >
        {@render children?.()}
    </BaseModal>
{/if}

<style>
    @import '$lib/styles/promise-dialog.css';
</style>
