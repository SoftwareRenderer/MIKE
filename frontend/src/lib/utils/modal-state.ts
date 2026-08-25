import { appStore, dialogStore, uiStore } from '$lib/stores';

/** True when any modal owns the Escape key (the app behind must not react to
 *  Escape while one of these is open). Shared by the explorer views' Escape
 *  handlers — the floating line-task modal in particular keeps the app behind
 *  interactive. */
export function isAnyModalOpen(): boolean {
    return !!(
        uiStore.showCreateTask || uiStore.showConfig || uiStore.showTerminal ||
        uiStore.showStats || uiStore.showSandboxConfig ||
        appStore.showEditTask ||
        dialogStore.confirmation || dialogStore.commitDialog || dialogStore.compressContextDialog
    );
}
