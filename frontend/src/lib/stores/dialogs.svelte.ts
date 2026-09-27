// ─── Toast ───
import { TOAST_DURATION_MS } from '$lib/constants';
import { t } from '$lib/i18n';

/**
 * How a promise-backed dialog colours its own answer. `destructive` is the thing being undone; `default`
 * is the ordinary yes, in the primary green every dialog's confirm button wears. `showConfirm` answers
 * with `destructive` when a caller names no variant, because most of what gets confirmed here destroys
 * something — a question that asks permission is the exception and says so.
 */
export type DialogVariant = 'default' | 'destructive';

/** The handle a promise-backed dialog settles its caller's promise with. */
interface SlotRequest<Answer> {
    _resolve?: (value: Answer) => void;
}

/**
 * One dialog that answers a promise: the pending request its dialog arms itself from, and the promise
 * its caller waits on. `open` is the only write that puts a request in and `clear` the only one that
 * takes it out, which is what keeps the slot the single source of "is this dialog open" — the rule the
 * dialog half of the pattern, `use-promise-dialog.svelte.ts`, is written against.
 *
 * A request still pending when a newer one opens is answered `declined` rather than dropped, so the
 * older caller resumes as cancelled instead of hanging on a dialog now showing someone else's request.
 */
export class PromiseSlot<Request, Answer> {
    /** The pending request, or null. Only `open` and `clear` write it. */
    public pending = $state<(Request & SlotRequest<Answer>) | null>(null);

    /** This dialog's own no — what a caller gets when its request never reached a button. */
    private readonly declined: Answer;

    constructor(declined: Answer) {
        this.declined = declined;
    }

    /** Put `request` up as the pending request, and be the promise the dialog settles. */
    public open(request: Request): Promise<Answer> {
        this.pending?._resolve?.(this.declined);
        return new Promise<Answer>((resolve) => {
            this.pending = { ...request, _resolve: resolve };
        });
    }

    /** Empty the slot. This is the write a dialog is given, and the only one. */
    public clear(): void {
        this.pending = null;
    }
}

/** What a caller asks `showConfirm` to put up. */
interface ConfirmationRequest {
    title: string;
    message: string;
    items?: string[];
    confirmText?: string;
    cancelText?: string;
    variant?: DialogVariant;
}

/** The request on screen: a confirmation, or a notice once `showNotice` has given it one button. */
interface PendingConfirmation extends ConfirmationRequest {
    /** Set by showNotice: the dialog gets a single button. */
    okText?: string;
}

interface CommitDialogRequest {
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

/**
 * A stat value may be a snapshot number or a live getter; getters let the
 * dialog's visualization track reactive sources (e.g. stream state) in real time.
 */
interface CompressContextRequest {
    used?: number | (() => number);
    limit?: number | (() => number);
    sessionCost?: number | (() => number);
    confirmText?: string;
    cancelText?: string;
}

export class DialogStore {
    public toast = $state<{ message: string; type: 'error' | 'success' } | null>(null);

    public readonly confirmationSlot = new PromiseSlot<PendingConfirmation, boolean>(false);
    public readonly commitDialogSlot = new PromiseSlot<CommitDialogRequest, string | null>(null);
    public readonly compressContextDialogSlot = new PromiseSlot<CompressContextRequest, boolean>(false);

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
    public showConfirm(options: ConfirmationRequest): Promise<boolean> {
        return this.confirmationSlot.open({
            ...options,
            confirmText: options.confirmText ?? t('app.confirm.confirm'),
            cancelText: options.cancelText ?? t('app.cancel'),
            variant: options.variant ?? 'destructive',
        });
    }

    /** Show a message with one button. It replaces any open confirmation, so two never stack. */
    public showNotice(options: {
        title: string;
        message: string;
        items?: string[];
        okText?: string;
    }): Promise<void> {
        // The answer is the notice's own business: the caller waits for the dismissal, not for a yes.
        return this.confirmationSlot.open({
            ...options,
            okText: options.okText ?? t('app.ok'),
            variant: 'default',
        }).then(() => undefined);
    }

    /**
     * Show a commit confirmation dialog and return a Promise that resolves with the edited message or null (cancelled).
     */
    public showCommitDialog(options: CommitDialogRequest): Promise<string | null> {
        return this.commitDialogSlot.open({
            ...options,
            confirmText: options.confirmText ?? t('app.dialog.commitAndDone'),
            cancelText: options.cancelText ?? t('app.cancel'),
        });
    }

    /**
     * Show the Compress Context dialog (includes a context donut + stats) and
     * return a Promise that resolves to true (confirmed) or false (cancelled).
     * Pass getters for `used`/`limit`/`sessionCost` to keep the visualization
     * and stats updating in real time while the dialog is open.
     */
    public showCompressContextDialog(options: CompressContextRequest): Promise<boolean> {
        return this.compressContextDialogSlot.open({
            ...options,
            confirmText: options.confirmText ?? t('app.dialog.compress'),
            cancelText: options.cancelText ?? t('app.cancel'),
        });
    }
}
