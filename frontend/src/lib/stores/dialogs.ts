// Legacy compat — re-export from reactive store
// Prefer importing from $lib/stores (appStore, uiStore, dialogStore)
import { dialogStore } from './index';

export const toast = {
    get: () => dialogStore.toast,
    set: (v: typeof dialogStore.toast) => { dialogStore.toast = v; },
    subscribe: (fn: (v: typeof dialogStore.toast) => void) => { fn(dialogStore.toast); return () => {}; },
} as any;

export function showToast(message: string, type: 'error' | 'success' = 'error', duration = 4000) {
    dialogStore.showToast(message, type, duration);
}

export const confirmation = {
    get: () => dialogStore.confirmation,
    set: (v: typeof dialogStore.confirmation) => { dialogStore.confirmation = v; },
    subscribe: (fn: (v: typeof dialogStore.confirmation) => void) => { fn(dialogStore.confirmation); return () => {}; },
} as any;

export function showConfirm(options: {
    title: string;
    message: string;
    confirmText?: string;
    cancelText?: string;
    variant?: 'default' | 'destructive';
}): Promise<boolean> {
    return dialogStore.showConfirm(options);
}

export interface CommitDialogOptions {
    message: string;
    confirmText?: string;
    cancelText?: string;
    authorName?: string;
    authorEmail?: string;
    mergeTarget?: string;
    streaming?: boolean;
    promptProgress?: number;
    projectId?: string;
    taskId?: string;
}

export const commitDialog = {
    get: () => dialogStore.commitDialog,
    set: (v: typeof dialogStore.commitDialog) => { dialogStore.commitDialog = v; },
    subscribe: (fn: (v: typeof dialogStore.commitDialog) => void) => { fn(dialogStore.commitDialog); return () => {}; },
} as any;

export function showCommitDialog(options: CommitDialogOptions): Promise<string | null> {
    return dialogStore.showCommitDialog(options);
}
