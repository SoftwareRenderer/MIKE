<script lang="ts">
    import { t } from '$lib/i18n';

    let {
        ariaLabel: ariaLabelProp,
        axis = 'vertical',
        onDragStart,
        onDragMove,
        onDragEnd,
    }: {
        ariaLabel?: string;
        /** Resize direction: 'vertical' (col-resize) or 'horizontal' (row-resize) */
        axis?: 'vertical' | 'horizontal';
        /** Called on mousedown/touchstart with the event and start position */
        onDragStart: (event: MouseEvent | TouchEvent, startPos: number) => void;
        /** Called during drag with the current pointer position */
        onDragMove: (pointerPos: number) => void;
        /** Called on mouseup/touchend with the final pointer position */
        onDragEnd?: (pointerPos: number) => void;
    } = $props();

    let dragging = $state(false);

    // Derived rather than a prop default: the fallback has to re-resolve when the catalog changes.
    const ariaLabel = $derived(ariaLabelProp ?? t('app.resizePanelAria'));

    function getPointerPos(event: MouseEvent | TouchEvent): number {
        const touch = 'touches' in event
            ? event.touches[0] ?? event.changedTouches[0]
            : null;
        const ptr = touch || event;
        return axis === 'horizontal' ? (ptr as MouseEvent).clientY : (ptr as MouseEvent).clientX;
    }

    function handlePointerMove(event: MouseEvent | TouchEvent) {
        onDragMove(getPointerPos(event));
    }

    function handlePointerUp(event: MouseEvent | TouchEvent) {
        dragging = false;
        onDragEnd?.(getPointerPos(event));
        document.removeEventListener('mousemove', handlePointerMove);
        document.removeEventListener('mouseup', handlePointerUp);
        document.removeEventListener('touchmove', handlePointerMove);
        document.removeEventListener('touchend', handlePointerUp);
        document.body.style.userSelect = '';
        document.body.style.cursor = '';
    }

    function handlePointerDown(event: MouseEvent | TouchEvent) {
        event.preventDefault();
        dragging = true;
        document.body.style.userSelect = 'none';
        document.body.style.cursor = axis === 'horizontal' ? 'ns-resize' : 'ew-resize';
        document.addEventListener('mousemove', handlePointerMove);
        document.addEventListener('mouseup', handlePointerUp);
        document.addEventListener('touchmove', handlePointerMove, { passive: false });
        document.addEventListener('touchend', handlePointerUp);
        onDragStart(event, getPointerPos(event));
    }
</script>

<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
<div
    role="separator"
    aria-orientation={axis}
    aria-label={ariaLabel}
    class="resize-handle"
    class:dragging={dragging}
    data-axis={axis === 'horizontal' ? 'horizontal' : undefined}
    onmousedown={handlePointerDown}
    ontouchstart={handlePointerDown}
></div>

<style>
    .resize-handle {
        cursor: ew-resize;
        flex-shrink: 0;
        z-index: var(--z-sticky);
        position: relative;
    }
    .resize-handle::before {
        content: '';
        position: absolute;
        background: var(--color-border);
        transition: background 0.15s ease;
    }
    .resize-handle:hover::before,
    .resize-handle.dragging::before {
        background: var(--color-accent);
    }

    /* Vertical divider (between columns) */
    .resize-handle:not([data-axis="horizontal"]) {
        width: 6px;
        margin-left: -3px;
        margin-right: -3px;
        cursor: col-resize;
    }
    .resize-handle:not([data-axis="horizontal"])::before {
        left: 50%;
        top: 0;
        bottom: 0;
        width: 1px;
        transform: translateX(-50%);
    }

    /* Horizontal divider (between rows) */
    .resize-handle[data-axis="horizontal"] {
        height: 6px;
        margin-top: -3px;
        margin-bottom: -3px;
        cursor: row-resize;
    }
    .resize-handle[data-axis="horizontal"]::before {
        top: 50%;
        left: 0;
        right: 0;
        height: 1px;
        transform: translateY(-50%);
    }
</style>
