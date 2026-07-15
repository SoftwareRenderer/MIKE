// ─── Toast ───

export class DialogStore {
    public toast = $state<{ message: string; type: 'error' | 'success' } | null>(null);
    public confirmation = $state<{
        title: string;
        message: string;
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

    /** Show a toast message that auto-dismisses after `duration` ms. */
    public showToast(message: string, type: 'error' | 'success' = 'error', duration = 4000) {
        this.toast = { message, type };
        setTimeout(() => this.toast = null, duration);
    }

    /**
     * Show a confirmation dialog and return a Promise that resolves to true (confirmed) or false (cancelled).
     */
    public showConfirm(options: {
        title: string;
        message: string;
        confirmText?: string;
        cancelText?: string;
        variant?: 'default' | 'destructive';
    }): Promise<boolean> {
        return new Promise((resolve) => {
            this.confirmation = {
                title: options.title,
                message: options.message,
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
