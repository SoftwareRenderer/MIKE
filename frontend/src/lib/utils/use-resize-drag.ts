/**
 * Svelte 5 adapter of the reference useResizeDrag hook.
 * Manages global mousemove/mouseup listeners during a drag session,
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

function getPointerPosition(axis: 'x' | 'y', event: MouseEvent): number {
    return axis === 'x' ? event.clientX : event.clientY;
}

function handleMouseMove(event: MouseEvent) {
    if (!session) return;
    const pointer = getPointerPosition(session.axis, event);
    session.onMove(pointer);
}

function handleMouseUp(event: MouseEvent) {
    if (!session) return;
    const pointer = getPointerPosition(session.axis, event);
    session.onEnd?.(pointer);
    stopDrag();
}

function stopDrag() {
    if (previousBodyStyle) {
        document.body.style.userSelect = previousBodyStyle.userSelect;
        document.body.style.cursor = previousBodyStyle.cursor;
    }
    previousBodyStyle = null;
    session = null;
    document.removeEventListener('mousemove', handleMouseMove);
    document.removeEventListener('mouseup', handleMouseUp);
}

/**
 * Start a resize drag session. Call from a mouseDown handler on a resize handle.
 * @param event The original mouse event
 * @param config Configuration for the drag session
 */
export function startDrag(
    event: MouseEvent,
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
        document.removeEventListener('mousemove', handleMouseMove);
        document.removeEventListener('mouseup', handleMouseUp);
    }
    session = config;
    previousBodyStyle = {
        userSelect: document.body.style.userSelect,
        cursor: document.body.style.cursor,
    };
    document.body.style.userSelect = 'none';
    document.body.style.cursor = config.cursor;
    document.addEventListener('mousemove', handleMouseMove);
    document.addEventListener('mouseup', handleMouseUp);
}


