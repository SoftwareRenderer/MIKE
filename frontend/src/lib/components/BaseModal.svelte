<script lang="ts">
    import type { Snippet } from 'svelte';
    import { onMount, onDestroy } from 'svelte';
    import { createFocusTrap } from '$lib/a11y';
    import { Z } from '$lib/constants/z-layers';
    import { t } from '$lib/i18n';
    import Icon from './Icon.svelte';
    import closeIcon from '$lib/assets/icons/close.svg?raw';

    let {
        title = '',
        width = '640px',
        zIndex = Z.modal,
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
        maxWidth,
        maxHeight,
        overlayClass,
    }: {
        title?: string;
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
        maxWidth?: string;
        maxHeight?: string;
        /** Name for this dialog's overlay, so a `:global()` rule can name the dialog it means. */
        overlayClass?: string;
    } = $props();

    let modalEl: HTMLDivElement | null = null;
    let focusTrap: ReturnType<typeof createFocusTrap> | null = null;
    let triggerEl: HTMLElement | null = null;
    let closed = $state(false);
    let modalClasses = $derived(modalClass ? `modal ${modalClass}` : 'modal');
    let overlayClasses = $derived(['overlay', floating ? 'overlay-floating' : '', overlayClass ?? ''].filter(Boolean).join(' '));

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

<div class={overlayClasses} style="z-index: {zIndex}" onclick={handleOverlayClick} role="presentation">
    <!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions — modal container stops click propagation, not an activation target -->
    <div
        class={modalClasses}
        style="width: {width}{maxWidth ? `; max-width: ${maxWidth}` : ''}{maxHeight ? `; max-height: ${maxHeight}` : ''}"
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
            <button class="close-btn" onclick={handleClose} aria-label={t('app.close')} title={t('app.close')}><Icon src={closeIcon} width={16} height={16} /></button>
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

    .overlay {
        position: fixed;
        inset: 0;
        background: var(--color-backdrop-modal);
        display: flex;
        align-items: center;
        justify-content: center;
        z-index: var(--z-modal);
    }
    /* Floating (non-modal) variant: no dim, clicks pass through to the app. */
    .overlay-floating {
        background: transparent;
        pointer-events: none;
    }
    .overlay-floating .modal {
        pointer-events: auto;
        box-shadow: var(--shadow-float);
    }
    .modal {
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        box-shadow: var(--shadow-modal);
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
        font-size: var(--text-lg);
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
        z-index: var(--z-sticky);
    }
    /* The footer's buttons are styled by `shared.css`'s `.btn` family, which every footer passes in;
       this shell owns the row, not the controls in it. */
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
