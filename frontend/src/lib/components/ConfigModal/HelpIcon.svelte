<script lang="ts">
    /**
     * Help icon ("?") next to a form label. Hover devices keep the native `title`
     * tooltip; touch devices never show one on tap, so the icon is a button toggling a
     * bubble. The bubble is fixed-positioned (the card clips overflow), follows the icon
     * on scroll rather than dismissing (the settings modal scrolls programmatically while
     * the press lands), and closes on re-press, outside tap or Escape.
     *
     * The dismissing listeners are attached imperatively: a `$effect` invalidated by
     * `open` would defer and drop the render update that opens the bubble.
     *
     * @param text  the tooltip message
     * @param label short field name for the accessible name (e.g. "Concurrency")
     */
    import { t } from '$lib/i18n';

    let { text, label }: { text: string; label: string } = $props();

    const TOUCH_QUERY = '(hover: none)';
    const MARGIN = 8;
    const GAP = 6;
    const MAX_WIDTH = 320;

    let touch = $state(false);
    let open = $state(false);
    let pos = $state({ top: 0, left: 0 });
    let btnEl = $state<HTMLButtonElement | null>(null);
    let bubbleEl = $state<HTMLDivElement | null>(null);
    let dismissingAttached = false;

    const onPointerDown = (e: PointerEvent) => {
        const t = e.target as Node | null;
        if (t && (btnEl?.contains(t) || bubbleEl?.contains(t))) return;
        close();
    };
    const onKeyDown = (e: KeyboardEvent) => {
        if (e.key === 'Escape') close();
    };
    const onScroll = () => positionBubble();

    function positionBubble() {
        if (!btnEl || !bubbleEl) return;
        // The bubble is always laid out (hidden) so its size is measurable
        // even before it becomes visible.
        const rect = btnEl.getBoundingClientRect();
        const vw = window.innerWidth;
        const width = Math.min(MAX_WIDTH, vw - MARGIN * 2);
        let top = rect.top - GAP - bubbleEl.offsetHeight;
        if (top < MARGIN) top = rect.bottom + GAP;
        const left = Math.max(MARGIN, Math.min(rect.left + rect.width / 2 - width / 2, vw - width - MARGIN));
        pos = { top, left };
    }

    function attachDismissing() {
        if (dismissingAttached) return;
        dismissingAttached = true;
        document.addEventListener('pointerdown', onPointerDown, true);
        document.addEventListener('keydown', onKeyDown, true);
        window.addEventListener('scroll', onScroll, true);
    }

    function detachDismissing() {
        if (!dismissingAttached) return;
        dismissingAttached = false;
        document.removeEventListener('pointerdown', onPointerDown, true);
        document.removeEventListener('keydown', onKeyDown, true);
        window.removeEventListener('scroll', onScroll, true);
    }

    if (typeof window !== 'undefined') {
        const mql = window.matchMedia(TOUCH_QUERY);
        touch = mql.matches;
        const onMediaChange = () => {
            if (mql.matches !== touch) {
                touch = mql.matches;
                if (open) close();
            }
        };
        const onResize = () => {
            onMediaChange();
            if (open) positionBubble();
        };
        mql.addEventListener('change', onMediaChange);
        window.addEventListener('resize', onResize);
        $effect(() => () => {
            mql.removeEventListener('change', onMediaChange);
            window.removeEventListener('resize', onResize);
            detachDismissing();
        });
    }

    function toggle() {
        if (!btnEl || !bubbleEl) return;
        if (open) {
            close();
            return;
        }
        positionBubble();
        open = true;
        attachDismissing();
    }

    function close() {
        open = false;
        detachDismissing();
    }
</script>

{#if touch}
    <button type="button" class="help-icon" bind:this={btnEl} onclick={toggle} aria-label={t('settings.help.aria', { label })} aria-expanded={open}>
        <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
            <circle cx="8" cy="8" r="6.5" stroke="currentColor" stroke-width="1.2"/>
            <text x="8" y="12" text-anchor="middle" fill="currentColor" font-size="0.75rem" font-weight="600">?</text>
        </svg>
    </button>
    <div class="help-tooltip" class:visible={open} role="tooltip" bind:this={bubbleEl} style:top="{pos.top}px" style:left="{pos.left}px">{text}</div>
{:else}
    <span class="help-icon" title={text}>
        <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
            <circle cx="8" cy="8" r="6.5" stroke="currentColor" stroke-width="1.2"/>
            <text x="8" y="12" text-anchor="middle" fill="currentColor" font-size="0.75rem" font-weight="600">?</text>
        </svg>
    </span>
{/if}

<style>
    .help-icon {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        color: var(--color-text-secondary);
        opacity: 0.5;
        cursor: help;
        background: none;
        border: none;
        padding: 0;
    }
    .help-icon:hover {
        opacity: 0.8;
    }
    .help-tooltip {
        position: fixed;
        z-index: var(--z-overlay);
        width: max-content;
        max-width: min(320px, calc(100vw - 16px));
        visibility: hidden;
        pointer-events: none;
        padding: 6px 8px;
        font-size: var(--text-xs);
        line-height: 1.4;
        color: var(--color-text-primary);
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        box-shadow: 0 8px 24px rgba(0, 0, 0, 0.2);
    }
    .help-tooltip.visible {
        visibility: visible;
    }
</style>
