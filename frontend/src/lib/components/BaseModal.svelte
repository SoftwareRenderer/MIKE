<script lang="ts">
    import type { Snippet } from 'svelte';
    import { onMount, onDestroy } from 'svelte';
    import { createFocusTrap } from '$lib/a11y';

    let {
        title = '',
        width = '640px',
        zIndex = 1000,
        closeOnOverlayClick = true,
        floating = false,
        onBeforeClose,
        onClose,
        header,
        toolbar,
        footer,
        children,
        bodyStyle,
        headerStyle,
        footerStyle,
        modalClass,
    }: {
        title: string;
        width?: string;
        zIndex?: number;
        closeOnOverlayClick?: boolean;
        /* Non-modal floating window */
        floating?: boolean;
        onBeforeClose?: () => boolean | Promise<boolean>;
        onClose: () => void | Promise<void>;
        header?: Snippet<[ { title: string } ]>;
        toolbar?: Snippet<[]>;
        footer?: Snippet<[]>;
        children?: Snippet<[]>;
        bodyStyle?: string;
        headerStyle?: string;
        footerStyle?: string;
        modalClass?: string;
    } = $props();

    let modalEl: HTMLDivElement | null = null;
    let focusTrap: ReturnType<typeof createFocusTrap> | null = null;
    let triggerEl: HTMLElement | null = null;
    let closed = $state(false);
    let modalClasses = $derived(modalClass ? `modal ${modalClass}` : 'modal');

    onMount(() => {
        triggerEl = document.activeElement as HTMLElement;
        if (modalEl && !floating) {
            focusTrap = createFocusTrap(modalEl, handleClose);
            queueMicrotask(() => focusTrap?.focusFirst());
        } else if (modalEl && floating) {
            // Focus the first focusable unless the content already took focus
            // (e.g. CreateTaskModal focuses its textarea in a microtask).
            queueMicrotask(() => {
                if (modalEl && !modalEl.contains(document.activeElement)) focusFirstIn(modalEl);
            });
            window.addEventListener('keydown', handleFloatingKeydown);
        }
    });

    onDestroy(() => {
        focusTrap?.destroy();
        if (floating) window.removeEventListener('keydown', handleFloatingKeydown);
    });

    function focusFirstIn(el: HTMLElement) {
        el.querySelector<HTMLElement>('[autofocus], input, textarea, select, button:not([disabled])')?.focus();
    }

    /** Floating (non-modal) mode: Escape closes only while focus is inside the
     *  modal — otherwise Escape belongs to the app behind (e.g. clearing a
     *  gutter line selection in the editor). */
    function handleFloatingKeydown(e: KeyboardEvent) {
        if (e.key === 'Escape' && modalEl && modalEl.contains(document.activeElement)) handleClose();
    }

    async function handleClose() {
        if (closed) return;
        if (onBeforeClose) {
            const ok = await onBeforeClose();
            if (!ok) return;
        }
        closed = true;
        focusTrap?.destroy();
        focusTrap = null;
        onClose();
        queueMicrotask(() => triggerEl?.focus());
    }

    function handleOverlayClick(e: MouseEvent) {
        if (e.target === e.currentTarget && closeOnOverlayClick) handleClose();
    }
</script>

<div class="overlay" class:overlay-floating={floating} style="z-index: {zIndex}" onclick={handleOverlayClick} role="presentation">
    <!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions — modal container stops click propagation, not an activation target -->
    <div
        class={modalClasses}
        style="width: {width}"
        onclick={(e) => e.stopPropagation()}
        bind:this={modalEl}
        role="dialog"
        aria-modal={floating ? undefined : 'true'}
        aria-labelledby={header ? undefined : 'modal-title'}
        tabindex="-1"
    >
        <div class="modal-header" style={headerStyle ?? ''}>
            {#if header}
                {@render header({ title })}
            {:else}
                <h2 id="modal-title">{title}</h2>
            {/if}
            <button class="close-btn" onclick={handleClose} aria-label="Close" title="Close">✕</button>
        </div>
        {#if toolbar}
            <div class="modal-toolbar">
                {@render toolbar()}
            </div>
        {/if}
        <div class="modal-body" style={bodyStyle ?? ''}>
            {@render children?.()}
        </div>
        {#if footer}
            <div class="modal-footer" style={footerStyle ?? ''}>
                {@render footer()}
            </div>
        {/if}
    </div>
</div>

<style>
    @import '$lib/styles/shared.css';

    .overlay {
        position: fixed;
        inset: 0;
        background: rgba(0, 0, 0, 0.6);
        display: flex;
        align-items: center;
        justify-content: center;
        z-index: 1000;
    }
    /* Floating (non-modal) variant: no dim, clicks pass through to the app. */
    .overlay-floating {
        background: transparent;
        pointer-events: none;
    }
    .overlay-floating .modal {
        pointer-events: auto;
        box-shadow: 0 12px 40px rgba(0, 0, 0, 0.35);
    }
    .modal {
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-lg);
        max-width: 90vw;
        max-height: 90vh;
        display: flex;
        flex-direction: column;
        overflow: hidden;
    }
    .modal-header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 16px 20px;
        border-bottom: 1px solid var(--color-border);
        flex-shrink: 0;
    }
    .modal-header h2 {
        margin: 0;
        font-size: 1rem;
        font-weight: 600;
    }
    .modal-body {
        padding: 8px 20px;
        overflow-y: auto;
        flex: 1;
    }

    .modal-toolbar {
        padding: 12px 20px 8px;
        border-bottom: 1px solid var(--color-border);
        flex-shrink: 0;
    }
    .modal-footer {
        display: flex;
        align-items: center;
        justify-content: flex-end;
        gap: 8px;
        padding: 12px 20px;
        border-top: 1px solid var(--color-border);
        background: var(--color-surface-1);
        flex-shrink: 0;
        position: sticky;
        bottom: 0;
        z-index: 10;
    }
    .modal-footer :global(.btn-secondary),
    .modal-footer :global(.btn-primary) {
        padding: 8px 16px;
        border-radius: var(--radius-md);
        font-size: 0.875rem;
        font-weight: 500;
        font-family: inherit;
        cursor: pointer;
        border: none;
        transition: background 0.15s, opacity 0.15s;
    }
    .modal-footer :global(.btn-secondary) {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }
    .modal-footer :global(.btn-secondary):hover:not(:disabled) {
        background: var(--color-surface-4);
    }
    .modal-footer :global(.btn-primary) {
        background: var(--color-accent-dark);
        color: white;
    }
    .modal-footer :global(.btn-primary):hover:not(:disabled) {
        opacity: 0.85;
    }
    .modal-footer :global(.btn-secondary):disabled,
    .modal-footer :global(.btn-primary):disabled {
        opacity: 0.5;
        cursor: not-allowed;
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
