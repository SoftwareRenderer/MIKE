import { writable } from 'svelte/store';

// ─── Toast ───

// Toast notification — empty string means no toast visible
export const toast = writable<{ message: string; type: 'error' | 'success' } | null>(null);

/** Show a toast message that auto-dismisses after `duration` ms. */
export function showToast(message: string, type: 'error' | 'success' = 'error', duration = 4000) {
    toast.set({ message, type });
    setTimeout(() => toast.set(null), duration);
}

// ─── Confirmation Dialog ───

export interface ConfirmOptions {
    title: string;
    message: string;
    confirmText?: string;
    cancelText?: string;
    variant?: 'default' | 'destructive';
}

export const confirmation = writable<ConfirmOptions | null>(null);

/**
 * Show a confirmation dialog and return a Promise that resolves to true (confirmed) or false (cancelled).
 */
export function showConfirm(options: ConfirmOptions): Promise<boolean> {
    return new Promise((resolve) => {
        confirmation.set({
            title: options.title,
            message: options.message,
            confirmText: options.confirmText ?? 'Confirm',
            cancelText: options.cancelText ?? 'Cancel',
            variant: options.variant ?? 'destructive',
            _resolve: resolve,
        } as ConfirmOptions & { _resolve?: (value: boolean) => void });
    });
}

// ─── Commit Dialog ───

export interface CommitDialogOptions {
    message: string;
    confirmText?: string;
    cancelText?: string;
    authorName?: string;
    authorEmail?: string;
    mergeTarget?: string;
    // Streaming support
    streaming?: boolean;       // true while LLM is streaming tokens
    promptProgress?: number;   // 0-100 prompt processing percentage
    projectId?: string;        // needed for streaming endpoint
    taskId?: string;           // needed for streaming endpoint
}

export const commitDialog = writable<CommitDialogOptions | null>(null);

/**
 * Show a commit confirmation dialog and return a Promise that resolves with the edited message or null (cancelled).
 */
export function showCommitDialog(options: CommitDialogOptions): Promise<string | null> {
    return new Promise((resolve) => {
        commitDialog.set({
            message: options.message,
            confirmText: options.confirmText ?? 'Commit and move to Done',
            cancelText: options.cancelText ?? 'Cancel',
            _resolve: resolve,
        } as CommitDialogOptions & { _resolve?: (value: string | null) => void });
    });
}
