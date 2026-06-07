<script lang="ts">
    let {
        ariaLabel = 'Resize panel',
        onDragStart,
        onDragMove,
        onDragEnd,
    }: {
        ariaLabel?: string;
        /** Called on mousedown with the event and startX position */
        onDragStart: (event: MouseEvent, startX: number) => void;
        /** Called during drag with the current pointer X position */
        onDragMove: (pointerX: number) => void;
        /** Called on mouseup with the final pointer X position */
        onDragEnd?: (pointerX: number) => void;
    } = $props();

    import { startDrag } from '$lib/utils/use-resize-drag';

    function handleMouseDown(event: MouseEvent) {
        const container = (event.currentTarget as HTMLElement).closest('.columns');
        if (!container) return;
        const containerWidth = (container as HTMLElement).offsetWidth;
        const startX = event.clientX;

        startDrag(event, {
            axis: 'x',
            cursor: 'ew-resize',
            onMove: (pointerX: number) => {
                onDragMove(pointerX);
            },
            onEnd: (pointerX: number) => {
                onDragEnd?.(pointerX);
            },
        });

        onDragStart(event, startX);
    }
</script>

<div
    role="separator"
    aria-orientation="vertical"
    aria-label={ariaLabel}
    class="resize-handle"
    onmousedown={handleMouseDown}
>
    <div class="resize-handle-line"></div>
</div>

<style>
    .resize-handle {
        position: relative;
        width: 1px;
        flex-shrink: 0;
        cursor: ew-resize;
        z-index: 10;
        display: flex;
        align-items: stretch;
    }

    .resize-handle-line {
        width: 1px;
        min-height: 100%;
        background: var(--color-border);
        transition: background 0.15s ease;
    }

    .resize-handle:hover .resize-handle-line {
        background: var(--color-accent);
    }

    /* Expand hit area */
    .resize-handle::before {
        content: '';
        position: absolute;
        top: 0;
        bottom: 0;
        left: -4px;
        right: -4px;
        z-index: 1;
    }
</style>
