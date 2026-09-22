import { dragCarriesFiles } from '$lib/utils/file-drop';

/**
 * The hover half of a file drop zone, counting enter/leave because a drag crosses the zone's
 * children and each child fires its own pair.
 */
export function useFileDropZone(onDrop: (event: DragEvent) => void): {
    readonly isOver: boolean;
    /** Put the highlight away without an event, for a run that ends with the pointer still inside. */
    clearHover(): void;
    handleDragEnter(e: DragEvent): void;
    handleDragOver(e: DragEvent): void;
    handleDragLeave(e: DragEvent): void;
    handleDrop(e: DragEvent): void;
} {
    let isOver = $state(false);
    let dragDepth = 0;

    return {
        get isOver() { return isOver; },
        clearHover() {
            dragDepth = 0;
            isOver = false;
        },
        handleDragEnter(e: DragEvent) {
            if (!dragCarriesFiles(e.dataTransfer)) return;
            e.preventDefault();
            dragDepth++;
            isOver = true;
        },
        handleDragOver(e: DragEvent) {
            if (!dragCarriesFiles(e.dataTransfer)) return;
            // Without this the browser refuses the drop and opens the file in the tab.
            e.preventDefault();
            if (e.dataTransfer) e.dataTransfer.dropEffect = 'copy';
            isOver = true;
        },
        handleDragLeave(e: DragEvent) {
            if (!dragCarriesFiles(e.dataTransfer)) return;
            dragDepth = Math.max(0, dragDepth - 1);
            if (dragDepth === 0) isOver = false;
        },
        handleDrop(e: DragEvent) {
            if (!dragCarriesFiles(e.dataTransfer)) return;
            e.preventDefault();
            dragDepth = 0;
            isOver = false;
            onDrop(e);
        },
    };
}
