<script lang="ts">
    import { onMount, onDestroy } from 'svelte';
    import { createFocusTrap } from '$lib/a11y';

    let {
        title = '',
        width = '640px',
        zIndex = 1000,
        trapFocus = true,
        onClose,
    }: {
        title: string;
        width?: string;
        zIndex?: number;
        trapFocus?: boolean;
        onClose: () => void | Promise<void>;
    } = $props();

    let modalEl: HTMLDivElement | null = null;
    let focusTrap: ReturnType<typeof createFocusTrap> | null = null;
    let triggerEl: HTMLElement | null = null;
    let closed = $state(false);

    onMount(() => {
        triggerEl = document.activeElement as HTMLElement;
        if (modalEl && trapFocus) {
            focusTrap = createFocusTrap(modalEl, handleClose);
            setTimeout(() => focusTrap?.focusFirst(), 50);
        }
    });

    onDestroy(() => {
        focusTrap?.destroy();
    });

    function handleClose() {
        if (closed) return;
        closed = true;
        focusTrap?.destroy();
        focusTrap = null;
        onClose();
        setTimeout(() => triggerEl?.focus(), 50);
    }

    function handleOverlayClick(e: MouseEvent) {
        if (e.target === e.currentTarget) handleClose();
    }
</script>

<div class="overlay" style="z-index: {zIndex}" onclick={handleOverlayClick} role="presentation">
    <div
        class="modal"
        style="width: {width}"
        onclick={(e) => e.stopPropagation()}
        bind:this={modalEl}
        role="dialog"
        aria-modal="true"
        aria-labelledby={$$slots.header ? undefined : 'modal-title'}
        tabindex="-1"
    >
        <div class="modal-header">
            <slot name="header">
                <h2 id="modal-title">{title}</h2>
            </slot>
            <button class="close-btn" onclick={handleClose} aria-label="Close" title="Close">✕</button>
        </div>
        <div class="modal-body">
            <slot />
        </div>
        {#if $$slots.footer}
            <div class="modal-footer">
                <slot name="footer" />
            </div>
        {/if}
    </div>
</div>

<style>
    @import '$lib/styles/shared.css';

    .modal-footer {
        display: flex;
        align-items: center;
        justify-content: space-between;
        gap: 8px;
        padding: 12px 20px;
        border-top: 1px solid var(--color-border);
        background: var(--color-surface-1);
        flex-shrink: 0;
        position: sticky;
        bottom: 0;
        z-index: 10;
    }

    @media (max-width: 768px) {
        .modal {
            width: 100vw !important;
            max-width: none;
            max-height: 100dvh;
            height: 100dvh;
            border-radius: 0;
            border: none;
        }
        .modal-header {
            padding: 16px;
        }
        .modal-body {
            padding: 16px;
            padding-bottom: calc(64px + env(safe-area-inset-bottom));
        }
        .modal-footer {
            position: fixed;
            bottom: 0;
            left: 0;
            right: 0;
            width: 100vw;
            padding: 8px 16px;
            padding-bottom: calc(4px + env(safe-area-inset-bottom));
        }
    }
</style>
