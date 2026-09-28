import { appStore, dialogStore, uiStore } from '$lib/stores';

/**
 * One modal that owns the Escape key while it is open. `store` + `field` name the property on a store
 * instance, which is what `__tests__/modal-state.unit.svelte.ts` enumerates; `open` says whether that
 * modal is up right now.
 */
export interface ModalFlag {
    store: 'uiStore' | 'appStore' | 'dialogStore';
    field: string;
    open: () => boolean;
}

/**
 * Every modal that swallows Escape, so the app behind it does not react to one. A store that grows a
 * modal-shaped field fails `__tests__/modal-state.unit.svelte.ts`: list the field here, or decline it below.
 *
 * @knipignore The completeness check enumerates the stores against the whole table.
 */
export const MODAL_FLAGS: readonly ModalFlag[] = [
    { store: 'uiStore', field: 'showCreateTask', open: () => uiStore.showCreateTask },
    { store: 'uiStore', field: 'showConfig', open: () => uiStore.showConfig },
    { store: 'uiStore', field: 'showTerminal', open: () => uiStore.showTerminal },
    { store: 'uiStore', field: 'showStats', open: () => uiStore.showStats },
    { store: 'uiStore', field: 'showSandboxConfig', open: () => uiStore.showSandboxConfig },
    { store: 'appStore', field: 'showEditTask', open: () => appStore.showEditTask },
    { store: 'dialogStore', field: 'confirmationSlot', open: () => dialogStore.confirmationSlot.pending !== null },
    { store: 'dialogStore', field: 'commitDialogSlot', open: () => dialogStore.commitDialogSlot.pending !== null },
    { store: 'dialogStore', field: 'compressContextDialogSlot', open: () => dialogStore.compressContextDialogSlot.pending !== null },
];

/**
 * Modal-shaped fields that own no Escape, named as `<store>.<field>`. A field in `MODAL_FLAGS` and a
 * field here is a contradiction the coverage test fails on; an entry naming no field is stale and
 * fails too.
 */
export const ESCAPE_INDIFFERENT_FIELDS: readonly string[] = [
    // The full task view is closed by Escape rather than protected from it.
    'uiStore.showCardDetail',
];

/** True when any modal owns the Escape key (the app behind must not react to
 *  Escape while one of these is open). The explorer views' Escape handlers, the
 *  board and task-view shortcut gates and the start-task shortcut all ask it, so
 *  a new modal needs no new check. */
export function isAnyModalOpen(): boolean {
    return MODAL_FLAGS.some((flag) => flag.open());
}
