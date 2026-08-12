// ─── Toast ───
import { TOAST_DURATION_MS } from '$lib/constants';

export class DialogStore {
    public toast = $state<{ message: string; type: 'error' | 'success' } | null>(null);
    public confirmation = $state<{
        title: string;
        message: string;
        items?: string[];
        confirmText?: string;
        cancelText?: string;
        variant?: 'default' | 'destructive';
        _resolve?: (value: boolean) => void;
    } | null>(null);
    public commitDialog = $state<{
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
        _resolve?: (value: string | null) => void;
    } | null>(null);
    public compressContextDialog = $state<{
        used?: number;
        limit?: number;
        sessionCost?: number;
        confirmText?: string;
        cancelText?: string;
        _resolve?: (value: boolean) => void;
    } | null>(null);

    /** Show a toast message that auto-dismisses after `duration` ms. */
    public showToast(message: string, type: 'error' | 'success' = 'error', duration = TOAST_DURATION_MS) {
        this.toast = { message, type };
        setTimeout(() => this.toast = null, duration);
    }

    /**
     * Show a confirmation dialog and return a Promise that resolves to true (confirmed) or false (cancelled).
     */
    public showConfirm(options: {
        title: string;
        message: string;
        items?: string[];
        confirmText?: string;
        cancelText?: string;
        variant?: 'default' | 'destructive';
    }): Promise<boolean> {
        return new Promise((resolve) => {
            this.confirmation = {
                title: options.title,
                message: options.message,
                items: options.items,
                confirmText: options.confirmText ?? 'Confirm',
                cancelText: options.cancelText ?? 'Cancel',
                variant: options.variant ?? 'destructive',
                _resolve: resolve,
            };
        });
    }

    /**
     * Show a commit confirmation dialog and return a Promise that resolves with the edited message or null (cancelled).
     */
    /**
     * Show the Compress Context dialog (includes a context donut + stats) and
     * return a Promise that resolves to true (confirmed) or false (cancelled).
     */
    public showCompressContextDialog(options: {
        used?: number;
        limit?: number;
        sessionCost?: number;
        confirmText?: string;
        cancelText?: string;
    }): Promise<boolean> {
        return new Promise((resolve) => {
            this.compressContextDialog = {
                used: options.used,
                limit: options.limit,
                sessionCost: options.sessionCost,
                confirmText: options.confirmText ?? 'Compress',
                cancelText: options.cancelText ?? 'Cancel',
                _resolve: resolve,
            };
        });
    }

    public showCommitDialog(options: {
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
    }): Promise<string | null> {
        return new Promise((resolve) => {
            this.commitDialog = {
                message: options.message,
                confirmText: options.confirmText ?? 'Commit and move to Done',
                cancelText: options.cancelText ?? 'Cancel',
                authorName: options.authorName,
                authorEmail: options.authorEmail,
                mergeTarget: options.mergeTarget,
                streaming: options.streaming,
                promptProgress: options.promptProgress,
                projectId: options.projectId,
                taskId: options.taskId,
                _resolve: resolve,
            };
        });
    }
}
