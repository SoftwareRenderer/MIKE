/**
 * Svelte 5 adapter of the reference useResizeDrag hook.
 * Manages global mousemove/mouseup and touchmove/touchend listeners during a drag session,
 * and restores body cursor/userSelect styles on end.
 */

interface ResizeDragSession {
    axis: 'x' | 'y';
    cursor: 'ew-resize' | 'ns-resize';
    onMove: (pointer: number) => void;
    onEnd?: (pointer: number) => void;
}

let session: ResizeDragSession | null = null;
let previousBodyStyle: { userSelect: string; cursor: string } | null = null;

function getPointerPosition(axis: 'x' | 'y', event: MouseEvent | TouchEvent): number {
    const pos = 'touches' in event ? event.touches[0] : event as MouseEvent;
    return axis === 'x' ? pos.clientX : pos.clientY;
}

function handlePointerMove(event: MouseEvent | TouchEvent) {
    if (!session) return;
    const pointer = getPointerPosition(session.axis, event);
    session.onMove(pointer);
}

function handlePointerUp(event: MouseEvent | TouchEvent) {
    if (!session) return;
    const pointer = 'changedTouches' in event ? event.changedTouches[0] : event as MouseEvent;
    const pos = session.axis === 'x' ? pointer.clientX : pointer.clientY;
    session.onEnd?.(pos);
    stopDrag();
}

function stopDrag() {
    if (previousBodyStyle) {
        document.body.style.userSelect = previousBodyStyle.userSelect;
        document.body.style.cursor = previousBodyStyle.cursor;
    }
    previousBodyStyle = null;
    session = null;
    document.removeEventListener('mousemove', handlePointerMove);
    document.removeEventListener('mouseup', handlePointerUp);
    document.removeEventListener('touchmove', handlePointerMove);
    document.removeEventListener('touchend', handlePointerUp);
}

/**
 * Start a resize drag session. Call from a mouseDown or touchStart handler on a resize handle.
 * @param event The original mouse or touch event
 * @param config Configuration for the drag session
 */
export function startDrag(
    event: MouseEvent | TouchEvent,
    config: {
        axis: 'x' | 'y';
        cursor: 'ew-resize' | 'ns-resize';
        onMove: (pointer: number) => void;
        onEnd?: (pointer: number) => void;
    }
): void {
    event.preventDefault();
    // Stop any existing session
    if (session) {
        document.removeEventListener('mousemove', handlePointerMove);
        document.removeEventListener('mouseup', handlePointerUp);
        document.removeEventListener('touchmove', handlePointerMove);
        document.removeEventListener('touchend', handlePointerUp);
    }
    session = config;
    previousBodyStyle = {
        userSelect: document.body.style.userSelect,
        cursor: document.body.style.cursor,
    };
    document.body.style.userSelect = 'none';
    document.body.style.cursor = config.cursor;
    document.addEventListener('mousemove', handlePointerMove);
    document.addEventListener('mouseup', handlePointerUp);
    document.addEventListener('touchmove', handlePointerMove, { passive: false });
    document.addEventListener('touchend', handlePointerUp);
}


