import { onDestroy } from 'svelte';
import {
    allSelectablePaths,
    applyRowClick,
    emptySelection,
    pruneSelection,
    type FileSelection,
    type RowOrder,
} from '$lib/utils/file-selection';

/**
 * The gestures that put rows in a selection — click, ctrl, shift, hold, Ctrl+A, Escape — for any listing
 * whose rows are named by a path. What the listing is about stays with the listing: this sees only a row
 * index.
 *
 * Read what comes back through its getters; a destructured value is a snapshot of one render.
 */

/** A finger must hold a row this long, and not wander further than this, to select instead of open. */
const LONG_PRESS_MS = 450;
const LONG_PRESS_SLOP = 10;

export interface FileSelectionOptions {
    /** The rows in render order. Read per gesture, so a re-listing needs no re-registration. */
    rows: () => RowOrder;
}

/**
 * What the selection made of a key press. `menu` names the row it asked about, found through the row's
 * `data-index`, and the point to hang the menu from.
 */
export type SelectionKey =
    | { kind: 'handled' }
    | { kind: 'menu'; index: number; at: { x: number; y: number } };

export function useFileSelection({ rows }: FileSelectionOptions) {
    // What a gesture last asked for; `selection` is that answer minus the rows the listing no longer has.
    let held = $state<FileSelection>(emptySelection());
    // Set when a gesture selects on purpose, which shows the action bar even for one row.
    let selectionMode = $state(false);

    let selection = $derived(pruneSelection(rows(), held));
    let selectedPaths = $derived([...selection.paths]);
    let showActionBar = $derived(selection.paths.size > 1 || (selectionMode && selection.paths.size > 0));

    function isSelected(path: string): boolean {
        return selection.paths.has(path);
    }

    function clear(): void {
        held = emptySelection();
        selectionMode = false;
    }

    function selectAll(): void {
        held = { paths: allSelectablePaths(rows()), anchor: -1 };
        selectionMode = true;
    }

    /** Point the selection at one row, which is what a right-click on a row outside it does first. */
    function selectRow(index: number): void {
        const row = rows()[index];
        if (!row || !row.selectable || selection.paths.has(row.path)) return;
        held = { paths: new Set([row.path]), anchor: index };
    }

    /**
     * Give the held rows back. True when there were any, which tells a host listening for the same key
     * that the key went to the selection rather than to closing the panel.
     */
    function releaseSelection(): boolean {
        if (selection.paths.size === 0) return false;
        clear();
        return true;
    }

    /** True when the row itself should open: a plain click does, a modifier only moves the selection. */
    function handleClick(e: MouseEvent, index: number): boolean {
        if (swallowClick) {
            swallowClick = false;
            return false;
        }
        const mods = { ctrl: e.ctrlKey || e.metaKey, shift: e.shiftKey };
        held = applyRowClick(rows(), selection, index, mods);
        if (mods.ctrl || mods.shift) return false;
        selectionMode = false;
        return true;
    }

    // Touch has no ctrl key, so holding a row is the multi-select gesture.
    let longPressTimer: ReturnType<typeof setTimeout> | null = null;
    let pressAt = { x: 0, y: 0 };
    // Set by a long press, so the click that follows doesn't also open the row.
    let swallowClick = false;
    // The pointer type of the press that is down, which is what makes a `contextmenu` from a finger
    // mean something other than a `contextmenu` from a right button.
    let pressType: string | null = null;

    function handlePointerDown(e: PointerEvent, index: number): void {
        // Clear this so a long press that never got its click doesn't swallow the next one.
        swallowClick = false;
        cancelLongPress();
        pressType = e.pointerType;
        if (e.pointerType !== 'touch') return;
        pressAt = { x: e.clientX, y: e.clientY };
        longPressTimer = setTimeout(() => {
            longPressTimer = null;
            swallowClick = true;
            selectionMode = true;
            held = applyRowClick(rows(), selection, index, { ctrl: true, shift: false });
        }, LONG_PRESS_MS);
    }

    function handlePointerMove(e: PointerEvent): void {
        if (!longPressTimer) return;
        if (Math.abs(e.clientX - pressAt.x) > LONG_PRESS_SLOP || Math.abs(e.clientY - pressAt.y) > LONG_PRESS_SLOP) {
            cancelLongPress();
        }
    }

    function cancelLongPress(): void {
        pressType = null;
        if (!longPressTimer) return;
        clearTimeout(longPressTimer);
        longPressTimer = null;
    }

    /**
     * Whether a `contextmenu` on a row is a menu request. A held finger raises one about when the hold
     * fires, and that gesture already went to the selection — answering it again would point the
     * selection back at the row the hold just released. A touch listing has no right button, and its
     * menu is the action bar. True when the gesture already answered it, so the host opens no menu.
     */
    function handleContextmenu(e: MouseEvent | KeyboardEvent): boolean {
        if (pressType !== 'touch') return false;
        e.preventDefault();
        return true;
    }

    onDestroy(cancelLongPress);

    function handleKeydown(e: KeyboardEvent): SelectionKey | null {
        if (e.key === 'ContextMenu' || (e.shiftKey && e.key === 'F10')) {
            const row = e.target instanceof HTMLElement ? e.target.closest<HTMLElement>('[data-index]') : null;
            const index = Number(row?.dataset.index);
            if (!row || !Number.isInteger(index) || !rows()[index]?.selectable) return null;
            const rect = row.getBoundingClientRect();
            return { kind: 'menu', index, at: { x: rect.left + 16, y: rect.bottom } };
        }
        if ((e.ctrlKey || e.metaKey) && (e.key === 'a' || e.key === 'A')) {
            e.preventDefault();
            selectAll();
            return { kind: 'handled' };
        }
        if (e.key === 'Escape' && releaseSelection()) {
            e.preventDefault();
            // Stop here so the panel behind the list, which closes on the same key, stays open.
            e.stopPropagation();
            return { kind: 'handled' };
        }
        return null;
    }

    return {
        get selectedPaths() { return selectedPaths; },
        get showActionBar() { return showActionBar; },
        isSelected,
        handleClick,
        handlePointerDown,
        handlePointerMove,
        handleContextmenu,
        cancelLongPress,
        handleKeydown,
        selectRow,
        releaseSelection,
        clear,
    };
}
