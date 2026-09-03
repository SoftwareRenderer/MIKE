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
    /**
     * A stat value may be a snapshot number or a live getter; getters let the
     * dialog's visualization track reactive sources (e.g. stream state) in real time.
     */
    public compressContextDialog = $state<{
        used?: number | (() => number);
        limit?: number | (() => number);
        sessionCost?: number | (() => number);
        confirmText?: string;
        cancelText?: string;
        _resolve?: (value: boolean) => void;
    } | null>(null);

    /** Pending dismissal — one toast at a time, so one timer at a time. */
    private toastTimer: ReturnType<typeof setTimeout> | null = null;

    /** Show a toast message that auto-dismisses after `duration` ms. */
    public showToast(message: string, type: 'error' | 'success' = 'error', duration = TOAST_DURATION_MS) {
        // Without this, a toast shown while an older one is still counting down inherits
        // the older timer and disappears on the previous toast's schedule.
        if (this.toastTimer) clearTimeout(this.toastTimer);
        this.toast = { message, type };
        this.toastTimer = setTimeout(() => {
            this.toastTimer = null;
            this.toast = null;
        }, duration);
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
     * Pass getters for `used`/`limit`/`sessionCost` to keep the visualization
     * and stats updating in real time while the dialog is open.
     */
    public showCompressContextDialog(options: {
        used?: number | (() => number);
        limit?: number | (() => number);
        sessionCost?: number | (() => number);
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
